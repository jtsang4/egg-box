#!/usr/bin/env bash
# Builds the macOS application with its privileged service, and the installer.
#
#   packaging/macos/build.sh [--arch universal|arm64|x86_64] [--no-pkg]
#
# What comes out, in $SBUI_OUT (default build/macos):
#
#   Sing-Box UI.app          the application, with the service inside it
#   SingBoxUI-<version>.pkg  the installer (unless --no-pkg)
#
# The application bundle holds, besides the program of the interface:
#
#   Contents/MacOS/agentd                                 the service
#   Contents/Frameworks/ServiceBridge.dylib               what only Swift can do
#   Contents/Library/LaunchDaemons/<label>.plist          how launchd runs it
#
# Environment:
#
#   SBUI_VERSION          the version; default: the one in mygo.json
#   SBUI_COMMIT           the source revision; default: git's
#   SBUI_SIGN_IDENTITY    codesign identity for the code; "-" (the default) signs
#                         ad hoc, which is enough to try the build here and is
#                         not enough to ship: a Developer ID is.
#   SBUI_INSTALLER_IDENTITY  identity that signs the .pkg; unsigned when unset.
#
# Nothing here installs anything or registers anything with the system.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

arch=universal
make_pkg=1
while [ $# -gt 0 ]; do
  case "$1" in
    --arch) arch="$2"; shift 2 ;;
    --no-pkg) make_pkg=0; shift ;;
    *) echo "usage: $0 [--arch universal|arm64|x86_64] [--no-pkg]" >&2; exit 2 ;;
  esac
done
case "$arch" in
  universal) archs=(arm64 x86_64); mygo_platform=darwin/universal ;;
  arm64)     archs=(arm64);        mygo_platform=darwin/arm64 ;;
  x86_64)    archs=(x86_64);       mygo_platform=darwin/amd64 ;;
  *) echo "unknown architecture: $arch" >&2; exit 2 ;;
esac

goarch() { case "$1" in arm64) echo arm64 ;; x86_64) echo amd64 ;; esac; }

# The version the application's metadata carries comes from mygo.json, so a
# release must name the same one.
config_version="$(python3 -c 'import json;print(json.load(open("mygo.json"))["version"])')"
version="${SBUI_VERSION:-$config_version}"
if [ "$version" != "$config_version" ]; then
  echo "SBUI_VERSION is $version but mygo.json says $config_version: change mygo.json first (the metadata of the application comes from it)" >&2
  exit 1
fi
commit="${SBUI_COMMIT:-$(git rev-parse --short=9 HEAD 2>/dev/null || true)}"
identity="${SBUI_SIGN_IDENTITY:--}"
out="${SBUI_OUT:-$root/build/macos}"
work="$out/work"
app_id=me.jtsang.singboxui
agent_id=me.jtsang.singboxui.agent
tags="with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname"
stamp="-X github.com/jtsang4/sing-box-ui/internal/version.Version=$version -X github.com/jtsang4/sing-box-ui/internal/version.Commit=$commit"
ldflags="-s -w $stamp"
# MyGo adds these to the ones it uses for the application.
export MYGO_LDFLAGS="$stamp"

say() { printf '==> %s\n' "$*"; }

# Only what this script makes is removed, never the folder as a whole.
rm -rf "$work" "$out/Sing-Box UI.app" "$out/SingBoxUI-$version.pkg"
mkdir -p "$work"

# 1. The Swift library, for each architecture, joined into one file.
say "ServiceBridge.dylib ($arch)"
native/macos/ServiceBridge/build.sh "$work" "${archs[@]}"

# 2. The service, for each architecture, joined into one file.
for a in "${archs[@]}"; do
  say "agentd ($a)"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$(goarch "$a")" \
    go build -trimpath -tags "$tags" -ldflags "$ldflags" -o "$work/agentd-$a" ./cmd/agentd
done
if [ "${#archs[@]}" -gt 1 ]; then
  lipo -create "${archs[@]/#/$work/agentd-}" -output "$work/agentd"
else
  cp "$work/agentd-${archs[0]}" "$work/agentd"
fi

# 3. The launchd property list, written by the service's own program so that
#    the names in it can never drift from the ones in the code. The program of
#    this machine's architecture runs it.
host="$(uname -m)"
[ -f "$work/agentd-$host" ] || { say "building a host agentd to write the property list"; \
  CGO_ENABLED=0 go build -trimpath -o "$work/agentd-host" ./cmd/agentd && cp "$work/agentd-host" "$work/agentd-$host"; }
