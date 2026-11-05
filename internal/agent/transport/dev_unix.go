//go:build eggbox_dev && (darwin || linux)

package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"eggbox/internal/agent/server"
	"eggbox/internal/agentclient"
)

// ListenDev listens on a Unix socket at path (mode 0600, in a directory only
// the owner can enter) and returns the verifier that admits only processes of
// the same user as the agent.
func ListenDev(path string) (net.Listener, server.Verifier, error) {
	if !filepath.IsAbs(path) {
		return nil, nil, errors.New("the development socket path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create socket directory: %w", err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm()&0o077 != 0 {
		return nil, nil, fmt.Errorf("the socket directory %s must not be accessible to other users", dir)
	}
	// Replace a socket a previous run left behind, but never anything else.
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		_ = os.Remove(path)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, nil, err
	}
	return l, verifySameUser, nil
}

// DialDev returns a dialer for the socket at path.
func DialDev(path string) agentclient.Dialer {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
}

func verifySameUser(conn net.Conn) (server.Peer, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return server.Peer{}, errors.New("not a unix connection")
	}
	uid, pid, err := peerCredentials(uc)
	if err != nil {
		return server.Peer{}, fmt.Errorf("read peer credentials: %w", err)
	}
	if uid != uint32(os.Geteuid()) {
		return server.Peer{}, fmt.Errorf("peer uid %d is not the agent's user", uid)
	}
	desc := fmt.Sprintf("uid=%d", uid)
	if pid > 0 {
		desc += fmt.Sprintf(" pid=%d", pid)
	}
	return server.Peer{Description: desc}, nil
}

// rawControl runs fn on the connection's descriptor.
func rawControl(uc *net.UnixConn, fn func(fd int)) error {
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	return raw.Control(func(fd uintptr) { fn(int(fd)) })
}
