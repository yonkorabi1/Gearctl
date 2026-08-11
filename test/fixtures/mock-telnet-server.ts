// A minimal raw-socket Telnet server for testing telnet-session.ts's
// hand-rolled transport: login-prompt sequence, IAC option-negotiation
// stripping, and prompt-based command completion.
import * as net from 'net';

export const MOCK_USERNAME = 'admin';
export const MOCK_PASSWORD = 'admin-secret';
export const MOCK_PROMPT = 'mock-telnet-olt>';

export interface MockTelnetServerHandle {
  port: number;
  stop(): Promise<void>;
}

// A couple of IAC option-negotiation bytes sent right after connect, like
// many real Telnet daemons do -- exercises the client's negotiation
// stripping/refusal path (see telnet-session.ts).
const IAC = 0xff;
const WILL = 0xfb;
const ECHO_OPTION = 0x01;
const NEGOTIATION_PREAMBLE = Buffer.from([IAC, WILL, ECHO_OPTION]);

/**
 * Just like the real telnet-session.ts client, a real telnet SERVER must
 * be prepared to receive IAC sequences from the peer too (here: the
 * client's WONT/DONT reply to our negotiation preamble above) and not
 * choke on them. This mirrors stripTelnetControlSequences() in
 * telnet-session.ts but only strips -- a mock server has no reason to
 * reply to further negotiation.
 */
function stripIncomingIac(chunk: Buffer): string {
  const out: number[] = [];
  let i = 0;
  while (i < chunk.length) {
    if (chunk[i] === IAC && i + 1 < chunk.length) {
      const cmd = chunk[i + 1];
      if (cmd >= 251 && cmd <= 254) {
        i += 3; // WILL/WONT/DO/DONT + 1 option byte
        continue;
      }
      if (cmd === IAC) {
        out.push(IAC);
        i += 2;
        continue;
      }
      i += 2;
      continue;
    }
    out.push(chunk[i]);
    i += 1;
  }
  return Buffer.from(out).toString('utf8');
}

export async function startMockTelnetServer(): Promise<MockTelnetServerHandle> {
  const sockets = new Set<net.Socket>();

  const server = net.createServer((socket) => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));

    let stage: 'login' | 'password' | 'shell' = 'login';
    let lineBuffer = '';

    socket.write(NEGOTIATION_PREAMBLE);
    socket.write('Login: ');

    socket.on('data', (chunk) => {
      const text = stripIncomingIac(chunk);
      if (text.length === 0) return;

      if (stage !== 'shell') {
        lineBuffer += text;
        const nl = lineBuffer.indexOf('\n');
        if (nl === -1) return;
        const line = lineBuffer.slice(0, nl).replace(/\r$/, '').trim();
        lineBuffer = lineBuffer.slice(nl + 1);

        if (stage === 'login') {
          stage = line === MOCK_USERNAME ? 'password' : 'login';
          socket.write('\r\nPassword: ');
          return;
        }
        if (stage === 'password') {
          if (line === MOCK_PASSWORD) {
            stage = 'shell';
            socket.write(`\r\nWelcome\r\n${MOCK_PROMPT} `);
          } else {
            // No clean "auth failed" signal for a generic driver -- just
            // go quiet, matching the real-world ambiguity documented in
            // telnet-session.ts.
          }
          return;
        }
        return;
      }

      // Shell stage -- local echo + canned command response, same shape
      // as mock-ssh-server.ts.
      socket.write(text);
      lineBuffer += text;
      const nl = lineBuffer.indexOf('\n');
      if (nl === -1) return;
      const command = lineBuffer.slice(0, nl).replace(/\r$/, '').trim();
      lineBuffer = lineBuffer.slice(nl + 1);
      const response = command === 'display version' ? 'MockTelnetOLT v1.0' : `% Unknown: ${command}`;
      socket.write(`\r\n${response}\r\n${MOCK_PROMPT} `);
    });
  });

  const port = await new Promise<number>((resolve) => {
    server.listen(0, '127.0.0.1', () => {
      const addr = server.address();
      resolve(typeof addr === 'object' && addr ? addr.port : 0);
    });
  });

  return {
    port,
    stop: () =>
      new Promise<void>((resolve) => {
        for (const s of sockets) s.destroy();
        server.close(() => resolve());
      }),
  };
}
