---
name: go-release-quality-gates
description: Sets up and audits the CI and release gates of a cross-platform Go project (desktop apps, root services, CLIs). Covers a vet/staticcheck matrix over GOOS/GOARCH and build tags (dev-only tags included), pinned tools, race and real-engine test tiers, govulncheck on a weekly schedule, shellcheck and actionlint, audit sweeps for dead code and vulnerable dependencies, building macOS .pkg and Windows NSIS installers without installing them, version stamping, third-party license notices, and license review of embedded components. Use when creating or reviewing CI for a Go repo, before a release, when a change builds locally but may break another OS or tag set, when packaging or signing installers, or when shipping third-party code. Not for non-Go projects or for the security design of the program itself. Go 项目的 CI、打包、许可证与发布质量闸门。
---

# Go release quality gates

`go build ./... && go test ./...` checks one platform, one tag set, one toolchain, and only the code that is reachable from there. Everything the project promises beyond that (other operating systems, the release tags, installers, licenses, vulnerabilities) needs a gate of its own, and every gate has a blind spot that another gate covers.

## Gates and what they cannot see

| Defect class | Gate | Blind to |
|---|---|---|
| Formatting | `gofmt -l .` must print nothing | Nothing that is syntax. It walks hidden directories (`.agents/`, `.github/`) that `go vet ./...` ignores, so tools and examples added there must be gofmt-clean too |
| Module drift | `go mod tidy` then `git diff --exit-code -- go.mod go.sum` | Whether the code works |
| Breaks on another OS, architecture or tag set | vet and build for every target, with and without the dev tags (`scripts/vet-matrix.sh`, the `cross` job) | Behaviour: it only proves the code compiles and passes vet there |
| Dead code, deprecated calls, subtle mistakes | `staticcheck`, once per target platform, with the release tags | Code behind constraints that the target does not select |
| Data races | `go test -race -short -timeout 30m -count=1 ./...` | Paths the tests do not run; it needs cgo and a C compiler, so it belongs on a Linux job rather than on every cross target |
| Behaviour behind tags | a tier that runs the real engine or the e2e suite with the release and dev tags | Platforms other than the runner's |
| Platform code | tests on a macOS and a Windows runner | Anything that needs a real privileged install |
| Known vulnerabilities | `govulncheck` with the release tags, also on a weekly cron | Unknown ones. It reports what your code reaches, so a "not reachable" result still gets a look |
| Scripts that run as root | `shellcheck` over every tracked script, including ones without a `.sh` suffix | Whether the installer works |
| Workflows | `actionlint`, pinned | Whether the secrets exist |
| Project rules (no web front end, nothing under `docs/`, no secrets) | a grep guard in CI | Anything it does not grep for |
| Packaging | build each installer in CI, inspect its structure | Installing it. See "Packaging" below |
| Licenses | a notices generator that fails on a module without a license file | What a license requires of you |

A gate that CI never runs is not a gate. When a test tier needs tags (the real engine behind a build tag), CI must pass the tags, or the tier silently stops existing; this was found in a real project by an audit after everything was "green".

## Workflow

1. **Start from the template.** Copy `assets/ci.yml` to `.github/workflows/ci.yml`, adapt the lines marked `ADAPT` (release tags, dev tags, the packages of the real-engine tier, the project rules), and lint it with `go run github.com/rhysd/actionlint/cmd/actionlint@<pinned>`. `references/ci-gates.md` explains every step and the tiers.
2. **Check the matrix locally before pushing a change that touches build constraints.** `scripts/vet-matrix.sh -t "$RELEASE_TAGS" -e "$DEV_TAGS" -s -b ./...` vets (and staticchecks and builds) every target with and without the dev tags and exits non-zero if any combination fails, after trying them all. The first time this ran on a real project it found a Windows-only compile break that the dev tag had introduced: a release stub covered "not dev" and "dev on Unix" but not "dev on Windows".
3. **Pin what can change under you.** Tool versions are pinned and updated on purpose, in a change of their own (a new check must not fail a change that did not cause it). `govulncheck` is the exception: its value is being current. Build checkers with the toolchain the project uses; a checker built with an older Go refuses newer code.
4. **Sweep after green.** When everything passes, run the tools that look for what no test exercises: `govulncheck`, `staticcheck` for unused code, `go mod why` for odd dependencies, the licenses of what is compiled in. One real sweep found 9 reachable vulnerabilities (one a gRPC authorization bypass through a malformed request path), 26 unused identifiers, an embedded component whose license carried a naming clause, tests that CI never ran, and the Windows break above. After upgrading a dependency to fix an advisory, re-run the whole test matrix, not only the package that changed. The procedure for each tool, and how to triage its findings, is in `references/ci-gates.md` ("Sweeps after everything is green").
5. **Package without installing.** `references/packaging.md`: building and checking `.pkg` and NSIS installers, signing inside out, stamping versions, one source of truth for the version, a release workflow that signs only if the secrets exist.
6. **Ship notices and review licenses.** `references/licensing.md` and `assets/go-notices/`: generate the notices for what is actually compiled into each build, bundle them with the installer, and read the license of anything that matters (copyleft, naming or branding clauses).
7. **Keep the README honest.** Every command in the README's development section is the one CI runs. Run them from a clean checkout once.

## Rules of thumb

- Run each gate for what ships: the release tags, not only the defaults. A change that only breaks the release build must fail CI, not the release.
- Stub files for build-tag combinations come in sets that must cover every combination. Vet with and without each tag, on every platform, to prove it.
- Slow tests honour `-short`. The race run uses it; a second, non-race step runs the slow package in full. Give every test run an explicit `-timeout` so a slow runner is not a failure and a hang is not eternal.
- Prefer `go run module/cmd/tool@version` or the `tool` directive in `go.mod` over installing tools globally, so a clean checkout and CI use the same versions.
- A flaky gate is a defect, not noise. Triage it (see the test-triage reference of the `long-horizon-delivery` skill) rather than rerunning until green.
- One source of truth for the version, with a check that fails when a release names another one.
- Never let a gate depend on a secret that forks and pull requests do not have; steps that need secrets are conditional on a non-empty environment variable (a step cannot ask whether a secret exists, only whether a variable is non-empty).
- Do not install, load or register what you build while verifying it. Inspect packages with the tools that read them (see the `host-safe-verification` skill).

## Anti-patterns

- `|| true` or `continue-on-error` on a gate to "fix CI". Fix the finding, or delete the gate with a stated reason.
- `staticcheck@latest` or an unpinned linter in the required path.
- Vetting only the host platform and calling the project cross-platform.
- Counting a suite green when the tier you changed was skipped (a suspiciously fast pass is a skip).
- Shipping a build with notices generated from `go.mod` rather than from what the build compiles.
- Release instructions that only work on the maintainer's machine.

## References

- `references/ci-gates.md`: the template step by step, test tiers, pinning, schedule, local parity.
- `references/packaging.md`: macOS and Windows installers built and inspected without installing; signing; version stamping; release workflow and checksums.
- `references/licensing.md`: notices, the license review, embedded components with naming or copyleft terms.
- `scripts/vet-matrix.sh`: vet, staticcheck and build for every target and tag set.
- `assets/ci.yml`: the workflow template (validated with actionlint).
- `assets/go-notices/`: a generic third-party notices generator with its tests.
