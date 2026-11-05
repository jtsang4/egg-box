# Verification matrix

Pick the tiers that apply, record the exact command for each in the README (so a newcomer and CI run the same thing), and keep the column "cannot see" in mind: it tells you which sweep to add.

| # | Tier | Typical command (Go example) | Catches | Cannot see |
| --- | --- | --- | --- | --- |
| 0 | Format and module hygiene | `gofmt -l .`; `go mod tidy` then `git diff --exit-code go.mod go.sum` | drift, forgotten tidy | everything else |
| 1 | Static checks, every platform | vet matrix (`go-release-quality-gates/scripts/vet-matrix.sh`), pinned `staticcheck`, `shellcheck`, `actionlint` | platform-only compile errors, dead code, script and workflow mistakes | behaviour |
| 2 | Unit tests | `go test ./...` | logic | races, real engines, other platforms |
| 3 | Race detector | `go test -race -short -timeout 30m ./...` | data races | what the tests never exercise concurrently |
| 4 | Fuzz and property tests | seeds run as ordinary tests; 60-90 s `-fuzz` runs locally for gates and parsers | what a validator lets through | logic the oracle shares with the code |
| 5 | Tagged real-engine tests | `go test -tags "dev,$RELEASE_TAGS" ./...` for the adapter packages | integration with the real core, build-tag-dependent features | the real OS |
| 6 | Sandbox end to end | in-process service + sim driver + local origin | the whole flow, wire API, rollback | OS behaviour |
| 7 | Live-credential end to end | opt-in tag + env file (`secret-safe-testing`) | real-world formats and servers | anything not in that account |
| 8 | UI matrix | headless screenshots (pages x themes x languages x platforms x sizes) and interaction tests (`native-ui-quality-loop`) | layout, copy, states, regressions | true GPU / window-system behaviour |
| 9 | Vulnerability scan | `govulncheck` built with the current toolchain, in CI and weekly | reachable known CVEs | unknown ones |
| 10 | Packaging | build every installer; inspect without installing (`go-release-quality-gates`) | broken scripts, signing, payload mistakes | install-time behaviour |
| 11 | Host state | `host-snapshot.sh` before and after (`host-safe-verification`) | collateral changes | changes by other software |
| 12 | Content audits | `secret-guard.sh audit`; forbidden-technology check; license notices generation | leaks, excluded tech, missing licenses | secrets not in the blocklist |
| 13 | Idle resource use | `ab-cpu.sh` for GUI/daemons (`native-ui-quality-loop`) | CPU regressions | memory growth over days |

## Order

Cheap and broad first (0, 1, 2), then the expensive and narrow. A failure in an early tier usually explains later ones. Run the sandbox and live tiers (6, 7) only with the host-safety and secret guards in place. Snapshot the host before the first tier that starts processes and after the last.

## At the final commit

Run the entire matrix again at the commit you will report. Record the commit hash with the results. Anything changed since (even a comment-only commit that touches build tags or generated files) needs the affected tiers again.

## Runtime notes

- `-race` is 5-20x slower for UI tests that draw whole screens: give the step an explicit timeout (30 min) and run screenshot generation as a separate non-race step.
- Cross-platform tests are only real on that platform: run them on its CI runner (disposable), not by imitation.
- Fuzzing: a failing input is either a gate bug or an oracle bug. Decide which before touching anything (`untrusted-input-gates`). Delete stale corpus entries after fixing an oracle.
- Keep each tier's output small. Print summaries, write details to a file, search the file.
