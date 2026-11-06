---
name: secret-safe-testing
description: Use real credentials (subscription links, tokens, private endpoints) in EggBox's opt-in live tests without leaking them, or anything derived from them, into git, commit messages, logs, fixtures, screenshots or the conversation - keeping the secret outside the repo, a blocklist of the secret plus what it yields, local git hooks, scanning output before reading it, count-only logging, invented fixtures and redaction. Use when the user hands over a real link, token or key, when running the RealSubscription/RealProviders live tests, or before pushing work that handled such data. Ships a tested guard script. 密钥不入库、真实订阅验证。
---

# Secret-safe testing

A real secret may be used by a test on this machine; nothing derived from it may reach git, a log, an artifact or this conversation.

## Rule zero: the secret is everything it yields

The link **and everything learned through it**: hosts, ports with hosts, UUIDs, passwords, keys, short ids, server and node names, exit IPs, account names. A subscription with 20 nodes is 20+ secrets; redacting only the link leaks the rest.

## Before the first live run on a machine

1. Keep the secret in a private directory outside the repo (`~/.eggbox-dev/`, mode 0700, files 0600) and pass it as a **file path**: `EGGBOX_TEST_SUBSCRIPTION_FILE`, `EGGBOX_TEST_SINGBOX_LINK_FILE`, `EGGBOX_TEST_CLASH_LINK_FILE` (never the value: it shows in shell history and `ps`).
2. If the hooks are not installed yet (`ls "$(git rev-parse --git-path hooks)"/pre-commit`), build a blocklist outside the repo (a script that reads the secret and writes the derived literals, one per line, ≥ 8 characters, **without printing them**; see `references/blocklist-and-hooks.md`) and run `scripts/secret-guard.sh install --blocklist FILE`. It installs `pre-commit`, `commit-msg` and `pre-push` hooks that name the failed check, never the text. Run `scripts/selftest.sh` once if the guard has never run on this machine.

Skip step 2 when the hooks and blocklist already exist; update the blocklist only when the secret changes.

## While running live tests

- The live tests are opt-in and print only counts, protocol names and failure classes; keep it that way in any test you add (subtests named by index, errors reduced to a class: Go's HTTP errors embed the full URL).
- **Scan before you read**: send output to a file in a private temp dir, `scripts/secret-guard.sh scan --blocklist FILE out.txt`, and read it only if clean. If not clean, delete it, fix the logging, rerun. Delete the file when done.
- Never open screenshots, raw captures or API responses made with real data; derive facts with scripts that print counts.
- Fixtures and demo data are invented (`example.net`, `*.invalid`, documentation IP ranges, zero UUIDs), never anonymized real data. Diagnostics redact with stable placeholders (`ip-1`, `host-2`) and keep loopback/private addresses.

## Before pushing

- Commit messages, branch and tag names, PR and issue text are published too.
- Stage explicit paths; never `--no-verify`.
- Run `scripts/secret-guard.sh audit --blocklist FILE` once before the push (staged changes, all history and messages, tags, the working tree including ignored files).

## If something leaked

1. Stop; do not echo it again to investigate.
2. Not pushed: remove it, rewrite the local commits, expire the reflog, prune, re-audit.
3. Pushed or shared: the credential is compromised. The user rotates or revokes it first; then clean history.
4. Fix the cause (the test that logged it, the missing blocklist entry) and add a test.

## References

- `references/blocklist-and-hooks.md` - what belongs in the blocklist, deriving it without printing it, what each hook and the audit check.
- `references/test-hygiene.md` - Go and shell patterns: opt-in live tests, error classes, count-only logs, invented fixtures, testing redaction.
- `scripts/secret-guard.sh` (`install`, `audit`, `scan`), `scripts/selftest.sh`.
