import { RawDriver } from '../src/drivers/raw-driver';
import {
  startMockTelnetServer,
  MockTelnetServerHandle,
  MOCK_USERNAME,
  MOCK_PASSWORD,
} from './fixtures/mock-telnet-server';
import { ConnectionParams } from '../src/types';

describe('RawDriver over Telnet (against a real mock Telnet server)', () => {
  let telnetServer: MockTelnetServerHandle;

  beforeAll(async () => {
    telnetServer = await startMockTelnetServer();
  });

  afterAll(async () => {
    await telnetServer.stop();
  });

  function params(overrides: Partial<ConnectionParams> = {}): ConnectionParams {
    return {
      host: '127.0.0.1',
      port: telnetServer.port,
      protocol: 'telnet',
      username: MOCK_USERNAME,
      password: MOCK_PASSWORD,
      connectTimeoutMs: 5000,
      perCommandTimeoutMs: 3000,
      ...overrides,
    };
  }

  it('completes the login sequence (surviving IAC option negotiation) and runs a command', async () => {
    const driver = new RawDriver();
    await driver.connect(params());
    const results = await driver.runCommands(['display version']);
    await driver.disconnect();

    expect(results).toHaveLength(1);
    expect(results[0].output).toContain('MockTelnetOLT v1.0');
    // Confirms IAC bytes never leaked into the captured output as garbage.
    expect(results[0].output).not.toMatch(/[�]|\xFF/);
  });

  it('never reaches a usable shell on wrong credentials, surfacing as a connect-time error rather than silently succeeding', async () => {
    // The mock server goes quiet after a wrong password (see
    // mock-telnet-server.ts's comment on why Telnet has no clean "auth
    // failed" signal for a generic driver) -- capturePrompt() should time
    // out waiting for a prompt that never comes, rather than open()
    // resolving successfully.
    const driver = new RawDriver();
    await expect(
      driver.connect(params({ password: 'wrong-password', connectTimeoutMs: 500 })),
    ).rejects.toMatchObject({ type: 'protocol' });
    await driver.disconnect();
  });
});
