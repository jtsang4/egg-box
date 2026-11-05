---
name: secret-safe-testing
description: Guides testing against real credentials (subscription links, API keys, tokens, private endpoints) without leaking them, or anything derived from them, into git history, commit messages, test output, logs, fixtures, diagnostics, screenshots or the conversation. Covers keeping secrets outside the repo, a blocklist of the secret plus everything it yields, local git hooks, history audits, scanning output before reading it, count-only test logging, invented fixtures and redacted diagnostics. Use when the user hands over a real token, URL or key for verification, when a test needs live credentials, or before committing or pushing in a project that handled such secrets. Ships a tested guard script. 密钥不入库、真实订阅验证。
---

# Secret-safe testing

Real credentials make verification honest and leaks easy. The goal is that a real secret can be used by a test on the dev machine while nothing derived from it can reach git, a log, an artifact or this conversation.

## Rule zero: the secret is everything it yields

Treat the credential **and everything learned through it** as secret: hosts, ports paired with hosts, UUIDs, passwords, public keys, short ids, server names, node display names, exit IPs found by probing, account names, emails. A subscription link that returns 20 nodes is 20+ secrets. Redacting only the link leaks the rest.

## Set up once per project and machine

1. Keep the secret outside the repository in a private directory (`~/.<project>-dev/`, mode 0700, files 0600). Pass it to tests as a **file path** in an env var (`APP_TEST_SECRET_FILE`), not as a value: values show up in shell history and process lists.
2. Build a blocklist (a file outside the repo, one literal per line) with a script that reads the credential and writes derived literals **without printing them**. Drop entries shorter than 8 characters and common words. Details: `references/blocklist-and-hooks.md`.
3. Install the guards: `scripts/secret-guard.sh install --blocklist FILE --never-commit '^private/' --exclude 'private/'`. This adds `pre-commit`, `commit-msg` and `pre-push` hooks and a local ignore. The hooks print which check failed, never the text.
4. Run `scripts/selftest.sh` once (throwaway repo, invented secrets) to confirm the guard works on this machine.

Hooks are local and `--no-verify` skips them, so also run `secret-guard.sh audit` at milestones and before any push. CI has no blocklist: the protection there is that real data never enters the repo.

## While working

- **Opt-in tests**: gate live tests on the env var and a build tag; skip when absent. Never run them by default or in CI.
- **Log counts and classes, not content**: number of items, protocol names, failure classes (`timeout`, `refused`, `tls`, `dns`), durations. No server, name, address or id, not even a prefix. Name subtests by index.
- **Scrub errors**: Go's HTTP errors embed the full URL; unwrap to the cause before logging.
- **Scan before you read**: `secret-guard.sh scan --blocklist FILE out.txt` on any output produced with real data, then read it only if clean. If it is not clean, delete it, fix the logging, rerun. Never paste it anywhere.
- **Derive facts with scripts that print counts** instead of opening raw captures or API responses.
- **Screenshots and recordings made with real data** are never opened by the assistant and never stored in a tracked path. Use invented fixtures for visual work.
- **Fixtures and demo data are invented** (`example.net`, `*.invalid`, RFC 5737 and 3849 address ranges, all-zero UUIDs), never "anonymized" real data.
- **Diagnostics and exports** redact with stable placeholders (`ip-1`, `host-2`) so reports stay readable; keep loopback and private addresses (needed to diagnose); test the redaction with known secrets.

## Before every commit and push

- The hooks run automatically (staged additions, staged file names, the message, and on push every commit that would be published). Hooks are skipped by `--no-verify`, so never use it here.
- Stage files explicitly; never `git add -A` or `git add .` in a repository that has owner files or secrets around, and look at `git diff --cached --stat` before committing.
- Commit messages, branch names, tag messages, PR text and issue text count as published.
- `secret-guard.sh audit --blocklist FILE` checks staged changes, all history (patches and messages, every ref), tag messages, and every file in the working tree including ignored ones. Run it at milestones and before the final report.

## If something leaked

1. Stop; do not echo it again to investigate.
2. Not pushed: remove it from the files, rewrite the local commits (`reset`, `rebase`, `commit --amend`), expire reflog and prune, then re-run the audit.
3. Pushed or shared: treat the credential as compromised. Rotate or revoke it first, then clean up history.
4. Fix the cause (the test that logged it, the missing blocklist entry) and add a test.

## Anti-patterns

- `.gitignore` as the protection (ignored files still sit in the working tree and in screenshots and logs).
- Redacting by the secret's shape (regex for UUIDs) and missing derived values.
- Printing "just the first four characters" or the host "for context".
- Letting the assistant read a raw capture, a raw API response or a screenshot of real data.
- Blocklist entries that are blank or very short (a blank line matches everything with `grep -F -f`, short ones match everywhere; teams then bypass the hook).
- A blocklist stored inside the repository.
- Secrets in test names, golden files, snapshots, or error strings.

## References

- `references/blocklist-and-hooks.md` - what belongs in the blocklist, deriving it safely, what each hook and audit check does and cannot see.
- `references/test-hygiene.md` - Go and shell patterns: opt-in live tests, error scrubbing, count-only logs, invented fixtures, redaction placeholders, proving the redaction.
- `scripts/secret-guard.sh` (`install`, `audit`, `scan`) and `scripts/selftest.sh` - tested on macOS (bash 3.2) and Linux (bash 5).
