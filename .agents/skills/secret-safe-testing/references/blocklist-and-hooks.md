# Blocklist and hooks

## What the blocklist is

A text file kept outside every repository (for example `~/.<project>-dev/blocklist.txt`, mode 0600), one literal string per line, `#` for comments. `scripts/secret-guard.sh` does plain substring matching (`grep -F -f`) against staged additions, file names, commit messages, history, the working tree and any output you ask it to scan. Matched text is never printed.

The tool ignores blank lines and entries shorter than 6 characters (`--min-length` changes it), because a blank line matches everything and a short entry matches almost everything. It refuses to run with no usable entry, so an empty guard cannot pass silently.

## What belongs in it

- The credential in every form it is handled: the whole URL, each URL component that is secret (token path segment, query values, host), percent-encoded and base64 forms when the credential is transformed on the way.
- Everything a test learns through the credential: server host names, UUIDs and passwords, public keys and short ids, SNI and host headers, node display names, exit IPs observed by probing, account ids and emails.
- Not in it: protocol names, `localhost`, `127.0.0.1`, product words, anything short or common. A false positive blocks legitimate commits and teaches people to bypass the guard. Keep a small allow-list file of common words and filter derived entries against it.

## Deriving it without printing it

Write the derivation as a script and keep its output in the blocklist file only. Never `cat` the result, and do not look at the entries; the counts are enough.

From a URL in a file:

```bash
{ cat url.txt; tr '/?&=:@#' '\n' < url.txt; } | awk 'length($0) >= 8' | sort -u | grep -vxFf allowlist.txt >> "$BLOCKLIST"
```

From a JSON payload (every string value, at any depth):

```bash
jq -r '.. | strings' payload.json | awk 'length($0) >= 8' | sort -u | grep -vxFf allowlist.txt >> "$BLOCKLIST"
```

From a line-oriented payload (share links, one per line): split each line on the separators above, then filter the same way. For base64-wrapped payloads decode first, then derive. After deriving, `wc -l` the blocklist and compare with what you expect; do not print it.

Add entries again whenever the test learns something new (a probe reveals exit IPs; a new account is used).

## The hooks

`secret-guard.sh install` writes three hooks into the repository's hooks directory (refusing to overwrite a hook that is not its own unless `--force`, which keeps the old one aside, and refusing when `core.hooksPath` points somewhere shared):

| Hook | Checks | Refuses when |
| --- | --- | --- |
| `pre-commit` | staged paths against `--never-commit` (an extended regex); added lines of the staged diff; staged file names | a never-commit path is staged; a blocklisted string is added or is in a file name |
| `commit-msg` | the message | it contains a blocklisted string |
| `pre-push` | every commit that would be published (patch and message), including all commits of a new branch that no remote has | a blocklisted string would be published |

`--exclude GLOB` appends to `.git/info/exclude`, a local ignore that is not versioned: right for files the owner supplied (private design documents) that must stay out of the repository without adding anything to the shared `.gitignore`.

Limits to remember:

- Hooks live in `.git/hooks`, are not versioned, and `--no-verify` bypasses them. They protect the machine they are on.
- Binary files are searched as text; images and archives are not decoded.
- A secret split over lines, or in a form not in the blocklist (another encoding), is not seen.
- Only added lines are checked at commit time; the audit checks everything.

## The audit

`secret-guard.sh audit --blocklist FILE [--repo DIR]` reports ok or FAIL for:

1. staged changes;
2. history of every ref: patches and messages (merge commits included); on failure it names the commits (short hashes);
3. tag and ref messages;
4. every file under the working tree, ignored files and build output included (on failure it prints the paths; a path itself can be revealing, so do not paste it).

Exit status is 1 on any finding. Run it at milestones, before pushing, and as the last step of a long task. Also scan whatever else leaves the machine: release artifacts, `dist/` folders, logs you attach to an issue (`secret-guard.sh scan --blocklist FILE path...`, or `-` for standard input).

## Testing the guard

`scripts/selftest.sh` builds a throwaway repository with invented secrets and checks: each hook refuses what it should and passes clean commits, short entries are ignored, a CRLF blocklist works, history/ignored-file/tag leaks are found, a push with a leak never reaches the remote, foreign hooks are not overwritten, an empty blocklist is an error, and no invented secret ever appears in the tool's output. Run it after editing the script and on any new machine (it needs only bash, git, grep, awk, sed; `shellcheck` is optional).
