#!/usr/bin/env bash
# selftest.sh - proves that secret-guard.sh does what its documentation says,
# in a throwaway repository under the temporary directory. Run it after any
# change to secret-guard.sh and on a new machine. It touches no real
# repository, no real blocklist and no network; the "secrets" below are
# invented.
set -u

here="$(cd "$(dirname "$0")" && pwd)"
guard="$here/secret-guard.sh"
work="$(mktemp -d "${TMPDIR:-/tmp}/secret-guard-selftest.XXXXXX")"
trap 'rm -rf "$work"' EXIT

# Nothing from the machine's own git configuration may leak into the test.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid

secret="FAKE-SECRET-VALUE-4711"
host="node-7.fake.invalid"
failures=0
all_output="$work/all-output.txt"
: >"$all_output"

ok() { printf 'ok    %s\n' "$1"; }
bad() {
  printf 'FAIL  %s\n' "$1"
  failures=$((failures + 1))
}
# check "name" expected-status command...: runs the command, keeps its output
# for the final "never printed" check.
check() {
  local name="$1" want="$2" got
  shift 2
  "$@" >>"$all_output" 2>&1
  got=$?
  if [ "$got" = "$want" ]; then ok "$name"; else bad "$name (status $got, wanted $want)"; fi
}

printf '# invented values\n%s\n%s\nab\n\n' "$secret" "$host" >"$work/blocklist.txt"
printf '%s\r\n' "CRLF-ENTRY-123456" >>"$work/blocklist.txt"

repo="$work/repo"
git init -q "$repo"
git -C "$repo" config commit.gpgsign false

# --- install
check "install" 0 "$guard" install --blocklist "$work/blocklist.txt" --never-commit '^private/' --exclude 'private/' --repo "$repo"
for hook in pre-commit commit-msg pre-push; do
  if [ -x "$repo/.git/hooks/$hook" ]; then ok "$hook is installed and executable"; else bad "$hook missing"; fi
  if command -v shellcheck >/dev/null 2>&1; then
    check "shellcheck $hook" 0 shellcheck -s sh "$repo/.git/hooks/$hook"
  fi
done
if grep -qx 'private/' "$repo/.git/info/exclude"; then ok "local exclude written"; else bad "local exclude missing"; fi
check "install is repeatable" 0 "$guard" install --blocklist "$work/blocklist.txt" --never-commit '^private/' --repo "$repo"

commit() { git -C "$repo" commit -q "$@"; }

# --- pre-commit and commit-msg
echo "nothing to see" >"$repo/clean.txt"
git -C "$repo" add clean.txt
check "a clean commit passes" 0 commit -m "clean"

echo "token=$secret" >"$repo/leak.txt"
git -C "$repo" add leak.txt
check "a staged secret is refused" 1 commit -m "leak"
git -C "$repo" reset -q leak.txt
rm -f "$repo/leak.txt"

echo "$host" >"$repo/leak2.txt"
git -C "$repo" add leak2.txt
check "a second entry is refused" 1 commit -m "leak"
git -C "$repo" reset -q leak2.txt
rm -f "$repo/leak2.txt"

echo "x" >"$repo/CRLF-ENTRY-123456.txt"
git -C "$repo" add CRLF-ENTRY-123456.txt
check "a secret in a file name (CRLF blocklist) is refused" 1 commit -m "name"
git -C "$repo" reset -q CRLF-ENTRY-123456.txt
rm -f "$repo/CRLF-ENTRY-123456.txt"

echo "short entries are ignored: ab" >"$repo/short.txt"
git -C "$repo" add short.txt
check "an entry below the minimum length is ignored" 0 commit -m "short"

echo "fine" >"$repo/fine.txt"
git -C "$repo" add fine.txt
check "a secret in a commit message is refused" 1 commit -m "see $secret"
git -C "$repo" reset -q fine.txt
rm -f "$repo/fine.txt"

mkdir -p "$repo/private"
echo "owner file" >"$repo/private/design.txt"
git -C "$repo" add -f private/design.txt
check "a never-commit path is refused" 1 commit -m "private"
git -C "$repo" reset -q private/design.txt
rm -rf "$repo/private"

# --- audit
check "audit of a clean repository" 0 "$guard" audit --blocklist "$work/blocklist.txt" --repo "$repo"

