import { ConnectionParams } from '../types';

/**
 * Common contract both SSH and Telnet sessions implement. This is what lets
 * drivers/raw-driver.ts stay transport-agnostic -- it just calls
 * sendAndWaitForPrompt() without caring whether the underlying pipe is an
 * SSH shell channel or a raw Telnet socket.
 */
export interface ShellSession {
  /**
   * Open the connection, complete any login-prompt sequence, and capture
   * the shell's idle prompt (see prompt-detector.ts). Throws OltCliError
   * with type 'connection' or 'auth' on failure.
   */
  open(params: ConnectionParams): Promise<void>;

  /**
   * Send a command, wait for the prompt to reappear (or timeoutMs to
   * elapse), and return everything printed in between (the command's
   * echo is stripped). Throws OltCliError with type 'timeout' if the
   * prompt never reappears in time -- callers must NOT reuse the session
   * after that, since its state is now indeterminate (see close()).
   */
  sendAndWaitForPrompt(command: string, timeoutMs: number): Promise<{ output: string; truncated: boolean }>;

  /** Tear down the connection. Safe to call multiple times. */
  close(): Promise<void>;
}
