# Environment gotchas of long unattended sessions

Small things that cost hours when met for the first time. Written for an agent driving a macOS or Linux shell through a tool, but most apply to people too.

## Running things

- **Background jobs started as `( cmd ) &` can die when the tool call returns.** Use the tool's own background mode, and poll for a marker line you print at the end (`ALLDONE`) with a wait/await primitive rather than `sleep` in the shell.
- **Long runs**: give `go test -race` an explicit `-timeout`; run heavy builds and timing-sensitive tests one after the other, not together.
- **Big outputs**: write them to a file and grep it. Do not cut with `head` when the error is at the end; use targeted searches or read the tail.
- **Parallel tool calls share the machine.** Never run, in parallel with other calls, anything that kills processes by pattern, cleans a shared directory, or reuses a fixed port.
- **Record the pid of what you start** and stop that pid. Single-instance apps exit on a second launch: that is not a crash.
- **Do not edit a bash script while it runs**: bash reads scripts incrementally and will execute a corrupted mixture.
- **Scripts that are not executable** run with `bash file` or `zsh file`; do not `chmod` just to test unless the file will ship that way (then set the bit and commit it).

## zsh

- `echo =====` fails ("`=` not found") because `=word` expands to a command path. Quote separators: `echo '====='`.
- An unmatched glob aborts the command (`rm -f ./*` in an empty directory) and, in an `&&` chain, everything after it. Use `find dir -mindepth 1 -delete`, or `setopt nonomatch`.
- Arrays are 1-indexed, and word splitting of unquoted variables does not happen: scripts meant for both shells should be bash and say so in the shebang.

## macOS tools

- BSD `sed -i ''` (empty suffix), no GNU long options; no `timeout` (use a background process and `kill`, or write the timeout into the program); `shasum -a 256` instead of `sha256sum`; `date -j`/`-v` instead of `date -d`; `grep -P` is absent (use `-E`); `ps -o time=` has centisecond resolution on macOS but whole seconds on Linux.
- `pgrep -f` matches the shell that is running your command and any system daemon with the pattern in its name (`agentd` matches `gamecontrolleragentd`). Anchor patterns and exclude your own ancestors, or look up by pid.
- `launchctl print` of `system/<label>` and `gui/<uid>/<label>` works without root and is a safe read-only status query. `bootstrap`, `bootout`, `kickstart` are not.
- Files created by `tar`/`zip` on macOS can carry AppleDouble `._*` entries from extended attributes such as `com.apple.provenance`. They are harmless; strip with `xattr -cr` before packaging if they would be shipped.

## Git

- Stage explicit paths. `git add -A` picks up owner files, scratch output and build artifacts.
- Hooks live in `.git/hooks` and are not versioned; `git rev-parse --git-path hooks` finds them (and `core.hooksPath` may redirect them).
- `.git/info/exclude` is a local, unversioned ignore: the right place for files that must never be committed but should not appear in the shared `.gitignore`.
- A commit message is read from stdin with `git commit -F -` and a quoted heredoc, which avoids shell quoting accidents.

## Go

- `go list -m -f '{{.Dir}}' <module>` finds a dependency's source in the module cache: the quickest way to check an API or option against the pinned version.
- Directories starting with `.` or `_`, and `testdata`, are ignored by `./...`; `gofmt -l .` still walks hidden directories, and so do `shellcheck` globs over `git ls-files`. Anything under `.agents/` that is Go or shell must be formatted and lint-clean.
- Build tags change which files exist: always say which tags a command was run with.
- A checker binary built with an older Go toolchain (`govulncheck`, `staticcheck`) can refuse or mis-analyze code that needs a newer one: rebuild it with the toolchain the project uses, and pin its version in CI.

## Reading results

- For "must not match" guard commands (a blocklist, a forbidden extension), exit status 1 means clean. Print the status explicitly (`echo "rc=$?"`) so a silent success cannot be mistaken for a skipped check.
- A command that prints nothing may not have run: check the exit status and that the section you expected is present.
