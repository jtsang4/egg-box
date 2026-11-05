# Sing-Box UI

A desktop client for [sing-box](https://github.com/SagerNet/sing-box) on macOS and Windows.

The GUI is written in Go alone, with [MyGo](https://github.com/egoist/mygo)'s native UI toolkit (`package ui`, drawn on
the GPU): there is no JavaScript, no web front end and no webview. The GUI never touches the network stack. A small
privileged agent (`agentd`) embeds sing-box and owns the TUN adapter, routes, DNS and the connection lifecycle, so
closing the window keeps the connection alive.

```
GUI (user)  ──authenticated local IPC──▶  agentd (service / daemon)  ──▶  sing-box core + TUN
```

Status: under construction. See `CONTRIBUTING.md` once it exists; until then this file only lists the layout.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/agentd` | privileged agent (Windows Service / macOS LaunchDaemon) |
| `cmd/sing-box-ui` | GUI process (MyGo native UI) |
| `internal/agent` | agent core: state machine, config store, policy, resource ledger, engine, platform drivers |
| `internal/agentapi` | GUI ⇄ agent contract (protobuf + gRPC) |
| `internal/gui` | GUI: application state, subscriptions, config generation, settings, and the native UI views |
| `native/macos` | Swift bridge (SMAppService, XPC, SCDynamicStore) |
| `packaging` | macOS `.pkg` and Windows installer |

## Development safety

Nothing in the development or test workflow changes the host network. Tests use an in-memory simulated platform driver and
loopback-only fake upstreams; the real platform drivers refuse to run unless the process is the installed service.

## User interface

Views are plain Go functions of the application state and are tested without a window (`ui.NewTester`), including
screenshots rendered in memory (`ui.Render`), so the interface can be reviewed without opening anything.
