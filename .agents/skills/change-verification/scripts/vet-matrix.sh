#!/usr/bin/env bash
# vet-matrix.sh - run go vet (and optionally staticcheck and a build) for every
# platform a Go project supports and for every set of build tags it ships, from
# one machine. Code behind GOOS or tag constraints is only compiled, and so only
# checked, for the platform that selects it; a local `go vet ./...` says nothing
# about the other platforms.
#
#   vet-matrix.sh [-t TAGS] [-e TAGS] [-T "os/arch ..."] [-s] [-b] [PACKAGES]
#
#   -t TAGS   the build tags of the release (comma separated), used throughout
#   -e TAGS   a second pass for every target with these extra tags added, for
#             code that is only built for development or tests
#   -T LIST   targets, space separated (default: linux/amd64 darwin/arm64
#             darwin/amd64 windows/amd64 windows/arm64)
#   -s        also run staticcheck for every target (it must be installed)
#   -b        also build for every target (catches what only the linker finds)
#   PACKAGES  default ./...
#
# CGO is off unless CGO_ENABLED is set in the environment, because cross
# compiling with it needs a C toolchain for every target. Exits 1 if any
# combination fails, after trying all of them.
set -u

usage() { awk 'NR > 1 && /^set -u/ { exit } NR > 1 { sub(/^# ?/, ""); print }' "$0"; }

tags=""
extra=""
targets="linux/amd64 darwin/arm64 darwin/amd64 windows/amd64 windows/arm64"
static=0
build=0
while getopts "t:e:T:sbh" opt; do
  case "$opt" in
    t) tags="$OPTARG" ;;
    e) extra="$OPTARG" ;;
    T) targets="$OPTARG" ;;
    s) static=1 ;;
    b) build=1 ;;
    h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
done
shift $((OPTIND - 1))
pkgs=("$@")
[ ${#pkgs[@]} -gt 0 ] || pkgs=("./...")

if [ "$static" = 1 ] && ! command -v staticcheck >/dev/null 2>&1; then
  echo "vet-matrix: -s needs staticcheck (go install honnef.co/go/tools/cmd/staticcheck@<pinned version>)" >&2
  exit 2
fi

failed=0
summary=()

# step LABEL GOOS GOARCH COMMAND...: runs one check, remembers the verdict.
step() {
  local label="$1" goos="$2" goarch="$3" out
  shift 3
  if out="$(CGO_ENABLED="${CGO_ENABLED:-0}" GOOS="$goos" GOARCH="$goarch" "$@" 2>&1)"; then
    summary+=("ok    $label")
  else
    summary+=("FAIL  $label")
    failed=1
    printf '%s\n' "$out" | sed 's/^/      /' >&2
  fi
}

join_tags() { # a b -> a,b without stray commas
  local a="$1" b="$2"
  if [ -n "$a" ] && [ -n "$b" ]; then echo "$a,$b"; else echo "$a$b"; fi
}

for target in $targets; do
  goos="${target%/*}"
  goarch="${target#*/}"
  step "$target vet [$tags]" "$goos" "$goarch" go vet -tags "$tags" "${pkgs[@]}"
  if [ -n "$extra" ]; then
    all="$(join_tags "$extra" "$tags")"
    step "$target vet [$all]" "$goos" "$goarch" go vet -tags "$all" "${pkgs[@]}"
  fi
  if [ "$static" = 1 ]; then
    step "$target staticcheck [$tags]" "$goos" "$goarch" staticcheck -tags "$tags" "${pkgs[@]}"
  fi
  if [ "$build" = 1 ]; then
    step "$target build [$tags]" "$goos" "$goarch" go build -trimpath -tags "$tags" "${pkgs[@]}"
  fi
done

printf '%s\n' "${summary[@]}"
if [ "$failed" = 0 ]; then
  echo "vet-matrix: every combination passed"
else
  echo "vet-matrix: something failed (details above)" >&2
  exit 1
fi
