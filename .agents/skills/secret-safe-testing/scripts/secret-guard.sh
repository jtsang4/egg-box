#!/usr/bin/env bash
# secret-guard.sh - keep real credentials, and everything derived from them,
# out of a git repository, its history, commit messages and test output.
#
#   secret-guard.sh install --blocklist FILE [--never-commit ERE] [--exclude GLOB]... [--repo DIR] [--force]
#       installs pre-commit, commit-msg and pre-push hooks in the repository.
#       --never-commit  refuse any staged path matching this extended regex
#       --exclude       add a pattern to .git/info/exclude (a local ignore that
#                       is not versioned), for files the owner supplied
#   secret-guard.sh audit --blocklist FILE [--repo DIR]
#       checks the staged changes, every ref's history (messages and patches),
#       tag messages, and every file under the working tree, ignored ones too
#   secret-guard.sh scan --blocklist FILE PATH...
#       checks files or directories (test output, logs, captures); "-" reads
#       standard input. Run it on output before anybody reads that output.
#
# The blocklist is a text file kept OUTSIDE the repository, one literal string
# per line (lines starting with # are comments). Matching is plain substring
# matching. Blank lines and entries shorter than --min-length (default 6) are
# ignored, because a blank line would match everything and a short entry
# almost everything. Matched text is never printed: only which check failed,
# and where.
#
# Exit status: 0 clean, 1 something found, 2 usage or setup problem.
# Binary files are searched as text; images and archives are not decoded.
set -u

die() {
  echo "secret-guard: $*" >&2
  exit 2
}

usage() { awk 'NR > 1 && /^set -u/ { exit } NR > 1 { sub(/^# ?/, ""); print }' "$0"; }

cmd="${1:-}"
case "$cmd" in
  install | audit | scan) shift ;;
  -h | --help | help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

blocklist="${SECRET_GUARD_BLOCKLIST:-}"
never=""
repo="."
force=0
min=6
excludes=()
paths=()
while [ $# -gt 0 ]; do
  case "$1" in
    --blocklist | --never-commit | --exclude | --repo | --min-length)
      [ $# -ge 2 ] || die "$1 needs a value"
      case "$1" in
        --blocklist) blocklist="$2" ;;
        --never-commit) never="$2" ;;
        --exclude) excludes+=("$2") ;;
        --repo) repo="$2" ;;
        --min-length) min="$2" ;;
      esac
      shift 2
      ;;
    --force)
      force=1
      shift
      ;;
    -)
      paths+=("-")
      shift
      ;;
    -*) die "unknown option: $1" ;;
    *)
      paths+=("$1")
      shift
      ;;
  esac
done

[ -n "$blocklist" ] || die "no blocklist: pass --blocklist FILE or set SECRET_GUARD_BLOCKLIST"
[ -r "$blocklist" ] || die "cannot read the blocklist $blocklist"
# The hooks run from anywhere in the repository, so they need an absolute path.
blocklist="$(cd "$(dirname "$blocklist")" && pwd)/$(basename "$blocklist")"
case "$min" in '' | *[!0-9]*) die "--min-length must be a number" ;; esac

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/secret-guard.XXXXXX")" || die "cannot create a temporary directory"
trap 'rm -rf "$tmpdir"' EXIT

# The usable entries, one per line, in $tmpdir/entries. Says how many there are
# and how many were left out, never what they are.
clean="$tmpdir/entries"
tr -d '\r' <"$blocklist" | awk -v min="$min" '!/^[[:space:]]*(#|$)/ && length($0) >= min' >"$clean"
used="$(wc -l <"$clean" | tr -d ' ')"
total="$(tr -d '\r' <"$blocklist" | awk '!/^[[:space:]]*(#|$)/' | wc -l | tr -d ' ')"
[ "$used" -gt 0 ] || die "the blocklist has no usable entry (an empty guard would pass everything)"
skipped=$((total - used))

contains() { grep -aqFf "$clean"; }

# ---------------------------------------------------------------- install

quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

marker="# secret-guard (managed): reinstall with secret-guard.sh install"

hook_prelude() { # with-never: only the pre-commit hook looks at paths
  echo "#!/bin/sh"
  echo "$marker"
  echo "list=$(quote "$blocklist")"
  [ "${1:-}" = "with-never" ] && echo "never=$(quote "$never")"
  echo "min=$min"
  cat <<'EOF'
tmp="$(mktemp "${TMPDIR:-/tmp}/secret-guard.XXXXXX")" || exit 1
trap 'rm -f "$tmp"' EXIT
have_list=0
if [ -r "$list" ]; then
  tr -d '\r' <"$list" | awk -v min="$min" '!/^[[:space:]]*(#|$)/ && length($0) >= min' >"$tmp"
  [ -s "$tmp" ] && have_list=1
fi
if [ "$have_list" = 0 ]; then
  echo "secret-guard: the blocklist $list is missing or empty; the content check is skipped" >&2
fi
status=0
EOF
}

write_hook() { # name [with-never], the body of the hook on standard input
  local name="$1" dir hook
  dir="$(git -C "$repo" rev-parse --git-path hooks)" || die "$repo is not a git repository"
  case "$dir" in /*) ;; *) dir="$repo/$dir" ;; esac
  mkdir -p "$dir"
  hook="$dir/$name"
  if [ -e "$hook" ] && ! grep -qF "$marker" "$hook"; then
    if [ "$force" = 1 ]; then
      mv "$hook" "$hook.before-secret-guard"
      echo "secret-guard: kept the existing $name hook as $name.before-secret-guard"
    else
      die "a $name hook that is not ours already exists ($hook); chain it by hand or pass --force"
    fi
  fi
  {
    hook_prelude "${2:-}"
    cat
  } >"$hook"
  chmod +x "$hook"
  echo "secret-guard: installed $hook"
}

install_hooks() {
  git -C "$repo" rev-parse --git-dir >/dev/null 2>&1 || die "$repo is not a git repository"
  if [ -n "$(git -C "$repo" config --get core.hooksPath)" ] && [ "$force" = 0 ]; then
    die "core.hooksPath is set, so the hooks may be shared with other repositories; pass --force to install there anyway"
  fi

  write_hook pre-commit with-never <<'EOF'
if [ -n "$never" ]; then
  hits="$(git diff --cached --name-only --diff-filter=ACMR | grep -E -- "$never")"
  if [ -n "$hits" ]; then
    echo "secret-guard: these staged paths must never be committed:" >&2
    echo "$hits" >&2
    status=1
  fi
fi
if [ "$have_list" = 1 ]; then
  if git diff --cached -U0 --diff-filter=ACMR | grep -E '^\+' | grep -Ev '^\+\+\+ ' | grep -aqFf "$tmp"; then
    echo "secret-guard: a staged change contains a blocklisted string (not shown)" >&2
    status=1
  fi
  if git diff --cached --name-only --diff-filter=ACMR | grep -aqFf "$tmp"; then
    echo "secret-guard: a staged file name contains a blocklisted string (not shown)" >&2
    status=1
  fi
fi
exit "$status"
EOF

  write_hook commit-msg <<'EOF'
if [ "$have_list" = 1 ] && grep -aqFf "$tmp" "$1"; then
  echo "secret-guard: the commit message contains a blocklisted string (not shown)" >&2
  status=1
fi
exit "$status"
EOF

  write_hook pre-push <<'EOF'
zero=0000000000000000000000000000000000000000
while read -r local_ref local_sha _ remote_sha; do
  [ "$have_list" = 1 ] || break
  [ "$local_sha" = "$zero" ] && continue
  if [ "$remote_sha" = "$zero" ]; then
    # A new branch: everything no remote has yet.
    if git log -m -p --format=%B "$local_sha" --not --remotes | grep -aqFf "$tmp"; then
      echo "secret-guard: pushing $local_ref would publish a blocklisted string (not shown)" >&2
      status=1
    fi
  elif git log -m -p --format=%B "$remote_sha..$local_sha" | grep -aqFf "$tmp"; then
    echo "secret-guard: pushing $local_ref would publish a blocklisted string (not shown)" >&2
    status=1
  fi
done
exit "$status"
EOF

  if [ ${#excludes[@]} -gt 0 ]; then
    local exclude pattern
    exclude="$(git -C "$repo" rev-parse --git-path info/exclude)"
    case "$exclude" in /*) ;; *) exclude="$repo/$exclude" ;; esac
    mkdir -p "$(dirname "$exclude")"
    touch "$exclude"
    for pattern in "${excludes[@]}"; do
      if ! grep -qxF -- "$pattern" "$exclude"; then
        echo "$pattern" >>"$exclude"
        echo "secret-guard: added $pattern to $exclude"
      fi
    done
  fi
  echo "secret-guard: $used blocklist entries in use, $skipped skipped as too short"
}

# ------------------------------------------------------------------ audit

report() { printf '  %-44s %s\n' "$1" "$2"; }

audit_repo() {
  git -C "$repo" rev-parse --git-dir >/dev/null 2>&1 || die "$repo is not a git repository"
  local bad=0 hits c f
  echo "secret-guard audit: $used blocklist entries in use, $skipped skipped as too short"

  if git -C "$repo" diff --cached -U0 | contains; then
    report "staged changes" "FAIL"
    bad=1
  else
    report "staged changes" "ok"
  fi

  if git -C "$repo" log --all -m -p --format=%B 2>/dev/null | contains; then
    hits=""
    for c in $(git -C "$repo" log --all --format=%H); do
      if git -C "$repo" show -m --format=%B "$c" | contains; then hits="$hits ${c:0:10}"; fi
    done
    report "history, all refs (messages and patches)" "FAIL in commits:$hits"
    bad=1
  else
    report "history, all refs (messages and patches)" "ok"
  fi

  if git -C "$repo" for-each-ref --format='%(refname) %(contents)' | contains; then
    report "tag and ref messages" "FAIL"
    bad=1
  else
    report "tag and ref messages" "ok"
  fi

  hits="$(cd "$repo" && grep -rlaFf "$clean" --exclude-dir=.git . 2>/dev/null)"
  if [ -n "$hits" ]; then
    report "working tree, ignored files included" "FAIL in files:"
    while IFS= read -r f; do echo "      $f"; done <<<"$hits"
    bad=1
  else
    report "working tree, ignored files included" "ok"
  fi

  if [ "$bad" = 0 ]; then
    echo "secret-guard audit: clean"
  else
    echo "secret-guard audit: FOUND something (the text is not shown)"
  fi
  return "$bad"
}

# ------------------------------------------------------------------- scan

scan_paths() {
  [ ${#paths[@]} -gt 0 ] || die "scan needs at least one file, directory or -"
  local bad=0 p
  for p in "${paths[@]}"; do
    if [ "$p" = "-" ]; then
      cat >"$tmpdir/stdin"
      if contains <"$tmpdir/stdin"; then
        echo "secret-guard: standard input contains a blocklisted string (not shown)"
        bad=1
      fi
    elif [ -d "$p" ]; then
      local f
      while IFS= read -r f; do
        echo "secret-guard: $f contains a blocklisted string (not shown)"
        bad=1
      done < <(grep -rlaFf "$clean" "$p" 2>/dev/null)
    elif [ -r "$p" ]; then
      if contains <"$p"; then
        echo "secret-guard: $p contains a blocklisted string (not shown)"
        bad=1
      fi
    else
      die "cannot read $p"
    fi
  done
  if [ "$bad" = 0 ]; then
    echo "secret-guard: clean ($used entries checked)"
  fi
  return "$bad"
}

case "$cmd" in
  install)
    install_hooks
    ;;
  audit)
    audit_repo
    ;;
  scan)
    scan_paths
    ;;
esac
