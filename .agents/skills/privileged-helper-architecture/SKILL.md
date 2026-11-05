---
name: privileged-helper-architecture
description: Guides the design, build and review of a desktop app whose risky system work (VPN/TUN, routes, DNS, firewall, drivers) runs in a separate privileged service while the UI stays unprivileged - a narrow versioned IPC API, a thin server over a serialized controller, idempotent operations with revision compare-and-swap and rollback, a write-ahead resource ledger with fingerprinted cleanup, one authorized controller, upgrade and uninstall choreography, and Windows service/named pipe and macOS LaunchDaemon/XPC specifics. Use when designing or reviewing a root/SYSTEM helper, a VPN or network client, an installer-managed service, or any GUI that must change system state safely. 特权服务、UI 与 helper 分离、崩溃后可清理。
---

# Privileged helper architecture

## 1. Shape

```
unprivileged GUI --(local IPC, authenticated)--> privileged service
  views, state mirror                              transport (verifies the peer)
  settings the user edits                          thin API server (translates, never decides)
  profile / subscription / config generation       controller (serialized queue, all decisions)
  changes nothing on the system                    policy gate (what a config may make us run)
                                                   engine adapter --> proxy core
                                                   platform driver --> OS (TUN, routes, DNS, watchers)
                                                   ledger + store in a protected data directory
```

- **The GUI never touches the system.** Anything privileged is a request to the service. Closing the GUI keeps the connection; reopening shows the real state.
- **The service owns** the core's lifecycle, system resources, persisted revisions and intent, and cleanup.
- **Config generation is the GUI's job; the service treats its output as untrusted** and gates it (`untrusted-input-gates`).
- **The API is narrow, typed and versioned.** Handshake (protocol version, driver name, dev flag, instance id, peer description); status snapshot and watch; settings get and update; validate and apply (expected revision, request id, apply mode); connect and disconnect; runtime operations (select outbound, test, mode, close connections); watch streams (groups, traffic, connections, logs); redacted diagnostics; retry cleanup; prepare upgrade. A new capability is a new RPC; never a generic "run this".
- **The handshake guards version skew** and refuses with an actionable message (update the app, restart the service) instead of misbehaving.
- **The server is thin**: it translates and never decides, so a second client or a test gets identical behaviour.

## 2. Operations (the controller)

- One serialized queue for every state-changing request; reads and streams stay outside it.
- Request ids make retries idempotent: replaying an id returns the first result.
- Applying a configuration: validate (policy, then an engine dry run) -> store as a new revision under compare-and-swap on the expected revision (a stale writer gets a conflict, never a silent overwrite) -> hot-apply -> on failure roll back to the last-good revision and say so. The last-good pointer moves only after the core really started.
- Keep **intent** (should be connected, with which profile) separate from **state** (is connected). After a crash, a wake or an upgrade the service reconciles state towards intent.
- Settings stay independent: service starts with the computer, GUI starts at login, auto-connect when the GUI starts. Entangling them confuses users and tests.
- Coalesce events: network, power and session events go through a debouncer (quiet period, longer after a wake, a cap on the total wait) that emits at most one reconnect decision per burst. Make it a pure state machine (`Add`, `Poll`, `NextDeadline`) behind a thin goroutine so tests use a fake clock.
- Judge the network with a pure analyzer fed a snapshot of interfaces and routes: foreign default routes, overlapping subnets, a free subnet for the TUN from candidates. It never proposes deleting somebody else's routes; a conflict means refuse or ask.
- Serve one user. When that user signs out or another takes the screen, disconnect; a locked screen keeps the connection.

## 3. The ledger: how a crash gets cleaned up

Write-ahead record of every system resource the service creates, so the next start removes exactly what the previous run owned and nothing else. Never "reset" routes or DNS wholesale. Contract, flow and recovery: `references/resource-ledger.md`.

## 4. Who may talk to the service

Two separate questions: who may connect (transport and peer verification) and what a connected peer may do (API, controller, policy). One authorized user, claimed on first use by the person at the screen; fail closed. Platform details: `references/authorization-and-ipc.md`.

## 5. Install, upgrade, uninstall

Three independent switches; an upgrade that reconnects what was connected via a marker only an installer can write; an uninstall that cleans before it removes. `references/lifecycle-and-installers.md`.

## 6. Testing the architecture

- Put everything behind interfaces: engine (a fake core), driver (a simulation), transport (in-process, socketpair, unix socket), clock.
- Keep decision rules in pure packages (pipe policy, uid policy, route analyzer, debouncer, session tracker, install-location rules) so Windows rules are tested on a Mac. OS-touching files only gather facts.
- Write guard tests that tie artifacts together: installer scripts spell names the Go code defines (marker file, service name, label, paths) because they cannot import it; a test parses both so they cannot drift.
- Test the crash windows: killed between "resource created" and "recorded", corrupt ledger, double disconnect, replayed request id, stale revision, rollback on a bad config.
- Run end to end in a sandbox (`host-safe-verification`).

## Pitfalls

- launchd discards a job's stdout and stderr unless the plist sets `StandardOutPath` and `StandardErrorPath`, and a Windows service has no valid stderr handle: log to files an administrator can read, and write start-up failures to a file.
- A service starts with cwd `/` or `System32` and root's environment: absolute paths only, no reliance on `HOME` or `PATH`.
- Data directory: owned by root/administrators, no parent writable by others; honor marker files only as plain files (not links, not directories).
- A TUN left behind after a crash blackholes traffic: recover from the ledger before anything else, and sweep by well-known names only when the ledger is unreadable (and only what carries your mark).
- Letting the GUI pass file paths to the service; the service resolves handles into its own protected directory.
- Trusting a client by name or path alone: verify code signature or image path and user.
- Running a service binary from a place a user could replace.
- A "fix my network" button that edits routes or DNS broadly.

## References

- `references/resource-ledger.md` - contract, entry design, session phases, recovery, sweep, release semantics.
- `references/authorization-and-ipc.md` - owner model, Windows named pipe, macOS XPC with fd hand-over, dev socket, fail-closed rules.
- `references/lifecycle-and-installers.md` - install, first run, three switches, upgrade protocol, uninstall, version skew.
- `references/platform-notes.md` - Windows service and macOS LaunchDaemon/SMAppService/Swift bridge/DNS specifics.
