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

## Before publishing

- **Android signing**: release builds are signed with the debug key unless `mygo.json` names `android.keystore` (with
  `MYGO_ANDROID_KEYSTORE_PASSWORD` and `MYGO_ANDROID_KEY_PASSWORD`); Google Play refuses the debug key.
- **App Store**: Apple accepts VPN applications from organization accounts only, and the China storefront removes them.

## Known limits

- iOS: switching the VPN on from Settings › VPN runs the configuration EggBox accepted last, but EggBox itself shows
  it as disconnected until it is connected from EggBox; Connect On Demand is not offered.
- Rules by application (Android packages) are not offered yet; the connection list names the application when Android
  says which one it is.
- iOS does not tell which application opened a connection.
