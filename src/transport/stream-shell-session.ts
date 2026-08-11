// ============================================================================
// Generic ShellSession built on top of any DataSource (an SSH shell channel
// or a raw Telnet socket, once each transport has finished its own
// connection/auth handshake). All prompt-detection, pagination, and
// per-command timeout logic lives here ONCE so ssh-session.ts and
// telnet-session.ts only need to establish the underlying pipe.
// ============================================================================

import { ShellSession } from './session.interface';
import { DataSource, capturePrompt, waitForPrompt } from './prompt-detector';
import { ConnectionParams, MAX_OUTPUT_BYTES, OltCliError } from '../types';

export class StreamShellSession implements ShellSession {
  private promptText = '';
  private closed = false;

  constructor(
    private readonly source: DataSource,
    /** Called by close(); transport-specific (ssh2 channel.end() / socket.end()). */
    private readonly onClose: () => void,
  ) {}

  /**
   * Assumes the transport has ALREADY completed connection + login (ssh2
   * handles SSH auth natively; TelnetSession runs its own login-prompt
   * state machine before constructing this). This only captures the idle
   * prompt and runs the optional pagination-disable command.
   */
  async open(params: ConnectionParams): Promise<void> {
    this.promptText = await capturePrompt(this.source, params.connectTimeoutMs);

    if (params.disablePagingCommand) {
      // Discard output -- this command's only purpose is to stop the
      // device from blocking on a "--More--" style pager, not to report
      // anything back to the caller.
      await waitForPrompt(
        this.source,
        params.disablePagingCommand,
        this.promptText,
        params.perCommandTimeoutMs,
      );
    }
  }

  async sendAndWaitForPrompt(
    command: string,
    timeoutMs: number,
  ): Promise<{ output: string; truncated: boolean }> {
    if (this.closed) {
      throw new OltCliError('protocol', 'Cannot send a command on a closed session');
    }
    const output = await waitForPrompt(this.source, command, this.promptText, timeoutMs);
    const truncated = Buffer.byteLength(output, 'utf8') > MAX_OUTPUT_BYTES;
    return {
      output: truncated ? truncateUtf8(output, MAX_OUTPUT_BYTES) : output,
      truncated,
    };
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.onClose();
  }
}

function truncateUtf8(text: string, maxBytes: number): string {
  // Truncate on a UTF-8 boundary by encoding, slicing the buffer, then
  // decoding with a replacement char for any split multi-byte sequence at
  // the cut point (acceptable for a diagnostics-output truncation marker).
  const buf = Buffer.from(text, 'utf8');
  return buf.subarray(0, maxBytes).toString('utf8') + '\n...[truncated]';
}
