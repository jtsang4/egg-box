# CI gates in detail

The template is `assets/ci.yml`. This file explains why each part exists, what it cannot see, and how to read its failures.

## Jobs

| Job | Runs on | Answers |
|---|---|---|
| `check` | Linux | Everything that does not depend on the operating system, and the rules of the project: format, modules, scripts, workflows, static analysis, vet, tests in tiers |
| `vulnerabilities` | Linux, also weekly | Does what ships, as built with the release tags, reach a known vulnerability? |
| `platforms` (or one job per OS) | macOS, Windows runners | Does the platform layer pass its tests on its own platform? Add the installer build here (see `packaging.md`) |
| `cross` | Linux | Does everything compile and vet for every supported target, with and without the dev tags, from one machine? |
| release (separate workflow, tag-triggered) | per platform | Builds the installers, signs them if the secrets exist, publishes with checksums |

Keep `check` fast enough to wait for. Put slow, platform-bound or scheduled work in its own job so that a failure names its class at a glance. Use `concurrency` with `cancel-in-progress` so pushes to a branch do not queue up.

## The steps of `check`

- **Project rules** (`ADAPT`). A grep over `git ls-files` for what the project forbids (a web front end, committed secrets, files under a directory that is kept private). A rule that lives only in a document is forgotten; a rule that fails CI is kept.
- **gofmt**. `gofmt -l .` prints files; a non-empty list fails. It walks hidden directories that package patterns skip, so scripts and examples under `.agents/`, `.github/` or `tools/` are held to the same standard.
- **Tidy modules**. `go mod tidy` then `git diff --exit-code -- go.mod go.sum`. Catches an import added without `go mod tidy`, and a dependency left behind.
- **shellcheck**. `git ls-files -z '*.sh' ... | xargs -0 shellcheck`. Add the installer scripts that have no suffix (`preinstall`, `postinstall`). They run as root on someone else's machine, so they are held to the strictest standard in the repository.
- **actionlint**. Pinned. It also hands the shell inside each `run:` to shellcheck, which catches quoting bugs in the workflows themselves.
- **staticcheck**. Once per target platform (`CGO_ENABLED=0 GOOS=... GOARCH=...`) with the release tags, because it, like the compiler, only sees the code a platform selects. Pinned. Unused-code findings after a big feature are usually real: they are the abandoned approaches.
- **vet**. Plain, with the release tags, and with the dev tags. Add the project's own vet (for example a framework's `vet` subcommand) here.
- **race tests**. `go test -race -short -timeout 30m -count=1 ./...`. `-count=1` defeats the test cache; `-short` lets slow tests opt out; the explicit timeout is for slow runners.
- **slow tests in full**. The packages that honour `-short` run once in full without the race detector (drawing whole screens under `-race` is several times slower; the race tier still runs their quick tests).
- **real-engine tier**. The tests that need the release tags (the third-party engine behind a build tag, a generator checked by the real thing) and the dev tags (a development agent, an end-to-end suite). Without this step those tests do not run anywhere.

## Test tiers

| Tier | Command | Notes |
|---|---|---|
| Unit, quick | `go test -short ./...` | The inner loop. Deterministic; no network; no clock sleeps (see the test-triage reference of the `long-horizon-delivery` skill) |
| Race | `go test -race -short -timeout 30m -count=1 ./...` | Linux job. Needs cgo and a compiler |
| Slow, full | `go test -count=1 -timeout 15m ./pkg` | The packages that skip under `-short` |
| Real engine / e2e | `go test -count=1 -tags "$DEV,$RELEASE" ./engine/... ./e2e/...` | Loopback only; builds the real components behind tags |
| Platform native | the `platforms` job | Real XPC, named pipes, services as far as an unprivileged runner can go |
| Fuzz | the seeds run in every normal `go test`; long runs are manual: `go test -run '^$' -fuzz 'FuzzX$' -fuzztime 90s ./pkg` | A fuzz target without seeds tests nothing in CI |
| Live | an opt-in build tag or environment variable, never part of CI | Real credentials never go into CI logs (see `secret-safe-testing`) |

## Pinning and updating

