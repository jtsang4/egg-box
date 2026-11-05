//go:build darwin

package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	dylibPath string
	buildErr  error
)

// TestMain builds the Swift library once for the machine's own architecture.
// Without a Swift toolchain the tests that need it are skipped.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sbui-bridge-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	arch := map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
	script, _ := filepath.Abs(filepath.Join("..", "..", "..", "native", "macos", "ServiceBridge", "build.sh"))
	if out, err := exec.Command(script, dir, arch).CombinedOutput(); err != nil {
		buildErr = fmt.Errorf("build the service bridge: %w\n%s", err, out)
	} else {
		dylibPath = filepath.Join(dir, "ServiceBridge.dylib")
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func load(t *testing.T) *Bridge {
	t.Helper()
	if buildErr != nil {
		t.Skipf("the Swift library could not be built here: %v", buildErr)
	}
	b, err := Load(dylibPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLoadRejectsMissingLibrary(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nothing.dylib")); err == nil {
		t.Fatal("loading a library that is not there succeeded")
	}
}

func TestServiceStatusOfAServiceTheProgramDoesNotContain(t *testing.T) {
	b := load(t)
	// Reading the status changes nothing. A test binary is not an application
	// bundle, so no such service can exist.
	if got := b.ServiceStatus("me.jtsang.singboxui.test-nothing.plist"); got != ServiceNotFound && got != ServiceNotRegistered {
		t.Errorf("status = %v", got)
	}
}

func TestXPCIntroducesAConnection(t *testing.T) {
	b := load(t)
	l, err := b.Serve("", "") // anonymous: nothing is registered with the system
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ep, err := l.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release(ep)

	type accepted struct {
		c   net.Conn
		err error
	}
	got := make(chan accepted, 1)
	go func() { c, err := l.Accept(); got <- accepted{c, err} }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := b.ConnectEndpoint(ctx, ep, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var srv net.Conn
	select {
	case a := <-got:
		if a.err != nil {
			t.Fatal(a.err)
		}
		srv = a.c
	case <-time.After(10 * time.Second):
		t.Fatal("the listener never saw the connection")
	}
	defer srv.Close()

	// The system says who called: this very process.
	peer, ok := PeerOf(srv)
	if !ok {
		t.Fatal("the accepted connection carries no peer")
	}
	if peer.UID != uint32(os.Getuid()) || peer.PID != int32(os.Getpid()) {
		t.Errorf("peer = %+v, want uid %d pid %d", peer, os.Getuid(), os.Getpid())
	}

	// And it is an ordinary stream, in both directions.
	go func() { _, _ = client.Write([]byte("ping")) }()
	buf := make([]byte, 4)
	_ = srv.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(srv, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("server read %q, %v", buf, err)
	}
	go func() { _, _ = srv.Write([]byte("pong")) }()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("client read %q, %v", buf, err)
	}
}

func TestXPCSeveralCallersGetTheirOwnConnection(t *testing.T) {
	b := load(t)
	l, err := b.Serve("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ep, _ := l.Endpoint()
	defer b.Release(ep)

	const n = 4
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c, err := b.ConnectEndpoint(ctx, ep, "")
		cancel()
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		defer c.Close()
		s, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		msg := fmt.Sprintf("hello %d", i)
		go func() { _, _ = c.Write([]byte(msg)) }()
		buf := make([]byte, len(msg))
		_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(s, buf); err != nil || string(buf) != msg {
			t.Fatalf("caller %d: server read %q, %v", i, buf, err)
		}
	}
}

func TestXPCConnectToAServiceThatIsNotThereFails(t *testing.T) {
	b := load(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := b.Connect(ctx, "me.jtsang.singboxui.test.nothing-listens-here", "")
	if err == nil {
		t.Fatal("connecting to a service nobody registered succeeded")
	}
	if !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrTimeout) {
		t.Errorf("unexpected error: %v", err)
	}
	if errors.Is(err, ErrUnavailable) && !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an unavailable service should look like a missing one: %v", err)
	}
}

func TestXPCRefusesAnUnreadableRequirement(t *testing.T) {
	b := load(t)
	// A requirement nobody can read is a mistake in the installation; the
	// service must not start as if there were none.
	if l, err := b.Serve("", "this is not a requirement"); err == nil {
		_ = l.Close()
		t.Fatal("serving with a requirement nobody can read succeeded")
	}

	l, err := b.Serve("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ep, _ := l.Endpoint()
	defer b.Release(ep)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// The same on the caller's side: it must not carry on without checking.
	if _, err := b.ConnectEndpoint(ctx, ep, "this is not a requirement either"); err == nil {
		t.Fatal("connecting with a requirement nobody can read succeeded")
	}
}

func TestXPCRequirementKeepsUnsignedCallersOut(t *testing.T) {
	b := load(t)
	// Only code signed by Apple satisfies this; the test program is not.
	l, err := b.Serve("", "anchor apple")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ep, _ := l.Endpoint()
	defer b.Release(ep)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if c, err := b.ConnectEndpoint(ctx, ep, ""); err == nil {
		_ = c.Close()
		t.Fatal("a caller that does not satisfy the requirement was let in")
	}
	accepted := make(chan struct{})
	go func() {
		if c, err := l.Accept(); err == nil {
			_ = c.Close()
			close(accepted)
		}
	}()
	select {
	case <-accepted:
		t.Fatal("the listener handed a connection to a caller that does not satisfy the requirement")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestXPCRequirementAdmitsTheRightCaller(t *testing.T) {
	b := load(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("codesign", "--verify", exe).Run(); err != nil {
		t.Skipf("the test program carries no valid signature here: %v", err)
	}
	out, err := exec.Command("codesign", "-dv", exe).CombinedOutput()
	if err != nil {
		t.Skipf("codesign cannot describe the test program: %v", err)
	}
	var id string
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "Identifier="); ok {
			id = strings.TrimSpace(v)
		}
	}
	if id == "" {
		t.Skip("the test program has no signing identifier")
	}

	l, err := b.Serve("", fmt.Sprintf("identifier %q", id))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ep, _ := l.Endpoint()
	defer b.Release(ep)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := b.ConnectEndpoint(ctx, ep, "")
	if err != nil {
		t.Fatalf("a caller that satisfies the requirement was refused: %v", err)
	}
	_ = c.Close()
}

func TestListenerCloseUnblocksAccept(t *testing.T) {
	b := load(t)
	l, err := b.Serve("", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := l.Accept(); done <- err }()
	time.Sleep(50 * time.Millisecond)
	_ = l.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("Accept returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not return after Close")
	}
	_ = l.Close() // twice is fine
}

func TestResolverEntryThatIsNotThere(t *testing.T) {
	b := load(t)
	// Only reads: nothing is written to the system's configuration here.
	const key = "State:/Network/Service/me.jtsang.singboxui.test-absent/DNS"
	if _, present := b.ResolverToken(key); present {
		t.Fatal("an entry that nobody wrote is there")
	}
	if err := b.RemoveResolver(key); err != nil {
		t.Errorf("removing what is not there failed: %v", err)
	}
}

func TestWatchersStartAndStop(t *testing.T) {
	b := load(t)
	stopC, err := b.WatchConsole(func(uint32, bool) {})
	if err != nil {
		t.Fatal(err)
	}
	stopP, err := b.WatchPower(func(bool) {})
	if err != nil {
		t.Fatal(err)
	}
	stopC()
	stopP()
	stopC() // twice is fine
	// Reading who is at the screen works whether or not anybody is.
	_, _ = b.ConsoleUser()
}

func TestSelfTeamOfAnAdHocSignedTestProgram(t *testing.T) {
	b := load(t)
	// A test binary carries no team: it is unsigned or signed ad hoc. The
	// call must say so rather than crash or invent one.
	if team, ok := b.SelfTeam(); ok {
		t.Errorf("a test program claims team %q", team)
	}
}

func TestUserName(t *testing.T) {
	b := load(t)
	name, ok := b.UserName(0)
	if !ok || name != "root" {
		t.Errorf("user 0 is %q (%v)", name, ok)
	}
	if name, ok := b.UserName(uint32(os.Getuid())); !ok || name == "" {
		t.Errorf("the current user has no name: %q %v", name, ok)
	}
	if name, ok := b.UserName(4000000000); ok {
		t.Errorf("a user that does not exist is called %q", name)
	}
}
