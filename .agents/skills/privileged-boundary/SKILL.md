---
name: privileged-boundary
description: The invariants to keep when changing EggBox's privileged side and its boundary - the agent/service (internal/agent/**), the wire API (internal/agentapi), the config gate (internal/agent/policy), config generation and provider import (configgen, planimport, subscription), platform drivers, installers and the sing-box dependency. Covers what the GUI and the service may each do, the gate's allow-list and ownership rules, the ledger, authorization, and the checks that guard each invariant (drift tests, fuzz seeds, crash-window tests), including the steps for upgrading sing-box. Use when touching any of those packages or adding a capability that crosses from GUI to service. Not needed for GUI-only or library-only work. 特权服务边界与配置闸门的不变量。
---

# Privileged boundary

EggBox splits an unprivileged GUI from a root/SYSTEM agent. The design is already built; the job when changing it is to **not break an invariant**. The authoritative descriptions live in the package docs (`internal/agent/policy/policy.go`, `internal/agent/ledger`, `internal/agent/transport/doc.go`); read the one for the package you touch.

## Invariants

**Split of duties**
- The GUI changes nothing on the system; everything privileged is a request to the agent. It generates configurations; the agent treats them as untrusted.
- The API is narrow, typed and versioned (`internal/agentapi/agent.proto`, regenerate with `scripts/gen-proto.sh`). A new capability is a new RPC, never a generic "run this". A wire change bumps the protocol version so the handshake refuses skew with an actionable message.
- The server translates and never decides; decisions live in the controller.

**Controller**
- One serialized queue for state changes; request ids make retries idempotent.
- Apply = gate, engine dry run, store a new revision under compare-and-swap on the expected revision, hot-apply, roll back to last-good on failure. Last-good moves only after the core really started.
- Intent (should be connected) is separate from state (is connected); after a crash, wake or upgrade, reconcile state toward intent.
- Network judgements come from pure analyzers over snapshots; never delete somebody else's routes or "reset" DNS/routes wholesale.

**Ledger**
- Every system resource is recorded before it is created, so a restart removes exactly what a previous run owned. A TUN left behind blackholes traffic: recover from the ledger before anything else.

**The gate (`internal/agent/policy`)**
- Allow-list sections, types and dangerous fields; unknown means refuse.
- Parse once (strict: size limit, duplicate keys and trailing data rejected), check and rewrite that tree, serialize the same tree. Never validate one copy and run another.
- Overwrite what the agent owns (TUN addressing, routes, DNS hijack, cache/log paths, listen addresses) instead of validating it.
- Files are handles: a path-like field must be `res:<name>` pointing into the handed-over bundle; every path-like option field in sing-box is classified, and an unclassified one fails a test.
- Loopback listeners only; no management APIs, services, system-proxy toggling or external programs.
- Issues carry severity, JSON path, a stable code and a message, never the input's content; a refused input yields no runnable output.
- Import is lenient, the gate strict: report skipped provider entries with a reason, never drop silently.

**Authorization and the service**
- One authorized user, fail closed; verify the peer by credentials/signature, not by name or path.
- The GUI never passes file paths; the service resolves handles into its protected data directory.
- The service runs with cwd `/` or `System32` and root's environment: absolute paths only. launchd discards output and a Windows service has no stderr: log to files.
- Real drivers are built only by the installed service entry, which refuses to run otherwise; no flag, env var or config may select one from a dev entry. `eggbox_dev` code must not reach release builds.

## Checks that guard them

Run the ones for the package you changed (plus the rows of `change-verification`):

| Changed | Run |
|---|---|
| `policy` | `go test -count=1 ./internal/agent/policy/...` (fuzz seeds run as ordinary tests); for a new rule, a short fuzz: `go test -run '^$' -fuzz FuzzNormalize -fuzztime 60s ./internal/agent/policy` |
| `configgen`, `planimport`, `subscription` | their tests; `FuzzProviderContent` seeds in `configgen`; a short `-fuzz` run when the generator or healing changes |
| `controller`, `ledger`, `server`, `transport` | their tests with `-race`; when changing apply/rollback/recovery, a test for the crash window you touched (killed between create and record, replayed request id, stale revision) |
| Platform code (`winplat`, `macplat`, `macsys`, `winsys`) | `change-verification/scripts/vet-matrix.sh` for the targets; pure rules unit-tested on any OS; real behaviour reported as needing a real machine |
| Installer scripts naming Go constants | the guard tests that parse both sides (`go test ./internal/agent/daemon`) |

A failing fuzz input is either a gate bug or an oracle bug: decide which before changing anything. The oracle must not reuse the gate's logic.

## Upgrading sing-box

1. Bump it, `go mod tidy`.
2. `go test -count=1 -tags "eggbox_dev,$TAGS" ./internal/agent/engine/singbox/...` - `TestPolicyAllowListsMatchRegistries`, `TestPathLikeFieldsAreClassified` and `TestNoServicesOrCertificateProvidersAreRegistered` flag new types and fields. Classify each deliberately; never widen the allow-list just to pass.
3. Policy and configgen tests with their fuzz seeds, then the real-core tier (`change-verification`).
4. `go run golang.org/x/vuln/cmd/govulncheck@latest -tags "$TAGS" ./...`.
5. Re-read the dependency's license terms if its license file changed.
