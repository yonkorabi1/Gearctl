import { CommandResult, ConnectionParams } from '../types';

/**
 * The NAPALM-like abstraction seam. Every vendor driver (Phase 2+)
 * implements this same contract; the HTTP route, transport layer, and
 * platform-api integration never need to change when a new driver is
 * added -- only driver-registry.ts gains a `case`.
 *
 * Phase 1 ships exactly one driver, RawDriver ('*'), which implements only
 * the required methods below with zero vendor knowledge: connect, run
 * whatever commands the caller asked for, disconnect. The optional methods
 * are the seams for later phases (see README.md):
 *   - getFacts(): Phase 2 -- vendor drivers parse `display`/`show` output
 *     into structured JSON (interface status, ONU list, optical levels).
 *   - getConfig()/loadConfig(): Phase 3 -- config backup/diff/push, with a
 *     candidate/diff/commit/rollback flow gated behind explicit approval.
 * A driver that hasn't implemented an optional method simply omits it;
 * callers must check for its presence before invoking it.
 */
export interface CliDriver {
  /** Vendor identifier this driver claims to handle, or '*' for the generic/raw driver. */
  readonly vendor: string;

  connect(params: ConnectionParams): Promise<void>;

  /** Run commands in order against the already-open session. */
  runCommands(commands: string[]): Promise<CommandResult[]>;

  disconnect(): Promise<void>;

  /** Phase 2 seam -- not implemented by RawDriver. */
  getFacts?(): Promise<Record<string, unknown>>;

  /** Phase 3 seam -- not implemented by RawDriver. */
  getConfig?(): Promise<string>;
  loadConfig?(candidate: string, dryRun: boolean): Promise<{ diff: string; applied: boolean }>;
}
