#!/bin/bash
# Builds ServiceBridge.dylib for Apple Silicon and Intel and joins them.
#
#   build.sh OUTPUT_DIR [ARCH...]
#
# With no ARCH it builds both (arm64 x86_64). The result is
# OUTPUT_DIR/ServiceBridge.dylib; it is not signed (the packaging step signs it
# together with the application).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="${1:?usage: build.sh OUTPUT_DIR [ARCH...]}"
shift || true
archs=("$@")
[ ${#archs[@]} -gt 0 ] || archs=(arm64 x86_64)

mkdir -p "$out"
parts=()
for arch in "${archs[@]}"; do
  part="$out/ServiceBridge-$arch.dylib"
  xcrun --sdk macosx swiftc -emit-library -O -module-name ServiceBridge -swift-version 5 \
    -target "$arch-apple-macos13.0" \
    -framework Foundation -framework ServiceManagement -framework SystemConfiguration \
    -framework IOKit \
    -Xlinker -install_name -Xlinker @rpath/ServiceBridge.dylib \
    -o "$part" "$here"/Sources/*.swift
  parts+=("$part")
done

if [ ${#parts[@]} -eq 1 ]; then
  cp "${parts[0]}" "$out/ServiceBridge.dylib"
else
  lipo -create "${parts[@]}" -output "$out/ServiceBridge.dylib"
fi
rm -f "${parts[@]}"
lipo -info "$out/ServiceBridge.dylib"
