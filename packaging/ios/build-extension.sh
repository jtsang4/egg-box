#!/usr/bin/env bash
# build-extension.sh - build EggBox's iOS packet tunnel extension,
# PacketTunnel.appex: the core (cmd/eggbox-tunnel) as a C archive, linked by
# the provider's Swift (native/ios/PacketTunnel). The application carries it
# in PlugIns/; mygo build puts it there and signs it with its own
# provisioning profile (mygo.json, ios.extensions).
#
#   packaging/ios/build-extension.sh [--simulator] [--app-id ID] [--out DIR]
#
#   --simulator  build for the iOS Simulator and sign it ad hoc, as mygo does
#                a simulator build's frameworks (the simulator runs no packet
#                tunnel; it only installs and lists one)
#   --app-id ID  the application's identifier (default: mygo.json's; a
#                development build's is <identifier>.dev): the extension is
#                ID.tunnel
#   --out DIR    where PacketTunnel.appex goes (default build/ios-extension)
#
# Environment:
#   EGGBOX_APP_GROUP  the App Group the application and the extension share
#                     (default group.<mygo.json identifier>)
#
# A device build is left unsigned: its profile names the entitlements of
# PacketTunnel.entitlements, which the provisioning profile of ID.tunnel must
# allow, and mygo signs it with that profile.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"

simulator=0
out="$root/build/ios-extension"
app_id=""
while [ $# -gt 0 ]; do
  case "$1" in
    --simulator) simulator=1 ;;
    --app-id) app_id="$2"; shift ;;
    --out) out="$2"; shift ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

read_config() { python3 -c 'import json,sys;print(json.load(open("mygo.json"))[sys.argv[1]])' "$1"; }
identifier="$(read_config identifier)"
version="$(read_config version)"
app_id="${app_id:-$identifier}"
group="${EGGBOX_APP_GROUP:-group.$identifier}"
min_os="17.0"
tags="with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname"

if [ "$simulator" = 1 ]; then
  sdk=iphonesimulator platform=iPhoneSimulator target="arm64-apple-ios$min_os-simulator" gotags="$tags,ios"
else
  sdk=iphoneos platform=iPhoneOS target="arm64-apple-ios$min_os" gotags="$tags"
fi
sdk_path="$(xcrun --sdk "$sdk" --show-sdk-path)"
clang="$(xcrun --sdk "$sdk" --find clang)"

work="$out/work"
appex="$out/PacketTunnel.appex"
rm -rf "$work" "$appex"
mkdir -p "$work" "$appex"

echo "==> core ($platform)"
GOOS=ios GOARCH=arm64 CGO_ENABLED=1 CC="$clang -isysroot $sdk_path -target $target" \
  go build -buildmode=c-archive -trimpath -tags "$gotags" -ldflags "-s -w" \
  -o "$work/libeggboxcore.a" ./cmd/eggbox-tunnel

fill() {
  sed -e "s|{BUNDLE_ID}|$app_id.tunnel|g" -e "s|{VERSION}|$version|g" -e "s|{PLATFORM}|$platform|g" \
    -e "s|{MIN_OS}|$min_os|g" -e "s|{APP_GROUP}|$group|g" "$1"
}
fill native/ios/PacketTunnel/PacketTunnel.entitlements > "$work/PacketTunnel.entitlements"
# A simulator reads an executable's entitlements from a section of its own,
# as Xcode links them; a device's come with the signature.
extra=()
if [ "$simulator" = 1 ]; then
  extra=(-Xlinker -sectcreate -Xlinker __TEXT -Xlinker __entitlements -Xlinker "$work/PacketTunnel.entitlements")
fi

echo "==> provider"
xcrun --sdk "$sdk" swiftc -target "$target" -sdk "$sdk_path" -O \
  -module-name PacketTunnel -parse-as-library -application-extension \
  -import-objc-header "$work/libeggboxcore.h" \
  native/ios/PacketTunnel/PacketTunnelProvider.swift \
  -L "$work" -leggboxcore -lresolv \
  -framework Foundation -framework Network -framework NetworkExtension -framework Security -framework UIKit \
  -Xlinker -e -Xlinker _NSExtensionMain -Xlinker -dead_strip ${extra[@]+"${extra[@]}"} \
  -o "$appex/PacketTunnel"

fill native/ios/PacketTunnel/Info.plist > "$appex/Info.plist"
plutil -lint "$appex/Info.plist" "$work/PacketTunnel.entitlements" >/dev/null

if [ "$simulator" = 1 ]; then
  codesign --force --sign - --timestamp=none "$appex"
fi
cp "$work/PacketTunnel.entitlements" "$out/PacketTunnel.entitlements"
echo "==> $appex ($app_id.tunnel, App Group $group)"