"$work/agentd-$host" print-plist > "$work/$agent_id.plist"
plutil -lint "$work/$agent_id.plist" >/dev/null

# 4. The interface, made by MyGo (it signs the bundle with the identity).
say "application ($mygo_platform)"
go tool mygo build -platform "$mygo_platform" -skip-dmg -skip-build-command -sign "$identity" -o "$work/mygo" >"$work/mygo.log" 2>&1 \
  || { cat "$work/mygo.log" >&2; exit 1; }
built="$(find "$work/mygo" -maxdepth 3 -name '*.app' -type d | head -1)"
[ -n "$built" ] || { echo "mygo build produced no .app; see $work/mygo.log" >&2; exit 1; }
app="$out/Sing-Box UI.app"
cp -R "$built" "$app"

# 5. The service goes into the bundle, and everything is signed from the
#    inside out: the service under its own identifier, which is what the
#    application asks for when it connects, then the bundle.
say "assembling the bundle"
mkdir -p "$app/Contents/Frameworks" "$app/Contents/Library/LaunchDaemons"
cp "$work/agentd" "$app/Contents/MacOS/agentd"
cp "$work/ServiceBridge.dylib" "$app/Contents/Frameworks/ServiceBridge.dylib"
cp "$work/$agent_id.plist" "$app/Contents/Library/LaunchDaemons/$agent_id.plist"
chmod 755 "$app/Contents/MacOS/agentd"

# Extended attributes (quarantine, Finder information, ...) do not belong in
# the signature or in the installer, which would carry them as "._" files.
# The mark some systems put on every file a tracked program writes
# (com.apple.provenance) is the one thing this cannot remove; it is only a
# mark, and the signature is verified with it below.
xattr -cr "$app"

sign_flags=(--force --timestamp=none)
if [ "$identity" != "-" ]; then sign_flags=(--force --timestamp --options runtime); fi
codesign "${sign_flags[@]}" -s "$identity" "$app/Contents/Frameworks/ServiceBridge.dylib"
codesign "${sign_flags[@]}" -s "$identity" -i "$agent_id" "$app/Contents/MacOS/agentd"
codesign "${sign_flags[@]}" -s "$identity" "$app"
codesign --verify --strict --deep "$app"
say "signed: $(codesign -dv "$app" 2>&1 | grep -E '^(Identifier|Signature)' | tr '\n' ' ')"
say "service: $(codesign -dv "$app/Contents/MacOS/agentd" 2>&1 | grep -E '^Identifier' | tr '\n' ' ')"

# 6. The installer. The files are installed owned by root: the service runs as
#    root and refuses to start from anything somebody else could have changed.
if [ "$make_pkg" -eq 1 ]; then
  say "installer package"
  stage="$work/pkgroot"
  scripts="$work/pkgscripts"
  mkdir -p "$stage" "$scripts"
  cp -R "$app" "$stage/"
  cp packaging/macos/scripts/* "$scripts/"
  xattr -cr "$stage" "$scripts"

  # The application must stay where it is installed: the service refuses to
  # run from anywhere but a place only administrators can write in, so the
  # installer must never "relocate" the update to another copy of the bundle
  # it finds on the disk.
  pkgbuild --analyze --root "$stage" "$work/component.plist" >/dev/null
  plutil -replace 0.BundleIsRelocatable -bool false "$work/component.plist"

  pkgbuild --root "$stage" --install-location /Applications --ownership recommended \
    --component-plist "$work/component.plist" \
    --identifier "$app_id.pkg" --version "$version" \
    --scripts "$scripts" "$work/component.pkg" >/dev/null
  sed "s/@VERSION@/$version/g" packaging/macos/distribution.xml > "$work/distribution.xml"
  pkg="$out/SingBoxUI-$version.pkg"
  if [ -n "${SBUI_INSTALLER_IDENTITY:-}" ]; then
    productbuild --distribution "$work/distribution.xml" --package-path "$work" --sign "$SBUI_INSTALLER_IDENTITY" "$pkg" >/dev/null
  else
    productbuild --distribution "$work/distribution.xml" --package-path "$work" "$pkg" >/dev/null
  fi
  say "built $pkg"
fi

say "built $app"
