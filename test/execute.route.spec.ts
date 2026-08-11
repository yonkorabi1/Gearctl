// HTTP-boundary tests only: shared-secret auth and request validation
// (including target-host blocking). The actual SSH/Telnet transport and
// prompt-detection logic is tested directly against a mock SSH server in
// raw-driver.spec.ts, which deliberately bypasses target validation since
// that's an HTTP-route concern that correctly blocks the loopback address
// a local test server binds to.
import { FastifyInstance } from 'fastify';
import { buildApp } from '../src/app';

const SHARED_SECRET = 'test-shared-secret';

describe('POST /v1/execute', () => {
  let app: FastifyInstance;

  beforeAll(() => {
    app = buildApp({ sharedSecret: SHARED_SECRET });
  });

  afterAll(async () => {
    await app.close();
  });

  function baseBody(overrides: Record<string, unknown> = {}) {
    return {
      host: '203.0.113.5', // TEST-NET-3 (RFC 5737) -- routable-looking, never actually dialed in these tests
      port: 22,
      protocol: 'ssh',
      credentials: { username: 'admin', password: 'admin-secret' },
      commands: ['display version'],
      connectTimeoutMs: 100,
      perCommandTimeoutMs: 100,
      ...overrides,
    };
  }

  it('rejects requests with no auth header', async () => {
    const res = await app.inject({ method: 'POST', url: '/v1/execute', payload: baseBody() });
    expect(res.statusCode).toBe(401);
  });

  it('rejects requests with the wrong shared secret', async () => {
    const res = await app.inject({
      method: 'POST',
      url: '/v1/execute',
      headers: { 'x-internal-auth': 'wrong-secret' },
      payload: baseBody(),
    });
    expect(res.statusCode).toBe(401);
  });

  it('rejects a malformed body with a validation error', async () => {
    const res = await app.inject({
      method: 'POST',
      url: '/v1/execute',
      headers: { 'x-internal-auth': SHARED_SECRET },
      payload: baseBody({ commands: [] }),
    });
    expect(res.statusCode).toBe(400);
    expect(res.json().error.type).toBe('validation');
  });

  it.each(['127.0.0.1', 'localhost', '169.254.169.254', '0x7f000001', '2130706433'])(
    'rejects a blocked target host: %s',
    async (host) => {
      const res = await app.inject({
        method: 'POST',
        url: '/v1/execute',
        headers: { 'x-internal-auth': SHARED_SECRET },
        payload: baseBody({ host }),
      });
      expect(res.statusCode).toBe(400);
      expect(res.json().error.message).toMatch(/not allowed/);
    },
  );

  it('allows an RFC1918 target host through validation (OLT management IPs legitimately live there)', async () => {
    const res = await app.inject({
      method: 'POST',
      url: '/v1/execute',
      headers: { 'x-internal-auth': SHARED_SECRET },
      // Nothing is actually listening here -- this only proves the
      // request got PAST target validation and attempted a real
      // connection (which then fails for an unrelated reason: nothing to
      // connect to). See raw-driver.spec.ts for actual successful runs.
      payload: baseBody({ host: '10.255.255.254', connectTimeoutMs: 500 }),
    });
    expect(res.statusCode).toBe(200);
    const body = res.json();
    expect(body.status).toBe('failed');
    expect(body.error.type).not.toBe('validation');
  });
});
