// Package agentclient is the GUI's side of the agent API: it keeps one verified
// connection to the agent alive, tells the interface what state the link is in
// (not installed, refused, incompatible, ready), and gives streams and
// operations the retry behaviour they need.
//
// It speaks only the agentapi contract; it knows nothing about sing-box.
package agentclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/jtsang4/sing-box-ui/internal/agentapi"
	"github.com/jtsang4/sing-box-ui/internal/brand"
)

// Dialer opens a raw connection to the agent. Each platform provides one.
type Dialer func(ctx context.Context) (net.Conn, error)

// LinkState is the condition of the connection to the agent.
type LinkState int

const (
	// LinkConnecting: dialing or handshaking.
	LinkConnecting LinkState = iota
	// LinkReady: the handshake succeeded; calls go through.
	LinkReady
	// LinkUnavailable: nothing answers. The agent is not installed, not running
	// or still starting.
	LinkUnavailable
	// LinkRefused: something answered and closed the connection before the
	// handshake finished. The agent only talks to the authorized user's
	// installed application.
	LinkRefused
	// LinkIncompatible: the agent speaks another version of the protocol. The
	// application and the agent must be reinstalled together.
	LinkIncompatible
	// LinkClosed: the client was closed.
	LinkClosed
)

func (s LinkState) String() string {
	return [...]string{"connecting", "ready", "unavailable", "refused", "incompatible", "closed"}[s]
}

// Link is a snapshot of the connection.
type Link struct {
	State LinkState
	// Err explains an unusable state.
	Err error
	// Hello is the agent's handshake answer (set when Ready).
	Hello *agentapi.HandshakeResponse
	// Generation counts successful handshakes: it changes when the link was
	// re-established, so a caller knows to refresh what it cached.
	Generation uint64
}

// Options tunes a client.
type Options struct {
	// Version is reported to the agent in the handshake.
	Version string
	// Backoff is the wait between connection attempts (default 250ms growing to 3s).
	Backoff func(attempt int) time.Duration
	// HandshakeTimeout bounds one handshake (default 5s).
	HandshakeTimeout time.Duration
}

// Client maintains the connection. It is safe for concurrent use.
type Client struct {
	dial Dialer
	opts Options

	cancel  context.CancelFunc
	done    chan struct{}
	started atomic.Bool

	mu    sync.Mutex
	link  Link
	api   agentapi.AgentClient
	subs  map[chan Link]struct{}
	gen   uint64
	ready chan struct{} // closed while the link is ready, replaced when it drops
}

// New returns a client that has not connected yet. Call Start.
func New(dial Dialer, opts Options) *Client {
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = 5 * time.Second
	}
	if opts.Backoff == nil {
		opts.Backoff = defaultBackoff
	}
	return &Client{
		dial: dial, opts: opts,
		link:  Link{State: LinkConnecting},
		subs:  map[chan Link]struct{}{},
		ready: make(chan struct{}),
	}
}

func defaultBackoff(attempt int) time.Duration {
	d := 250 * time.Millisecond << min(attempt, 4)
	return min(d, 3*time.Second)
}

// Start begins connecting in the background and keeps the link alive until
// ctx ends or Close is called.
func (c *Client) Start(ctx context.Context) {
	if !c.started.CompareAndSwap(false, true) {
		return
	}
	ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		c.run(ctx)
	}()
}

// Close stops the client and waits for its goroutine.
func (c *Client) Close() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	<-c.done
}

// Link returns the current state of the connection.
func (c *Client) Link() Link {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.link
}

