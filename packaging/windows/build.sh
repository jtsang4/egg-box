#!/usr/bin/env bash
# Builds the Windows installer.
#
#   packaging/windows/build.sh [--arch amd64|arm64]
#
# What comes out, in $SBUI_OUT (default build/windows):
#
#   SingBoxUI-Setup-<version>-<arch>.exe
#
# It needs NSIS (makensis) and builds from any operating system: Go compiles
# for Windows without cgo, and MyGo too.
#
# Environment:
#   SBUI_VERSION   the version; default: the one in mygo.json
#   SBUI_COMMIT    the source revision; default: git's
#   SBUI_OUT       where the result goes
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

arch=amd64
while [ $# -gt 0 ]; do
  case "$1" in
    --arch) arch="$2"; shift 2 ;;
    *) echo "usage: $0 [--arch amd64|arm64]" >&2; exit 2 ;;
  esac
done
case "$arch" in amd64|arm64) ;; *) echo "unknown architecture: $arch" >&2; exit 2 ;; esac

command -v makensis >/dev/null || { echo "makensis (NSIS) is needed to build the installer" >&2; exit 1; }

# The version the application's metadata carries comes from mygo.json, so a
# release must name the same one.
config_version="$(python3 -c 'import json;print(json.load(open("mygo.json"))["version"])')"
version="${SBUI_VERSION:-$config_version}"
if [ "$version" != "$config_version" ]; then
  echo "SBUI_VERSION is $version but mygo.json says $config_version: change mygo.json first (the metadata of the application comes from it)" >&2
  exit 1
fi
commit="${SBUI_COMMIT:-$(git rev-parse --short=9 HEAD 2>/dev/null || true)}"
out="${SBUI_OUT:-$root/build/windows}"
work="$out/work-$arch"
tags="with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname"
stamp="-X github.com/jtsang4/sing-box-ui/internal/version.Version=$version -X github.com/jtsang4/sing-box-ui/internal/version.Commit=$commit"
ldflags="-s -w $stamp"
# MyGo adds these to the ones it uses for the application.
export MYGO_LDFLAGS="$stamp"

say() { printf '==> %s\n' "$*"; }

rm -rf "$work"
mkdir -p "$work"

say "agentd.exe ($arch)"
CGO_ENABLED=0 GOOS=windows GOARCH="$arch" \
  go build -trimpath -tags "$tags" -ldflags "$ldflags" -o "$work/agentd.exe" ./cmd/agentd

# MyGo makes the application; its own per-user installer is not wanted, this
# one installs for the whole computer.
say "application (windows/$arch)"
go tool mygo build -platform "windows/$arch" -skip-installer -skip-build-command -o "$work/mygo" >"$work/mygo.log" 2>&1 \
  || { cat "$work/mygo.log" >&2; exit 1; }
gui="$(find "$work/mygo" -maxdepth 3 -name '*.exe' | head -1)"
[ -n "$gui" ] || { echo "mygo build produced no .exe; see $work/mygo.log" >&2; exit 1; }

# The licenses of everything compiled into the programs: nearly all of them
# ask to travel with the program. The application's Settings opens this file.
say "third-party notices"
go run ./scripts/notices -goos windows -goarch "$arch" -tags "$tags" -out "$work/THIRD-PARTY-NOTICES.txt"

say "installer"
mkdir -p "$out"
setup="$out/SingBoxUI-Setup-$version-$arch.exe"
numeric="$(printf '%s' "$version" | sed -E 's/^([0-9]+)\.([0-9]+)\.([0-9]+).*/\1.\2.\3.0/')"
makensis -V2 -DVERSION="$version" -DVERSION_NUMERIC="$numeric" -DARCH="$arch" -DAGENT="$work/agentd.exe" -DGUI="$gui" -DNOTICES="$work/THIRD-PARTY-NOTICES.txt" -DOUTFILE="$setup" \
  packaging/windows/installer.nsi

say "built $setup"
