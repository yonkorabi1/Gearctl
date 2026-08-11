// ============================================================================
// A real, in-process SSH server (ssh2's server-side API) used by tests to
// exercise the whole SSH transport + prompt-detection stack without any
// real OLT hardware. Emulates: a fixed prompt, canned command responses, a
// deliberately non-responding command (to exercise the timeout path), and
// a paginated response (to exercise the disable-paging seam).
// ============================================================================

import { Server, ServerChannel, Connection } from 'ssh2';
import * as crypto from 'crypto';

export const MOCK_USERNAME = 'admin';
export const MOCK_PASSWORD = 'admin-secret';
export const MOCK_PROMPT = 'mock-olt>';

const CANNED_RESPONSES: Record<string, string> = {
  'display version': 'MockOLT Software Version 1.0.0',
  'screen-length 0 temporary': '', // paging-disable command -- no output
  'display board 0': 'Slot  Type   Status\r\n0     GPBA   Online',
};

const PAGINATED_COMMAND = 'display paginated-output';
const PAGINATED_FIRST_PAGE = 'line one\r\nline two\r\n  ---- More ( Press \'Q\' to break ) ----';
const PAGINATED_SECOND_PAGE = 'line three\r\nline four';

const HANGING_COMMAND = 'hang-forever';

export interface MockSshServerHandle {
  port: number;
  stop(): Promise<void>;
}

export async function startMockSshServer(): Promise<MockSshServerHandle> {
  const hostKey = crypto.generateKeyPairSync('rsa', {
    modulusLength: 2048,
    privateKeyEncoding: { type: 'pkcs1', format: 'pem' },
    publicKeyEncoding: { type: 'pkcs1', format: 'pem' },
  }).privateKey;

  // Track every client connection so stop() can force-close them --
  // net.Server.close()'s callback doesn't fire until all connections end
  // on their own, which would hang test teardown if a test intentionally
  // leaves a connection open (e.g. the timeout/auth-failure tests).
  const clients = new Set<Connection>();

  const server = new Server({ hostKeys: [hostKey] }, (client: Connection) => {
    clients.add(client);
    client.on('close', () => clients.delete(client));

    client.on('authentication', (ctx) => {
      if (ctx.method === 'password' && ctx.username === MOCK_USERNAME && ctx.password === MOCK_PASSWORD) {
        ctx.accept();
      } else {
        ctx.reject();
      }
    });

    client.on('ready', () => {
      client.on('session', (accept) => {
        const session = accept();
        // ssh2's client.shell() requests a PTY by default -- a real OLT
        // SSH server always grants one for an interactive CLI session, so
        // the mock server needs to as well or client.shell() errors out
        // with "Unable to request a pseudo-terminal" before ever reaching
        // the shell handler below.
        session.on('pty', (acceptPty: () => void) => acceptPty());
        session.on('shell', (acceptShell: () => ServerChannel) => {
          const stream = acceptShell();
          handleShell(stream);
        });
      });
    });
  });

  const port = await new Promise<number>((resolve) => {
    server.listen(0, '127.0.0.1', function (this: { address: () => { port: number } }) {
      resolve(this.address().port);
    });
  });

  return {
    port,
    stop: () =>
      new Promise<void>((resolve) => {
        for (const client of clients) client.end();
        server.close(() => resolve());
      }),
  };
}

function handleShell(stream: ServerChannel): void {
  let lineBuffer = '';
  let paginating = false;

  const writePrompt = () => stream.write(`\r\n${MOCK_PROMPT} `);

  // Initial prompt, matching a real device greeting the session.
  stream.write(`Welcome to MockOLT\r\n${MOCK_PROMPT} `);

  stream.on('data', (chunk: Buffer) => {
    const text = chunk.toString('utf8');
    // Local echo, like a real interactive terminal.
    stream.write(text);

    if (paginating) {
      // Any keypress advances past the pager, matching real "--More--" UX.
      paginating = false;
      stream.write(`\r\n${PAGINATED_SECOND_PAGE}`);
      writePrompt();
      return;
    }

    lineBuffer += text;
    const newlineIdx = lineBuffer.indexOf('\n');
    if (newlineIdx === -1) return;

    const command = lineBuffer.slice(0, newlineIdx).replace(/\r$/, '').trim();
    lineBuffer = lineBuffer.slice(newlineIdx + 1);

    if (command === HANGING_COMMAND) {
      // Deliberately never respond -- exercises the client's per-command
      // timeout path.
      return;
    }

    if (command === PAGINATED_COMMAND) {
      paginating = true;
      stream.write(`\r\n${PAGINATED_FIRST_PAGE}`);
      return;
    }

    const response = CANNED_RESPONSES[command] ?? `% Unknown command: ${command}`;
    if (response) {
      stream.write(`\r\n${response}`);
    }
    writePrompt();
  });
}
