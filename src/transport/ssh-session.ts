import { Client, ClientChannel } from 'ssh2';
import { ShellSession } from './session.interface';
import { StreamShellSession } from './stream-shell-session';
import { DataSource } from './prompt-detector';
import { ConnectionParams, OltCliError } from '../types';

/**
 * SSH transport. Uses ssh2's interactive shell channel (client.shell()),
 * NOT a one-shot exec channel -- OLT CLIs are stateful multi-command
 * sessions (pagination state, privilege level, etc. persist across
 * commands within one connection), which exec() doesn't model.
 *
 * ssh2 handles the actual SSH-protocol authentication (password auth)
 * natively -- this file only needs to open the shell channel afterward and
 * hand it to StreamShellSession for prompt detection. Some OLT CLIs gate a
 * SECOND, in-band prompt after SSH auth succeeds (e.g. an "enable"-style
 * privileged-mode password); StreamShellSession.open() doesn't handle that
 * today (Phase 1 has no confirmed real-hardware case for it over SSH
 * specifically -- see README.md's login-sequence note, which focuses on
 * Telnet). Revisit if a real OLT needs it.
 */
export async function openSshSession(params: ConnectionParams): Promise<ShellSession> {
  const client = new Client();

  const stream = await new Promise<ClientChannel>((resolve, reject) => {
    let settled = false;

    client.on('ready', () => {
      client.shell((err, channel) => {
        if (settled) return;
        if (err) {
          settled = true;
          reject(new OltCliError('protocol', `Failed to open shell channel: ${err.message}`));
          return;
        }
        settled = true;
        resolve(channel);
      });
    });

    client.on('error', (err: Error) => {
      if (settled) return;
      settled = true;
      const isAuthError = /authentication/i.test(err.message);
      reject(
        new OltCliError(
          isAuthError ? 'auth' : 'connection',
          `SSH connection failed: ${err.message}`,
        ),
      );
    });

    client.connect({
      host: params.host,
      port: params.port,
      username: params.username,
      password: params.password,
      readyTimeout: params.connectTimeoutMs,
      // Some OLT SSH stacks are old enough to need legacy algorithm
      // support -- not enabled by default here since it weakens security
      // for every other target. If a real OLT fails to negotiate, this is
      // the place to add an `algorithms: {...}` override scoped to that
      // vendor's driver once one exists (see README.md's testing caveats).
    });
  });

  const source: DataSource = {
    onData(cb) {
      const listener = (chunk: Buffer) => cb(chunk);
      stream.on('data', listener);
      return () => stream.off('data', listener);
    },
    write(data) {
      stream.write(data);
    },
  };

  const session = new StreamShellSession(source, () => {
    stream.end();
    client.end();
  });

  await session.open(params);
  return session;
}