// Updates returns a channel that receives the link state whenever it changes.
// A slow reader gets the latest state, never a backlog. The channel closes
// when ctx ends.
func (c *Client) Updates(ctx context.Context) <-chan Link {
	ch := make(chan Link, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	ch <- c.link
	c.mu.Unlock()
	go func() {
		<-ctx.Done()
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
		close(ch)
	}()
	return ch
}

// ErrNotReady is returned for calls made while the link is down.
var ErrNotReady = errors.New("the agent is not reachable")

// API returns the generated client of the live connection.
func (c *Client) API() (agentapi.AgentClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.link.State != LinkReady || c.api == nil {
		return nil, fmt.Errorf("%w (%s)", ErrNotReady, c.link.State)
	}
	return c.api, nil
}

// WaitReady blocks until the link is ready, ctx ends, or the link becomes
// permanently unusable (incompatible).
func (c *Client) WaitReady(ctx context.Context) error {
	for {
		c.mu.Lock()
		ready, state, err := c.ready, c.link.State, c.link.Err
		c.mu.Unlock()
		switch state {
		case LinkReady:
			return nil
		case LinkIncompatible, LinkClosed:
			if err == nil {
				err = ErrNotReady
			}
			return err
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (c *Client) set(l Link) {
	c.mu.Lock()
	if l.State == LinkReady {
		c.gen++
		l.Generation = c.gen
	} else {
		l.Generation = c.gen
	}
	was := c.link.State
	c.link = l
	if l.State != LinkReady {
		c.api = nil
	}
	switch {
	case l.State == LinkReady && was != LinkReady:
		close(c.ready)
	case l.State != LinkReady && was == LinkReady:
		c.ready = make(chan struct{})
	}
	for ch := range c.subs {
		select {
		case ch <- l:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- l:
			default:
			}
		}
	}
	c.mu.Unlock()
}

// ------------------------------------------------------------------ the loop

func (c *Client) run(ctx context.Context) {
	defer c.set(Link{State: LinkClosed})
	attempt := 0
	for ctx.Err() == nil {
		c.set(Link{State: LinkConnecting})
		h, err := c.attempt(ctx)
		if err == nil {
			attempt = 0
			c.hold(ctx, h)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		state := classify(err)
		c.set(Link{State: state, Err: err})
		wait := c.opts.Backoff(attempt)
		if state == LinkIncompatible {
			wait = max(wait, 5*time.Second) // nothing changes until the user reinstalls
		}
		attempt++
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

type held struct {
	conn  *grpc.ClientConn
	hello *agentapi.HandshakeResponse
}

type dialFailure struct{ error }

func (e dialFailure) Unwrap() error { return e.error }

type incompatibleError struct{ agent uint32 }

func (e incompatibleError) Error() string {
	return fmt.Sprintf("the agent speaks protocol version %d, this application speaks %d", e.agent, brand.ProtocolVersion)
}

// attempt dials and handshakes once. The raw connection is dialed here, not by
// gRPC, so "nothing is listening" and "something listens but turned us away"
// are told apart by where the attempt failed.
func (c *Client) attempt(ctx context.Context) (*held, error) {
	dctx, cancel := context.WithTimeout(ctx, c.opts.HandshakeTimeout)
	raw, err := c.dial(dctx)
	cancel()
	if err != nil {
		return nil, dialFailure{err}
	}
	var used atomic.Bool
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// gRPC may only use the connection we dialed; when it is lost the whole
		// attempt starts over, with a new handshake.
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			if used.CompareAndSwap(false, true) {
				return raw, nil
			}
			return nil, errors.New("the connection to the agent was lost")
		}),
		grpc.WithDisableRetry(),
	}
	opts = append(opts, agentapi.WithProtocol(brand.ProtocolVersion)...)
	conn, err := grpc.NewClient("passthrough:///agent", opts...)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	hctx, hcancel := context.WithTimeout(ctx, c.opts.HandshakeTimeout)
	defer hcancel()
	hello, err := agentapi.NewAgentClient(conn).Handshake(hctx, &agentapi.HandshakeRequest{
		ProtocolVersion: brand.ProtocolVersion, ClientVersion: c.opts.Version,
	})
	if err != nil {
		_ = conn.Close()
		if code := status.Code(err); code == codes.Unavailable || code == codes.DeadlineExceeded {
			// Connected, then dropped before answering: the agent's peer check turned us away.
			return nil, refusedError{err}
		}
		return nil, err
	}
	if hello.GetProtocolVersion() != brand.ProtocolVersion {
		_ = conn.Close()
		return nil, incompatibleError{agent: hello.GetProtocolVersion()}
	}
	return &held{conn: conn, hello: hello}, nil
}

type refusedError struct{ error }

func (e refusedError) Unwrap() error { return e.error }

func (e refusedError) Error() string {
	return "the agent closed the connection: it only serves the authorized user's installed application (" + e.error.Error() + ")"
}

func classify(err error) LinkState {
	var inc incompatibleError
	var ref refusedError
	var df dialFailure
	switch {
	case errors.As(err, &inc):
		return LinkIncompatible
	case errors.As(err, &ref):
		return LinkRefused
	case errors.As(err, &df):
		return LinkUnavailable
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ENOENT):
		return LinkUnavailable
	}
	return LinkUnavailable
}

// hold publishes a ready link and blocks until the connection dies or ctx ends.
func (c *Client) hold(ctx context.Context, h *held) {
	defer h.conn.Close()
	c.mu.Lock()
	c.api = agentapi.NewAgentClient(h.conn)
	c.mu.Unlock()
	c.set(Link{State: LinkReady, Hello: h.hello})

	// A connection that leaves READY is gone (gRPC cannot redial: it only owns
	// the connection it was given). Rebuilding it repeats the handshake, which
	// is how a restarted agent is noticed.
	for {
		s := h.conn.GetState()
		if s != connectivity.Ready {
			return
		}
		if !h.conn.WaitForStateChange(ctx, s) {
			return
		}
	}
}

// NewRequestID returns a fresh idempotency key.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system's random source is gone; nothing sensible can follow
	}
	return hex.EncodeToString(b[:])
}
