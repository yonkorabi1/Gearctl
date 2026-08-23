# gearctl

A NAPALM-like, vendor-agnostic SSH/Telnet automation layer for network
gear — routers, switches, OLTs, anything with an interactive CLI. Run
commands or pull structured facts, from a terminal (no server, no GUI) or
as an HTTP API any language can call.

```
# CLI, no server involved
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -cmd "show version"

# Same driver layer, as an HTTP API
GEARCTL_SHARED_SECRET=... gearctl -serve
curl -X POST http://localhost:8090/v1/execute -H "X-Internal-Auth: ..." -d '...'
```

Implemented in Go: this is the sole place in the platform that speaks raw
SSH/Telnet to network gear, and it runs as its own binary/container so
that callers (e.g. `platform-api`) only ever talk to it over one internal
HTTP contract, regardless of what runs behind it.

## Why this exists

Network gear generally has no live, structured management API — you get
an interactive CLI over SSH or Telnet, and its exact syntax/prompt/
pagination behavior varies by vendor. gearctl provides one contract
(`CliDriver` — see below) that works against *any* vendor's device out of
the box (raw command passthrough), and can be upgraded per-vendor to add
structured facts (`GetFacts()`), richer per-interface state
(`GetInterfaces()` — up/down, enabled, description, IP address, MTU,
speed, MAC),
LLDP topology discovery (`GetLLDPNeighbors()`), read-only config
retrieval (`GetConfig()`),
or config push — either a full candidate config (`LoadConfig()`) or just
the commands that changed (`LoadMergeCommands()`) — both a merge/overlay
onto the existing config, gated behind a required dry-run-first,
hash-checked apply flow; see [Merging a config
change](#merging-a-config-change) before using either — without changing
how callers use it.

This project grew out of an OLT-specific tool for a TR-069/CWMP ACS
platform (topology inventory tracks OLT vendor/model/management IP but
had no connection, credentials, or command execution) — hence some
OLT-flavored naming still in the codebase (`OltCliError`,
`IsAllowedOltTarget`, the `OLT_GATEWAY_SHARED_SECRET` value platform-api
sends). The driver layer itself was never OLT-specific; it's a generic
`ConnectionParams` → `CliDriver` contract that applies equally to routers
and switches.

## Getting gearctl

```
go build -o gearctl ./gearctl   # from this repo
```

Or via Docker (multi-stage `Dockerfile`, static binary on
`golang:1.25-alpine`, runs on plain `alpine`):

```
docker build -t gearctl ./gearctl
```

Requires Go 1.25+ (`golang.org/x/crypto`'s SSH package needs it).

## Installation status

Not yet published anywhere with a stable public module path or a tagged
release — see the project tracker. Build from source for now.

## Usage

### CLI mode (default — no server involved)

```
gearctl -host <ip> -username <user> -password <pass> [flags] -cmd "<command>" [-cmd "<command>" ...]
gearctl -host <ip> -username <user> -password <pass> [flags] -facts
gearctl -host <ip> -username <user> -password <pass> [flags] -interfaces
gearctl -host <ip> -username <user> -password <pass> [flags] -lldp-neighbors
gearctl -host <ip> -username <user> -password <pass> [flags] -config
gearctl -host <ip> -username <user> -password <pass> [flags] -merge-config <file> [-apply [-yes]]
gearctl -host <ip> -username <user> -password <pass> [flags] -merge-cmd "<command>" [-merge-cmd "<command>" ...] [-apply [-yes]]
```

`-cmd`, `-facts`, `-interfaces`, `-lldp-neighbors`, and `-config` can be
combined — pass 2+ of them together and gearctl opens **one** connection
and runs all requested ones over it, instead of paying a separate
connect cost per invocation (see [Combining modes / batching](#combining-modes--batching)).
At least one of these five, or exactly one of `-merge-config`/`-merge-cmd`,
is required. `-merge-config` and `-merge-cmd` are mutually exclusive of
everything — each other included — since their dry-run/confirm/apply flow
doesn't compose the same way a batch of read-only fetches does.

`gearctl -h` / `-help` / `--help` prints this same flag list plus usage
examples, and exits `0` (not treated as a usage error).

| Flag | Default | Meaning |
|---|---|---|
| `-host` | *(required)* | Device host/IP |
| `-port` | `22` | Device port |
| `-protocol` | `ssh` | `ssh` or `telnet` |
| `-vendor` | `""` | Free-text hint, e.g. `cisco` — selects the driver ([Vendor support](#vendor-support)) and gates enable-mode escalation |
| `-username` | *(required)* | Login username |
| `-password` | *(required)* | Login password. Falls back to `$OLT_CLI_PASSWORD` so it never has to appear in shell history |
| `-enable-password` | `""` | Enable/privileged-mode password, if the vendor needs one. Falls back to `$OLT_CLI_ENABLE_PASSWORD` |
| `-disable-paging` | `""` | Command sent first to disable output paging, e.g. `"terminal length 0"` (Cisco-style) or `"screen-length 0 temporary"` (Huawei) |
| `-connect-timeout-ms` | `10000` | Connect timeout |
| `-per-command-timeout-ms` | `15000` | Per-command timeout |
| `-cmd` | — | A command to run. Repeatable: `-cmd "show version" -cmd "show ip interface brief"` |
| `-facts` | `false` | Fetch structured facts (`GetFacts()`) instead of running `-cmd` commands. Fails immediately (no dial attempted) if the resolved vendor has no facts-capable driver |
| `-interfaces` | `false` | Fetch richer per-interface state (`GetInterfaces()`) — up/down, enabled, description, IP address, MTU, speed, MAC — instead of running `-cmd` commands. Richer than `-facts`'s bare `interface_list`. Read-only. Fails immediately (no dial attempted) if the resolved vendor has no interfaces-capable driver |
| `-lldp-neighbors` | `false` | Fetch LLDP topology (`GetLLDPNeighbors()`) — which local port sees which remote device/port — instead of running `-cmd` commands. Requires LLDP enabled on the device (`lldp run`); if it isn't, fails with a distinct error rather than a misleading empty result. Read-only. Fails immediately (no dial attempted) if the resolved vendor has no LLDP-capable driver |
| `-config` | `false` | Fetch the device's running configuration verbatim (`GetConfig()`) instead of running `-cmd` commands. Read-only. Fails immediately (no dial attempted) if the resolved vendor has no config-capable driver |
| `-merge-config` | `""` | Path to a candidate config file to **merge** onto the device (`LoadConfig()`) — see [Merging a config change](#merging-a-config-change) before using this. Fails immediately if the resolved vendor has no config-push-capable driver |
| `-merge-cmd` | — | A command to **merge** onto the device (`LoadMergeCommands()`) — the lighter, delta-only sibling of `-merge-config`, no candidate file needed. Repeatable: `-merge-cmd "interface GigabitEthernet1" -merge-cmd "description new-desc"`. See [Merging a config change](#merging-a-config-change) before using this. Fails immediately if the resolved vendor has no config-push-capable driver |
| `-apply` | `false` | Actually apply `-merge-config`'s/`-merge-cmd`'s candidate. Without it, either only shows a diff — nothing is written to the device |
| `-yes` | `false` | Skip `-apply`'s interactive confirmation prompt (for scripted use). Has no effect without `-apply` |
| `-json` | `false` | Print machine-readable JSON instead of human-readable text. Same `status`/`error.type`/`error.message`/`totalDurationMs` shape as the HTTP API, so a script parsing one can parse the other unchanged |
| `-serve` | `false` | Run as the HTTP API server instead (see below). Every other flag above is ignored in this mode |

**Exit codes:** `0` full success (a `-merge-config`/`-merge-cmd` dry run
without `-apply` is "success" — it did what was asked, which was only to
show a diff) · `1` connection/command/facts/interfaces/LLDP/config/merge
failure, or an `-apply` confirmation prompt explicitly declined · `2`
usage error — standard Unix scripting conventions.

**Examples:**

```bash
# Raw commands, human-readable
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -cmd "show version" -cmd "show ip interface brief"

# Structured facts, JSON
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -facts -json

# Per-interface state (up/down, enabled, description, IP, MTU, speed, MAC)
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -interfaces

# LLDP topology -- which local port sees which remote device/port
# (requires "lldp run" configured on the device)
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -lldp-neighbors

# Running configuration, verbatim
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -config

# Telnet, with a paging-disable command and a longer timeout
gearctl -host 10.0.0.1 -protocol telnet -username admin -password secret \
  -disable-paging "screen-length 0 temporary" -per-command-timeout-ms 30000 \
  -cmd "display ont info 0 1"

# Credential from env instead of a flag
OLT_CLI_PASSWORD=secret gearctl -host 10.0.0.1 -username admin \
  -cmd "show version"
```

### Combining modes / batching

`-cmd`/`-facts`/`-interfaces`/`-lldp-neighbors`/`-config` can be combined
in one invocation — gearctl opens a single connection, runs every
requested mode over it in a fixed order (facts, interfaces,
lldp-neighbors, config, cmd), then disconnects once:

```bash
# One connection, three sections in the output
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -facts -interfaces -config -json
```

**Why this exists:** a benchmark against NAPALM/Netmiko on the same real
device found NAPALM's session-reuse model (open once, run several
getters, close once) beats gearctl's original per-invocation
connect-then-close for any multi-fact scenario, purely on connection
setup/teardown overhead. The fix here is a **batch of independent
read-only fetches over one connection** — not connection pooling across
separate requests (see [Architecture](#architecture) for why pooling was
explicitly rejected: it would mean holding credential-comparison material
in memory to validate a later request against an already-open session, a
real deviation from "no credential ever outlives one request/invocation";
a batch instead stays within a single request/response lifecycle, so
nothing new is cached and nothing new is retained after it returns).

**Per-mode independence:** if the resolved vendor doesn't support one of
the requested modes, only *that* mode is reported as failed — the others
still run. In JSON output, a failed mode's error lands in the new
`modeErrors` map (keyed by mode name), separate from the top-level
`error` field (which is reserved for a failure that prevented connecting
at all):

```bash
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor huawei -facts -config -json
# facts and config both succeed or fail independently of one another;
# if huawei's driver doesn't implement one of them, only that one shows
# up under "modeErrors" -- the other's result is still populated.
```

`-merge-config`/`-merge-cmd` are **not** batchable with anything,
including each other — passing either alongside any of the five modes
above, or alongside one another, is a usage error (exit `2`). Their
dry-run/confirm/apply flow (and, for `-merge-cmd`, the hash-freshness
check between dry run and apply) doesn't compose with "run N read-only
fetches and report what happened to each."

The HTTP equivalent is `POST /v1/batch` — see [Server mode](#server-mode--serve).

### Session pooling

**HTTP (`-serve` mode) only — there is no CLI equivalent.** A CLI
invocation is its own OS process; there's no memory that outlives it for
a session to live in, no matter what's built here. `-cmd`/`-facts`/etc.
combined in one invocation (above) is as far as connection reuse can go
for the CLI — for the HTTP API, sessions cover the case batching
structurally can't: separate calls that arrive over time and don't know
about each other up front.

```bash
# 1. Open a session -- credentials used exactly once, right here.
curl -X POST http://localhost:8090/v1/session/open -H "X-Internal-Auth: ..." -d '{
  "host": "10.0.0.1", "port": 22, "protocol": "ssh", "vendor": "cisco",
  "credentials": {"username": "admin", "password": "secret"}
}'
# {"status":"success","sessionId":"S1wfAT...","vendor":"cisco","connectedAt":"...","totalDurationMs":485}

# 2. Use the token instead of credentials for as many calls as you want,
#    spread out over time -- every one of these runs against the SAME
#    already-open connection (same connectedAt in every response).
curl -X POST http://localhost:8090/v1/facts -H "X-Internal-Auth: ..." \
  -d '{"sessionId": "S1wfAT..."}'
curl -X POST http://localhost:8090/v1/interfaces -H "X-Internal-Auth: ..." \
  -d '{"sessionId": "S1wfAT..."}'
curl -X POST http://localhost:8090/v1/merge-cmd -H "X-Internal-Auth: ..." \
  -d '{"sessionId": "S1wfAT...", "commands": ["interface GigabitEthernet1", "description new"], "dryRun": true}'

# 3. Close it when you're done -- or just stop using it; an idle session
#    closes itself after 30s with no activity either way.
curl -X POST http://localhost:8090/v1/session/close -H "X-Internal-Auth: ..." \
  -d '{"sessionId": "S1wfAT..."}'
```

**Every** operation endpoint accepts `sessionId` as an alternative to
`host`/`port`/`protocol`/`vendor`/`credentials` — `/v1/execute`,
`/v1/facts`, `/v1/interfaces`, `/v1/lldp-neighbors`, `/v1/config`,
`/v1/merge-config`, and `/v1/merge-cmd` all work identically either way,
reads and writes both (config push included — deliberately, unlike
`/v1/batch`, which excludes it; see [Architecture](#architecture) for
why that's not a contradiction). `/v1/batch` itself does **not** accept a
`sessionId` — it already has its own one-connection-many-operations
model for the "I know all my operations up front" case; sessions are for
the case batching can't cover, not a replacement for it.

**Why a token, not a credential-hash pool.** See
[Architecture](#architecture) for the full reasoning — in short,
`POST /v1/session/open` is the ONLY place credentials are ever used; it
returns a fresh, high-entropy random ID (`crypto/rand`, never anything
derived from the password) with no way to reconstruct or verify a
password from it. Every later call needs only the token, so nothing
credential-shaped has to be cached anywhere for the token to be reusable
— a leaked token exposes "someone can drive this one connection for a
little while," never anything about the underlying password.

**Idle timeout: 30 seconds.** A session with no activity for 30s is
closed and disconnected automatically, same as an explicit
`POST /v1/session/close` — the connection doesn't linger indefinitely
just because nobody remembered to close it. Every successful operation
against a session resets its idle clock; only genuine inactivity counts.

**Concurrency: one operation at a time, per session.** A device CLI
session has exactly one prompt to read — it cannot safely run two
commands at once. A second request arriving while a session is mid-operation
waits briefly (default up to 5s) for the first to finish; if it's still
busy after that, the second request gets a `session_busy` error rather
than corrupting output by racing the first. This is a per-session limit,
not a server-wide one — unrelated sessions run fully in parallel.

**Failure modes are explicit, not silent:**
- `session_not_found` — the ID doesn't match any currently-open session
  (never existed, already closed, or idled out). Every operation
  endpoint reports this the same way a fresh-connect call reports a bad
  host.
- `session_busy` — the session exists but is currently running another
  operation and didn't free up within the wait.

Closing an unknown/already-closed session is also reported as
`session_not_found`, not silently treated as success — see
`SessionCloseRequest`'s doc comment.

See `internal/sessionpool`'s package doc comment for the full
implementation (the lock-with-timeout design that avoids leaking a
goroutine on a timed-out acquire, the idle-reaper loop, and why
`Store.Open` never dials anything itself — it only ever registers a
connection the caller already established).

### Merging a config change

**Read this before using `-merge-config` or `-merge-cmd` — either writes
to a live device.** See [Vendor support](#vendor-support) for which
vendors implement them.

`candidate.cfg` should be a full config, typically a prior `-config`
output, edited — diffing a small snippet against the full running config
would just show most of the device's real config as "removed" noise, not
a meaningful diff.

**This is a merge, not a replace.** Applying only sends the lines in
`candidate.cfg` into `configure terminal`; it never removes a line that's
in the CURRENT config but absent from your candidate. If you deleted an
`access-list permit` line from `candidate.cfg` expecting it gone from the
device, it won't be — the diff will show it as removed for your review,
but removing a line from the diff never translates into a `no ...` command
on the device. The disclaimer is printed with every diff for this reason.

```bash
# 1. Get the current config, edit it into candidate.cfg
gearctl -host 10.0.0.1 -username admin -password secret -vendor cisco -config > current.cfg
cp current.cfg candidate.cfg
# ... edit candidate.cfg ...

# 2. Dry run -- shows the diff, writes nothing
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -merge-config candidate.cfg

# 3. Apply -- shows the diff again, asks "Type \"apply\" to confirm:"
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -merge-config candidate.cfg -apply

# Scripted/non-interactive apply -- skips the confirmation prompt
gearctl -host 10.0.0.1 -username admin -password secret \
  -vendor cisco -merge-config candidate.cfg -apply -yes
```

**What's refused outright, not attempted:** candidates containing
`banner ... ^C`, `crypto pki certificate chain ...`, or
`crypto key generate ...` — these open multi-line, terminator-delimited
blocks that can't be safely replayed one line at a time. gearctl detects
these and refuses before ever connecting, rather than risk hanging the
session or misapplying.

**What can still go wrong** (not solved by this feature, know before you
apply):
- **You can lock yourself out.** A candidate line that changes the
  addressing, ACL, VTY, or AAA config your own SSH session depends on can
  cut your access mid-apply. Verify you have out-of-band access (console,
  a second session) before applying anything that touches these.
- **No automatic rollback.** There's no IOS-XE `configure confirm`/
  revert-timer safety net here — if a change breaks reachability, you are
  responsible for recovering it. Verify reachability yourself after
  applying.
- **Deeply nested sub-modes** (`router bgp` → `address-family`) may not
  always cleanly exit when replayed line-by-line the way `interface X` →
  `interface Y` does — review the diff carefully for config in these
  sections.
- If the device rejects a line mid-apply (e.g. a real syntax error),
  gearctl stops immediately, exits config mode cleanly, and reports
  exactly which line and how many were applied before that — it does not
  attempt the remaining lines.
- If the connection drops mid-apply, gearctl reports that distinctly (and
  does *not* attempt to exit config mode, since the session state is
  unknown at that point) — re-run `-config` to see what actually landed
  before retrying.
- Between your dry run and your apply, if anything else changes the
  device's config, the apply is refused (the freshness/hash check) —
  re-run the dry run and review the new diff.

**Manual test checklist** (the apply path is mock-tested, not yet
verified against real hardware — do this before trusting it against
production gear):

1. A trivial, reversible change (e.g. an interface description) — dry
   run, review the diff, apply, then `-config` again to confirm it landed.
2. A deliberately invalid candidate line — confirm apply stops
   immediately, config mode is exited cleanly (device responds normally
   to the next command), and the error names the right line.
3. If feasible: a deliberately interrupted connection mid-apply — confirm
   gearctl reports the connection-drop case distinctly from a rejection,
   and does not claim more lines were applied than actually were.

#### `-merge-cmd`: pushing just the commands that changed

`-merge-config` needs a full candidate file — fine for a broad change,
heavyweight for "just update this one description." `-merge-cmd` is the
lighter path: give it only the commands you'd type live, and it stages
them through the exact same safe flow (dry-run diff, `-apply`/`-yes`/
confirmation, hash-freshness check, one-line-at-a-time apply with
rejection detection):

```bash
# Dry run -- shows a projected before/after diff, writes nothing
gearctl -host 10.0.0.1 -username admin -password secret -vendor cisco \
  -merge-cmd "interface GigabitEthernet1" -merge-cmd "description new-desc"

# Apply -- shows the diff again, asks "Type \"apply\" to confirm:"
gearctl -host 10.0.0.1 -username admin -password secret -vendor cisco \
  -merge-cmd "interface GigabitEthernet1" -merge-cmd "description new-desc" -apply
```

**The diff is a projected preview, not a literal text diff of your
input.** A delta like `interface GigabitEthernet1` / `description
new-desc` isn't a full config, so diffing it directly against the live
running-config would be meaningless — nearly everything would show as
"removed" noise. Instead, gearctl fetches the current config, projects
what it would look like *after* your commands are applied (replacing an
existing `description` line in place if one exists in that stanza,
appending it if not, honoring `no <command>` as a removal of the matching
existing line, tracking which `interface`/`router`/`line` context is
"open" as it walks your commands in order), and diffs *that projection*
against the current config. This is what makes it possible to accurately
show a replace as a replace instead of hiding it as an unrelated
addition.

**This is a heuristic, not real Cisco command-grammar parsing** — every
`-merge-cmd` diff carries this disclaimer unconditionally, not only when
a replacement happens to be detected. It matches existing lines by first
token (first *two* tokens for `ip ...` commands specifically, since `ip
address`/`ip helper-address`/`ip access-group`/`ip nat`/... are all
genuinely different commands sharing an overloaded prefix) within the
same config stanza. It can miss replacements for less common command
shapes, or attribute a context-less command (typed with no preceding
`interface`/`router`/`line`) to the wrong place if the current config has
no matching global-level line either — in that case it's shown as a plain
addition, not flagged. **Review the diff carefully before applying,
especially for anything that isn't a straightforward
description/address/state-style change.**

**The projection is preview-only.** Applying never replays the
projection — it always sends your literal `-merge-cmd` commands, one at a
time, exactly like `-merge-config`'s apply path does with candidate
lines. A bug in the projection can only produce a misleading preview,
never an incorrect command sent to the device.

**Undoing a change.** Every apply prints a ready-to-copy "how to undo
this" hint:

```
Applied.

To undo this change, re-run with:
  -merge-cmd "interface GigabitEthernet1"
  -merge-cmd "description old-desc"
```

This comes from the same reconciliation the projection already does —
for each command you sent, it knows exactly what it replaced, added, or
removed, and inverting that is well-defined (re-establish what was
replaced/removed exactly as it was; `no <cmd>` whatever was added). It's
also in `-json` output as `reverseCommands` (see `POST /v1/merge-cmd`
below) — the vendor-agnostic building block for a caller wanting "commit
confirmed" semantics without any Cisco-specific device feature: apply,
start your own timer, and if not explicitly confirmed within N minutes,
merge these commands to revert. `-merge-config` has no equivalent (a
full-candidate text diff has no per-command operation log to invert
from).

Prefer `-merge-config` over `-merge-cmd` when: the change is broad enough
that reviewing a handful of delta commands in isolation (without their
surrounding context) would be hard to reason about, or when you want to
diff against a candidate you already have as a file (e.g. generated
elsewhere, or a known-good config to reconcile toward).

**Verified live** against a real Cisco ISRv (IOS-XE 17.01.01): adding a
description to a previously-bare interface, replacing it with a new
value (a clean `-old`/`+new` in-place pair, not the old line left
elsewhere plus the new one as an unrelated addition), and removing it via
`no description` — all three matched their dry-run diffs exactly, and
the surrounding ~7KB of config was untouched in every case (see
`TestProjectMergedConfigPreservesUnrelatedLineOrder` for the regression
test this live run caught before it did: an earlier version of the
projection reordered the entire file for a one-line change). A second
live round -- toggling the global, context-less `ip scp server enable` on
and off -- caught and fixed a real `no <rest>` removal bug (see
[Testing](#testing) for the full story: it matched the wrong `ip ...`
line before the fix, the wrong line was only ever in the *shown diff*,
never the actual device write). Same manual checklist below remains worth
re-running for anything not yet exercised live (the `ip ...` two-token
case at stanza scope specifically -- the global case is now covered by
the bug above -- and a rejected line mid-apply):

1. A trivial, reversible change (e.g. an interface description) — dry
   run, confirm the projected diff shows a clean `-old`/`+new` pair in
   place (not the old line unchanged elsewhere plus the new line as an
   unrelated addition), apply, then `-config` again to confirm the
   projected diff actually matched what landed on the device. **Done.**
2. The same change expressed as `-merge-cmd "interface X" -merge-cmd
   "no shutdown"` (or similar) — confirm `no <cmd>` removal projects
   correctly against an existing matching line. **Done** (at the global,
   context-less scope -- see the bug above; still worth trying at stanza
   scope with more than one same-first-token line present).
3. A deliberately invalid command — confirm apply stops immediately,
   config mode is exited cleanly, and the error names the right line
   (same rejection-detection path `-merge-config` uses).

**Compared against NAPALM's own config-merge path** (`napalm-bench/bench_merge.py`,
same real ISR): NAPALM's `load_merge_candidate()` doesn't accept a bare
delta the way `-merge-cmd` does -- it SCPs a config *file* to the
device's flash and applies it via IOS's own `configure replace ...
merge`, which needs `ip scp server enable` configured on the device
first (a real operational prerequisite `-merge-cmd` has none of, since it
never leaves the SSH session or writes to flash). More notably, its
`compare_config()` diff for the exact same replace-a-description case
this project's own design review anticipated turned out exactly as
predicted: since NAPALM diffs the delta candidate directly against the
running config rather than projecting a merged result first, the diff
showed the *entire* small candidate snippet as a clean addition (`+
interface GigabitEthernet1` / `+ description ...-REPLACED`) with no
indication at all that an existing description was being overwritten --
the same "naive diff" failure mode `-merge-cmd`'s projection step exists
specifically to avoid. Its `no description` case was worse still: the
diff showed `+interface GigabitEthernet1` / `-no description`, which
doesn't meaningfully describe what changes on the device at all. Both
tools' *actual applies* worked correctly in this test; the difference is
entirely in what the operator sees before confirming.

### Server mode (`-serve`)

```
GEARCTL_SHARED_SECRET=<shared secret> gearctl -serve
```

Reads `PORT` (default `8090`) and `GEARCTL_SHARED_SECRET` (required) from
the environment.

`GET /health` — unauthenticated, `{"status":"ok"}`.

`POST /v1/execute` — requires `X-Internal-Auth: <shared secret>`. Runs
caller-supplied commands. Every field below (except `commands`) can be
replaced with a single `sessionId` from `POST /v1/session/open` instead
— see [Session pooling](#session-pooling) further down.

```json
// Request
{
  "host": "10.0.0.1", "port": 22, "protocol": "ssh", "vendor": "cisco",
  "credentials": {"username": "admin", "password": "secret"},
  "commands": ["show version"],
  "disablePagingCommand": "terminal length 0",
  "connectTimeoutMs": 10000, "perCommandTimeoutMs": 15000
}
// Equivalently, reusing an already-open session:
// {"sessionId": "S1wfATmmWwQ7GImXXJhUXZcH1RG2RunLkFnogsDnuqg", "commands": ["show version"]}
// Response
{
  "status": "success",
  "results": [{"command": "show version", "output": "...", "durationMs": 120, "truncated": false}],
  "error": null, "connectedAt": "2026-08-13T12:00:00Z", "totalDurationMs": 730
}
```

`POST /v1/facts` — requires `X-Internal-Auth`. Same request shape minus
`commands`; calls the resolved driver's `GetFacts()` instead.

```json
// Response
{
  "status": "success",
  "facts": {
    "vendor": "Cisco", "model": "ISRV", "hostname": "ISR", "fqdn": "ISR",
    "os_version": "17.01.01", "serial_number": "", "uptime": "18 minutes",
    "interface_list": ["GigabitEthernet1", "GigabitEthernet2", "..."]
  },
  "error": null, "connectedAt": "...", "totalDurationMs": 730
}
```

`POST /v1/interfaces` — requires `X-Internal-Auth`. Same request shape
minus `commands`; calls the resolved driver's `GetInterfaces()` instead —
richer per-interface state (keyed by interface name) than `/v1/facts`'s
bare `interface_list`. Read-only.

```json
// Response
{
  "status": "success",
  "interfaces": {
    "GigabitEthernet1": {
      "isUp": true, "isEnabled": true, "description": "",
      "ipAddress": "192.168.14.13/24",
      "mtu": 1500, "speedMbps": 1000, "macAddress": "5000.0001.0000"
    },
    "GigabitEthernet3": {
      "isUp": false, "isEnabled": false, "description": "", "ipAddress": "",
      "mtu": 1500, "speedMbps": 1000, "macAddress": "5000.0001.0002"
    }
  },
  "error": null, "connectedAt": "...", "totalDurationMs": 530
}
```

`POST /v1/lldp-neighbors` — requires `X-Internal-Auth`. Same request
shape minus `commands`; calls the resolved driver's `GetLLDPNeighbors()`
instead — topology discovery, keyed by LOCAL interface name. Requires
LLDP enabled on the device (`lldp run`); if it isn't, the device's own
"% LLDP is not enabled" reply is surfaced as a distinct `validation`
error rather than a misleadingly-empty `neighbors: {}`. Read-only.

```json
// Response (zero neighbors -- still a well-formed success, not an error)
{
  "status": "success",
  "neighbors": {},
  "error": null, "connectedAt": "...", "totalDurationMs": 540
}
// Response (a neighbor seen on Gi1)
{
  "status": "success",
  "neighbors": {
    "Gi1": [{"remoteSystemName": "switch1.example.com", "remotePortId": "Gi0/1"}]
  },
  "error": null, "connectedAt": "...", "totalDurationMs": 540
}
```

`POST /v1/config` — requires `X-Internal-Auth`. Same request shape minus
`commands`; calls the resolved driver's `GetConfig()` and returns the raw
running configuration verbatim (no parsing). Read-only — there is no
config-push route.

```json
// Response
{
  "status": "success",
  "config": "version 17.1\nhostname ISR\ninterface GigabitEthernet1\n ...",
  "error": null, "connectedAt": "...", "totalDurationMs": 730
}
```

`POST /v1/merge-config` — requires `X-Internal-Auth`. Same request shape
minus `commands`, plus `candidate`/`dryRun`/`expectedConfigHash`. Calls
the resolved driver's `LoadConfig()` — **read [Merging a config
change](#merging-a-config-change) before using this, it writes to a live
device.**

`dryRun` is a **required** field (`true`/`false`, no implicit default —
omitting it is a validation error, not treated as either value). Call
with `dryRun: true` first:

```json
// Request
{
  "host": "10.0.0.1", "port": 22, "protocol": "ssh", "vendor": "cisco",
  "credentials": {"username": "admin", "password": "secret"},
  "candidate": "hostname R1\ninterface GigabitEthernet1\n description new\n...",
  "dryRun": true
}
// Response
{
  "status": "success",
  "diff": "NOTE: this is a merge/overlay diff... \n--- current running-config\n+++ candidate\n...",
  "configHash": "3f8a9c...", "applied": false,
  "error": null, "connectedAt": "...", "totalDurationMs": 610
}
```

Then call again with `dryRun: false` and `expectedConfigHash` set to the
`configHash` the dry run returned — this is the freshness check: if the
device's live config no longer matches that hash (it changed since your
dry run), the apply is refused, not attempted:

```json
// Request
{
  "host": "10.0.0.1", "port": 22, "protocol": "ssh", "vendor": "cisco",
  "credentials": {"username": "admin", "password": "secret"},
  "candidate": "hostname R1\n...", "dryRun": false,
  "expectedConfigHash": "3f8a9c..."
}
// Response (success)
{
  "status": "success",
  "diff": "...", "configHash": "3f8a9c...", "applied": true,
  "error": null, "connectedAt": "...", "totalDurationMs": 1840
}
```

`status: "success"` means *this call* completed as expected — for a dry
run, that's true even though nothing was written. **Check `applied`, not
`status`, to know whether a device write actually happened.**
`diff`/`configHash` are populated even on a failed apply attempt
(rejection, connection drop, stale hash) when `LoadConfig` got far enough
to compute them — they're the record of what was being proposed
regardless of outcome.

`POST /v1/merge-cmd` — requires `X-Internal-Auth`. `LoadConfig`'s
delta-based sibling: same request shape minus `commands`/`candidate`,
plus `commands: string[]` (the commands to merge — not a full config) and
the same `dryRun`/`expectedConfigHash` contract. Calls the resolved
driver's `LoadMergeCommands()` — **read [the `-merge-cmd`
subsection](#-merge-cmd-pushing-just-the-commands-that-changed) before
using this, it writes to a live device, and its diff is a projected
preview, not a literal diff of the request body.** Response shape is
`/v1/merge-config`'s (`diff`/`configHash`/`applied`) plus one field
`/v1/merge-config` doesn't have — `reverseCommands`:

```json
// Request (dry run)
{
  "host": "10.0.0.1", "port": 22, "protocol": "ssh", "vendor": "cisco",
  "credentials": {"username": "admin", "password": "secret"},
  "commands": ["interface GigabitEthernet1", "description new-desc"],
  "dryRun": true
}
// Response
{
  "status": "success",
  "diff": "NOTE: this is a projected preview... \n--- current running-config\n+++ projected\n...",
  "configHash": "3f8a9c...",
  "reverseCommands": ["interface GigabitEthernet1", "description old-desc"],
  "applied": false,
  "error": null, "connectedAt": "...", "totalDurationMs": 610
}
```

`reverseCommands` — the commands that would undo this exact merge (here,
the interface's description as it was *before* this change), derived
from the same per-command reconciliation the projection already does. It
exists as the vendor-agnostic building block for a caller wanting
"commit confirmed" semantics: apply, start your own timer, and if not
explicitly confirmed within N minutes, `POST /v1/merge-cmd` again with
`commands` set to `reverseCommands` to revert — going through the exact
same dry-run/hash-freshness/apply safety machinery as any other merge.
gearctl itself stays stateless (see [Architecture](#architecture)) — the
timer and the decision to actually replay it belong to the caller (e.g.
`platform-api`), not gearctl. This is deliberately **not** the same thing
as Cisco's native `configure revert timer`: that requires the `archive`
feature configured on the device (a real, Cisco-specific prerequisite);
`reverseCommands` needs nothing from the device at all, and works for any
vendor `LoadMergeCommands` supports. Populated on every call — dry run or
apply — same as `diff`/`configHash`, so it can be reviewed before ever
applying. `null` if there was nothing to reconcile (e.g. every command
was a `no ...` that matched nothing). `/v1/merge-config` never sets this
field — see `LoadConfig`'s doc comment for why a full-candidate text diff
has no per-command operation log to invert from.

Same two-call dry-run-then-apply pattern as `/v1/merge-config`: call
again with `dryRun: false` and `expectedConfigHash` echoed back from the
dry run's `configHash`.

`POST /v1/batch` — requires `X-Internal-Auth`. Same request shape minus
`commands`, plus `operations: []{type, commands?}` (1-10 entries).
`type` is one of `facts`/`interfaces`/`lldp-neighbors`/`config`/`execute`
(`execute`'s `commands` field is required and follows `/v1/execute`'s own
1-50/non-empty rules; every other type ignores `commands`). Runs every
operation over **one** connection, in request order, then closes it once
— this is the fix for the "NAPALM/Netmiko can amortize connection setup
across getters in one session, gearctl's other endpoints couldn't"
benchmark finding (see [Combining modes / batching](#combining-modes--batching)
and [Architecture](#architecture)), without changing gearctl's stateless
design at all: still bounded by one request/response lifecycle, no
session persists past it, no credentials held anywhere once it returns.
Config *push* (`-merge-config`/`-merge-cmd`) is deliberately not a valid
`type` — their dry-run/hash-freshness/interactive-confirm flow doesn't
compose with "run N things and report what happened to each."

```json
// Request
{
  "host": "10.0.0.1", "port": 22, "protocol": "ssh", "vendor": "cisco",
  "credentials": {"username": "admin", "password": "secret"},
  "operations": [
    {"type": "facts"},
    {"type": "interfaces"},
    {"type": "execute", "commands": ["show clock"]}
  ]
}
// Response
{
  "status": "success",
  "results": [
    {"type": "facts", "status": "success", "facts": {"...": "..."}, "durationMs": 108},
    {"type": "interfaces", "status": "success", "interfaces": {"...": "..."}, "durationMs": 65},
    {"type": "execute", "status": "success", "results": [{"command": "show clock", "output": "...", "durationMs": 40, "truncated": false}], "durationMs": 40}
  ],
  "error": null, "connectedAt": "...", "totalDurationMs": 719
}
```

**Per-operation independence:** a vendor missing the capability one
operation needs doesn't fail the whole batch — only that
`BatchOperationResult` gets `status: "failed"` and `error.type:
"unsupported"` (checked *before* connecting, same fail-fast posture as
`/v1/facts`/`/v1/interfaces`/etc. individually); every other requested
operation still runs if the driver supports it:

```json
// Request against a vendor with no interfaces-capable driver
{
  "host": "10.0.0.1", "port": 22, "protocol": "ssh", "vendor": "huawei",
  "credentials": {"username": "admin", "password": "secret"},
  "operations": [{"type": "facts"}, {"type": "interfaces"}]
}
// Response -- facts still ran; only interfaces was unsupported
{
  "status": "partial",
  "results": [
    {"type": "facts", "status": "success", "facts": {"...": "..."}, "durationMs": 95},
    {"type": "interfaces", "status": "failed", "error": {"type": "unsupported", "message": "vendor \"huawei\" has no interfaces-capable driver"}, "durationMs": 0}
  ],
  "error": null, "connectedAt": "...", "totalDurationMs": 95
}
```

If **every** requested operation is unsupported, gearctl never dials at
all — `status: "failed"`, `connectedAt: null`, and every result already
carries its own `unsupported` error. `Error` at the top level is reserved
for a request that never got far enough to attempt any operation
(malformed JSON, validation failure, disallowed target) — distinct from
each operation's own `error`, which is scoped to just that operation.

`POST /v1/session/open` — requires `X-Internal-Auth`. Same request shape
as `/v1/execute` minus `commands` (and `sessionId` must NOT be set — see
[Session pooling](#session-pooling)). Dials a connection exactly like
every other endpoint's fresh-connect path, but instead of running one
operation and closing it, registers the connection in gearctl's session
pool and returns a token to use instead of credentials on later calls.

```json
// Request
{
  "host": "10.0.0.1", "port": 22, "protocol": "ssh", "vendor": "cisco",
  "credentials": {"username": "admin", "password": "secret"}
}
// Response
{
  "status": "success",
  "sessionId": "S1wfATmmWwQ7GImXXJhUXZcH1RG2RunLkFnogsDnuqg",
  "vendor": "cisco",
  "error": null, "connectedAt": "...", "totalDurationMs": 485
}
```

`sessionId` is a 256-bit random token (`crypto/rand`) — not derived from
the credentials in any way, and the credentials themselves are never
stored past this one call. Pass it as every other endpoint's `sessionId`
field instead of `host`/`credentials`/etc. See [Session
pooling](#session-pooling) for the idle-timeout/concurrency/error-mode
details.

`POST /v1/session/close` — requires `X-Internal-Auth`. Closes and
disconnects a pooled session immediately, rather than waiting for it to
idle out.

```json
// Request
{"sessionId": "S1wfATmmWwQ7GImXXJhUXZcH1RG2RunLkFnogsDnuqg"}
// Response
{"status": "success", "error": null, "totalDurationMs": 0}
```

Closing an unknown, already-closed, or already-idled-out session reports
`error.type: "session_not_found"`, not a silent success — a caller
relying on this to free a resource deserves to know if there was nothing
to free.

**Every** operation endpoint (`/v1/execute`, `/v1/facts`,
`/v1/interfaces`, `/v1/lldp-neighbors`, `/v1/config`, `/v1/merge-config`,
`/v1/merge-cmd` — **not** `/v1/batch`, see [Session
pooling](#session-pooling)) accepts `sessionId` as a top-level field,
replacing `host`/`port`/`protocol`/`vendor`/`credentials` entirely:

```json
// Instead of the usual host/port/protocol/credentials shape:
{"sessionId": "S1wfATmmWwQ7GImXXJhUXZcH1RG2RunLkFnogsDnuqg"}
// or, for /v1/execute:
{"sessionId": "S1wfATmmWwQ7GImXXJhUXZcH1RG2RunLkFnogsDnuqg", "commands": ["show version"]}
```

`status` is one of `success` (all commands/facts/interfaces/config/
merge-attempt calls completed as expected), `partial` (some commands or
batch operations failed — `/v1/execute` and `/v1/batch` only), or
`failed`.

`error.type` is one of:

| Type | Meaning |
|---|---|
| `validation` | Malformed request, the target host isn't allowed (see [Security](#security)), a `-merge-config`/`-merge-cmd` candidate contains a refused multi-line directive, `expectedConfigHash` is missing/stale, or the device rejected a candidate line mid-apply |
| `auth` | Login/enable-password rejected by the device |
| `timeout` | No response within the configured timeout |
| `connection` | Couldn't reach the device (dial failure, refused, etc.), or the connection dropped mid-`/v1/merge-config`/`/v1/merge-cmd` apply (see [Merging a config change](#merging-a-config-change)) |
| `protocol` | Something went wrong at the SSH/Telnet/session level |
| `unsupported` | *(`/v1/facts`, `/v1/interfaces`, `/v1/lldp-neighbors`, `/v1/config`, `/v1/merge-config`, `/v1/merge-cmd` only)* The resolved vendor has no `FactsProvider`/`InterfaceProvider`/`LLDPNeighborsProvider`/`ConfigGetter`/`ConfigLoader`-capable driver. `/v1/batch` reports this per-operation (`results[i].error.type`), not at the top level — see [`POST /v1/batch`](#server-mode--serve) |
| `session_not_found` | *(any endpoint accepting `sessionId`, plus `/v1/session/close`)* No currently-open session matches the given `sessionId` — never existed, already closed, or idled out (see [Session pooling](#session-pooling)) |
| `session_busy` | *(any endpoint accepting `sessionId`)* The session exists but is running another operation and didn't free up within the wait — try again shortly |

## Vendor support

| Vendor hint | Driver | Raw commands | Structured facts (`GetFacts()`) | Per-interface state (`GetInterfaces()`) | LLDP topology (`GetLLDPNeighbors()`) | Config retrieval (`GetConfig()`) | Full-config merge (`LoadConfig()`) | Delta merge (`LoadMergeCommands()`) |
|---|---|---|---|---|---|---|---|---|
| `cisco` | `CiscoIOSDriver` | ✅ | ✅ — verified live against a real Cisco ISRv (IOS-XE 17.01.01) | ✅ — verified live (`show interfaces`, up/administratively-down/description/IP-address/MTU/speed/MAC all confirmed against real output) | ✅ — header/trailer/zero-neighbor case verified live after enabling `lldp run`; **populated-neighbor row parsing built against the documented format only, not yet confirmed against a real neighbor device (no second LLDP-speaking device available in this lab)** | ✅ — `show running-config`, verbatim | ✅ — dry run only, mock-tested; **apply path not yet verified against real hardware, see [Merging a config change](#merging-a-config-change)** | ✅ — dry run AND apply both verified live (add/replace/`no`-removal on a real interface description); the diff is a projected preview, see [the `-merge-cmd` subsection](#-merge-cmd-pushing-just-the-commands-that-changed) |
| `juniper` | `JuniperDriver` | ✅ — verified live against a real Juniper vMX (Junos 18.2R1.9): root SSH lands in the FreeBSD shell (`disablePagingCommand: "cli"` enters the Junos CLI), a paginated `show version \| display json` came back complete and valid (see [Pagination](#pagination) and [PTY size and large-response completeness](#pty-size-and-large-response-completeness) for the real bugs this surfaced and fixed) | ✅ — verified live (`show system information`/`show system uptime`/`show interfaces terse`, all via `\| display json`; model/os_version/serial_number/uptime/interface_list all confirmed against real output — see [juniperdriver.go](internal/drivers/juniperdriver.go)) | ✅ — verified live (`show interfaces terse` + `show interfaces`, both via `\| display json`; up/down, admin-enabled, MTU, speed, MAC, IP address all confirmed against real output; description confirmed live by deliberately setting one on a real interface, capturing the resulting JSON, then removing it again; 27/27 consecutive live `GetInterfaces()` calls succeeded after the PTY-size and completion-aware-wait fixes, see [PTY size and large-response completeness](#pty-size-and-large-response-completeness)) | ❌ — not implemented yet | ✅ — verified live, `show configuration`, verbatim (Junos's hierarchical curly-brace format, not `\| display json`) | ✅ — verified live, both dry-run AND apply (`configure private` → delta lines → Junos's own `show \| compare` diff → `commit check` → `commit`; a real committed interface-description change, verified present on the device, then reverted via `rollback 1`); candidate must be Junos's flat `set`/`delete` form, NOT `GetConfig()`'s own curly-brace output — see [Config push for Junos](#config-push-for-junos) | ✅ — same apply path as `LoadConfig()`, verified live the same way; `reverseCommands` is always exactly `["rollback 1"]`, Junos's own native undo, see [Config push for Junos](#config-push-for-junos) |
| `extreme` / `exos` | `ExtremeDriver` (ExtremeXOS/EXOS only — NOT VOSS/Fabric Engine, which has no driver yet) | ✅ | ✅ — verified live against a real EXOS-VM (ExtremeXOS 32.7.2.19): `show version`/`show switch`/`show ports information`; model/hostname/os_version/uptime/interface_list confirmed against real output, serial_number confirmed to correctly stay empty on this VM (no `SN:` token on its Switch line — regression-tested against wrongly matching a PSU component's own serial instead, see [extremedriver.go](internal/drivers/extremedriver.go)) | ✅ — verified live (`show ports information` admin-enabled/link-state, `show ports description`; **only the link-DOWN case was observed live — this lab had no cabled neighbor to confirm the "up" link-state word/casing, or MTU/speed/duplex/MAC/IP, which are deliberately left unpopulated rather than guessed, see the doc comment on `GetInterfaces()`**) | ✅ — confirmed live that zero neighbors means `show lldp neighbors` prints nothing at all (no header/trailer, unlike Cisco); **populated-neighbor row parsing built against documented format only, not confirmed against a real neighbor device** | ✅ — `show configuration`, verbatim; **note this is EXOS's "diff from factory default" script, not a full effective-config dump the way Cisco's/Junos's is — see the doc comment on `GetConfig()`** | ❌ — not implemented yet: EXOS's flat, non-modal config needs its own reconciliation design, not a port of `CiscoIOSDriver`'s indentation-based stanza logic, see [extremedriver.go](internal/drivers/extremedriver.go) | ❌ — not implemented yet, same reason as `LoadConfig()` |
| anything else (including `""`, `huawei`, `zte`, `nokia`, unknown vendors) | `RawDriver` | ✅ | ❌ — `error.type: "unsupported"` on `/v1/facts` | ❌ — `error.type: "unsupported"` on `/v1/interfaces` | ❌ — `error.type: "unsupported"` on `/v1/lldp-neighbors` | ❌ — `error.type: "unsupported"` on `/v1/config` | ❌ — `error.type: "unsupported"` on `/v1/merge-config` | ❌ — `error.type: "unsupported"` on `/v1/merge-cmd` |

Raw command execution works against **any** vendor regardless of this
table — only `GetFacts()`/`GetInterfaces()`/`GetLLDPNeighbors()`/
`GetConfig()`/`LoadConfig()`/`LoadMergeCommands()` are vendor-specific.
See [Adding a vendor driver](#adding-a-vendor-driver) to add more. Config
*replace* (an
atomic, non-merge alternative to `LoadConfig()`) isn't implemented by any
driver, and isn't exposed via CLI or HTTP API.

`cisco` and `arista` are also in the enable-mode escalation allowlist
(`internal/transport/enablevendors.go`) — vendors confirmed to gate
privileged commands behind a separate `enable` step. Huawei/Junos-lineage
CLIs have no such concept and are correctly skipped.

## Troubleshooting

- **`error.type: "unsupported"` on `/v1/facts` or `/v1/config`** — the
  vendor hint you passed has no facts/config-capable driver yet (see the
  table above). Use `/v1/execute` with raw commands instead, or add a
  driver.
- **`error.type: "timeout"`, zero output** — device never responded, or
  is silently paginating (`--More--`-style) without `disablePagingCommand`
  set. Try setting it explicitly.
- **`error.type: "auth"` after "enable"** — the vendor is in the
  enable-escalation allowlist and the device asked for a password, but
  `-enable-password`/`enablePassword` wasn't provided.
- **CLI mode: "-password is required"** — set `-password` or
  `$OLT_CLI_PASSWORD`.
- **Slow/hanging `docker compose build`** — if you're building the *core*
  ACS services (not gearctl) from the repo root and it's transferring
  gigabytes of build context, check `.dockerignore` at the repo root
  excludes `platform/`, `debug-logs/`, etc. Not a gearctl-specific issue.

## Architecture

This service holds **no database, no credential storage, no memory of
past runs** — everything needed for one command run arrives in the
request body (or CLI flags), and nothing about *who you are* is ever
persisted here. The one deliberate exception is [session
pooling](#session-pooling) below: an open, already-authenticated
*connection* can outlive the single request that created it, for up to
30 seconds of inactivity — but never the credentials that authenticated
it, which are used exactly once and then discarded. A caller like
`platform-api` (specifically its `modules/olt-cli/` module) is expected
to own identity/credentials/run-history itself and call this service
per-request (or per-session).

```
caller --POST /v1/execute (X-Internal-Auth)--> gearctl --SSH/Telnet--> device
```

**Two connection-reuse features, layered on for two different
shapes of the same underlying problem.** A benchmark against
NAPALM/Netmiko on the same real device (`napalm-bench/`, a standalone
spin-off container, not part of gearctl itself) found NAPALM's
session-reuse model — open once, run several getters, close once — beat
gearctl's original per-call connect-then-close for any multi-fact
scenario, purely on connection setup/teardown overhead.

The first fix, batching (`runBatchModes` in CLI `main.go`, `BatchHandler`
backing `POST /v1/batch`), covers the case where a caller knows every
operation it wants *up front*, in one call: one connection, multiple
*independent, read-only* operations run against it, all within a single
request/response (or single CLI invocation)'s lifecycle — no session ID,
no idle timeout, no credential caching, nothing that outlives the call
that opened it. Config *push* (`-merge-config`/`-merge-cmd`) is
deliberately excluded — their dry-run/hash-freshness/interactive-confirm
flow doesn't compose with "run N things and report what happened to
each."

The second, [session pooling](#session-pooling), covers the case
batching structurally can't: separate calls arriving over time, that
don't know about each other ahead of time (a `-facts` call now, a
`-merge-cmd` two minutes later, from what might even be different
processes). Naively pooling a connection across genuinely separate
requests has an obvious-looking approach that was deliberately rejected:
reusing a pooled connection for a new request by validating that
request's credentials against whatever authenticated the pooled session
means holding some credential-comparison material (e.g. a password hash)
in memory for as long as the connection stays open — a real deviation
from "no credential ever outlives one request." Session pooling gets the
same reuse win without that tradeoff: `POST /v1/session/open` takes
credentials exactly once and returns a fresh, high-entropy random token
with zero mathematical relationship to the password — reuse afterward
requires only the token (a bearer-capability, like a database
connection-pool checkout), never the credentials again. See [Session
pooling](#session-pooling) for the full design and
`internal/sessionpool`'s package doc comment for the implementation.

### The driver seam (NAPALM-like)

`internal/drivers/driver.go` defines `CliDriver` — a contract every
vendor driver implements. `internal/drivers/basedriver.go`'s `BaseDriver`
holds the session-lifecycle boilerplate (Connect/RunCommands/Disconnect)
every driver needs; vendor drivers embed it (Go's answer to NAPALM
subclassing a shared base class) instead of copy-pasting it. `RawDriver`
(`internal/drivers/rawdriver.go`, `vendor: "*"`) is just `BaseDriver` plus
`Vendor()` — zero vendor knowledge, `Resolve()`'s fallback for any vendor
without a dedicated driver.

`CiscoIOSDriver` (`internal/drivers/ciscoiosdriver.go`, `vendor: "cisco"`)
is the first vendor-specific driver: also `BaseDriver` embedded, plus:

- `GetFacts()` (the `FactsProvider` optional-capability interface) —
  parses `show version`/`show ip interface brief` into structured facts
  (vendor, model, hostname, os_version, serial_number, uptime,
  interface_list). Verified live against a real Cisco ISRv (IOS-XE
  17.01.01); found and fixed one real parsing bug that unit tests alone
  didn't catch (a trailing device prompt line leaking into
  `interface_list` — see `TestParseInterfaceBriefSkipsTrailingPromptLine`).
- `GetInterfaces()` (the `InterfaceProvider` optional-capability
  interface) — runs `show interfaces` (the full per-interface output, not
  `show ip interface brief` — that alone doesn't carry MTU/speed/
  description/IP/MAC) and parses it into per-interface
  up/enabled/description/IP-address/MTU/speed/MAC state, the same job
  NAPALM's `get_interfaces()` getter does (IP address is extra --
  NAPALM keeps that in a separate getter, `get_interfaces_ip()`; folded
  in here since it's cheap to parse from the same output already being
  read). Verified live against a real Cisco ISRv (IOS-XE 17.01.01) across
  every real shape observed: up with an IP but no description, up with a
  description but no IP, and administratively down with neither.
- `GetLLDPNeighbors()` (the `LLDPNeighborsProvider` optional-capability
  interface) — runs `show lldp neighbors` and parses the table into
  local-interface → remote-system/remote-port entries, the same job
  NAPALM's `get_lldp_neighbors()` getter does. Requires LLDP enabled on
  the device (`lldp run`) -- surfaces the device's own "% LLDP is not
  enabled" reply as a distinct error rather than a misleadingly-empty
  result. The header line and zero-neighbor "Total entries displayed: 0"
  trailer are verified live against a real Cisco ISRv (IOS-XE 17.01.01,
  after enabling `lldp run`); the populated-neighbor row parsing is built
  against the documented format only -- this lab has no second
  LLDP-speaking device on the same segment to produce a real sample.
  Validate against a real neighbor before relying on it in production.
- `GetConfig()` (the `ConfigGetter` optional-capability interface) — runs
  `show running-config` and returns it verbatim, no parsing.
- `LoadConfig()` (the `ConfigLoader` optional-capability interface) —
  config *merge* (not replace, see [Merging a config
  change](#merging-a-config-change)): diffs the current config against a
  candidate, and on a confirmed apply, replays the candidate's lines one
  at a time inside `configure terminal` (one `RunCommands` call per line,
  deliberately — a single batched call can't fail-fast on a rejected
  line, since `BaseDriver.RunCommands` only stops on a transport-level
  error, not a semantic one; see `applyMergeConfig`'s doc comment).
  Refuses candidates containing multi-line directives (`banner`, `crypto
  pki certificate chain`, `crypto key generate`) outright rather than
  risk misapplying them. Gated behind a required dry-run-first,
  hash-checked apply (see `driver.go`'s `ConfigLoader` doc comment) — the
  apply path is mock-tested but **not yet verified against real
  hardware**, unlike the enable-escalation and pagination fixes elsewhere
  in this codebase.
- `LoadMergeCommands()` (`ConfigLoader`'s other method) — `LoadConfig()`'s
  delta-based sibling: takes just the commands that changed instead of a
  full candidate. Since a delta isn't a full config, diffing it directly
  against `current` would be meaningless (most of `current` would show as
  "removed" noise); instead it *projects* what the config would look like
  after applying the delta and diffs *that projection* against `current`
  via the same `computeUnifiedDiff` `LoadConfig()` uses. Crucially, the
  projection mutates a copy of `current`'s own lines in place (`locateLines`
  finds each target stanza's exact line range by indentation and
  re-derives it fresh after every command; `applyStanzaCommand`/
  `applyGlobalCommand` splice/replace/insert only the specific lines a
  delta command actually changes, tracking which `interface`/`router`/
  `line` context is "open" and reconciling each line against its target
  by first-token match key — first *two* tokens for `ip ...` commands —
  replacing in place, appending, or honoring `no <cmd>` as a removal)
  rather than reparsing and reserializing the whole file from a
  normalized model — a real-hardware finding caught exactly that failure
  mode in an earlier version: reserializing collapsed every global
  command into one block rendered first, so a single one-line change
  produced a diff of the *entire* config (see
  `TestProjectMergedConfigPreservesUnrelatedLineOrder`). This projection
  is still preview-only and heuristic, not real Cisco command-grammar
  parsing (see [the `-merge-cmd`
  subsection](#-merge-cmd-pushing-just-the-commands-that-changed) for its
  documented limits) — critically, apply never replays the projection,
  only the literal delta commands, one at a time, through the same
  already-tested `applyMergeConfig` `LoadConfig()` uses. That keeps
  the blast radius of any projection bug scoped to "misleading preview
  text," never "wrong command sent to the device." `applyStanzaCommand`/
  `applyGlobalCommand` also return a `mergeOperation` per command (what
  it replaced/added/removed) that `reverseCommands` walks in reverse to
  derive the commands that would undo the whole delta — see `POST
  /v1/merge-cmd`'s doc above for what it's for (a vendor-agnostic,
  no-device-prerequisites "commit confirmed" building block) and
  `TestProjectMergedConfigReverseCommandsRestoreOriginal` for the
  round-trip proof (apply a delta, apply its own reverseCommands, get
  back the exact original config).

Config *replace* (an atomic alternative to `LoadConfig()`'s merge
semantics) is not implemented by any driver -- kept as a conceptually
separate, not-yet-declared interface, since it's a meaningfully different
(and differently risky) operation.

#### Prompt prediction: why config-mode commands used to be much slower

`applyMergeConfig` sends `"configure terminal"`, each delta line, and
`"end"` one at a time (see its own doc comment for why one at a time, not
batched). Three of those -- entering config mode, an
`interface`/`router`/`line` context opener, and `end` -- change the
device's prompt (e.g. `ISR#` → `ISR(config)#` → `ISR(config-if)#` →
back to `ISR#`). gearctl's transport layer has no vendor-specific prompt
grammar of its own (see [Prompt-return detection](#prompt-return-detection)
below): when a command changes the prompt, the fast substring-match path
can't find the OLD (now-stale) prompt text in the response, so it falls
back to waiting out the full `commandQuiescenceMs` (2 seconds) of silence
before concluding the device is done -- **on every single mode-changing
command**. For a typical `-merge-cmd` apply (`configure terminal` +
`interface X` + one child line + `end`), that's up to 3 unnecessary
2-second waits stacked on top of what should be a sub-second round trip.
This was the real, measured cause behind `-merge-cmd`'s apply times
looking slow next to NAPALM's in the [comparison
report](https://claude.ai/code/artifact/b15e5f24-9cb0-4c6b-b0dc-3c3fab452c3a) --
found by actually profiling it after being asked "why is ours slower?",
not guessed at.

The fix: `CiscoIOSDriver.predictNextPrompt(currentPrompt, cmd)` knows
Cisco's own prompt grammar well enough to guess the *upcoming* prompt for
exactly these three transitions (deriving the hostname from whatever the
device's prompt looks like right now, via the new
`ShellSession.CurrentPrompt()`), and passes that guess to a new
`SendAndWaitForPromptHint` -- the SAME fast substring-match path a
steady-prompt command already gets, just given the right prompt to look
for instead of a stale one. A wrong guess is never unsafe: if the hint
doesn't actually appear, `waitForPrompt`'s existing quiescence fallback
still fires and self-corrects exactly as if no hint had been given at
all (see `TestStreamShellSessionWrongHintStillSelfCorrectsViaQuiescence`)
-- this is purely a speed optimization with a safety net, not a new way
to get the wrong answer faster.

**Verified live** against the real Cisco ISRv: the same add/replace/remove
interface-description sequence from the NAPALM comparison, averaged over
3 runs each, before and after this fix:

| Step | Before (no hint) | After (with hint) |
|---|---|---|
| Add | 9,216 ms | 5,161 ms |
| Replace | 9,059 ms | 5,145 ms |
| Remove | 7,016 ms | 3,058 ms |

**gearctl's `-merge-cmd` now beats NAPALM's equivalent merge
(8,297 / 5,452 / 5,478 ms) on all three steps** -- despite gearctl paying
its own connect/disconnect cost per call where NAPALM's numbers share one
open session (merges aren't batchable, see [Combining modes /
batching](#combining-modes--batching)). One honest loose end: `remove`
is consistently ~2 seconds faster than `add`/`replace` even though all
three send the identical three mode-changing commands -- not yet
explained, flagged here rather than papered over, and not blocking
anything since the actual applied commands and their correctness are
unaffected either way.

At the mock-session level (no real device involved, so the timing itself
is deterministic, not just "usually fast"): a correct hint completes in
~0.3s against a scripted mode change that takes 2.33s without one (see
`TestStreamShellSessionHintAvoidsQuiescenceFallbackOnModeChange`).

#### Adding a vendor driver

1. Create `internal/drivers/<vendor>driver.go` with a struct embedding
   `BaseDriver`, a `Vendor()` method, and optionally `GetFacts()`/
   `GetInterfaces()`/`GetLLDPNeighbors()`/`GetConfig()`/
   `LoadConfig()`+`LoadMergeCommands()` (both `ConfigLoader` methods — a
   driver implementing one is expected to implement both).
2. Add a `case` to `internal/drivers/registry.go`'s `Resolve()`.
3. Nothing else changes — not the HTTP layer, not the CLI, not any
   caller's integration.

### Prompt-return detection

The hard problem: a device CLI session is a stateful, ongoing interactive
shell — there's no "command exits" signal like a one-shot SSH exec
channel. `internal/transport/promptdetector.go` solves this with zero
vendor knowledge:

1. On connect, send a bare newline and wait for the buffer to go quiet (no
   new bytes for ~300ms). Whatever trailing line remains is the prompt.
2. For every command, accumulate output until the buffer (past this
   command's own echo) contains that captured prompt text.

**Echo-gating + carry-forward** (`findEchoEnd`/the `carry` parameter
threaded through `waitForPrompt`/`capturePrompt`, ported from Netmiko's
`_read_buffer` design): confirmed live against a real Cisco ISR that a
low-output command (e.g. `terminal length 0`) can be followed by a few
stray trailing bytes — a duplicate prompt redraw was the observed case —
landing in the same read as its own completion. Left unhandled, that
residue leaked into the NEXT command's detection window, which matched
on it instantly and returned empty, with the next command's real output
shifted one slot down the pipeline. The fix has two parts: a command's
own echo is only ever searched for in bytes that arrived *after* it was
written (never inside carried-over residue, so stale data can never be
mistaken for a fresh echo), and anything read past a match is returned as
`newCarry` and threaded into the *next* call's buffer rather than waited
out and discarded. This closes the bug with **no added per-command
latency** — an earlier fix used an explicit ~300ms drain-and-discard
after every command instead, which worked but cost real time on every
single command in a session. See `TestWaitForPromptCarriesResidualBytesToNextCall`/
`TestStreamShellSessionCarriesResidualBytesBetweenCommands`.

Both SSH (`internal/transport/sshsession.go`, via `golang.org/x/crypto/ssh`'s
interactive shell channel — not one-shot exec, since these CLIs are
stateful multi-command sessions) and Telnet (`internal/transport/telnetsession.go`,
hand-rolled on a raw `net.Conn` — no third-party Telnet library; see below)
funnel through this same logic via the `DataSource`/`ShellSession`
abstraction in `internal/transport/types.go` and `internal/transport/session.go`.

**Prompt prediction (`SendAndWaitForPromptHint`/`CurrentPrompt`):** this
package has zero vendor-specific prompt grammar by design, so a command
that changes the prompt (entering config mode, say) can't be matched via
the fast substring path above — the old prompt text is gone, and the new
one isn't known in advance. Without help, that means a full
`commandQuiescenceMs` (2s) wait on every such command. A caller that DOES
know its vendor's prompt grammar (`CiscoIOSDriver.predictNextPrompt`, see
[Prompt prediction](#prompt-prediction-why-config-mode-commands-used-to-be-much-slower)
above) can pass a predicted prompt through `SendAndWaitForPromptHint`
instead of the plain `SendAndWaitForPrompt`, getting the same fast path a
steady-prompt command already gets. A wrong guess just falls back to the
same quiescence behavior as no hint at all — this package's own logic
doesn't change, it just gets a better first guess when one's available.

### Telnet: hand-rolled IAC parsing, with real hardware fixes baked in

`internal/transport/telnetsession.go`'s `telnetIacParser` is a stateful
byte-stream parser for RFC 854 IAC (Interpret As Command) sequences,
including `IAC SB <option> ... IAC SE` subnegotiation blocks that can split
across TCP reads — it carries incomplete sequences forward across `feed()`
calls rather than assuming one call always sees one full sequence.

It also answers `TTYPE` (terminal type) negotiation instead of blanket
refusing every option: some real Telnet servers hang indefinitely, never
showing a login prompt, until they get a proper `IAC WILL TTYPE` followed
by `IAC SB TTYPE IS "VT100" IAC SE` in response to their `SEND`
subnegotiation. Both of these were found and fixed via live debugging
against real hardware, not from a spec read — see `replyToNegotiation`/
`replyToSubnegotiation` for the exact behavior.

### Pagination

Accept an optional `disablePagingCommand` in the request (e.g.
`"screen-length 0 temporary"` for Huawei, `"terminal length 0"` for
Cisco-style CLIs, `"cli"` for Junos -- see below). If provided, it's sent
first and its output discarded. If omitted, it's the caller's problem —
it can also just be the first entry in `commands`.

**Junos's pagination marker is `---(more N%)---`** (a live progress
percentage), recognized by `paginationMarkerPattern` alongside Cisco/
Huawei-style `--More--`/`(more)`. A real Juniper vMX (Junos 18.2R1.9)
surfaced a real bug here beyond just "recognize the marker": Junos erases
it in place with a bare `\r` (no `\n`), then continues real content on
what's technically the same `\n`-delimited line --
`---(more 3%)---\r<spaces>\r        {\r\n` -- so a naive whole-line drop
(`stripPaginationMarkerLines`'s original behavior) silently ate the real
`{` sharing that line with the marker. Confirmed live: `show version |
display json` (long enough to paginate) came back with a `{` missing
from partway through the JSON, breaking it. Fixed by resolving the `\r`
overwrite first — keep only what's after the *last* `\r` in the line —
and keeping that survivor content instead of dropping the whole line,
only actually dropping it if nothing survives (the ordinary Cisco/Huawei
case). This is a generic transport-layer fix, not Junos-specific code —
it benefits any device using this same carriage-return erase-in-place
convention. See `stripPaginationMarkerLines`'s doc comment and
`TestCleanOutput`'s Junos cases (byte-for-byte the real captured
sequence, not a hand-simplified approximation).

**For real usage against Junos, disable paging properly rather than
relying on the page-by-page fallback above** — root SSH lands in the
FreeBSD shell, not the Junos CLI, so `disablePagingCommand: "cli"` (or
`-disable-paging "cli"` on the CLI) is what enters it; then send
`set cli screen-length 0` as your first real command. Confirmed live:
the identical `show version | display json` that took ~21s paging
through 33 screens completed in ~3s with paging disabled properly,
producing byte-for-byte the same valid, complete JSON either way — the
pagination fix is a correctness guarantee for whatever a caller sends,
not a substitute for disabling paging when you can.

### PTY size and large-response completeness

Two more real bugs this same live Juniper vMX session surfaced, both
transport-layer (not Junos-specific — they benefit any vendor/command):

**The requested PTY size was silently backwards.** `sshsession.go`'s
`RequestPty` call is `(term string, h, w int, ...)` in
`golang.org/x/crypto/ssh` — HEIGHT then WIDTH — but this codebase called
it as `RequestPty("vt100", 80, 40, modes)`, which reads like "80 columns,
40 rows" and actually negotiated the opposite: 80 ROWS x 40 COLUMNS. At
an effective 40-column width, Junos's CLI line-wraps/redraws (its own
"..." + tail-of-line convention) any command whose echoed `prompt +
command text` exceeds that width — e.g. `rabi> show interfaces terse |
display json` (43 characters) — splicing leftover redraw bytes into the
captured output before the real response and breaking JSON parsing
outright (`invalid character 's' looking for beginning of value`, the
echoed tail of the command itself). Cisco's own commands tested here all
happened to stay under 40 visible columns, which is why this went
unnoticed until a longer Junos command crossed the threshold. Fixed by
swapping the argument order (40, 80) — see
`TestOpenSSHSessionRequestsWideEnoughPty`, which asserts the actual
negotiated Columns/Rows against a mock server.

**A large `| display json` response can pause mid-transmission longer
than the ordinary quiescence window, even with paging correctly
disabled.** Confirmed live, ~30 direct repro attempts against the same
vMX: `show interfaces | display json` (~200KB) intermittently arrived in
bursts with an internal pause exceeding `commandQuiescenceMs`'s 2-second
window, and every failure was a clean mid-structure cutoff (`unexpected
end of JSON input`, `expecting property name`) — the read loop concluding
"no more bytes are coming" before they actually finished arriving, the
same quiescence-fallback failure mode described above, just triggered by
device-side burstiness on a large payload instead of by pagination. An
earlier fix attempt retried by resending the whole command from scratch
on an incomplete-looking result — it helped (roughly halved the failure
rate) but was wasteful and, under heavier device load (this is a shared
lab device), still left a meaningful failure rate (confirmed live:
~25-35% even with 3 attempts).

The actual fix targets the root cause instead: `waitForPrompt` now
accepts an optional `isComplete(settled string) bool` predicate that
gates ONLY the quiescence-fallback conclusion (the genuine
prompt-reappeared substring match is untouched, since that's already
authoritative) — if `isComplete` says the accumulated output isn't
finished yet, the quiet timer resets and reading continues instead of
concluding the device is done, bounded the whole time by the ordinary
per-command timeout. Exposed via a new optional capability,
`transport.CompletionAwareSession` (implemented by
`StreamShellSession`, both SSH and Telnet), reached from the driver
layer via `BaseDriver.RunCommandUntilComplete` — falls back to ordinary
`RunCommandWithHint` behavior automatically if a session doesn't
implement it (e.g. a test double), and every existing caller passing no
predicate is completely unaffected. `JuniperDriver.runJSONCommand` uses
`encoding/json.Valid` as `isComplete`. Confirmed live: 27/27 consecutive
`GetInterfaces()` calls succeeded after this fix, up from ~65-75% with
the resend-retry mitigation. See `TestWaitForPromptIsCompleteDefersQuiescenceConclusion`
and `TestJuniperDriverRunJSONCommandUsesCompletionAwareSessionWhenAvailable`.

### Config push for Junos

`JuniperDriver` implements `ConfigLoader` (`LoadConfig()`/
`LoadMergeCommands()`) using Junos's own native primitives instead of a
heuristic reconciliation the way `CiscoIOSDriver` does:

- **`configure private`**, not plain `configure` — confirmed live this
  matters on a shared device: `configure` uses one candidate config
  shared by every concurrent session (this lab device routinely has
  several: `root terminal u0 ... idle 15:13:14`), so two sessions
  editing at once would merge into the same pending changes. `private`
  gives each session its own isolated candidate.
- **The diff is Junos's own `show | compare`** — a real comparison the
  device computes between the candidate and the committed config, not a
  reconstruction. Confirmed live: `[edit interfaces ge-0/0/0 unit 0]` /
  `+    description X;`.
- **Apply is `commit check` (validate) then `commit`** — confirmed live:
  `configuration check succeeds` / `commit complete`. A rejected line
  (confirmed live: Junos's own `syntax error.`, which the CLI can print
  more than once for one bad command as it validates incrementally
  while "typing") or a failed check/commit triggers `rollback 0` +
  `exit` — the private candidate is discarded, the device's committed
  config is untouched.
- **`reverseCommands` is always exactly `["rollback 1"]`** — Junos's own
  "revert candidate to the state before the last commit" primitive.
  Confirmed live: commit a description, verify it's really there via a
  separate read, then `LoadMergeCommands(["rollback 1"], ...)` (the
  exact same function, no special-casing) correctly reverts it and the
  device shows no description again. Dramatically simpler and more
  reliable than `CiscoIOSDriver.reverseCommands`' own per-line
  heuristic derivation.
- **`LoadConfig()`'s candidate must be Junos's flat `set`/`delete` form**
  (e.g. from `show configuration | display set`), **NOT**
  `GetConfig()`'s own curly-brace `show configuration` output — unlike
  `CiscoIOSDriver`, where `GetConfig()`'s output is exactly what
  `LoadConfig()` expects back, edited. This is deliberate: Junos's native
  way to load a full hierarchical candidate is `load merge terminal`, an
  interactive paste-then-Ctrl-D flow this driver does NOT attempt to
  automate — a real early attempt at it left multiple SSH sessions
  hung on this exact lab device (`request system logout terminal <pts>`
  was needed to clean them up), since getting Ctrl-D delivery exactly
  right over a scripted, non-interactive channel is a genuine foot-gun.
  Flat `set`/`delete` form sidesteps that entirely: `LoadConfig()` is a
  thin wrapper that splits candidate into lines and calls
  `LoadMergeCommands()` with them — same safe, already-verified
  per-line mechanism, no special multi-line input mode involved.

**You don't send `configure private`/`commit check`/`commit` yourself —
just the `set`/`delete` delta lines.** This is enforced, not just
documented: a real live test included them anyway (an old-habit
carryover from before this driver auto-managed those steps), and it
turned out to be actively dangerous, not merely redundant —
`applyJuniperMergeConfig` replays every line in the candidate for real,
REGARDLESS of `dryRun` (dry-run only controls what happens *after*
replay: discard vs. commit-check+commit), so an embedded `"commit"` line
got committed for real during what should have been a safe, no-write
dry-run preview — before the CLI ever asked "Type `apply` to confirm".
`LoadMergeCommands`/`LoadConfig` now refuse outright (before ever calling
`GetConfig` or connecting further) if the candidate contains a line
matching `configure`/`configure private`/`configure exclusive`/
`configure batch`/`commit`(` ...`) — confirmed live:

```
failed (validation): candidate contains "configure private", which LoadMergeCommands/LoadConfig
already handle automatically (entering "configure private" and running "commit check"/"commit" at
the right time, gated by dry-run/apply) -- including it yourself bypasses that safety flow entirely
and can commit for real during what should be a safe dry-run preview -- just supply the set/delete
delta lines
```

`"rollback N"` is deliberately exempt — it's the documented, legitimate
way a caller replays `reverseCommands`.

**Three more real bugs found live while building this, all fixed:**

- Junos reprints a bare `[edit]` context line immediately before its own
  prompt on every response while in configuration mode — confirmed live
  that `show | compare`'s real captured output is
  `[edit interfaces ge-0/0/0 unit 0]\n+    description X;\n\n[edit]`,
  with that trailing `[edit]` being prompt-adjacent noise, not part of
  the diff. `stripTrailingEditContext` removes ONLY a bare trailing
  `[edit]` line, never a populated `[edit <path>]` header (which IS real
  diff content).
- `commit check`/`commit` do real device-side work and can take
  noticeably longer than an ordinary config-mode command — confirmed
  live, reproducible specifically when preceded by a realistic full
  sequence (`GetConfig()` + `configure private` + delta lines + `show |
  compare`): the ordinary quiescence-fallback wait concluded "done" on a
  whitespace-only settled buffer moments before the real confirmation
  text arrived, surfacing as `"commit check" failed ()` with an empty
  reason. Fixed the same way as the large-JSON-response case above —
  `RunCommandUntilComplete` with `isNonWhitespace` as `isComplete`,
  deliberately NOT applied to commands that can correctly succeed with
  empty output (a plain `set`/`delete` line, `show | compare` with no
  diff, `rollback 0`).
- `detectJunosRejection` only recognized Junos's `syntax error`
  convention, missing its OTHER general rejection convention,
  `error: ...` — confirmed live via a real SEMANTIC (not syntax) failure:
  setting a second logical unit on an interface whose encapsulation only
  allows unit 0 produced `error: configuration check-out failed`, which
  silently slipped past detection during the per-line apply loop (only
  this driver's own automatic `commit check` step downstream happened to
  catch it, via a separate, unrelated `"check succeeds"` substring
  check). Also, that trailing bare `[edit]` noise (previous bullet) was
  leaking into user-facing rejection/error messages too, not just the
  diff — both fixed together: `detectJunosRejection` now also matches
  `error:`-prefixed lines, and strips `[edit]` noise before returning a
  reason, applied consistently everywhere a rejection message is built.

See `juniperdriver_configloader_test.go` for the full test suite
(dry-run diff, rejection — both `syntax error` and the real semantic
`error:` case, hash-freshness, commit-check/commit failure, the
whitespace-completion recovery, `stripTrailingEditContext`, and the
reserved-command-line refusal for every `configure`/`commit` variant) —
all built from real captured Junos output, same as everywhere else in
this driver.

### Security

- Every HTTP request requires `X-Internal-Auth: <shared secret>`
  (`GEARCTL_SHARED_SECRET`, constant-time comparison via
  `internal/auth/sharedsecret.go`).
- Never persists credentials — in-memory for one request's duration only.
- `internal/security/targetvalidator.go`'s `IsAllowedOltTarget` blocks
  loopback/link-local/metadata targets (including decimal/octal/hex IP
  literal obfuscation), but deliberately **allows RFC1918** — management
  IPs for this kind of gear legitimately live on private management
  VLANs. Callers that resolve the target host server-side from their own
  trusted inventory (never from raw client input) get the real SSRF
  closure that way; this validator is defense-in-depth on top of that.
- If deployed with no `ports:` mapping in Compose, reachable only from
  other containers on the same internal network.

## Building and running

```
go build ./...
go vet ./...
go test ./...
```

## Testing

`stripPaginationMarkerLines`'s Junos carriage-return-erase fix has
dedicated `TestCleanOutput` cases using the byte-for-byte real sequence
captured live from a Juniper vMX (a raw Paramiko session dump, not a
hand-simplified approximation) -- one confirming real content sharing a
line with the marker survives, one confirming the ordinary Cisco/Huawei
case (nothing survives the erase) still vanishes entirely, same as
before this fix. `TestPaginationMarkerPattern` covers the marker
recognition itself (`---(more 10%)---`/`---(more)---`) alongside the
existing Cisco/Huawei cases and false-positive traps.

No real hardware needed for most of this — mock SSH (via
`golang.org/x/crypto/ssh`'s server-side API) and mock Telnet (a raw
`net.Listener`, including a `requireTtypeNegotiation` mode that reproduces
the real-hardware TTYPE hang) fixtures exercise the whole transport +
prompt-detection stack end-to-end. Driver-layer parsing (`ciscoiosdriver_test.go`)
is tested against canned CLI output, including a regression test for the
real trailing-prompt bug found live. `GetInterfaces()`'s parsing is
tested against `sampleShowInterfaces` -- real output captured live
against the Cisco ISRv, not just the documented format, covering three
interfaces in one fixture: up with an IP but no description, up with a
description but no IP, and administratively down with neither.
`GetLLDPNeighbors()`'s parsing is
tested against both `sampleShowLLDPNeighborsEmpty` (real output, captured
live after enabling `lldp run`) and `sampleShowLLDPNeighborsPopulated`
(NOT live-verified -- built against the documented format, since this
lab has no second LLDP-speaking device to produce a real populated
sample; explicitly labeled as such in the fixture's own doc comment, not
silently presented as equally trustworthy), plus a regression test for
the real-hardware finding that this exact device initially replied
"% LLDP is not enabled" before `lldp run` was applied -- confirming that
surfaces as a distinct error rather than a silently-empty result.

`JuniperDriver` (`juniperdriver_test.go`) is tested against real "|
display json" output captured live against the same Juniper vMX: `show
system information`/`show system uptime`/`show interfaces terse`/`show
interfaces` fixtures cover `GetFacts()` (including `host-name` coming
back as a present-but-empty `[{}]` on a device with no hostname
configured -- a real shape, not a synthesized edge case) and
`GetInterfaces()` (an interface with an IP found via `show interfaces
terse`'s ready-made CIDR notation, a loopback with non-numeric `mtu:
"Unlimited"` and no `speed` key at all, and an interface carrying a
description -- confirmed live by deliberately setting one on a real
interface, capturing the resulting JSON field, then removing it again so
the device was left unchanged). `GetConfig()` is tested against real
`show configuration` output (hierarchical curly-brace format, with the
two real encrypted-password values swapped for placeholders -- the only
intentional deviation from byte-for-byte capture anywhere in this
driver's tests, since `GetConfig()` does no parsing at all and the
literal hash values aren't load-bearing for the test). Truncated-JSON
handling on both `GetInterfaces()` and `GetConfig()` is also covered --
see `juniperdriver.go`'s doc comments for why a cut-off JSON response is
refused outright rather than fed to `encoding/json` best-effort.
`TestJuniperDriverRunJSONCommandUsesCompletionAwareSessionWhenAvailable`
confirms `runJSONCommand` actually wires `encoding/json.Valid` through as
`isComplete`, not just that it compiles -- see
[PTY size and large-response completeness](#pty-size-and-large-response-completeness)
for the live bug this exists to fix and
`TestWaitForPromptIsCompleteDefersQuiescenceConclusion`/
`TestWaitForPromptIsCompleteNilPreservesOriginalBehavior` (in
`promptdetector_test.go`) for the mechanism itself, plus
`TestOpenSSHSessionRequestsWideEnoughPty` for the PTY-size regression.

`LoadConfig()`/`applyMergeConfig` (`mergeconfig_test.go`) are tested
against a fake session double covering: the happy path; stopping on a
device rejection (with `end` still sent, and a curated table of real
Cisco error-message formats plus deliberate false-positive traps like
syslog-style `%SYS-...` messages); stopping on a connection drop (`end`
*not* sent); the dry-run/apply-hash freshness check (stale hash refused,
fresh hash succeeds); and multi-line-directive refusal before ever
connecting.

`predictNextPrompt` (the prompt-prediction speedup — see [Prompt
prediction](#prompt-prediction-why-config-mode-commands-used-to-be-much-slower))
has its own table test (`TestPredictNextPrompt`) covering every
transition `applyMergeConfig` actually sends, plus
`TestApplyMergeConfigPassesPredictedPromptHints`, confirming the ACTUAL
hint values `runSingle` computes and passes down for a real
`applyMergeConfig` run, not just the pure function in isolation. The
underlying transport-level mechanism (`SendAndWaitForPromptHint`) has its
own mock-session tests in `internal/transport/session_test.go`:
`TestStreamShellSessionHintAvoidsQuiescenceFallbackOnModeChange` proves a
correct hint completes in ~0.3s against a scripted mode change that takes
2.33s without one, and `TestStreamShellSessionWrongHintStillSelfCorrectsViaQuiescence`
proves a wrong hint is never unsafe — just as slow as no hint at all,
never a wrong result.

`LoadMergeCommands()`'s stanza-locating/projection/reconciliation logic
(`mergecmd_test.go`) is table-driven-tested separately from the above:
replace-in-place, `no <cmd>`-as-removal (matched via the same `matchKey`
the replace/add path uses, ip-prefix two-token special case included --
see below for the live-caught bug this fixed), clean addition, new-stanza
addition, the `ip ...`-prefix two-token match-key special case, sibling
`interface X` → `interface Y` context switches with no `exit` between
them, a bare context-less delta (e.g. `hostname new-name`) resolving
against the global context, unrelated lines keeping their exact original
position and formatting (`TestProjectMergedConfigPreservesUnrelatedLineOrder`
— the regression test for a real-hardware finding: an earlier version of
the projection reparsed and reserialized the whole config from a
normalized model, which reordered everything for even a one-line change),
and — the regression test for a real bug caught during design review,
not by a reviewer or live testing — proof that
`findMultiLineDirective`/`applyMergeConfig` only ever see the raw delta
lines, never a synthesized `current+delta` blob (a fake `current`
containing an untouched `banner motd ^C...^C` block still succeeds,
rather than being refused). Apply itself reuses the already-tested
`applyMergeConfig`/rejection-detection/hash-freshness machinery
unchanged, so it isn't retested here.

`reverseCommands` gets its own coverage on top: a hand-built
`mergeOperation` log exercising all three invertible kinds (replace/add/
remove) plus a no-op, confirming reverse order (last change undone
first) and correct per-kind inversion; and — the strongest check —
`TestProjectMergedConfigReverseCommandsRestoreOriginal`, which applies a
delta, then applies THAT delta's own `reverseCommands` to the result, and
asserts the config comes back byte-identical to the original, not just
"looks plausible".

`LoadMergeCommands()`'s dry run AND apply have both been verified live
against a real Cisco ISRv — adding, replacing, and removing (`no
description`) an interface description all matched their projected
diffs exactly, with the rest of the ~7KB config untouched in every case.

**A real bug was caught live during this verification**, dogfooding
`-merge-cmd` to toggle `ip scp server enable` on and off (to unblock a
NAPALM config-merge comparison, which needs SCP enabled to push its
candidate file): `no ip scp server enable`'s projected diff showed the
*wrong* line being removed -- `ip domain name isr.com` instead of `ip scp
server enable`. Root cause: the `no <rest>` removal match used a plain
first-token key (`"ip"`), while the replace/add path already special-cased
the overloaded `ip` prefix down to a two-token key (`"ip scp"` vs `"ip
domain"`) specifically to avoid this kind of collision -- removal just
hadn't been given the same protection. **The actual device write was
never wrong** -- apply only ever replays the literal delta command, never
the projection (see `projectMergedConfig`'s doc comment); `ip scp server
enable` really was removed, `ip domain name isr.com` really was left
alone -- but the *diff* shown for review was misleading, which matters for
a "review carefully before applying" feature. Fixed by having `no <rest>`
share the exact same `matchKey` (ip-prefix special case included) the
replace/add path uses; see
`TestProjectMergedConfigNoRemovalUsesIPTwoTokenMatchKey` and its
stanza-scoped sibling for the regression tests, and re-verified live
after the fix (the same toggle now shows the correct line in its diff).

`LoadConfig()`'s apply path (the full-candidate-file sibling) still
hasn't — see [Merging a config change](#merging-a-config-change) for its
manual test checklist to run before trusting it in production. Also
still needing real hardware:
additional vendor prompt/pagination strings beyond what's been tested
live, legacy SSH cipher/kex compatibility (some device SSH stacks may
need `ssh.Config` overrides), and real command latency at scale (some
`display`/`show` commands enumerating hundreds of entries can be slow).

`/v1/batch` (`batchhandler_test.go`) and the CLI's combined-mode path
(`main_test.go`) are tested with fakes covering: invalid JSON/missing
host/disallowed target/empty operations/unknown operation type/
execute-without-commands (request-level validation, no connection
attempted); a vendor unsupported for one requested operation only
failing that operation, not the whole batch/invocation; and a connection
failure marking every *supported* operation "failed" with the same
connection error. Both the HTTP endpoint and the CLI combined-mode flag
combination have been verified live against a real Cisco ISRv: one
`facts`+`interfaces`+`config` CLI invocation and one `facts`+
`interfaces`+`lldp-neighbors` `/v1/batch` call, both over a single
connection (`connectedAt` set once), all requested sections populated
correctly in one response/output.

**Session pooling** (`internal/sessionpool/store_test.go`) is tested with
a fake `drivers.CliDriver` double (this package never dials anything
itself, see its doc comment) covering: open/acquire/release; a second
`Acquire` while the first holder still has the lock getting `ErrBusy`,
then succeeding once `Release` is called; a timed-out `Acquire` NOT
leaking the lock (the regression test for the exact bug a
`sync.Mutex`-plus-goroutine try-lock would have — see
`TestStoreAcquireTimeoutDoesNotLeakTheLock`'s doc comment for why the
channel-based lock design avoids it); `Close` disconnecting and removing
a session; the idle reaper actually disconnecting and removing a session
once its timeout elapses, and NOT reaping one that's had recent activity
(`TestStoreReleaseResetsIdleClock`); and 100 generated IDs all unique and
non-empty.

Every operation handler's session-based path (`factshandler_test.go` and
its five siblings, plus `sessionopenhandler_test.go`/
`sessionclosehandler_test.go`) is tested via a shared `fakeSessionDriver`
(`session_fake_driver_test.go`) registered directly into the shared
`sessionStore` — bypassing any real network connection entirely, since
`sessionpool.Store.Open` never dials anything itself. This is genuinely
new test coverage the fresh-connection path structurally couldn't offer:
every other handler test in this package can only exercise
validation/connection-error paths without a real reachable device (see
e.g. `TestFactsHandlerReturnsFailedForConnectionError`), but a
session-based test can inject a fake already-"connected" driver and
assert a real **success** response — confirming each handler actually
reaches the pooled driver, not a fresh one, when `sessionId` is set, and
that a session-based call never calls `Disconnect()` on the pooled
connection. Each handler's suite covers: unknown/expired `sessionId` →
`session_not_found`; success via a fake driver implementing every
optional capability; and (for every handler except `/v1/execute`, which
has no capability check — `RunCommands` is on the base `CliDriver`
interface every driver implements) `unsupported` via a bare
`*drivers.RawDriver{}` registered into a session, confirming the type
assertion fails cleanly without ever needing a real connection.

Verified live against the real Cisco ISRv: `POST /v1/session/open`
followed by `/v1/facts`, `/v1/interfaces`, and `/v1/config` all reusing
the identical `connectedAt` (proving one real shared connection, not
three); a `/v1/merge-cmd` dry-run-then-apply pair through the same
session, confirmed to land on the device and be readable back through
`/v1/interfaces` on that same session; explicit `POST /v1/session/close`
followed by a call against the closed ID correctly reporting
`session_not_found`; and the 30-second idle timeout, confirmed by
opening a session, waiting past it with no activity, and getting
`session_not_found` on the next call with no explicit close ever sent.
