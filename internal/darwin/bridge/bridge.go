//go:build darwin

// Package bridge is the Go side of ServiceBridge.dylib, the small Swift
// library that does what macOS only lets Swift or Objective-C do: register the
// background service, listen on and connect to its XPC channel, set a
// temporary system resolver, and hear about sleep and who is at the screen.
//
// The library is loaded at run time (no cgo): the application and the service
// both find it in the application bundle, next to their own program.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ebitengine/purego"
)

// ErrUnavailable means the service is not there: not registered, not allowed
// to run yet, or not able to start.
var ErrUnavailable = errors.New("the service is not available")

// ErrTimeout means the service did not answer in time.
var ErrTimeout = errors.New("the service did not answer in time")

const (
	codeUnavailable = -2
	codeTimeout     = -3
	errCapacity     = 512
)

// ServiceStatus is where the system says the background service stands.
type ServiceStatus int

const (
	ServiceNotRegistered ServiceStatus = iota
	ServiceEnabled
	// ServiceRequiresApproval: registered, waiting for the person to switch it
	// on in System Settings.
	ServiceRequiresApproval
	// ServiceNotFound: macOS could not find a matching service. This can
	// also precede the service's first registration.
	ServiceNotFound
)

func (s ServiceStatus) String() string {
	switch s {
	case ServiceNotRegistered:
		return "not-registered"
	case ServiceEnabled:
		return "enabled"
	case ServiceRequiresApproval:
		return "requires-approval"
	case ServiceNotFound:
		return "not-found"
	}
	return "unknown"
}

// Bridge is a loaded ServiceBridge.dylib.
type Bridge struct {
	lib uintptr

	serviceStatus     func(plist string) int32
	serviceRegister   func(plist string, err *byte, capacity int32) int32
	serviceUnregister func(plist string, err *byte, capacity int32) int32
	openLoginItems    func()

	xpcServe           func(service *byte, requirement *byte, callback uintptr, err *byte, capacity int32) int64
	xpcEndpoint        func(listener int64) int64
	xpcStop            func(handle int64)
	xpcReleaseEndpoint func(handle int64)
	xpcConnect         func(service string, requirement *byte, timeoutMs int32, err *byte, capacity int32) int32
	xpcConnectEndpoint func(endpoint int64, requirement *byte, timeoutMs int32, err *byte, capacity int32) int32

	dnsSet    func(key, servers, token string, err *byte, capacity int32) int32
	dnsToken  func(key string, out *byte, capacity int32) int32
	dnsRemove func(key string, err *byte, capacity int32) int32

	selfTeam func(out *byte, capacity int32) int32
	proxies  func(out *byte, capacity int32) int32
	userName func(uid uint32, out *byte, capacity int32) int32

	consoleUser  func() int64
	watchConsole func(callback uintptr) int64
	watchPower   func(callback uintptr) int64
	watchStop    func(handle int64)
}

// DefaultPath is where the library is in an application bundle, found from the
// running program: Contents/MacOS/<program> next to Contents/Frameworks.
func DefaultPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "..", "Frameworks", "ServiceBridge.dylib"), nil
}

