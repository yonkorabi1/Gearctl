// Tests the actual SSH transport + prompt-detection + RawDriver stack
// end-to-end against a real (mock) SSH server. Deliberately calls
// RawDriver directly rather than going through the HTTP route -- target
// validation (execute.route.ts's isAllowedOltTarget) is an HTTP-boundary
// concern that correctly blocks loopback in production, which would also
// block this in-process test server; testing at the driver level avoids
// that conflict while still exercising the real transport code.
import { RawDriver } from '../src/drivers/raw-driver';
import {
  startMockSshServer,
  MockSshServerHandle,
  MOCK_USERNAME,
  MOCK_PASSWORD,
} from './fixtures/mock-ssh-server';
import { ConnectionParams, OltCliError } from '../src/types';

describe('RawDriver over SSH (against a real mock SSH server)', () => {
  let sshServer: MockSshServerHandle;

  beforeAll(async () => {
    sshServer = await startMockSshServer();
  });

  afterAll(async () => {
    await sshServer.stop();
  });

  function params(overrides: Partial<ConnectionParams> = {}): ConnectionParams {
    return {
      host: '127.0.0.1',
      port: sshServer.port,
      protocol: 'ssh',
      username: MOCK_USERNAME,
      password: MOCK_PASSWORD,
      connectTimeoutMs: 5000,
      perCommandTimeoutMs: 3000,
      ...overrides,
    };
  }

  it('connects and runs a single command, returning its real output', async () => {
    const driver = new RawDriver();
    await driver.connect(params());
    const results = await driver.runCommands(['display version']);
    await driver.disconnect();

    expect(results).toHaveLength(1);
    expect(results[0].output).toContain('MockOLT Software Version 1.0.0');
    expect(results[0].error).toBeUndefined();
    expect(results[0].truncated).toBe(false);
  });

  it('runs multiple commands in order over one session', async () => {
    const driver = new RawDriver();
    await driver.connect(params());
    const results = await driver.runCommands(['display version', 'display board 0']);
    await driver.disconnect();

    expect(results).toHaveLength(2);
    expect(results[0].output).toContain('Version 1.0.0');
    expect(results[1].output).toContain('GPBA');
  });

  it('sends disablePagingCommand first and discards its output, without it leaking into the next command', async () => {
    const driver = new RawDriver();
    await driver.connect(params({ disablePagingCommand: 'screen-length 0 temporary' }));
    const results = await driver.runCommands(['display board 0']);
    await driver.disconnect();

    expect(results[0].output).toContain('GPBA');
    expect(results[0].output).not.toContain('screen-length');
  });

  it('fails the hanging command with a timeout and does not attempt subsequent commands', async () => {
    const driver = new RawDriver();
    await driver.connect(params({ perCommandTimeoutMs: 300 }));
    const results = await driver.runCommands(['hang-forever', 'display version']);
    await driver.disconnect();

    expect(results).toHaveLength(1);
    expect(results[0].error).toBe('timeout');
  });

  it('rejects connect() with an auth-typed OltCliError on wrong credentials', async () => {
    const driver = new RawDriver();
    await expect(driver.connect(params({ password: 'wrong-password' }))).rejects.toMatchObject({
      type: 'auth',
    } as Partial<OltCliError>);
  });

  it('rejects connect() with a connection-typed error when the port is unreachable', async () => {
    const driver = new RawDriver();
    await expect(
      driver.connect(params({ port: 1, connectTimeoutMs: 1000 })),
    ).rejects.toMatchObject({ type: 'connection' } as Partial<OltCliError>);
  });
});
