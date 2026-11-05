package transport

import (
	"errors"
	"fmt"

	"eggbox/internal/agent/server"
)

// UIDPolicy decides which user may control the agent on macOS, once the
// system has vouched for the caller's code signature. It is the counterpart of
// PeerPolicy, which does the same job for the Windows pipe.
//
// Nobody is told at installation who the user is. The first user to ask, who
// must be the one sitting at the screen, becomes the owner and is recorded;
// from then on only that user is let in. Removing the record (an
// administrator does it) lets the next user claim the service.
type UIDPolicy struct {
	// Owner reads the recorded user; ok is false when there is none.
	Owner func() (uid uint32, ok bool)
	// SetOwner records the first user.
	SetOwner func(uid uint32) error
	// Console says who is at the screen; ok is false at the login window.
	Console func() (uid uint32, ok bool)
}

// Check admits or refuses a caller and says who it is for the log.
func (p UIDPolicy) Check(uid uint32, pid int32) (server.Peer, error) {
	describe := fmt.Sprintf("uid=%d pid=%d", uid, pid)
	if uid == 0 {
		return server.Peer{}, errors.New("root is not the application's user")
	}
	if owner, ok := p.Owner(); ok {
		if uid != owner {
			return server.Peer{}, fmt.Errorf("user %d is not the authorized user", uid)
		}
		return server.Peer{Description: describe}, nil
	}
	// Nobody yet: only the person at the screen may claim the service, so a
	// background process of another account cannot get in first.
	if console, ok := p.Console(); !ok || console != uid {
		return server.Peer{}, errors.New("nobody is authorized yet, and the caller is not the person at the screen")
	}
	if err := p.SetOwner(uid); err != nil {
		return server.Peer{}, fmt.Errorf("record the authorized user: %w", err)
	}
	return server.Peer{Description: describe + " (now authorized)"}, nil
}