// Load opens the library at path.
func Load(path string) (*Bridge, error) {
	lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, fmt.Errorf("load the service bridge: %w", err)
	}
	b := &Bridge{lib: lib}
	for name, fn := range map[string]any{
		"eggbox_service_status":       &b.serviceStatus,
		"eggbox_service_register":     &b.serviceRegister,
		"eggbox_service_unregister":   &b.serviceUnregister,
		"eggbox_open_login_items":     &b.openLoginItems,
		"eggbox_xpc_serve":            &b.xpcServe,
		"eggbox_xpc_endpoint":         &b.xpcEndpoint,
		"eggbox_xpc_stop":             &b.xpcStop,
		"eggbox_xpc_release_endpoint": &b.xpcReleaseEndpoint,
		"eggbox_xpc_connect":          &b.xpcConnect,
		"eggbox_xpc_connect_endpoint": &b.xpcConnectEndpoint,
		"eggbox_dns_set":              &b.dnsSet,
		"eggbox_dns_token":            &b.dnsToken,
		"eggbox_dns_remove":           &b.dnsRemove,
		"eggbox_self_team":            &b.selfTeam,
		"eggbox_system_proxies":       &b.proxies,
		"eggbox_user_name":            &b.userName,
		"eggbox_console_user":         &b.consoleUser,
		"eggbox_watch_console":        &b.watchConsole,
		"eggbox_watch_power":          &b.watchPower,
		"eggbox_watch_stop":           &b.watchStop,
	} {
		if _, err := purego.Dlsym(lib, name); err != nil {
			return nil, fmt.Errorf("the service bridge is missing %s: %w", name, err)
		}
		purego.RegisterLibFunc(fn, lib, name)
	}
	return b, nil
}

// cstring makes a NUL-terminated copy for a call; nil for "none". The result
// must be kept alive until the call returns.
func cstring(s string) []byte { return append([]byte(s), 0) }

func ptr(b []byte) *byte {
	if len(b) == 0 {
		return nil
	}
	return &b[0]
}

// errBuffer is where a call writes its message.
type errBuffer [errCapacity]byte

func (e *errBuffer) text() string {
	n := 0
	for n < len(e) && e[n] != 0 {
		n++
	}
	return string(e[:n])
}

// ------------------------------------------------------------------ service

// ServiceStatus asks where the background service stands. plist is the file
// name of its launchd property list inside the application.
func (b *Bridge) ServiceStatus(plist string) ServiceStatus {
	return ServiceStatus(b.serviceStatus(plist))
}

// RegisterError is a refusal of the system to register the service.
type RegisterError struct {
	Code    int
	Message string
}

func (e *RegisterError) Error() string {
	return fmt.Sprintf("the system refused to register the service: %s (code %d)", e.Message, e.Code)
}

// Register registers the service with the system. The first time, it does not
// run until the person switches it on in System Settings; ServiceStatus says
// when it may.
func (b *Bridge) Register(plist string) error {
	var eb errBuffer
	if code := b.serviceRegister(plist, &eb[0], errCapacity); code != 0 {
		return &RegisterError{Code: int(code), Message: eb.text()}
	}
	return nil
}

// Unregister removes the registration.
func (b *Bridge) Unregister(plist string) error {
	var eb errBuffer
	if code := b.serviceUnregister(plist, &eb[0], errCapacity); code != 0 {
		return &RegisterError{Code: int(code), Message: eb.text()}
	}
	return nil
}

// OpenLoginItems opens the page of System Settings where the service is
// switched on.
func (b *Bridge) OpenLoginItems() { b.openLoginItems() }

// ---------------------------------------------------------------------- xpc

// Peer is who is on the other end of an introduced connection, as the system
// reports it.
type Peer struct {
	UID uint32
	PID int32
}

// Conn is a connection introduced over XPC.
type Conn struct {
	net.Conn
	Peer Peer
}

// PeerOf says who is on the other end of a connection that came from a
// Listener.
func PeerOf(c net.Conn) (Peer, bool) {
	if c, ok := c.(*Conn); ok {
		return c.Peer, true
	}
	return Peer{}, false
}

// Listener accepts the connections introduced over the service's XPC channel.
// It implements net.Listener, so the gRPC server serves on it as on any other.
type Listener struct {
	b      *Bridge
	handle int64
	conns  chan *Conn

	once   sync.Once
	closed chan struct{}
}

