import { CliDriver } from './driver.interface';
import { ShellSession } from '../transport/session.interface';
import { openSshSession } from '../transport/ssh-session';
import { openTelnetSession } from '../transport/telnet-session';
import { CommandResult, ConnectionParams, OltCliError } from '../types';

/**
 * Phase 1's only driver: generic, vendor-agnostic. Opens an SSH or Telnet
 * session and runs whatever commands the caller supplied, with zero
 * parsing/vendor knowledge. This satisfies the literal requirement ("SSH
 * or Telnet into a device and run commands") for any OLT, regardless of
 * vendor.
 */
export class RawDriver implements CliDriver {
  readonly vendor = '*';
  private session: ShellSession | null = null;
  private params: ConnectionParams | null = null;

  async connect(params: ConnectionParams): Promise<void> {
    this.params = params;
    this.session =
      params.protocol === 'ssh' ? await openSshSession(params) : await openTelnetSession(params);
  }

  async runCommands(commands: string[]): Promise<CommandResult[]> {
    if (!this.session || !this.params) {
      throw new OltCliError('protocol', 'runCommands() called before connect()');
    }
    const results: CommandResult[] = [];

    for (const command of commands) {
      const startedAt = Date.now();
      try {
        const { output, truncated } = await this.session.sendAndWaitForPrompt(
          command,
          this.params.perCommandTimeoutMs,
        );
        results.push({
          command,
          output,
          durationMs: Date.now() - startedAt,
          truncated,
        });
      } catch (err) {
        // Fail-fast: a timed-out session is in an indeterminate state
        // (see ShellSession's contract) -- record this command as failed
        // and stop attempting the remaining ones rather than risk sending
        // more commands into a session that may still be mid-response.
        results.push({
          command,
          output: null,
          durationMs: Date.now() - startedAt,
          truncated: false,
          error: err instanceof OltCliError && err.type === 'timeout' ? 'timeout' : 'session_closed',
        });
        break;
      }
    }

    return results;
  }

  async disconnect(): Promise<void> {
    await this.session?.close();
    this.session = null;
  }
}
