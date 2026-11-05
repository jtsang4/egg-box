# Sing-Box UI

A desktop client for [sing-box](https://github.com/SagerNet/sing-box) on macOS and Windows.

The GUI ([MyGo](https://github.com/egoist/mygo) + a web front end) never touches the network stack. A small
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
| `cmd/sing-box-ui` | GUI process |
| `internal/agent` | agent core: state machine, config store, policy, resource ledger, engine, platform drivers |
| `internal/agentapi` | GUI ⇄ agent contract (protobuf + gRPC) |
| `internal/gui` | GUI backend: subscriptions, config generation, settings |
| `frontend` | React + TypeScript UI |
| `native/macos` | Swift bridge (SMAppService, XPC, SCDynamicStore) |
| `packaging` | macOS `.pkg` and Windows installer |

## Development safety

Nothing in the development or test workflow changes the host network. Tests use an in-memory simulated platform driver and
loopback-only fake upstreams; the real platform drivers refuse to run unless the process is the installed service.
