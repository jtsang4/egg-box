---
name: host-safe-verification
description: Verifies software that changes system state (network TUN/routes/DNS/proxy, services, launchd or Windows services, installers, root/SYSTEM helpers) without changing the developer's own machine - sandbox by construction, hard guards on real drivers, loopback stand-ins, before/after host snapshots, inspecting installers instead of installing them, and recording what only a real machine can verify. Use when building or testing anything privileged or network-affecting, when the user says not to disturb their network or global proxy, or before running installers, launch agents or root commands locally. Not for handling credentials (see secret-safe-testing). 沙箱验证、不影响本机网络。
---

# Host-safe verification

A developer machine holds state that is expensive to restore: a global proxy or TUN, DNS, routes, launchd and service jobs. Every check run on it must be pure, read-only, or confined to resources only the test owns. Everything else is replaced by a stand-in, inspected without being run, or written down as unverified.

## 1. Classify every command before running it

| Class | Examples | Rule |
| --- | --- | --- |
| Pure | parsers, gates, state machines, ACL / route / fingerprint rules | run anywhere, in CI |
| Read-only on the host | `ifconfig`, `netstat -rn`, `scutil --dns`, `launchctl print`, service status queries | safe; use for baselines and smoke checks |
| Owned and confined | loopback listeners on random ports, temp dirs, a unix socket in a 0700 dir, in-process anonymous XPC, `httptest` | safe; reserve ports for TCP **and** UDP |
| Host-mutating | TUN, routes, DNS, system proxy, registering a daemon or service, installing a package, anything under `sudo` | never on the dev machine: replace it (2), inspect it (4) or document it (5) |
| Outbound only | fetching a subscription, delay tests through a user-space proxy | fine (a normal app does it) if it has no system-wide effect; real credentials: see `secret-safe-testing` |

## 2. Make the dangerous part replaceable (design, not discipline)

- Put the OS behind an interface (`Driver`: create tunnel, apply DNS, watch the network, release). Keep decisions in pure code; a thin per-OS file gathers facts and asks the pure rules.
- Ship a simulated driver that cannot touch the host; tests, the dev entry and demos use it.
- Real drivers are built only by the installed service entry, which itself refuses to start unless it is root/SYSTEM and runs from a location only administrators can write. No flag, env var or config may select a real driver from a dev entry.
- The dev entry exists only under a build tag, so release binaries contain no dev transport. It refuses to run as root and listens on a unix socket inside a 0700 directory you own, with a same-uid check. Give every platform without dev code a release-equivalent fallback (a missing counterpart breaks that platform's build) and vet the tagged build for every platform in CI.
- Replace system-wide effects by loopback equivalents: TUN becomes a loopback proxy on a random port, DNS writes go to a fake store, route changes to a read-only analyzer, the credential store to a temp file.
- Give engines a dry run (`Check`) that resolves references without opening a listener or device, and require absolute state/cache paths so a service started with cwd `/` or System32 cannot write somewhere surprising. The unprivileged side never chooses a path.
- Add a demo mode that runs the whole UI against an invented in-process backend (see `native-ui-quality-loop`).

Sketches in `references/sandbox-by-construction.md`.

## 3. Prove it: snapshot, run, snapshot

`scripts/host-snapshot.sh` (macOS and Linux, read-only) prints interfaces, default routes (v4 and v6), system proxy and resolvers, plus `--process REGEX`, `--launchd LABEL`, `--systemd UNIT`:

```bash
S=.agents/skills/host-safe-verification/scripts/host-snapshot.sh
$S --process 'myagent|myapp' --launchd com.example.agent > /tmp/before.txt
# ... the whole verification ...
$S --compare /tmp/before.txt --process 'myagent|myapp' --launchd com.example.agent   # prints "identical" or a diff
```

- Also assert inside end-to-end tests: capture the interface table (name, flags, MTU, addresses) before and after a connect/disconnect cycle and compare.
- A difference is not automatically yours. The user's own proxy client, VPN or Wi-Fi roaming changes `utun` and resolver entries. Re-run in a quiet moment and attribute only differences that involve your processes or labels. Never "repair" them.
- Process patterns hit system daemons by accident (`agentd` matches `gamecontrolleragentd`); anchor patterns or use the full path.
- Afterwards confirm that nothing of yours is left: processes (by recorded pid), sockets, temp dirs, launchd jobs.

## 4. Inspect installers; never install them

- macOS: build the package, then read it: `pkgutil --payload-files`, `pkgutil --expand-full`, `codesign --verify --strict --deep`, `plutil -lint`, `lipo -info`, `shellcheck` on the installer scripts.
- Windows: compile the installer (NSIS runs from any OS), shellcheck scripts, cross-compile and vet the Windows code, unit-test its pure rules (ACL strings, SID checks, route fingerprints) anywhere. Run the platform's own tests on a disposable CI runner of that platform.
- Never run privileged entry points locally as root (service entry, cleanup, reset-owner), never `sudo` an uninstaller, never register a daemon or service (`launchctl bootstrap`, `SMAppService.register`, `sc create`). Read-only status queries are fine.

## 5. Write down what was not verified

Keep a table in the README: area, what was verified, what remains, how to verify on a real machine. Distinguish "compiled and vetted for the platform", "pure logic unit-tested" and "ran for real". Never write "works" for a path that only compiled. Typical residue: approval flows (SMAppService, UAC), real signing requirements against the installed bundle, real TUN/route/DNS behaviour, sleep/wake, coexistence with other VPNs, upgrade over an older install, notarization.

## 6. Hazards while driving real processes

- Record the pid of everything you start and stop exactly that pid (TERM, wait, then KILL). A pattern-based `pkill` fired from one of several parallel tool calls killed another call's process.
- A single-instance lock makes a second launch exit at once; that is not a crash.
- Do not automate the user's real desktop. Render headless instead.
- launchd discards a job's output unless its plist names a log file, and a Windows service has no valid stderr handle: log to a file or ring buffer.

## Anti-patterns

- "It only runs a read-only command" without checking that the command is read-only on this OS.
- A real-driver code path reachable from a dev flag, an env var or a config field.
- Verifying a TUN by really creating one "just briefly".
- Claiming Windows or macOS service behaviour from a build that only compiled.
- Comparing snapshots that contain PIDs, counters, timestamps or reachability flags.

## References

- `references/sandbox-by-construction.md` - driver seam, build-tag trio, guards, loopback stand-ins, port reservation, e2e shape.
- `references/host-snapshots.md` - what to compare and what to leave out, per OS, including Windows equivalents.
- `scripts/host-snapshot.sh` - the snapshot tool (tested on macOS with bash 3.2 and in a Linux container).