// Serve starts accepting. With a service name it is the agent's Mach service,
// which only the system can have given to this program; without a name it is
// anonymous and reached through Endpoint. requirement is a code signing
// requirement every caller must satisfy; empty means none (tests only).
func (b *Bridge) Serve(service, requirement string) (*Listener, error) {
	l := &Listener{b: b, conns: make(chan *Conn, 16), closed: make(chan struct{})}
	cb := purego.NewCallback(func(fd int32, uid uint32, pid int32) {
		f := os.NewFile(uintptr(fd), "xpc")
		if f == nil {
			return
		}
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			return
		}
		conn := &Conn{Conn: c, Peer: Peer{UID: uid, PID: pid}}
		select {
		case l.conns <- conn:
		case <-l.closed:
			_ = conn.Close()
		default:
			_ = conn.Close() // nobody is accepting; do not stall the system's queue
		}
	})
	var svc, req []byte
	if service != "" {
		svc = cstring(service)
	}
	if requirement != "" {
		req = cstring(requirement)
	}
	var eb errBuffer
	h := b.xpcServe(ptr(svc), ptr(req), cb, &eb[0], errCapacity)
	runtime.KeepAlive(svc)
	runtime.KeepAlive(req)
	if h < 0 {
		return nil, fmt.Errorf("listen on the XPC channel: %s", eb.text())
	}
	l.handle = h
	return l, nil
}

// Accept returns the next connection.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close stops listening.
func (l *Listener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		l.b.xpcStop(l.handle)
		for {
			select {
			case c := <-l.conns:
				_ = c.Close()
			default:
				return
			}
		}
	})
	return nil
}

type xpcAddr struct{}

func (xpcAddr) Network() string { return "xpc" }
func (xpcAddr) String() string  { return "xpc" }

// Addr is a placeholder: the channel has no address.
func (*Listener) Addr() net.Addr { return xpcAddr{} }

// Endpoint is how a client in this process reaches an anonymous listener.
// Release it when done.
type Endpoint int64

// Endpoint makes an endpoint for an anonymous listener.
func (l *Listener) Endpoint() (Endpoint, error) {
	h := l.b.xpcEndpoint(l.handle)
	if h < 0 {
		return 0, errors.New("the listener has no endpoint")
	}
	return Endpoint(h), nil
}

// Release forgets an endpoint.
func (b *Bridge) Release(e Endpoint) { b.xpcReleaseEndpoint(int64(e)) }

// Connect asks the service for a connection. requirement is the code signing
// requirement the service must satisfy; empty means none.
func (b *Bridge) Connect(ctx context.Context, service, requirement string) (net.Conn, error) {
	return b.connect(ctx, requirement, func(req *byte, timeout int32, eb *byte) int32 {
		return b.xpcConnect(service, req, timeout, eb, errCapacity)
	})
}

// ConnectEndpoint asks an anonymous listener, by its endpoint.
func (b *Bridge) ConnectEndpoint(ctx context.Context, e Endpoint, requirement string) (net.Conn, error) {
	return b.connect(ctx, requirement, func(req *byte, timeout int32, eb *byte) int32 {
		return b.xpcConnectEndpoint(int64(e), req, timeout, eb, errCapacity)
	})
}

func (b *Bridge) connect(ctx context.Context, requirement string, call func(req *byte, timeoutMs int32, err *byte) int32) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		timeout = time.Until(dl)
	}
	if timeout < 50*time.Millisecond {
		timeout = 50 * time.Millisecond
	}
	var req []byte
	if requirement != "" {
		req = cstring(requirement)
	}
	var eb errBuffer
	fd := call(ptr(req), int32(timeout/time.Millisecond), &eb[0])
	runtime.KeepAlive(req)
	switch {
	case fd >= 0:
	case fd == codeUnavailable:
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, eb.text(), os.ErrNotExist)
	case fd == codeTimeout:
		return nil, fmt.Errorf("%w: %s", ErrTimeout, eb.text())
	default:
		return nil, errors.New(eb.text())
	}
	f := os.NewFile(uintptr(fd), "xpc")
	defer f.Close()
	c, err := net.FileConn(f)
	if err != nil {
		return nil, fmt.Errorf("use the connection the service gave: %w", err)
	}
	return c, nil
}

// ---------------------------------------------------------------------- dns

