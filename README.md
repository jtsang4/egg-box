# EggBox

<img src="resources/icon.png" width="96" alt="EggBox logo">

A client for [sing-box](https://github.com/SagerNet/sing-box) on macOS and Windows, and on Android and iOS, with the
conveniences people know from Clash, Shadowrocket and Surge: subscriptions that refresh themselves, a node list with
latency tests, rule and global modes, a live connection list, a menu bar / tray panel on the desktop.

The interface is written in Go alone, with [MyGo](https://github.com/egoist/mygo)'s native UI toolkit (`package ui`,
drawn on the GPU). There is no JavaScript, no web front end and no webview; CI fails if one appears.

The interface never touches the network stack. A small privileged agent (`agentd`) embeds sing-box and owns the TUN
adapter, routes, DNS and the connection lifecycle, so closing the window keeps the connection alive and a crashed GUI
cannot leave the network half configured.

```
GUI (your user, no privileges)
   │  authenticated local IPC, a narrow gRPC API
   ▼
agentd (Windows Service / macOS LaunchDaemon)  ──▶  embedded sing-box core  ──▶  TUN, routes, DNS
```

## Features

- **Subscriptions.** Remote links (refreshed on a schedule, with traffic and expiry read from the provider's headers) and
  local imports (pasted links, files). Formats: sing-box JSON, Clash YAML, plain or base64 share links. Protocols: VLESS
  (including Reality), VMess, Trojan, Shadowsocks (including over ShadowTLS), Hysteria, Hysteria2, TUIC, AnyTLS, Snell
  (v4/v5 and v6), WireGuard, SSH, SOCKS and HTTP. Links are credentials: they live in the system keychain, never in an
  ordinary file, a log or a diagnostic report.
- **Nodes.** Grouped by region, latency tests, a main exit that is "Automatic" (the fastest node), the fastest node of a
  region, or one node. Latency is the round trip of a request on a connection already open through the node (what mihomo
  calls "unified delay"), so it compares with other clients; the handshakes of a new connection are not counted. A node
  is known by its subscription and its name, so a provider rotating a password does not lose the choice; when a node
  chosen by hand is down or gone, the fastest node of its region stands in and the screen says so. Any node or
  subscription can also dial through a two-hop relay (a specific direct node, the fastest direct node in a region, or
  the fastest direct node overall, as well as provider-declared two-hop chains); an unavailable relay fails closed
  rather than falling back to direct.
- **Routing.** Rule, Global and Direct modes. In Rule mode a *plan* decides: an ordered list of items ("AI services",
  "Streaming", "China sites", ...), each with a destination (the main exit, the fastest node of a region, one node, direct
  or blocked), a switch and one or more rule sources. EggBox ships a recommended plan; a subscription that comes with its
  own groups and rules (sing-box JSON or Clash YAML) brings its plan along, and one plan is in force at a time; a plan of
  your own can be saved from either. Exceptions (domain, suffix, keyword, regex, IP range, port, process) come first,
  whatever plan is in force; they are made in the page, pasted as a list (Clash syntax included), or from a connection or
  the answer of **Test a URL**, which says where an address goes and why. Rule sources are community rule sets in any
  common format (sing-box `.srs` and JSON, Clash and Surge lists, plain lists, AdGuard filters, hosts files), found in a
  built-in directory of the well-known projects or added by link (GitHub pages and mirror prefixes are understood). The
  application downloads them, converts them and hands the agent local files only: the agent never goes to the network.
  Changing where an item goes is a selection in the running core, not a restart.
- **Connection.** One-click connect with a real TUN adapter, or with TUN off (Settings → Network): then only a local
  HTTP/SOCKS proxy runs and the system's routes and DNS are left alone. Reconnects when the network changes; recovers
  from an unexpected core exit; optional auto-connect. Live speed charts, the connection list (close one or all), logs.
- **Desktop.** Menu bar / tray panel, notifications, launch at login, keep running in the tray, light and dark
  appearance, English and 简体中文.
- **Support.** A redacted diagnostic report (versions, status, recent errors; never links, passwords or node details).

Three things are independent settings, as they are in any serious VPN client: the service starting with the computer,
the GUI starting at login, and connecting automatically.

## How it stays safe

- The GUI hands the agent a sing-box configuration and the agent decides what of it may run as SYSTEM / root
  (`internal/agent/policy`): an allow-list of sections and types, no arbitrary file paths (only resources the GUI hands
  over), loopback-only listeners, no management APIs or external programs. The TUN's addressing, routes and DNS are
  written by the agent, never taken from the GUI or a provider.
- Only one authorized user can control the agent, and only one TUN is active on a machine.
- Every system resource the agent creates is written to a ledger first. After a crash it removes exactly what it
  recorded, and only if the resource still carries its fingerprint; it never "resets" routes or DNS.
- Operations are serialized, carry a request id and a settings revision (compare and swap), and roll back when a step
  fails; the last configuration that worked is kept.
- An upgrade runs in a fixed order: the running service disconnects and puts the network back, the installer replaces
  the whole package (application, service, bridge and core together), and the new service brings back the connection the
  person had. The installer tells the service the stop is for an upgrade by leaving a note in its protected data folder;
  any other stop (shutting down, stopping the service by hand) restores nothing by itself, that is up to "Connect
  automatically".
- Windows: a Windows Service (LocalSystem) behind a named pipe whose ACL admits SYSTEM, administrators and the
  authorized user and refuses network logons. Each client is verified by its user, by its program (it must be the
  installed application) and, when the service itself is signed, by its code signature. Wintun comes embedded with
  sing-tun; the data directory is under protected `ProgramData`.
- macOS: a root LaunchDaemon registered through `SMAppService` (the person approves it once in System Settings),
  reached over XPC whose listener checks the caller's code-signing requirement and user; DNS through a temporary
  `SCDynamicStore` resolver. The Swift bridge (`native/macos/ServiceBridge`) is loaded with purego, so there is no cgo.

## Phones

On Android and iOS the same interface, laid out for a phone, runs the same agent and core inside the application; the
system makes the VPN and asks the person once to allow it (a `VpnService` on Android, a packet tunnel extension on iOS).
Building, what is verified, and what a device still needs before the iOS tunnel ships: [docs/mobile.md](docs/mobile.md).

## Try it without installing anything

The demonstration runs the whole interface against an invented world in the same process. Nothing leaves the computer,
nothing is installed, nothing is kept.

```sh
go run ./cmd/egg-box --demo            # a populated, connected app
go run ./cmd/egg-box --demo --fresh    # the first-run walkthrough
go run ./cmd/egg-box --help
```

## Develop

Requirements: Go (see `go.mod`), and on macOS the Xcode command line tools for the Swift bridge. `protoc` is only needed
when `internal/agentapi/agent.proto` changes (`scripts/gen-proto.sh`).

**Nothing in the development or test workflow changes the host network.** Tests use an in-memory simulated platform
driver and loopback-only upstreams. The real platform drivers refuse to run unless the process is the installed
service. A development build (build tag `eggbox_dev`) adds `agentd dev`, which runs the same agent on a Unix socket you
own, against the simulated operating system, with a loopback `mixed` proxy where the TUN would be:

```sh
TAGS=with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname
go build -tags "eggbox_dev,$TAGS" -o /tmp/agentd ./cmd/agentd
go build -tags eggbox_dev -o /tmp/egg-box ./cmd/egg-box
/tmp/agentd dev --socket /tmp/eggbox-dev/agent.sock --data-dir /tmp/eggbox-dev/agent &
/tmp/egg-box --dev-socket /tmp/eggbox-dev/agent.sock --dev-data /tmp/eggbox-dev/gui
```

The socket's directory is created private to you (the agent refuses a directory others can enter), `agentd dev`
refuses to run as root, and release binaries do not contain the development transport.

### Tests

```sh
go vet ./... && go tool mygo vet
bash scripts/ci/backend-tests.sh -race -short -timeout 10m -count=1
                                                                  # backend and native-platform behaviour
go test -race -short -timeout 10m -count=1 ./internal/gui             # UI behaviour and concurrency
go test -count=1 -timeout 10m -run '^(TestShots|TestPanelShots)' ./internal/gui
                                                                  # full-resolution, normal-motion screenshots
go test -count=1 -timeout 10m -tags "eggbox_dev,$TAGS" ./internal/agent/engine/... ./internal/agent/e2e/... ./internal/configgen/...
                                                                  # with the real sing-box core
```

CI runs on manual dispatch and as a release prerequisite. It runs the GUI suite once on Linux, plus backend and
native-platform tests on Linux, macOS and Windows, and builds the Android application, the iOS simulator application and
the iOS packet tunnel extension.
The five cross-build targets also vet development builds; `staticcheck` covers each operating system. Compiler and
analysis caches are isolated by workload and target, with module downloads shared. `shellcheck`, `actionlint` and
module/format checks run before expensive platform jobs. Tests execute afresh, including the race and real-core tiers.

Use **Actions → ci → Run workflow** for ad hoc verification; enable **installers** to also build unsigned packages.
The weekly schedule runs only `govulncheck`. CI artifacts expire after three days; release workflow transfer artifacts
expire after one day. Only standard hosted runners are used.
Native macOS tests and universal packaging use the fixed `macos-15-intel` image; packages contain both Intel and Apple
Silicon executables.

The interface is tested without a window (`ui.NewTester`). Interaction fixtures use reduced motion and half-resolution
backing pixels at the full logical window size; normal-motion interactions have their own regression tests. The complete
screenshot matrix uses native resolution and normal motion. To review it without opening a window:
`EGGBOX_SHOTS=/some/dir go test ./internal/gui -run '^(TestShots|TestPanelShots|TestPhoneShots)'`.

The routing is checked against the real core as well: a test builds plans, asks the **Test a URL** logic where a set of
names and addresses go, sends real requests through the sandbox's loopback proxy to loopback "exits" and compares which
exit carried each one (and what the connection list said decided it) with the prediction; it does so for hand-made
plans, for the plans of providers in both formats, for exceptions, for selections made while running, and for a node that
stops and comes back. With `EGGBOX_E2E_NETWORK=1` the rule data is the public data of the well-known projects, with the
exits still on loopback; `EGGBOX_LIVE_RULES=1 go test ./internal/catalog -run Live` reads one rule set from each project
of the directory and checks that jsDelivr serves the data when GitHub does not.

One end-to-end test runs the whole product path (service, agent, real sing-box core) against a **real subscription**,
inside the sandbox, and checks every node can carry traffic. It is skipped unless you provide a link, and it prints
only counts, protocol names and failure classes, never the link or any node detail:

```sh
EGGBOX_TEST_SUBSCRIPTION_URL='…'   # or EGGBOX_TEST_SUBSCRIPTION_FILE=/path/to/a/file/holding/the/link
go test -tags "eggbox_dev,$TAGS" -run RealSubscription -v ./internal/agent/e2e

# The plan a provider publishes, in its sing-box and in its Clash form, read into the same items and run in the core
EGGBOX_TEST_SINGBOX_LINK_FILE=… EGGBOX_TEST_CLASH_LINK_FILE=… go test -run RealSubscriptionPlans -v ./internal/planimport
EGGBOX_E2E_NETWORK=1 EGGBOX_TEST_SINGBOX_LINK_FILE=… EGGBOX_TEST_CLASH_LINK_FILE=… \
  go test -tags "eggbox_dev,$TAGS" -run RealProvidersPlan -v ./internal/agent/e2e
```

Keep such links out of the repository: not in code, fixtures, docs, scripts or commit messages. Fixtures and demo data
are invented (`example.net`, documentation IP ranges).

### Build the installers

Both scripts check that `EGGBOX_VERSION` (default: the version in `mygo.json`) agrees with `mygo.json`, stamp the version
into every binary, and leave their output in `build/` (git-ignored).

```sh
# macOS (on a Mac): universal EggBox.app and EggBox-<version>.pkg
packaging/macos/build.sh [--arch universal|arm64|x86_64] [--no-pkg]
#   EGGBOX_SIGN_IDENTITY / EGGBOX_INSTALLER_IDENTITY: Developer ID identities. Unset, the code is signed by the
#   keychain's one Developer ID Application or Apple Development identity (with several, the build asks which), and
#   ad hoc only when there is none or when EGGBOX_SIGN_IDENTITY=-. Every version must keep the same signer: macOS
#   refuses to start the service of a build installed over one of another signer (ad hoc over a team above all), and
#   only `sudo sfltool resetbtm`, which drops every application's login and background items, undoes that

# Windows (from any machine with NSIS; makensis): EggBox-Setup-<version>-<arch>.exe
packaging/windows/build.sh --arch amd64|arm64
```

The macOS package installs `EggBox.app` into `/Applications`; the app bundles `agentd`, `ServiceBridge.dylib`, the
LaunchDaemon plist and the licenses of everything inside (`THIRD-PARTY-NOTICES.txt`, which Settings → About opens), and
asks the person to approve the service in System Settings the first time. To remove everything, use Settings → Network
service → Remove the service, or `sudo packaging/macos/uninstall.sh [--keep-data]`.
The Windows installer is per machine: it installs under Program Files (the programs and the same notices file),
registers the service, authorizes the installing user, and its uninstaller removes the service, the adapter and the data
(`agentd.exe uninstall --purge`).

Android APKs for a release come from `packaging/android/build.sh`, signed with the release key the release workflow
holds as secrets ([docs/mobile.md](docs/mobile.md#releases)).

CI (`.github/workflows/ci.yml`) verifies Linux, macOS, Windows and every cross-build target. A `v*` tag matching the
version in `mygo.json` first runs that complete workflow, then builds the installers and publishes the release
(`.github/workflows/release.yml`). Failed verification blocks both packaging and publication. Signing uses the secrets
listed in the release workflow; without them the installers are not fit to hand to anybody.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/agentd` | privileged agent: Windows Service / macOS LaunchDaemon, and `agentd dev` in development builds |
| `cmd/egg-box` | the application: window, menu bar / tray, platform glue (on phones, the agent in process too) |
| `cmd/eggbox-tunnel` | the core of the iOS packet tunnel extension, a C archive |
| `internal/agent` | agent core: controller (state machine), policy, ledger, store, engine (sing-box adapter), platform drivers, server and transports |
| `internal/agentapi` | the GUI ⇄ agent contract (protobuf + gRPC) and the protocol version |
| `internal/agentclient` | the GUI's client of that contract |
| `internal/app` | application logic between the interface and the agent: operations, refresh schedule, state |
| `internal/boot` | assembles the application for the installed, development and demonstration modes |
| `internal/gui` | the native UI: tokens, widgets, pages, dialogs, localisation (`i18n_en.go`, `i18n_zh.go`) |
| `internal/library` | persistent state: subscriptions, nodes, preferences, routing (secrets go to the keychain) |
| `internal/subscription` | parses what providers hand out into nodes |
| `internal/routing` | the routing model: plans, items, destinations, exceptions, node identity and what the person chose |
| `internal/planimport`, `internal/plancompile` | a provider's groups and rules into a plan; a plan and its rule data into what the core is given |
| `internal/rulefmt`, `internal/ruleurl`, `internal/ruledata`, `internal/catalog` | rule-set formats (read, convert, match), addresses and mirrors, download and storage, the built-in directory |
| `internal/configgen` | writes the sing-box configuration the agent accepts |
| `internal/demodata` | the invented world of the demonstration |
| `internal/redact`, `internal/brand`, `internal/version` | redaction of diagnostics, names and identifiers, version |
| `internal/darwin`, `internal/macsys`, `internal/winsys` | platform services behind the GUI and the agent |
| `internal/phone` | phones: the core's platform interface, Android's VpnService and iOS's tunnel manager (native modules), the iOS extension's side and the agent's engine for it ([docs/mobile.md](docs/mobile.md)) |
| `native/macos`, `native/ios` | the Swift bridge (SMAppService, XPC, SCDynamicStore); the iOS packet tunnel provider |
| `packaging` | macOS `.pkg` and Windows installer, uninstaller, scripts; the iOS extension's build |
| `scripts` | protobuf generation; `notices`, which collects the licenses of the modules a build contains |

## What is verified here and what needs a real machine

Development happened on one Apple Silicon Mac, without changing its network, so the platform-specific behaviour is
checked as far as software on that machine can: against the simulated driver, against a real sing-box core with the TUN
replaced by a loopback proxy, against real XPC with a real Swift bridge, and by building every installer. These remain
to be exercised on the real thing before a release is handed to people:

| Area | Not yet exercised |
| --- | --- |
| macOS | the `SMAppService` approval flow with a Developer ID signed and notarized app; XPC code-signing requirement against the installed bundle; a real TUN, routes and the `SCDynamicStore` resolver; sleep / wake and network switching; coexistence with other VPNs; upgrade over an older version and uninstall residue; Intel Macs |
| Windows | everything that needs Windows: service install and start, UAC, the named-pipe ACL and client verification, Wintun, route and DNS changes, sleep / wake, the session (WTS) logic, the Credential Manager, the NSIS installer run (it is compiled, not run), Windows 10 and 11, ARM64 |
| Phones | the iOS tunnel on a device (a paid team, its profiles and MyGo's `ios.extensions`); Android on devices from several makers, its release signing; see [docs/mobile.md](docs/mobile.md) |
| Release | code signing, notarization and the release workflow's signing path |

Two limits of the routing follow from the core, not from the application. A connection that is being dialled to a node at
the moment the node goes down fails once (sing-box ties the dial to the node's own outbound); the next ones use the stand-in
of the same region. And a stand-in gives the connection back to a node that has recovered after the next health check, about
a minute while traffic flows. Where a plan or an exception has address ranges, the core resolves every name that no earlier
rule decided, so that the ranges can be matched, and refuses the connection when the name does not resolve; **Test a URL**
says so when that applies.

The Windows code compiles for amd64 and arm64 and its pure parts (ACL construction, owner policy, trust rules) are unit
tested; that is not a substitute for running it.

## Licensing

EggBox is licensed under the [GNU General Public License, version 3 or later](LICENSE) (`GPL-3.0-or-later`).
Source code for each release is available from the [matching version tag](https://github.com/jtsang4/egg-box/tags).

EggBox embeds [sing-box](https://github.com/SagerNet/sing-box), which is GPL-3.0-or-later with an added condition
restricting use of its name or implied association without prior consent. EggBox is an independent product.
`internal/agent/engine/singbox/urltestgroup` is adapted from sing-box's URL test group and carries its notice.
Installers include the GPL text and the copyright and license notices for EggBox and its dependencies in
`THIRD-PARTY-NOTICES.txt`.
