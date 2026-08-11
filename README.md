# olt-cli-gateway

A stateless SSH/Telnet CLI executor for OLT devices — Phase 1 of a
NAPALM-like, vendor-agnostic automation layer for the upstream GPON
hierarchy this platform already models (Region → Site → OLT → PON Port →
Splitter → ONT).

Implemented in Go: this is the sole place in the platform that speaks raw
SSH/Telnet to network gear, and it runs as its own binary/container so
that `platform-api` only ever talks to it over the same internal HTTP
contract, regardless of what runs behind it.

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

`internal/drivers/driver.go` defines `CliDriver` — a contract every
vendor driver implements. Phase 1 ships exactly one driver, `RawDriver`
(`internal/drivers/rawdriver.go`, `vendor: "*"`), which requires zero
vendor knowledge: connect, run whatever commands the caller asked for,
disconnect. `internal/drivers/registry.go`'s `Resolve()` always returns
`RawDriver` today; Phase 2 adds vendor-specific drivers (structured
`GetFacts()`) and Phase 3 adds config backup/push (`GetConfig()`/
`LoadConfig()`) via the `FactsProvider`/`ConfigProvider` optional-capability
interfaces already declared in `driver.go` — without touching the HTTP
layer, transport layer, or platform-api integration, only the registry
gains a branch.

### Prompt-return detection

The hard problem: an OLT CLI session is a stateful, ongoing interactive
shell — there's no "command exits" signal like a one-shot SSH exec channel.
`internal/transport/promptdetector.go` solves this with zero vendor
knowledge:

1. On connect, send a bare newline and wait for the buffer to go quiet (no
   new bytes for ~300ms). Whatever trailing line remains is the prompt.
2. For every command, accumulate output until the buffer's tail contains
   that captured prompt text.

Both SSH (`internal/transport/sshsession.go`, via `golang.org/x/crypto/ssh`'s
interactive shell channel — not one-shot exec, since OLT CLIs are stateful
multi-command sessions) and Telnet (`internal/transport/telnetsession.go`,
hand-rolled on a raw `net.Conn` — no third-party Telnet library; see below)
funnel through this same logic via the `DataSource`/`ShellSession`
abstraction in `internal/transport/types.go` and `internal/transport/session.go`.

### Telnet: hand-rolled IAC parsing, with a real hardware fix baked in

`internal/transport/telnetsession.go`'s `telnetIacParser` is a stateful
byte-stream parser for RFC 854 IAC (Interpret As Command) sequences,
including `IAC SB <option> ... IAC SE` subnegotiation blocks that can split
across TCP reads — it carries incomplete sequences forward across `feed()`
calls rather than assuming one call always sees one full sequence.

It also answers `TTYPE` (terminal type) negotiation instead of blanket
refusing every option: some real OLT Telnet servers hang indefinitely,
never showing a login prompt, until they get a proper `IAC WILL TTYPE`
followed by `IAC SB TTYPE IS "VT100" IAC SE` in response to their `SEND`
subnegotiation. Both of these were found and fixed via live debugging
against a real OLT, not from a spec read — see `replyToNegotiation`/
`replyToSubnegotiation` for the exact behavior.

### Pagination

Accept an optional `disablePagingCommand` in the request (e.g.
`"screen-length 0 temporary"` for Huawei, `"terminal length 0"` for
Cisco-style CLIs). If provided, it's sent first and its output discarded.
If omitted, it's the caller's problem in Phase 1 — it can also just be the
first entry in `commands`.

### Security

- Every request requires `X-Internal-Auth: <shared secret>`
  (`OLT_GATEWAY_SHARED_SECRET`, constant-time comparison via
  `internal/auth/sharedsecret.go`).
- No `ports:` mapping in compose — reachable only as
  `http://olt-cli-gateway:8090` inside the internal Docker network.
- Never persists credentials — in-memory for one request's duration only.
- `internal/security/targetvalidator.go`'s `IsAllowedOltTarget` blocks
  loopback/link-local/metadata targets (including decimal/octal/hex IP
  literal obfuscation), but deliberately **allows RFC1918** (unlike the
  SSRF guard pattern used elsewhere in this platform for outbound download
  URLs) — OLT management IPs legitimately live on private management
  VLANs. This is defense-in-depth; the real SSRF closure is that
  `platform-api` resolves the target host server-side from the `olts`
  table by ID, never from raw client input.

## HTTP API

`GET /health` — unauthenticated, `{"status":"ok"}`.

`POST /v1/execute` — requires `X-Internal-Auth`. Request/response JSON
shapes are defined in `internal/httpapi/types.go` and are unchanged from
this service's original TypeScript implementation — `platform-api`'s
`OltCliService` needed zero changes for this rewrite.

## Building and running

```
go build ./...
go vet ./...
go test ./...
```

Or via Docker (multi-stage `Dockerfile`, builds a static binary on
`golang:1.25-alpine`, runs on plain `alpine`):

```
docker compose -f docker-compose.yml -f docker-compose.platform.yml \
  -f docker-compose.oltgw.yml build olt-cli-gateway
```

## Testing

No real OLT hardware needed for most of this — mock SSH (via
`golang.org/x/crypto/ssh`'s server-side API) and mock Telnet (a raw
`net.Listener`, including a `requireTtypeNegotiation` mode that reproduces
the real-hardware TTYPE hang) fixtures exercise the whole transport +
prompt-detection stack end-to-end.

What genuinely needs real hardware (not blocking, see the project plan):
actual vendor prompt/pagination strings, legacy SSH cipher/kex
compatibility (some OLT SSH stacks may need `ssh.Config` overrides), and
real command latency (some `display`/`show` commands enumerating hundreds
of ONUs can be slow).