// SetResolver points the system's own resolver at servers for every domain, as
// a temporary entry under key that carries token. It takes root.
func (b *Bridge) SetResolver(key string, servers []netip.Addr, token string) error {
	addrs := make([]string, len(servers))
	for i, s := range servers {
		addrs[i] = s.String()
	}
	var eb errBuffer
	if code := b.dnsSet(key, strings.Join(addrs, ","), token, &eb[0], errCapacity); code != 0 {
		return errors.New(eb.text())
	}
	return nil
}

// ResolverToken reads the token of the entry under key. present is false when
// there is no entry; an entry with an empty token is somebody else's.
func (b *Bridge) ResolverToken(key string) (token string, present bool) {
	var out [128]byte
	n := b.dnsToken(key, &out[0], int32(len(out)))
	if n < 0 {
		return "", false
	}
	return string(out[:n]), true
}

// RemoveResolver takes the entry under key down. There being none is fine.
func (b *Bridge) RemoveResolver(key string) error {
	var eb errBuffer
	if code := b.dnsRemove(key, &eb[0], errCapacity); code != 0 {
		return errors.New(eb.text())
	}
	return nil
}

// ------------------------------------------------------------------ proxies

// SystemProxies reads the manual proxy settings of the network services, as
// the JSON report sysproxy.ParseDarwin reads. It changes nothing and needs no
// privileges.
func (b *Bridge) SystemProxies() ([]byte, error) {
	size := int32(16 << 10)
	for range 3 {
		out := make([]byte, size)
		n := b.proxies(&out[0], size)
		switch {
		case n >= 0:
			return out[:n], nil
		case n == -1:
			return nil, errors.New("the system proxy settings cannot be read")
		}
		size = -n
	}
	return nil, errors.New("the system proxy settings keep growing")
}

// ------------------------------------------------------------------ signing

// SelfTeam is the team identifier this program was signed under. ok is false
// for a program that is unsigned or signed ad hoc, which has none.
func (b *Bridge) SelfTeam() (team string, ok bool) {
	var out [64]byte
	n := b.selfTeam(&out[0], int32(len(out)))
	if n <= 0 {
		return "", false
	}
	return string(out[:n]), true
}

// UserName is the account name of a user id; ok is false when there is no such
// user. The directory of users is only reachable through the system's C
// library, which a program without cgo calls through the bridge.
func (b *Bridge) UserName(uid uint32) (name string, ok bool) {
	var out [256]byte
	n := b.userName(uid, &out[0], int32(len(out)))
	if n <= 0 {
		return "", false
	}
	return string(out[:n]), true
}

// -------------------------------------------------------------------- watch

// ConsoleUser is the user id of the person at the screen; false at the login
// window.
func (b *Bridge) ConsoleUser() (uid uint32, ok bool) {
	u := b.consoleUser()
	if u < 0 {
		return 0, false
	}
	return uint32(u), true
}

// WatchConsole calls fn whenever the person at the screen changes: with their
// user id, or ok=false when nobody is. fn runs on a thread of the system's;
// it must return quickly. The result stops the watching.
func (b *Bridge) WatchConsole(fn func(uid uint32, ok bool)) (stop func(), err error) {
	cb := purego.NewCallback(func(uid int32) { fn(uint32(uid), uid >= 0) })
	h := b.watchConsole(cb)
	if h < 0 {
		return nil, errors.New("cannot watch who is at the screen")
	}
	var once sync.Once
	return func() { once.Do(func() { b.watchStop(h) }) }, nil
}

// WatchPower calls fn when the computer goes to sleep (awake=false) and when it
// wakes. fn runs on a thread of the system's; it must return quickly.
func (b *Bridge) WatchPower(fn func(awake bool)) (stop func(), err error) {
	cb := purego.NewCallback(func(state int32) { fn(state == 1) })
	h := b.watchPower(cb)
	if h < 0 {
		return nil, errors.New("cannot watch the power state")
	}
	var once sync.Once
	return func() { once.Do(func() { b.watchStop(h) }) }, nil
}