echo "token=$secret" >"$repo/leak.txt"
git -C "$repo" add leak.txt
commit --no-verify -m "slipped past the hooks"
git -C "$repo" rm -q --cached leak.txt
rm -f "$repo/leak.txt"
commit --no-verify -m "removed again"
check "audit finds a secret that is only in history" 1 "$guard" audit --blocklist "$work/blocklist.txt" --repo "$repo"
git -C "$repo" reset -q --hard HEAD~2
git -C "$repo" reflog expire --expire=now --all
git -C "$repo" gc -q --prune=now
check "audit is clean again after the history is rewritten" 0 "$guard" audit --blocklist "$work/blocklist.txt" --repo "$repo"

echo "ignored=$secret" >"$repo/ignored.log"
echo "ignored.log" >>"$repo/.git/info/exclude"
check "audit finds a secret in an ignored file" 1 "$guard" audit --blocklist "$work/blocklist.txt" --repo "$repo"
rm "$repo/ignored.log"

git -C "$repo" tag -a v1 -m "release $host"
check "audit finds a secret in a tag message" 1 "$guard" audit --blocklist "$work/blocklist.txt" --repo "$repo"
git -C "$repo" tag -d v1 >/dev/null

# --- scan
echo "all quiet" >"$work/out-clean.txt"
echo "log line with $secret in it" >"$work/out-dirty.txt"
check "scan of a clean file" 0 "$guard" scan --blocklist "$work/blocklist.txt" "$work/out-clean.txt"
check "scan of a dirty file" 1 "$guard" scan --blocklist "$work/blocklist.txt" "$work/out-dirty.txt"
check "scan of a directory" 1 "$guard" scan --blocklist "$work/blocklist.txt" "$work"
check "scan of standard input" 1 "$guard" scan --blocklist "$work/blocklist.txt" - <<<"from a pipe: $host"

# --- pre-push
remote="$work/remote.git"
git init -q --bare "$remote"
git -C "$repo" remote add origin "$remote"
branch="$(git -C "$repo" symbolic-ref --short HEAD)"
check "pushing clean history passes" 0 git -C "$repo" push -q origin "$branch"
echo "late=$secret" >"$repo/late.txt"
git -C "$repo" add late.txt
commit --no-verify -m "slipped"
check "pushing a secret is refused" 1 git -C "$repo" push -q origin "$branch"
if git -C "$remote" log --all -p 2>/dev/null | grep -aqF "$secret"; then bad "the secret reached the remote"; else ok "the remote never saw the secret"; fi
git -C "$repo" checkout -q -b topic
check "pushing a new branch with the secret is refused" 1 git -C "$repo" push -q origin topic

# --- installing over somebody else's hook, and shared hook directories
other="$work/other"
git init -q "$other"
printf '#!/bin/sh\nexit 0\n' >"$other/.git/hooks/pre-commit"
chmod +x "$other/.git/hooks/pre-commit"
check "a foreign hook is not overwritten" 2 "$guard" install --blocklist "$work/blocklist.txt" --repo "$other"
check "--force keeps the foreign hook aside" 0 "$guard" install --blocklist "$work/blocklist.txt" --repo "$other" --force
if [ -f "$other/.git/hooks/pre-commit.before-secret-guard" ]; then ok "the old hook was kept"; else bad "the old hook was lost"; fi
git -C "$other" config core.hooksPath /nonexistent-shared-hooks
check "a shared hooks path is refused without --force" 2 "$guard" install --blocklist "$work/blocklist.txt" --repo "$other"

# --- an empty guard must not pass silently
printf 'ab\n\n# nothing usable\n' >"$work/empty.txt"
check "a blocklist with no usable entry is a setup error" 2 "$guard" scan --blocklist "$work/empty.txt" "$work/out-clean.txt"
check "a missing blocklist is a setup error" 2 "$guard" scan --blocklist "$work/does-not-exist.txt" "$work/out-clean.txt"

# --- the promise: matched text is never printed
if grep -aqF -e "$secret" -e "$host" -e "CRLF-ENTRY-123456" "$all_output"; then
  bad "a blocklisted string appeared in the output"
else
  ok "no blocklisted string was ever printed"
fi

if [ "$failures" = 0 ]; then
  echo "secret-guard selftest: all checks passed"
else
  echo "secret-guard selftest: $failures check(s) FAILED"
  exit 1
fi
