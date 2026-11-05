# Sing-Box UI

A desktop client for [sing-box](https://github.com/SagerNet/sing-box) on macOS and Windows, with the conveniences people
know from Clash, Shadowrocket and Surge: subscriptions that refresh themselves, a node list with latency tests, rule and
global modes, a live connection list, a menu bar / tray panel.

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
  (including Reality), VMess, Trojan, Shadowsocks, Hysteria2, TUIC, AnyTLS, WireGuard, SOCKS and HTTP. Links are
  credentials: they live in the system keychain, never in an ordinary file, a log or a diagnostic report.
- **Nodes.** Grouped by region, latency tests, an "Auto" choice that follows the fastest node, per-region fastest groups.
- **Routing.** Rule, Global and Direct modes; presets (mainland China direct, proxy everything, proxy only foreign
  sites); ad blocking; your own rules (domain, suffix, keyword, regex, IP range, port, process) that win over the
  preset; rule data (geosite / geoip) fetched from a mirror you choose and refreshed on a schedule.
- **Connection.** One-click connect with a real TUN adapter; reconnects when the network changes; recovers from an
  unexpected core exit; optional auto-connect. Live speed charts, the connection list (close one or all), logs.
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

## Try it without installing anything

The demonstration runs the whole interface against an invented world in the same process. Nothing leaves the computer,
nothing is installed, nothing is kept.

```sh
go run ./cmd/sing-box-ui --demo            # a populated, connected app
go run ./cmd/sing-box-ui --demo --fresh    # the first-run walkthrough
go run ./cmd/sing-box-ui --help
```

## Develop

Requirements: Go (see `go.mod`), and on macOS the Xcode command line tools for the Swift bridge. `protoc` is only needed
when `internal/agentapi/agent.proto` changes (`scripts/gen-proto.sh`).

**Nothing in the development or test workflow changes the host network.** Tests use an in-memory simulated platform
driver and loopback-only upstreams. The real platform drivers refuse to run unless the process is the installed
service. A development build (build tag `sbui_dev`) adds `agentd dev`, which runs the same agent on a Unix socket you
own, against the simulated operating system, with a loopback `mixed` proxy where the TUN would be:

```sh
TAGS=with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname
go build -tags "sbui_dev,$TAGS" -o /tmp/agentd ./cmd/agentd
go build -tags sbui_dev -o /tmp/sing-box-ui ./cmd/sing-box-ui
/tmp/agentd dev --socket /tmp/sbui-dev/agent.sock --data-dir /tmp/sbui-dev/agent &
/tmp/sing-box-ui --dev-socket /tmp/sbui-dev/agent.sock --dev-data /tmp/sbui-dev/gui
```

The socket's directory is created private to you (the agent refuses a directory others can enter), `agentd dev`
refuses to run as root, and release binaries do not contain the development transport.

### Tests

```sh
go vet ./... && go tool mygo vet
go test -race -short ./...                                        # the quick suite
go test ./...                                                     # everything, including screenshots
go test -tags "sbui_dev,$TAGS" ./internal/agent/engine/... ./internal/agent/e2e/... ./internal/configgen/...
                                                                  # with the real sing-box core
```

CI also runs `staticcheck` for the three platforms, `shellcheck` on the installer scripts, `actionlint` on the workflows,
and `govulncheck` (on every change and weekly): the service runs as root, so a dependency with a known vulnerability
the code reaches is a failure.

The interface is tested without a window (`ui.NewTester`), including screenshots rendered in memory (`ui.Render`), so a
change can be reviewed without opening anything: `SBUI_SHOTS=/some/dir go test ./internal/gui -run TestShots`.

One end-to-end test runs the whole product path (service, agent, real sing-box core) against a **real subscription**,
inside the sandbox, and checks every node can carry traffic. It is skipped unless you provide a link, and it prints
only counts, protocol names and failure classes, never the link or any node detail:

```sh
SBUI_TEST_SUBSCRIPTION_URL='…'   # or SBUI_TEST_SUBSCRIPTION_FILE=/path/to/a/file/holding/the/link
go test -tags "sbui_dev,$TAGS" -run RealSubscription -v ./internal/agent/e2e
```

Keep such links out of the repository: not in code, fixtures, docs, scripts or commit messages. Fixtures and demo data
are invented (`example.net`, documentation IP ranges).

### Build the installers

Both scripts check that `SBUI_VERSION` (default: the version in `mygo.json`) agrees with `mygo.json`, stamp the version
into every binary, and leave their output in `build/` (git-ignored).

```sh
# macOS (on a Mac): universal Sing-Box UI.app and SingBoxUI-<version>.pkg
packaging/macos/build.sh [--arch universal|arm64|x86_64] [--no-pkg]
#   SBUI_SIGN_IDENTITY / SBUI_INSTALLER_IDENTITY: Developer ID identities. The default is an ad hoc signature, which
#   is enough to build and inspect the package; whether macOS accepts the service of an ad-hoc app is not verified

# Windows (from any machine with NSIS; makensis): SingBoxUI-Setup-<version>-<arch>.exe
packaging/windows/build.sh --arch amd64|arm64
```

The macOS package installs `Sing-Box UI.app` into `/Applications`; the app bundles `agentd`, `ServiceBridge.dylib` and
the LaunchDaemon plist, and asks the person to approve the service in System Settings the first time. To remove
everything, use Settings → Network service → Remove the service, or `sudo packaging/macos/uninstall.sh [--keep-data]`.
The Windows installer is per machine: it installs under Program Files, registers the service, authorizes the installing
user, and its uninstaller removes the service, the adapter and the data (`agentd.exe uninstall --purge`).

CI (`.github/workflows/ci.yml`) runs the checks and the tests on Linux, macOS and Windows, builds both installers, and
cross-builds every target. A `v*` tag builds the installers and attaches them to a release
(`.github/workflows/release.yml`); with the signing secrets listed there they are signed, without them they are not
fit to hand to anybody.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/agentd` | privileged agent: Windows Service / macOS LaunchDaemon, and `agentd dev` in development builds |
| `cmd/sing-box-ui` | the application: window, menu bar / tray, platform glue |
| `internal/agent` | agent core: controller (state machine), policy, ledger, store, engine (sing-box adapter), platform drivers, server and transports |
| `internal/agentapi` | the GUI ⇄ agent contract (protobuf + gRPC) and the protocol version |
| `internal/agentclient` | the GUI's client of that contract |
| `internal/app` | application logic between the interface and the agent: operations, refresh schedule, state |
| `internal/boot` | assembles the application for the installed, development and demonstration modes |
| `internal/gui` | the native UI: tokens, widgets, pages, dialogs, localisation (`i18n_en.go`, `i18n_zh.go`) |
| `internal/library` | persistent state: subscriptions, nodes, preferences, rules (secrets go to the keychain) |
| `internal/subscription` | parses what providers hand out into nodes |
| `internal/configgen` | writes the sing-box configuration the agent accepts |
| `internal/ruledata`, `internal/redact`, `internal/brand`, `internal/version` | rule sets, redaction of diagnostics, names and identifiers, version |
| `internal/darwin`, `internal/macsys`, `internal/winsys` | platform services behind the GUI and the agent |
| `native/macos` | the Swift bridge (SMAppService, XPC, SCDynamicStore) |
| `packaging` | macOS `.pkg` and Windows installer, uninstaller, scripts |

## What is verified here and what needs a real machine

Development happened on one Apple Silicon Mac, without changing its network, so the platform-specific behaviour is
checked as far as software on that machine can: against the simulated driver, against a real sing-box core with the TUN
replaced by a loopback proxy, against real XPC with a real Swift bridge, and by building every installer. These remain
to be exercised on the real thing before a release is handed to people:

| Area | Not yet exercised |
| --- | --- |
| macOS | the `SMAppService` approval flow with a Developer ID signed and notarized app; XPC code-signing requirement against the installed bundle; a real TUN, routes and the `SCDynamicStore` resolver; sleep / wake and network switching; coexistence with other VPNs; upgrade over an older version and uninstall residue; Intel Macs |
| Windows | everything that needs Windows: service install and start, UAC, the named-pipe ACL and client verification, Wintun, route and DNS changes, sleep / wake, the session (WTS) logic, the Credential Manager, the NSIS installer run (it is compiled, not run), Windows 10 and 11, ARM64 |
| Release | code signing, notarization and the release workflow's signing path |

The Windows code compiles for amd64 and arm64 and its pure parts (ACL construction, owner policy, trust rules) are unit
tested; that is not a substitute for running it.

## Licensing

The program embeds [sing-box](https://github.com/SagerNet/sing-box), which is GPL-3.0-or-later with one added condition:
no derivative work may use its name or imply association with it without prior consent. Two things follow before
binaries are handed to anybody: the combined program has to be distributed under terms compatible with the GPL (this
repository has no license file yet; choosing one is the owner's decision), and the product name has to be settled with
the sing-box authors, since "Sing-Box UI" contains theirs. The name and the identifiers made from it (`me.jtsang.singboxui`,
`SingBoxUI`) are used in `internal/brand`, `mygo.json`, `packaging/`, the interface texts and the service protocol.
