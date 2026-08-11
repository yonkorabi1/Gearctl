import * as net from 'net';
import { ShellSession } from './session.interface';
import { StreamShellSession } from './stream-shell-session';
import { DataSource } from './prompt-detector';
import { ConnectionParams, OltCliError } from '../types';

const IAC = 0xff;

/**
 * Minimal hand-rolled Telnet transport on a raw net.Socket, rather than a
 * third-party telnet client library (see README.md's transport section for
 * why: most maintained options either own the whole prompt-matching loop
 * themselves in a way that doesn't compose with prompt-detector.ts's
 * shared logic, or are thin/unmaintained wrappers). Telnet option
 * negotiation (RFC 854/855) is handled by refusing every option the
 * server proposes (IAC WONT/DONT for anything the server WILLs/DOes) --
 * this keeps the session in plain, unnegotiated line-mode text, which is
 * sufficient for a CLI shell and avoids implementing the full option
 * subnegotiation surface.
 */
export async function openTelnetSession(params: ConnectionParams): Promise<ShellSession> {
  const socket = new net.Socket();

  await new Promise<void>((resolve, reject) => {
    let settled = false;
    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      socket.destroy();
      reject(new OltCliError('connection', `Telnet connection to ${params.host}:${params.port} timed out`));
    }, params.connectTimeoutMs);

    socket.once('connect', () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve();
    });
    socket.once('error', (err) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(new OltCliError('connection', `Telnet connection failed: ${err.message}`));
    });

    socket.connect(params.port, params.host);
  });

  const dataListeners: Array<(chunk: Buffer) => void> = [];
  socket.on('data', (chunk: Buffer) => {
    const stripped = stripTelnetControlSequences(chunk, socket);
    if (stripped.length > 0) {
      for (const listener of dataListeners) listener(stripped);
    }
  });
  socket.on('error', () => {
    // Swallow post-setup socket errors here -- in-flight
    // waitForPrompt()/login-step promises have their own timeouts and will
    // reject on their own; a session-level error handler avoids an
    // unhandled 'error' event crashing the process if the peer resets the
    // connection mid-command.
  });

  const source: DataSource = {
    onData(cb) {
      dataListeners.push(cb);
      return () => {
        const idx = dataListeners.indexOf(cb);
        if (idx !== -1) dataListeners.splice(idx, 1);
      };
    },
    write(data) {
      socket.write(data);
    },
  };

  await runLoginSequence(source, params);

  const session = new StreamShellSession(source, () => {
    socket.end();
    socket.destroy();
  });

  await session.open(params);
  return session;
}

/**
 * Strip IAC (0xFF) command sequences from a chunk, replying WONT/DONT to
 * any option negotiation request so the peer stops trying to negotiate.
 * Returns the remaining plain-text bytes.
 */
function stripTelnetControlSequences(chunk: Buffer, socket: net.Socket): Buffer {
  const out: number[] = [];
  let i = 0;
  while (i < chunk.length) {
    if (chunk[i] === IAC && i + 1 < chunk.length) {
      const cmd = chunk[i + 1];
      // WILL(251)/WONT(252)/DO(253)/DONT(254) are each followed by a 1-byte
      // option code -- reply refusing it and skip all 3 bytes.
      if (cmd >= 251 && cmd <= 254 && i + 2 < chunk.length) {
        const option = chunk[i + 2];
        const reply = cmd === 253 || cmd === 251 ? 252 : 254; // WILL/DO -> WONT/DONT
        socket.write(Buffer.from([IAC, reply, option]));
        i += 3;
        continue;
      }
      // IAC IAC (255 255) is an escaped literal 0xFF byte.
      if (cmd === IAC) {
        out.push(IAC);
        i += 2;
        continue;
      }
      // Other 2-byte IAC commands (NOP, etc.) -- skip.
      i += 2;
      continue;
    }
    out.push(chunk[i]);
    i += 1;
  }
  return Buffer.from(out);
}

/**
 * Expect-style state machine for Telnet's plaintext login sequence:
 * login/username prompt -> username, password prompt -> password, and an
 * optional third enable-password step. Each step has its own timeout.
 */
async function runLoginSequence(source: DataSource, params: ConnectionParams): Promise<void> {
  await waitForSubstring(source, [/login[: ]*$/i, /username[: ]*$/i], params.connectTimeoutMs);
  source.write(params.username + '\n');

  await waitForSubstring(source, [/password[: ]*$/i], params.connectTimeoutMs);
  source.write(params.password + '\n');

  if (params.enablePassword) {
    // Best-effort: not every device has a third prompt. Use a shorter
    // timeout and don't fail the whole connection if it never appears --
    // capturePrompt() in StreamShellSession.open() will just capture
    // whatever prompt is showing once this returns.
    const matched = await waitForSubstring(
      source,
      [/password[: ]*$/i],
      Math.min(3000, params.connectTimeoutMs),
    ).catch(() => -1);
    if (matched >= 0) {
      source.write(params.enablePassword + '\n');
    }
  }
}

function waitForSubstring(source: DataSource, patterns: RegExp[], timeoutMs: number): Promise<number> {
  return new Promise((resolve, reject) => {
    let buffer = '';
    let settled = false;
    let unsubscribe: () => void;

    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      unsubscribe();
      reject(new OltCliError('auth', 'Timed out waiting for a login/password prompt'));
    }, timeoutMs);

    unsubscribe = source.onData((chunk) => {
      if (settled) return;
      buffer += chunk.toString('utf8');
      const idx = patterns.findIndex((p) => p.test(buffer));
      if (idx !== -1) {
        settled = true;
        clearTimeout(timer);
        unsubscribe();
        resolve(idx);
      }
    });
  });
}