- Pin linters and the workflow linter (`@vX.Y.Z`); update in a change that does only that and fix its new findings there.
- `govulncheck@latest` is deliberate: the database and analysis move quickly. Build it with the project's toolchain (`go run` does).
- Take the Go version from `go.mod` (`setup-go` with `go-version-file`) so CI and developers cannot disagree.
- Actions are pinned to a major tag; review their changelogs when bumping.

## Schedules

- A weekly cron on the vulnerability job: advisories appear without any commit. A red scheduled run means "upgrade", not "someone broke CI".
- Triage a vulnerability by reachability: if `govulncheck` shows a call path, upgrade the module (and re-run everything), and if it only imports the package, record why that is acceptable before ignoring it.

## Adding a gate

When a defect slips through, ask which gate would have caught it and add that gate in the same change that fixes the defect. Real examples:

- A break that only existed with the dev tag on one platform: add `go vet -tags dev,...` to the `cross` job, for every target.
- Tests behind tags that CI never ran: add the tagged tier.
- A vulnerability found by hand: add `govulncheck` with a schedule.
- A shell quoting bug in an installer script: add shellcheck for scripts without a suffix.

## Sweeps after everything is green

Run these once when a feature set is complete, and again before a release. They look for what no test exercises.

**Vulnerabilities.**
1. Build `govulncheck` with the project's toolchain (a binary built with an older Go cannot analyse newer code; `go run golang.org/x/vuln/cmd/govulncheck@latest` does it right). Pass the release tags.
2. Read each finding's call path. Reachable ones: upgrade the module to the fixed version or later with `go get module@version` (nothing in the module graph can cap it, so the risk is behavioural, not selectable), run `go mod tidy`, and look at the `go.mod` diff for a changed `go` directive.
3. Re-run everything after the upgrade: build and vet for every platform, the full tests, the race tier for the packages that use the upgraded module, the tagged and e2e tiers. An upgrade of a network or RPC stack can change behaviour (stricter path handling, for instance).
4. Findings in packages the code only imports, not calls: upgrade the trivial ones for tidiness; for a module with no fix, record why it is unreachable and leave it.
5. Add the scheduled CI job in the same change.

**Dead code.**
1. Run `staticcheck` for every configuration that ships: the release tags on each platform. A run with the dev tags reports as unused the code that the dev tag replaces, and a run for one platform reports another platform's helpers as unused; neither is a defect.
2. Remove what is genuinely unused, then run again: each removal unmasks the next layer. Read the context first: a declaration can sit in a `var` block or have a doc comment that goes with it.
3. Look for machinery that the tool cannot call dead because something still reads it: a field that is read and initialized but never meaningfully written, a pruning function for a feature that was removed.
4. After removing UI code, run the orphan-text test (see `native-ui-quality-loop`): translations for deleted screens linger otherwise.
5. Fix deprecation findings in the same pass (a deprecated field in an interface implementation, for example).

**Security linter on privileged code.** `gosec` over the code that runs with elevated rights, excluding generated files. Triage by tracing each finding to the check that follows it: most integer-conversion findings (G115) are caught by a bounds check one line later, a weak random generator in demo data is fine, a path from an environment variable in a test is fine. Fix what is real; do not add the tool to CI unless its noise is manageable.

## Reading failures

- `undefined: X` on one platform only: a stub is missing for a build-constraint combination. List the constraints of the files that define `X`, and write down the combinations (see the sandbox-by-construction reference of the `host-safe-verification` skill, section "build-tag trio").
- `build constraints exclude all Go files in ...`: a package without a file for that platform. Add a stub or exclude the package from that target.
- A test passes locally and fails on the Windows runner: paths (`\`), line endings (add a `.gitattributes` with `eol=lf` for Go sources, scripts and golden files), file locking (an open file cannot be deleted), and the default shell (PowerShell). Use `shell: bash` for steps that run scripts.
- `govulncheck` fails with no change: a new advisory. Read the call path.
- `go mod tidy` diff: run it locally with the same Go version as `go.mod`.

## Reproduce a CI failure locally

- Any platform's compile and vet errors: `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet -tags "$TAGS" ./...` from any machine (`scripts/vet-matrix.sh` loops over all of them).
- The race tier: `go test -race -short -timeout 30m -count=1 ./...`.
- The workflow itself: `go run github.com/rhysd/actionlint/cmd/actionlint@<pinned>`.
- Keep the README's development commands identical to these, and run them once from a clean checkout.
