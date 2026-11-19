# EggBox on phones

EggBox runs on Android and iOS with the same interface, library, routing and core as on the desktop. What changes is
who owns the VPN: on a phone there is no privileged service. The system makes the VPN interface and hands it to the
application, and asks the person once to allow it.

```
Android                                         iOS
───────                                         ───
EggBox (one process)                            EggBox (application)          Packet tunnel extension
  GUI ── agent (in process) ── sing-box core      GUI ── agent ── iosengine ──▶ sing-box core
                                  │                         │   Unix socket in       │
                     VpnService (Kotlin, droidvpn)          │   the App Group        │
                       makes tun0, protects sockets         └── NETunnelProvider-    NEPacketTunnelProvider
                                                                Manager (iosvpn)      (Swift) makes the utun
```

- **The agent** (controller, configuration gate, store) runs inside the application, served in memory
  (`internal/agent/inproc`, production mode). The gate is the desktop's: the configuration the interface generates is
  checked and rewritten the same way before anything runs.
- **The core** gets the VPN interface through sing-box's platform interface (`internal/phone/tunnel`): it opens the
  descriptor the system made, keeps its own sockets out of the tunnel (Android's `VpnService.protect`), and follows the
  device's default network itself, so moving between Wi-Fi and cellular needs no reconnect.
- **Android**: `internal/phone/droidvpn` is a native module whose Kotlin runs the `VpnService`. The core runs in the
  application's process, which the system keeps alive while the VPN is up.
- **iOS**: the VPN runs in a packet tunnel extension, a process the system starts with the tunnel. The agent stays in
  the application (it is needed while disconnected too); `internal/phone/iosengine` writes the configuration the agent
  accepted into the App Group container and asks the system for the tunnel. The extension
  (`cmd/eggbox-tunnel` as a C archive, `native/ios/PacketTunnel` in Swift) runs the core and serves its groups, traffic,
  connections and log back to the agent over a Unix socket in the container (`internal/agent/engine/remote`).
- **Turning the VPN off in the system's settings** (or another VPN app taking over) is a disconnect, not a crash:
  EggBox does not reconnect behind the person's back.
- **Android's always-on VPN** is supported: when the system starts the VPN service by itself (as always-on is switched
  on, and at boot), the service starts the application's Go without a window (MyGo's `HostImpl.startHeadless`) and the
  application connects with the choices made last. A Disconnect in EggBox stays disconnected until the system starts
  the VPN again (at the next boot). Android 16 starts it again neither after the application stopped it nor after its
  process died (killed, crashed or force-stopped): the next time EggBox opens, the agent resumes the connection
  (Settings › Resume after an unexpected restart).
- **The interface** lays itself out for a phone (`gui.Options.Phone`): a bar of tabs (Home, Nodes and Subscriptions,
  Routing, Connections and Logs, Settings), single-column pages, and texts that speak of a device and the system's VPN.
  Desktop-only settings (tray, login item, TUN off with a local proxy, reconnect on network change, the privileged
  service) are left out.

## Build

