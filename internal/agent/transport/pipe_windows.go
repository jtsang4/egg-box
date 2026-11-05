//go:build windows

package transport

import (
	"errors"
	"fmt"
	"net"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/jtsang4/sing-box-ui/internal/agent/server"
	"github.com/jtsang4/sing-box-ui/internal/brand"
)

// PipeConfig says how the agent's pipe is guarded.
type PipeConfig struct {
	// Path is the pipe's name; empty means brand.WindowsPipePath.
	Path string
	// OwnerSID is the authorized user.
	OwnerSID string
	// Images are the executables allowed to speak for that user.
	Images []string
	// RequireSigned demands a valid Authenticode signature of the client's
	// file. The service turns it on whenever its own file is signed.
	RequireSigned bool
}

// ListenPipe opens the agent's named pipe with an explicit access list and
// returns the verifier that admits only the authorized user's application.
// The system's default pipe permissions are never used.
func ListenPipe(cfg PipeConfig) (net.Listener, server.Verifier, error) {
	path := cfg.Path
	if path == "" {
		path = brand.WindowsPipePath
	}
	sddl, err := PipeSDDL(cfg.OwnerSID)
	if err != nil {
		return nil, nil, err
	}
	l, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	policy := PeerPolicy{OwnerSID: cfg.OwnerSID, Images: cfg.Images, RequireSigned: cfg.RequireSigned}
	if cfg.RequireSigned {
		policy.Signed = verifySignature
	}
	return l, pipeVerifier(policy), nil
}

// pipeVerifier turns the policy into a Verifier: it asks the system who is on
// the other end of the pipe, and only then asks the policy.
func pipeVerifier(policy PeerPolicy) server.Verifier {
	return func(conn net.Conn) (server.Peer, error) {
		h, ok := conn.(interface{ Fd() uintptr })
		if !ok {
			return server.Peer{}, errors.New("not a named pipe connection")
		}
		var pid uint32
		if err := windows.GetNamedPipeClientProcessId(windows.Handle(h.Fd()), &pid); err != nil {
			return server.Peer{}, fmt.Errorf("find the client process: %w", err)
		}
		facts, err := InspectProcess(pid)
		if err != nil {
			return server.Peer{}, err
		}
		return policy.Check(facts)
	}
}

// InspectProcess reads who a process runs as and which file it runs.
func InspectProcess(pid uint32) (PeerFacts, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return PeerFacts{}, fmt.Errorf("open client process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)

	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return PeerFacts{}, fmt.Errorf("open the client's token: %w", err)
	}
	defer tok.Close()
	user, err := tok.GetTokenUser()
	if err != nil {
		return PeerFacts{}, fmt.Errorf("read the client's user: %w", err)
	}

	size := uint32(windows.MAX_LONG_PATH)
	buf := make([]uint16, size)
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return PeerFacts{}, fmt.Errorf("read the client's program: %w", err)
	}
	return PeerFacts{PID: pid, SID: user.User.Sid.String(), Image: windows.UTF16ToString(buf[:size])}, nil
}

// verifySignature checks a file's Authenticode signature and chain, without
// asking the user anything and without a network lookup that could stall a
// connection.
func verifySignature(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	file := &windows.WinTrustFileInfo{
		Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
		FilePath: p,
	}
	data := &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(file),
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	return verifyErr
}

// FileIsSigned reports whether a file carries a valid signature. The service
// uses it on itself: a signed agent only talks to a signed application.
func FileIsSigned(path string) bool { return verifySignature(path) == nil }
