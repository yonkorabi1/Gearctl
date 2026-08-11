# olt-cli-gateway

A stateless SSH/Telnet CLI executor for OLT devices — Phase 1 of a
NAPALM-like, vendor-agnostic automation layer for the upstream GPON
hierarchy this platform already models (Region → Site → OLT → PON Port →
Splitter → ONT).

## Why this exists

This platform manages ONTs/CPEs over TR-069/CWMP. OLTs themselves have no
live management capability — the topology module (`platform/api/src/modules/topology/`)
tracks OLT *inventory* (vendor, model, management IP, site) but has no
connection, credentials, or command execution. This service fills that gap:
an operator can run diagnostic CLI commands (`display version`, `show
interface`, etc.) against any OLT over SSH or Telnet, regardless of vendor.

## Architecture

This service is **stateless** — no database, no credential storage, no
memory of past runs. Everything needed for one command run arrives in the
request body; nothing is persisted here. `platform-api` (specifically its
`modules/olt-cli/` module) is the sole stateful orchestrator: it owns OLT
identity, encrypted credentials, and run history, and calls this service
per-request. This mirrors the existing `genieacs-nbi` ↔ `platform-api`
relationship elsewhere in this platform.

```
platform-api --POST /v1/execute (X-Internal-Auth)--> olt-cli-gateway --SSH/Telnet--> OLT
```

### The driver seam (NAPALM-like)

`src/drivers/driver.interface.ts` defines `CliDriver` — a contract every
vendor driver implements. Phase 1 ships exactly one driver, `RawDriver`
(`vendor: '*'`), which requires zero vendor knowledge: connect, run
whatever commands the caller asked for, disconnect. `driver-registry.ts`
always resolves `RawDriver` today; Phase 2 adds vendor-specific drivers
(structured `getFacts()`) and Phase 3 adds config backup/push
(`getConfig()`/`loadConfig()`) without touching the HTTP layer, transport
layer, or platform-api integration — only the registry gains a `case`.

### Prompt-return detection

The hard problem: an OLT CLI session is a stateful, ongoing interactive
shell — there's no "command exits" signal like a one-shot SSH exec channel.
`src/transport/prompt-detector.ts` solves this with zero vendor knowledge:

1. On connect, send a bare newline and wait for the buffer to go quiet (no
   new bytes for ~300ms). Whatever trailing line remains is the prompt.
2. For every command, accumulate output until the buffer's tail contains
   that captured prompt text.

Both SSH (`ssh2`, via an interactive shell channel — not one-shot exec,
since OLT CLIs are stateful multi-command sessions) and Telnet (hand-rolled
on a raw `net.Socket` — see `transport/telnet-session.ts` for why a
third-party telnet client library wasn't used) funnel through this same
logic via the `DataSource`/`ShellSession` abstraction in
`transport/session.interface.ts` and `transport/stream-shell-session.ts`.

### Pagination

Accept an optional `disablePagingCommand` in the request (e.g.
`"screen-length 0 temporary"` for Huawei, `"terminal length 0"` for
Cisco-style CLIs). If provided, it's sent first and its output discarded.
If omitted, it's the caller's problem in Phase 1 — it can also just be the
first entry in `commands`.

### Security

- Every request requires `X-Internal-Auth: <shared secret>`
  (`OLT_GATEWAY_SHARED_SECRET`, constant-time comparison).
- No `ports:` mapping in compose — reachable only as
  `http://olt-cli-gateway:<port>` inside the internal Docker network.
- Never persists credentials — in-memory for one request's duration only;
  the logger explicitly redacts `credentials.password`/`enablePassword`.
- `src/security/target-validator.ts` blocks loopback/link-local/metadata
  targets, but deliberately **allows RFC1918** (unlike the SSRF guard
  pattern used elsewhere in this platform for outbound download URLs) —
  OLT management IPs legitimately live on private management VLANs. This
  is defense-in-depth; the real SSRF closure is that `platform-api`
  resolves the target host server-side from the `olts` table by ID, never
  from raw client input.

## Testing

No real OLT hardware needed for most of this — `test/fixtures/mock-ssh-server.ts`
and `test/fixtures/mock-telnet-server.ts` are real, in-process servers
(ssh2's server-side API / a raw `net.Server`) with a scripted interactive
shell, used to exercise the whole transport + prompt-detection stack
end-to-end. See `test/raw-driver.spec.ts` and `test/telnet-session.spec.ts`.

What genuinely needs real hardware (not blocking, see the project plan):
actual vendor prompt/pagination strings, legacy SSH cipher/kex
compatibility (some OLT SSH stacks may need `ssh2` `algorithms:`
overrides), and real command latency (some `display`/`show` commands
enumerating hundreds of ONUs can be slow).

```
npm install
npm test
npm run build && npm start   # requires OLT_GATEWAY_SHARED_SECRET in env
```
