// Package engine defines what the controller needs from a proxy core. The
// production implementation embeds sing-box (engine/singbox); tests use a
// programmable fake (engine/fake). The controller never imports sing-box.
package engine

import (
	"context"
	"errors"
	"time"

	"eggbox/internal/agentapi"
)

// Sentinel errors shared by implementations.
var (
	// ErrNotRunning is returned by runtime operations while no core is running.
	ErrNotRunning = errors.New("core is not running")
	// ErrNotFound means the named group, outbound or connection does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid means the request itself is wrong (not a selector, bad mode...).
	ErrInvalid = errors.New("invalid request")
)

// Prepared is a configuration the policy accepted, ready to run.
type Prepared struct {
	Revision uint64
	JSON     []byte
	HasTun   bool
	TunTag   string
	// LogLevel is informational; the level is already inside JSON.
	LogLevel string
}

// ExitEvent reports a core that stopped without being asked to.
type ExitEvent struct {
	Err error
}

// LogSink receives the core's log lines.
type LogSink func(level, message string)

// Engine runs one core at a time.
type Engine interface {
	// Version is the core's version string.
	Version() string
	// SetLogSink installs the receiver for the core's log output.
	SetLogSink(LogSink)
	// Check instantiates the configuration without starting it. It must not
	// touch the network or the operating system.
	Check(ctx context.Context, p Prepared) error
	// Start boots the core and returns once it is serving (TUN up when configured).
	// A cancelled ctx aborts the start and leaves nothing running.
	Start(ctx context.Context, p Prepared) error
	// Stop shuts the core down and returns after everything it held is released.
	// Stopping a core that is not running is not an error.
	Stop(ctx context.Context) error
	// Running reports whether a core is serving.
	Running() bool
	// Exited delivers one event when a running core dies on its own. A Stop
	// requested through this interface never produces one. The channel is the
	// same for the lifetime of the engine, so callers can select on it forever.
	Exited() <-chan ExitEvent
	// TunInterface is the name of the TUN device the running core created ("" when none).
	TunInterface() string

	Observer
}

// Observer is the read/control surface used while a core is running. Streams
// end when ctx is cancelled or the core stops.
type Observer interface {
	WatchGroups(ctx context.Context) (<-chan *agentapi.Groups, error)
	WatchTraffic(ctx context.Context, interval time.Duration) (<-chan *agentapi.Traffic, error)
	WatchConnections(ctx context.Context, interval time.Duration) (<-chan *agentapi.ConnectionEvents, error)
	SelectOutbound(group, outbound string) error
	URLTest(ctx context.Context, tag string) error
	Mode() (current string, all []string, err error)
	SetMode(mode string) error
	CloseConnection(id string) error
	CloseAllConnections() error
}