Android needs the SDK (platform 36), the NDK and JDK 17 (MyGo's Gradle files ask for a Java 17 toolchain); iOS needs
Xcode. `go tool mygo doctor` says what is missing. The release tags go in through `GOFLAGS`:

```sh
export GOFLAGS="-tags=with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname"

go tool mygo build -platform android        # build/android/egg-box-<version>.apk and .aab (debug key, see below)
go tool mygo dev -platform android          # run on the emulator or device adb reaches, rebuilt on every change

go tool mygo build -platform ios/simulator  # build/ios-simulator/EggBox.app
packaging/ios/build-extension.sh            # build/ios-extension/PacketTunnel.appex (device; --simulator for the simulator)
```

For end-to-end runs, add the `e2e` tag and start a development build (`mygo dev`) with `MYGO_AUTOMATION` (Android:
`am start ... --es mygo.env 'MYGO_AUTOMATION=@eggbox'` and `adb forward tcp:… localabstract:eggbox`; the simulator:
`SIMCTL_CHILD_MYGO_AUTOMATION=/path`), then drive it with
`go run github.com/egoist/mygo/plugins/automation/cmd/mygo-automation`.

## What is verified, and how

- Unit tests on any machine: the platform interface against a fake phone, the phone driver, the controller treating
  a revoked VPN as a disconnect, the remote protocol, `iosengine` against a simulated extension (start, calls, log,
  stop, a crash, the extension vanishing), the extension's side running the real core, and the phone interface
  (`TestPhone*`, screenshots in `TestPhoneShots`).
- Android, on an Android 16 emulator against a shadowsocks node on the host's loopback: the permission asked before the
  first connection, `tun0` up, a request from another process answered through the node with DNS answered by the core,
  the VPN kept in the background, Wi-Fi off and on followed without reconnecting, Disconnect ending the system's VPN,
  and a disconnect from the system settings not reconnected.
- iOS, in the simulator: the application runs, keeps its agent in the App Group container and checks configurations;
  the extension, embedded by hand, installs and registers as a packet tunnel provider; connecting reports the
  simulator's refusal (it runs no packet tunnel).

**Not verified: the iOS tunnel on a device.** It needs a paid Apple Developer team and an iPhone (the simulator cannot
run a packet tunnel), and MyGo's `ios.extensions`, which embeds and signs the extension, is not in a release yet
(merged after MyGo 0.10.0). Once both are there:

1. In the developer account, create the App Group `group.org.eggbox.app`, and the App IDs `org.eggbox.app` and
   `org.eggbox.app.tunnel` (for development builds also `org.eggbox.app.dev` and `org.eggbox.app.dev.tunnel`), each
   with the Network Extensions and App Groups capabilities and that group. Make a provisioning profile for each and
   install them (Xcode › Settings › Accounts).
2. Upgrade MyGo to the release with `ios.extensions` and add to `mygo.json`:
   `"ios": { …, "extensions": [ { "path": "build/ios-extension/PacketTunnel.appex", "provisioningProfile": "<the tunnel's profile>" } ] }`.
3. `packaging/ios/build-extension.sh && go tool mygo build -platform ios`, install the `.ipa`, and check: the system
   asks to add the VPN configuration on the first Connect; EggBox shows Connected and the status bar shows VPN; Safari
   loads a page through the node and Connections lists it; Disconnect removes the VPN; turning it off in Settings › VPN
   leaves EggBox disconnected; Wi-Fi to cellular keeps working; the extension stays under its memory limit with a large
   subscription (Console › the extension's process).

## Releases

Every `v*` release carries an Android APK per ABI, `EggBox-<version>-android-arm64.apk` for practically every phone in
use and `EggBox-<version>-android-arm.apk` for 32-bit ones, built by `packaging/android/build.sh` in the release
workflow and signed with the **release key**:

- Android installs an update only over a copy signed with the same key, so every release must be signed with this one.
  The release job refuses to run without it, and checks the certificate it signed with against its fingerprint,
  `3A:D4:2A:4F:4E:A4:F5:FD:F2:B8:51:A4:B3:73:7F:6A:34:E7:AB:8E:28:44:B2:3B:79:AA:04:CF:DB:B5:3C:37` (public).
- The key lives in the repository's Actions secrets `EGGBOX_ANDROID_KEYSTORE` (the keystore, base64) and
  `EGGBOX_ANDROID_KEYSTORE_PASSWORD`, which can be written but never read back, and in the owner's backup (keystore
  file and a note with the alias and password), which is the only copy that can restore them. Losing it means
  installed copies can never be updated; whoever has it can sign an "update".
- By hand: `MYGO_ANDROID_KEYSTORE_PASSWORD=… packaging/android/build.sh --keystore …/eggbox-release.jks [--abi arm64]`.
  The script names the key in `mygo.json` for that build only; `go tool mygo build -platform android` alone signs with
  the machine's debug key, for development.
- Google Play takes the `.aab` the script leaves beside the APK, uploaded with `go tool mygo publish -platform android`.

iOS releases need what the section above lists; there is no iOS file in a release until then.

## Before publishing

- **App Store**: Apple accepts VPN applications from organization accounts only, and the China storefront removes them.

## Known limits

- iOS: switching the VPN on from Settings › VPN runs the configuration EggBox accepted last, but EggBox itself shows
  it as disconnected until it is connected from EggBox; Connect On Demand is not offered.
- Rules by application (Android packages) are not offered yet; the connection list names the application when Android
  says which one it is.
- iOS does not tell which application opened a connection.
