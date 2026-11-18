#!/usr/bin/env bash
# Run backend and native-platform tests; the GUI has a dedicated Linux job.
set -euo pipefail

# On macOS the go command adds its own -lobjc for every cgo package with
# Objective-C files, sing-box has four, and Apple's linker warns "ignoring
# duplicate libraries: '-lobjc'" at every test binary it links. Harmless (the
# library is linked once); golang/go#67799 is fixing it in the go command.
# Until then the linker is told not to warn, if it knows the flag (Xcode 15+).
quiet=-Wl,-no_warn_duplicate_libraries
if [ "$(uname -s)" = Darwin ]; then
  probe="$(mktemp -d)"
  if printf 'int main(void) { return 0; }\n' >"$probe/main.c" &&
    cc "$quiet" -o "$probe/main" "$probe/main.c" 2>/dev/null; then
    export CGO_LDFLAGS="${CGO_LDFLAGS:--O2 -g} $quiet"
  fi
  rm -rf "$probe"
fi

listed="$(go list ./...)"
packages=()
while IFS= read -r package; do
  case "$package" in
    */internal/gui) ;;
    *) packages+=("$package") ;;
  esac
done <<< "$listed"
exec go test "$@" "${packages[@]}"
