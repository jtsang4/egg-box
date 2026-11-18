---
name: change-verification
description: How to verify a change in EggBox before committing it, sized to what the change touches - the safety lines that always hold (the host network is never changed, nothing is installed or run as root), which local checks each kind of change needs, when to hand the full matrix to CI instead of running it locally, how to handle a failing or flaky test, and how to report what was and was not verified. Use when about to commit, when deciding what tests to run, when a test fails, when cutting a release, or when finishing a large multi-step task. 按改动范围验证、提交前检查、发布。
---

# Change verification

Run the checks that can catch a defect in **what you changed**, then stop. The full matrix (every OS, race, real core, screenshots, staticcheck, govulncheck, installers) already exists in CI and runs automatically as the prerequisite of every release tag; do not replay it locally.

## Lines that always hold

- **Never change the host.** Tests and `agentd dev` use the simulated driver and loopback only; keep it that way. Never run anything under `sudo`, never install or register what you build (`installer`, `launchctl bootstrap`, `SMAppService.register`, `sc create`), never run the service entry, cleanup or uninstaller locally. Read-only queries (`ifconfig`, `netstat -rn`, `scutil --dns`, `launchctl print`) are fine. If something does need a real machine, say so instead of trying it.
- **Real credentials** (a subscription link, a token): follow `secret-safe-testing` before running anything with them.
- **Commits**: stage explicit paths, never `git add -A`; commit locally; do not push unless asked.
- **Never weaken an assertion, lengthen a sleep or rerun until green** to get a pass. Understand the failure first.

## What to run for a change

Always: `gofmt -l .` prints nothing, and `go vet` on the packages you touched. Then add only the rows that match:

| The change touches | Run locally |
|---|---|
| Ordinary Go logic | `go test -count=1 ./<touched packages>/...` |
| Goroutines, locks, channels, shared state | the same with `-race` |
| `internal/gui` | see `gui-changes` (targeted tests, only the affected screenshots) |
| Build tags, `_windows`/`_darwin` files, the `eggbox_dev` split | `scripts/vet-matrix.sh -t "$TAGS" -e eggbox_dev ./<touched>/...` (all targets, with and without the dev tag) |
| `internal/agent/policy`, `configgen`, `engine/singbox`, routing that reaches the core | the touched packages plus the real-core tier for them: `go test -count=1 -tags "eggbox_dev,$TAGS" ./internal/agent/engine/... ./internal/configgen/...` (add `./internal/agent/e2e/...` when behaviour end to end changes); see `privileged-boundary` |
| `go.mod` (a dependency upgrade) | `go mod tidy` clean, backend `-short` suite, real-core tier, `go run golang.org/x/vuln/cmd/govulncheck@latest -tags "$TAGS" ./...` |
| `.github/workflows`, shell scripts | `actionlint` (pinned in `ci.yml`), `shellcheck` on the changed scripts |
| Packaging scripts | build the package and inspect it (`pkgutil --payload-files`, `pkgutil --expand-full`, `codesign --verify`, `makensis`); never install it |
| README commands | run the commands you changed |

`TAGS=with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname` (the release tags; the README and `ci.yml` hold the same list).

Measured on the maintainer's laptop (no race, warm cache): backend `-short` suite about 55 s; GUI `-short` suite about 30 s; real-core tier about 2 min, of which `internal/agent/e2e` is almost all (engine and configgen alone take about 15 s). The `-race` versions are several times slower, the GUI one by far the most. Run a whole suite only when the change really spans it; otherwise pass `-run` and package paths.

## When the whole matrix is worth it

Use CI (`gh workflow run ci.yml --ref <branch>`, add `-f installers=true` for packaging changes; it runs the pushed ref, so ask before pushing) instead of a local full run when: a change crosses several areas (service + GUI + platform), it changes platform code that only a macOS or Windows runner exercises, or it is the last commit before a release. A release tag runs the same verification as a prerequisite, so a release does not need a local full run.

## Releasing

- Bump the version in `mygo.json` and the default in `internal/version/version.go` together and commit `chore: release X.Y.Z` (the packaging scripts and the release workflow refuse a tag or `EGGBOX_VERSION` that disagrees with `mygo.json`).
- Pushing the tag `vX.Y.Z` runs the full CI verification as a prerequisite, then builds and publishes. A tag push publishes, so only on the user's say-so.
- Third-party notices are generated from what each build compiles (`scripts/notices`, called by the packaging scripts). When a new dependency is embedded, read its license for copyleft or naming terms.

## When a test fails

1. Read the failure and classify it: product bug, test bug, flake, environment (another heavy build running at the same time is the usual one).
2. Fix the cause, then re-run the failing test and the tests of the packages the fix touched.
3. A flake is a bug: reproduce with `-run '^TestX$' -count=20`, replace sleeps with waiting on the condition, give the test private resources (`:0` ports for TCP and UDP, `t.TempDir()`, `t.Setenv`), prove it with the same `-count` run.
4. A suite that passes suspiciously fast may have skipped: check the build tags and env vars it needs, and count `--- SKIP` with `-v`.

## Reporting

- Say exactly what ran (commands, tags) and what did not. "Compiled and vetted for Windows" is not "works on Windows"; behaviour that needs a real install (service approval, real TUN/route/DNS, upgrade over an older install, signing) is reported as not verified here.
- Decisions that belong to the owner (license, naming, signing identity) are reported, not made.
- If you started processes, stop them by recorded pid (never `pkill` by pattern: `agentd` also matches `gamecontrolleragentd`), and remove temp directories.

## Large, multi-step tasks

For a task that spans hours or many commits:

- Keep the requirements and constraints, including ones added mid-task, as a numbered list in the todo list; re-read it before each stage and before the final report.
- Prove the riskiest assumption first with a small spike. Check a dependency feature against its pinned source (`go list -m -f '{{.Dir}}' <module>`) before designing around it.
- Commit per coherent stage, each verified by the table above.
- At the end, run CI on the final commit (with the user's OK to push), then report requirement by requirement: done (with the evidence), not verified here (and how to verify it), open decision, or not done.

## Shell pitfalls met here

- zsh: `echo =====` fails (quote it); an unmatched glob aborts the command.
- macOS: BSD `sed -i ''`, no `timeout`, no `grep -P`.
- macOS cgo links print `ld: warning: ignoring duplicate libraries: '-lobjc'`: the go command adds `-lobjc` once per Objective-C cgo package (golang/go#67799), harmless. `scripts/ci/backend-tests.sh` silences it; a direct `go build`/`go test` still shows it.
- Use the tool's background mode for long runs, not `( cmd ) &`; do not run timing-sensitive tests beside a heavy build.
- `gofmt -l .` and the CI `shellcheck` step also cover files under `.agents/`.

## Scripts

- `scripts/vet-matrix.sh` - vet (and with `-s` staticcheck, with `-b` build) every target with and without extra tags; exits non-zero if any combination fails.
- `scripts/host-snapshot.sh` - read-only snapshot of interfaces, routes, proxy and DNS (`--compare FILE` prints `identical` or a diff). Use it only when you ran something beyond tests and the dev build that could plausibly touch the host; differences from the user's own VPN or Wi-Fi are not yours.
