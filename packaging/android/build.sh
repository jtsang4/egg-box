#!/usr/bin/env bash
# build.sh - build EggBox's Android application for a release, signed with the
# release key: build/android/EggBox-<version>-android.apk (every ABI Go builds
# for, large) or, with --abi, EggBox-<version>-android-<abi>.apk, and the
# matching .aab (what Google Play takes).
#
#   packaging/android/build.sh --keystore PATH [--alias NAME] [--abi ABI]
#
#   --keystore PATH  the release keystore (PKCS12); see docs/mobile.md
#   --alias NAME     the key's alias in it (default eggbox)
#   --abi ABI        one ABI only (arm64, arm or amd64): EggBox-<version>-android-ABI.apk
#
# Environment:
#   MYGO_ANDROID_KEYSTORE_PASSWORD  the keystore's password, which is the key's
#   EGGBOX_VERSION                  the version; default: the one in mygo.json
#   EGGBOX_ANDROID_CERT_SHA256      when set, the certificate the APK is signed
#                                   with must have this SHA-256 (AA:BB:...)
#
# Needs the Android SDK and NDK, and JDK 17 (MyGo's Gradle files ask for it).
# There is no build without the key: Android installs an update only over a
# copy signed by the same key, and a debug key differs on every machine.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

keystore=""
alias=eggbox
abi=""
while [ $# -gt 0 ]; do
  case "$1" in
    --keystore) keystore="$2"; shift ;;
    --alias) alias="$2"; shift ;;
    --abi) abi="$2"; shift ;;
    -h|--help) sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done
if [ -z "$keystore" ] || [ ! -f "$keystore" ]; then
  echo "the release keystore is missing (--keystore): a release is never signed with a debug key" >&2
  exit 1
fi
if [ -z "${MYGO_ANDROID_KEYSTORE_PASSWORD:-}" ]; then
  echo "MYGO_ANDROID_KEYSTORE_PASSWORD is not set" >&2
  exit 1
fi
keystore="$(cd "$(dirname "$keystore")" && pwd)/$(basename "$keystore")"

config_version="$(python3 -c 'import json;print(json.load(open("mygo.json"))["version"])')"
version="${EGGBOX_VERSION:-$config_version}"
if [ "$version" != "$config_version" ]; then
  echo "EGGBOX_VERSION is $version but mygo.json says $config_version: change mygo.json first (the metadata of the application comes from it)" >&2
  exit 1
fi
commit="${EGGBOX_COMMIT:-$(git rev-parse --short=9 HEAD 2>/dev/null || true)}"
export MYGO_LDFLAGS="-X eggbox/internal/version.Version=$version -X eggbox/internal/version.Commit=$commit"
export GOFLAGS="${GOFLAGS:-} -tags=with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname"

# MyGo reads the key from mygo.json: it is named there for this build only, and
# the file is put back however the build ends.
work="$(mktemp -d)"
cp mygo.json "$work/mygo.json"
trap 'cp "$work/mygo.json" mygo.json; rm -rf "$work"' EXIT
python3 - "$keystore" "$alias" <<'PY'
import json, sys
c = json.load(open("mygo.json"))
c.setdefault("android", {}).update({"keystore": sys.argv[1], "keyAlias": sys.argv[2]})
json.dump(c, open("mygo.json", "w"), indent=2, ensure_ascii=False)
PY

echo "==> application ($version, $commit)"
rm -f "build/android/egg-box-$version.apk" "build/android/egg-box-$version.aab"
platform=android suffix=""
if [ -n "$abi" ]; then
  platform="android/$abi" suffix="-$abi"
fi
go tool mygo build -platform "$platform"
apk="build/android/EggBox-$version-android$suffix.apk"
mv "build/android/egg-box-$version.apk" "$apk"
mv "build/android/egg-box-$version.aab" "build/android/EggBox-$version$suffix.aab"

echo "==> signature"
sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
apksigner="$(find "$sdk/build-tools" -name apksigner -type f | sort -V | tail -1)"
certs="$("$apksigner" verify --print-certs "$apk")"
# "Signer #1 certificate SHA-256 digest: …" up to build-tools 36,
# "V2 Signer: certificate SHA-256 digest: …" from 37.
sha256="$(printf '%s\n' "$certs" | sed -n '/Source Stamp/d; s/^.*Signer.* certificate SHA-256 digest: //p' | head -1 | tr 'a-f' 'A-F' | sed 's/../&:/g; s/:$//')"
if [ -z "$sha256" ]; then
  printf 'no signing certificate in what apksigner printed:\n%s\n' "$certs" >&2
  exit 1
fi
if [ -n "${EGGBOX_ANDROID_CERT_SHA256:-}" ] && [ "$sha256" != "$EGGBOX_ANDROID_CERT_SHA256" ]; then
  echo "the APK is signed by $sha256, not by the release key $EGGBOX_ANDROID_CERT_SHA256" >&2
  exit 1
fi
echo "==> $apk, signed by $sha256"
