package transport

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"eggbox/internal/agent/server"
)

// The rules of the Windows named pipe, kept free of any Windows call so they
// can be read, tested and reasoned about anywhere. pipe_windows.go gathers the
// facts about the process on the other end and asks these rules.

// sidPattern is the string form of a security identifier. Anything else is
// refused before it can reach a security descriptor string.
var sidPattern = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+){1,14}$`)

// ValidSID reports whether s is a well-formed security identifier.
func ValidSID(s string) bool { return sidPattern.MatchString(s) }

// PipeSDDL is the security descriptor of the agent's pipe. The default one
// Windows gives a pipe lets Everyone read, which is far wider than wanted:
//
//	deny   network logons (a pipe is never a remote service)
//	allow  SYSTEM and administrators, everything
//	allow  the authorized user, read and write
//
// Nobody else can even open the pipe. Deny comes first, as a canonical ACL
// has it, so no reordering by the system can change the meaning.
func PipeSDDL(ownerSID string) (string, error) {
	if !ValidSID(ownerSID) {
		return "", fmt.Errorf("%q is not a security identifier", ownerSID)
	}
	return "D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;" + ownerSID + ")", nil
}

// PeerFacts is what the system says about the process that opened the pipe.
type PeerFacts struct {
	PID uint32
	// SID is the user of the process's token, as a string.
	SID string
	// Image is the Win32 path of the process's executable.
	Image string
}

// PeerPolicy decides who may talk to the agent once the pipe is open.
type PeerPolicy struct {
	// OwnerSID is the one user who may control the agent. It comes from the
	// system's own record of who sits at the machine, never from a name typed
	// during installation.
	OwnerSID string
	// Images are the executables that may speak for that user: the installed
	// application, in a directory only administrators can write.
	Images []string
	// RequireSigned asks for a valid code signature on the client's file, and
	// is turned on whenever the agent itself is signed.
	RequireSigned bool
	// Signed checks a file's code signature; nil when RequireSigned is off.
	Signed func(path string) error
}

// Check admits or refuses a client and says who it is for the log.
func (p PeerPolicy) Check(f PeerFacts) (server.Peer, error) {
	if !ValidSID(p.OwnerSID) {
		return server.Peer{}, errors.New("the agent has no authorized user")
	}
	if !strings.EqualFold(f.SID, p.OwnerSID) {
		return server.Peer{}, fmt.Errorf("the client runs as %s, which is not the authorized user", f.SID)
	}
	if !p.allowedImage(f.Image) {
		return server.Peer{}, fmt.Errorf("the client program %s is not part of the installation", f.Image)
	}
	if p.RequireSigned {
		if p.Signed == nil {
			return server.Peer{}, errors.New("a signature is required but nothing can check it")
		}
		if err := p.Signed(f.Image); err != nil {
			return server.Peer{}, fmt.Errorf("the client program is not validly signed: %w", err)
		}
	}
	return server.Peer{Description: fmt.Sprintf("sid=%s pid=%d image=%s", f.SID, f.PID, f.Image)}, nil
}

func (p PeerPolicy) allowedImage(image string) bool {
	if image == "" {
		return false
	}
	want := normalizePath(image)
	for _, ok := range p.Images {
		if ok != "" && normalizePath(ok) == want {
			return true
		}
	}
	return false
}

// normalizePath makes two spellings of one Windows path equal: slashes,
// letter case, a trailing dot or separator, the long-path prefix.
func normalizePath(s string) string {
	s = strings.ReplaceAll(s, "/", `\`)
	s = strings.TrimPrefix(s, `\\?\`)
	s = filepath.Clean(strings.ReplaceAll(s, `\`, string(filepath.Separator)))
	s = strings.ReplaceAll(s, string(filepath.Separator), `\`)
	return strings.ToLower(strings.TrimRight(s, `. `))
}
