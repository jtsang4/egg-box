#!/usr/bin/env bash
# Run backend and native-platform tests; the GUI has a dedicated Linux job.
set -euo pipefail

listed="$(go list ./...)"
packages=()
while IFS= read -r package; do
  case "$package" in
    */internal/gui) ;;
    *) packages+=("$package") ;;
  esac
done <<< "$listed"
exec go test "$@" "${packages[@]}"
