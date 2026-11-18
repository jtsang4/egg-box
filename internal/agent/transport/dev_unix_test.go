//go:build eggbox_dev && ((darwin && !ios) || (linux && !android))

package transport

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortDir returns a temporary directory with a path short enough for a Unix socket.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sbt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestListenDevCreatesAPrivateSocket(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "sub", "a.sock")
	l, verify, err := ListenDev(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	st, err := os.Stat(path)
	if err != nil || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", st, err)
	}
	if dst, _ := os.Stat(filepath.Dir(path)); dst.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the directory must be private, mode %v", dst.Mode())
	}

	go func() {
		c, err := DialDev(path)(context.Background())
		if err == nil {
			time.Sleep(200 * time.Millisecond)
			c.Close()
		}
	}()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer, err := verify(conn)
	if err != nil {
		t.Fatalf("a process of the same user must be admitted: %v", err)
	}
	if !strings.HasPrefix(peer.Description, "uid=") || !strings.Contains(peer.Description, "pid=") {
		t.Fatalf("peer = %q", peer.Description)
	}
}

func TestListenDevRefusesUnsafePaths(t *testing.T) {
	dir := shortDir(t)

	if _, _, err := ListenDev("relative.sock"); err == nil {
		t.Fatal("a relative path must be refused")
	}

	file := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(file, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ListenDev(file); err == nil {
		t.Fatal("an existing regular file must never be replaced")
	}
	if b, _ := os.ReadFile(file); string(b) != "precious" {
		t.Fatal("the file was touched")
	}

	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ListenDev(filepath.Join(open, "a.sock")); err == nil {
		t.Fatal("a directory other users can enter must be refused")
	}
}

func TestListenDevReplacesAStaleSocket(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "a.sock")
	l1, _, err := ListenDev(path)
	if err != nil {
		t.Fatal(err)
	}
	// Closing a Unix listener removes the file; simulate a crash that left it.
	if ul, ok := l1.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	l1.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Skip("this platform removes the socket on close")
	}
	l2, _, err := ListenDev(path)
	if err != nil {
		t.Fatalf("a stale socket must be replaced: %v", err)
	}
	l2.Close()
}

func TestDialDevFailsWhenNothingListens(t *testing.T) {
	_, err := DialDev(filepath.Join(shortDir(t), "missing.sock"))(context.Background())
	if err == nil || !os.IsNotExist(unwrap(err)) {
		t.Fatalf("err = %v", err)
	}
}

func unwrap(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}
