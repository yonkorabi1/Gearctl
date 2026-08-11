// ============================================================================
// Shared request/response types + zod validation schemas for /v1/execute.
//
// This service is deliberately stateless (see README.md) — everything it
// needs for one command run arrives in the request body, and nothing is
// persisted here. platform-api is the sole stateful orchestrator.
// ============================================================================

import { z } from 'zod';

export const ProtocolSchema = z.enum(['ssh', 'telnet']);
export type Protocol = z.infer<typeof ProtocolSchema>;

export const CredentialsSchema = z.object({
  username: z.string().min(1),
  password: z.string().min(1),
  enablePassword: z.string().min(1).optional(),
});
export type Credentials = z.infer<typeof CredentialsSchema>;

export const ExecuteRequestSchema = z.object({
  host: z.string().min(1),
  port: z.number().int().positive().max(65535),
  protocol: ProtocolSchema,
  // Advisory only in Phase 1 -- accepted for audit-trail/forward-compat.
  // driver-registry.ts always resolves RawDriver regardless of this value
  // until vendor-specific drivers exist (see drivers/driver-registry.ts).
  vendor: z.string().optional(),
  credentials: CredentialsSchema,
  commands: z.array(z.string().min(1)).min(1).max(50),
  // Sent as the first command and its output discarded, e.g.
  // "screen-length 0 temporary" (Huawei) or "terminal length 0"
  // (Cisco-style). Optional escape hatch -- see README.md's "Pagination"
  // section for why this isn't hardcoded per-vendor in Phase 1.
  disablePagingCommand: z.string().optional(),
  connectTimeoutMs: z.number().int().positive().max(60_000).default(10_000),
  perCommandTimeoutMs: z.number().int().positive().max(120_000).default(15_000),
});
export type ExecuteRequest = z.infer<typeof ExecuteRequestSchema>;

export interface ConnectionParams {
  host: string;
  port: number;
  protocol: Protocol;
  username: string;
  password: string;
  enablePassword?: string;
  connectTimeoutMs: number;
  perCommandTimeoutMs: number;
  disablePagingCommand?: string;
}

export interface CommandResult {
  command: string;
  /** null when the command itself failed (e.g. timed out) -- see `error`. */
  output: string | null;
  durationMs: number;
  truncated: boolean;
  error?: 'timeout' | 'session_closed';
}

export type ExecuteErrorType = 'auth' | 'timeout' | 'connection' | 'protocol' | 'validation';

export interface ExecuteError {
  type: ExecuteErrorType;
  message: string;
}

export interface ExecuteResponse {
  status: 'success' | 'partial' | 'failed';
  results: CommandResult[];
  error: ExecuteError | null;
  connectedAt: string | null;
  totalDurationMs: number;
}

/** Thrown by transport/driver code; routes/execute.route.ts maps this to ExecuteError. */
export class OltCliError extends Error {
  constructor(
    public readonly type: ExecuteErrorType,
    message: string,
  ) {
    super(message);
    this.name = 'OltCliError';
  }
}

/** Max bytes of output captured per command before truncating (see README.md). */
export const MAX_OUTPUT_BYTES = 256 * 1024;
