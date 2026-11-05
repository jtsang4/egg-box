# Sandbox by construction

Sketches in Go; the ideas carry to any language. The point of each is that the dangerous behaviour is unreachable from the code paths a developer or a test can start, rather than avoided by care.

## The driver seam

```go
// Driver is everything the service does to the operating system.
type Driver interface {
	// Name reaches logs and the handshake, so a client (and a test) can see that it talks to a simulation.
	Name() string
	CreateTunnel(ctx context.Context, plan TunnelPlan) (Tunnel, error)
	ApplyDNS(ctx context.Context, plan DNSPlan) (release func() error, err error)
	WatchNetwork(ctx context.Context) <-chan NetworkEvent
	// Release removes one recorded resource. It must be idempotent and must
	// only remove what still carries our fingerprint.
	Release(ctx context.Context, e ledger.Entry) (Outcome, error)
}
```

- The simulated driver holds everything in memory and records its calls, so tests can assert on them.
- Decision logic (who may connect, which routes conflict, when to re-apply after a wake) takes plain values and returns plain values. The per-OS file only gathers facts (the peer's SID and image path, the route table) and calls the pure rule. That split is why the Windows pipe policy could be unit-tested on a Mac.
- Real drivers live in per-platform packages that only the installed service's entry file imports.

## The build-tag trio

For the binary that hosts the dev entry:

| File | Constraint | Content |
| --- | --- | --- |
| `dev.go` | `dev && (darwin \|\| linux)` | dev subcommand: refuses root, unix socket in a 0700 dir, sim driver |
| `dev_unsupported.go` | `dev && !(darwin \|\| linux)` | prints "not supported on this platform", exits |
| (none) | release | the dev code is not compiled in, so a release binary has no dev transport |

For a second binary that only needs to know whether dev options exist (a GUI), the release stub must cover **every combination the dev code does not**: `!(dev && (darwin || linux))`. A stub tagged only `!dev` leaves `dev` on Windows without a definition and the build breaks - this happened, and only a vet across all GOOS/GOARCH with the tag found it. Vet the tagged build for every platform in CI (`go-release-quality-gates/scripts/vet-matrix.sh -e dev`).

Optional proof for a release build: compile it and search the binary for a marker string that exists only in the dev transport.

## The guard in front of the real thing

```go
func runService() error {
	if os.Geteuid() != 0 {
		return errors.New("the service runs as root and is started by the system, not by hand")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := checkInstallLocation(self); err != nil {
		return fmt.Errorf("not installed where only administrators can change it: %w", err)
	}
	return serve(realDriver())
}
```

`checkInstallLocation` resolves symlinks, then walks the file and every parent directory up to `/`: each must belong to root, not be writable by everybody, and be group-writable only for the administrators' group (on macOS `/Applications` is `root:admin 0775`). A service that runs a binary a user can replace is a privilege escalation, so it refuses. Make the walk testable by taking the top directory as a parameter (tests cannot choose who owns the machine's real folders). The same idea on Windows: the install directory's ACL.

A developer who runs this by mistake as an ordinary user gets a refusal; nobody can get a real driver through a flag.

## Loopback and fake stand-ins

| Real thing | Stand-in |
| --- | --- |
| TUN device and automatic routes | a loopback proxy listener on a free port with the same outbound chain; traffic comes from an `http.Client` using that proxy against a local `httptest` origin |
| DNS override | in-memory store that checks the ownership token; assertions on the calls |
| Route table edits | a pure analyzer fed with a captured table; it never runs `route` |
| launchd / SCM control | a fake controller that records requests |
| Login item, auto start | an optional func left nil in dev, so the UI hides the row (test both presence and absence) |
| Keychain, DPAPI | a file under a temp dir |
| Subscription server | `httptest.Server` serving invented fixtures; the live URL only in opt-in tests |

A sandbox variant of the config policy turns "create a TUN" into "listen on loopback"; assert in a fuzz oracle that a sandbox config can never contain a TUN.

## Reserving ports

A random free port must be free for both transports if the component binds both (a core's DNS or QUIC listener binds UDP too). Better still, pass `:0` and read the bound port back. If a port must be chosen first:

```go
func freePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, uerr := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		l.Close()
		if uerr == nil {
			u.Close()
			return port
		}
	}
	t.Fatal("no port free for both TCP and UDP")
	return 0
}
```

Unix socket paths are short (about 104 bytes on macOS, 108 on Linux): `t.TempDir()` can exceed that, so use `os.MkdirTemp("", "x")` with a short prefix and mode 0700.

## Shape of an end-to-end test

1. Start the service in-process with the sim driver and the real engine, on a unix socket in a private temp dir.
2. Start an origin with `httptest.NewServer`.
3. Connect as a client: apply a config, connect.
4. Send a request through the loopback proxy; assert the origin's body comes back.
5. Assert the handshake says `driver == "sim"` and dev mode, so a wrong wiring fails loudly.
6. Disconnect; assert the ledger is clean and the host interface table (name, flags, MTU, addresses) is identical to the one taken at the start.
7. Wait for conditions with "settled or deadline" polling (20 ms steps), never a fixed sleep.
