package remote

import (
	"context"
	"errors"
	"net"
	"time"

	"google.golang.org/protobuf/proto"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agentapi"
)

// callTimeout bounds a call that answers once.
const callTimeout = 15 * time.Second

// Client calls an engine served by a Server.
type Client struct {
	// Dial connects to the server.
	Dial func(ctx context.Context) (net.Conn, error)
}

// Status is what the served engine says about itself.
type Status struct {
	Running bool
	Tun     string
	Version string
}

// open makes the call and reads its answer; the connection stays open for
// what streams after it.
func (c *Client) open(ctx context.Context, req request) (net.Conn, answer, error) {
	conn, err := c.Dial(ctx)
	if err != nil {
		return nil, answer{}, err
	}
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	var a answer
	if err := writeJSON(conn, req); err != nil {
		conn.Close()
		return nil, a, err
	}
	if err := readJSON(conn, &a); err != nil {
		conn.Close()
		return nil, a, err
	}
	_ = conn.SetDeadline(time.Time{})
	if err := a.error(); err != nil {
		conn.Close()
		return nil, a, err
	}
	return conn, a, nil
}

// call makes a call that answers once.
func (c *Client) call(ctx context.Context, req request) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	conn, a, err := c.open(ctx, req)
	if err != nil {
		return a, err
	}
	conn.Close()
	return a, nil
}

// Status asks whether the core runs.
func (c *Client) Status(ctx context.Context) (Status, error) {
	a, err := c.call(ctx, request{Op: opStatus})
	return Status{Running: a.Running, Tun: a.Tun, Version: a.Version}, err
}

// watch opens a stream of messages made by mk.
func watch[T proto.Message](ctx context.Context, c *Client, req request, mk func() T) (<-chan T, error) {
	conn, _, err := c.open(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan T)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	go func() {
		defer close(out)
		defer stop()
		defer conn.Close()
		for {
			b, err := readFrame(conn)
			if err != nil {
				return
			}
			m := mk()
			if proto.Unmarshal(b, m) != nil {
				return
			}
			select {
			case out <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (c *Client) WatchGroups(ctx context.Context) (<-chan *agentapi.Groups, error) {
	return watch(ctx, c, request{Op: opWatchGroups}, func() *agentapi.Groups { return &agentapi.Groups{} })
}

func (c *Client) WatchTraffic(ctx context.Context, interval time.Duration) (<-chan *agentapi.Traffic, error) {
	return watch(ctx, c, request{Op: opWatchTraffic, IntervalMS: interval.Milliseconds()}, func() *agentapi.Traffic { return &agentapi.Traffic{} })
}

func (c *Client) WatchConnections(ctx context.Context, interval time.Duration) (<-chan *agentapi.ConnectionEvents, error) {
	return watch(ctx, c, request{Op: opWatchConnections, IntervalMS: interval.Milliseconds()}, func() *agentapi.ConnectionEvents { return &agentapi.ConnectionEvents{} })
}

func (c *Client) SelectOutbound(group, outbound string) error {
	_, err := c.call(context.Background(), request{Op: opSelectOutbound, Group: group, Outbound: outbound})
	return err
}

func (c *Client) URLTest(ctx context.Context, tag string) error {
	_, err := c.call(ctx, request{Op: opURLTest, Tag: tag})
	return err
}

func (c *Client) Mode() (string, []string, error) {
	a, err := c.call(context.Background(), request{Op: opMode})
	return a.Mode, a.Modes, err
}

func (c *Client) SetMode(mode string) error {
	_, err := c.call(context.Background(), request{Op: opSetMode, Mode: mode})
	return err
}

func (c *Client) CloseConnection(id string) error {
	_, err := c.call(context.Background(), request{Op: opCloseConnection, ID: id})
	return err
}

func (c *Client) CloseAllConnections() error {
	_, err := c.call(context.Background(), request{Op: opCloseAll})
	return err
}

// Logs streams the core's log, the last lines first.
func (c *Client) Logs(ctx context.Context) (<-chan LogLine, error) {
	conn, _, err := c.open(ctx, request{Op: opLogs})
	if err != nil {
		return nil, err
	}
	out := make(chan LogLine)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	go func() {
		defer close(out)
		defer stop()
		defer conn.Close()
		for {
			var l LogLine
			if readJSON(conn, &l) != nil {
				return
			}
			select {
			case out <- l:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// ErrGone is the served process going away before the core said it exited.
var ErrGone = errors.New("remote: the engine's process went away")

// WaitExit waits until the served core stops by itself. When the process
// serving it ends first, it returns ErrGone.
func (c *Client) WaitExit(ctx context.Context) (engine.ExitEvent, error) {
	conn, err := c.Dial(ctx)
	if err != nil {
		return engine.ExitEvent{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := writeJSON(conn, request{Op: opExit}); err != nil {
		return engine.ExitEvent{}, err
	}
	var a answer
	if err := readJSON(conn, &a); err != nil {
		if ctx.Err() != nil {
			return engine.ExitEvent{}, ctx.Err()
		}
		return engine.ExitEvent{}, ErrGone
	}
	ev := engine.ExitEvent{Revoked: a.Revoked}
	if a.Cause != "" {
		ev.Err = errors.New(a.Cause)
	}
	return ev, nil
}
