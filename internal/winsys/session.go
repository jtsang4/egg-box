package winsys

// The notifications Windows sends a service when a session changes (the
// WTS_* values of a SERVICE_CONTROL_SESSIONCHANGE).
const (
	SessionConsoleConnect    uint32 = 1
	SessionConsoleDisconnect uint32 = 2
	SessionLogon             uint32 = 5
	SessionLogoff            uint32 = 6
)

// SessionTracker follows whether the authorized user is signed in at the
// computer. The service feeds it the session notifications; it says when the
// answer changes.
//
// Signing out, or another person taking the screen over, ends the user's
// presence: the connection is taken down then. Locking the screen is not a
// notification it reacts to, so a locked computer keeps its connection.
type SessionTracker struct {
	ownerSID string
	session  uint32
	present  bool
}

// NewSessionTracker returns a tracker for one user. If the user is already
// signed in (the service was started while they were), pass the session.
func NewSessionTracker(ownerSID string, present bool, session uint32) *SessionTracker {
	return &SessionTracker{ownerSID: ownerSID, present: present, session: session}
}

// Present reports whether the user is believed to be signed in.
func (t *SessionTracker) Present() bool { return t.present }

// Apply takes one notification. userSID tells who is signed in to a session.
// It reports whether the user's presence changed, and what it is now.
func (t *SessionTracker) Apply(event, session uint32, userSID func(session uint32) (string, error)) (changed, present bool) {
	was := t.present
	switch event {
	case SessionLogon, SessionConsoleConnect:
		if sid, err := userSID(session); err == nil && sid == t.ownerSID {
			t.session, t.present = session, true
		}
	case SessionLogoff, SessionConsoleDisconnect:
		if t.present && session == t.session {
			t.present = false
		}
	}
	return t.present != was, t.present
}
