package winsys

import (
	"errors"
	"testing"
)

func lookup(users map[uint32]string) func(uint32) (string, error) {
	return func(s uint32) (string, error) {
		if sid, ok := users[s]; ok {
			return sid, nil
		}
		return "", errors.New("nobody there")
	}
}

func TestSessionTrackerFollowsTheOwner(t *testing.T) {
	const owner, other = "S-1-5-21-1-2-3-1001", "S-1-5-21-1-2-3-1002"
	users := map[uint32]string{2: owner, 3: other}
	tr := NewSessionTracker(owner, false, 0)

	// Somebody else signing in changes nothing.
	if changed, present := tr.Apply(SessionLogon, 3, lookup(users)); changed || present {
		t.Fatalf("another user's sign-in changed the answer: %v %v", changed, present)
	}
	// The owner signs in.
	if changed, present := tr.Apply(SessionLogon, 2, lookup(users)); !changed || !present {
		t.Fatalf("owner sign-in: %v %v", changed, present)
	}
	// The same notification again is not a change.
	if changed, _ := tr.Apply(SessionConsoleConnect, 2, lookup(users)); changed {
		t.Error("a repeated notification reported a change")
	}
	// Another person signing out is not the owner leaving.
	if changed, present := tr.Apply(SessionLogoff, 3, lookup(users)); changed || !present {
		t.Errorf("another user's sign-out: %v %v", changed, present)
	}
	// Another person taking the screen over: the owner's session disconnects.
	if changed, present := tr.Apply(SessionConsoleDisconnect, 2, lookup(users)); !changed || present {
		t.Errorf("switching away: %v %v", changed, present)
	}
	// Coming back.
	if changed, present := tr.Apply(SessionConsoleConnect, 2, lookup(users)); !changed || !present {
		t.Errorf("coming back: %v %v", changed, present)
	}
	if changed, present := tr.Apply(SessionLogoff, 2, lookup(users)); !changed || present {
		t.Errorf("sign-out: %v %v", changed, present)
	}
}

func TestSessionTrackerIgnoresLockAndUnknownNotifications(t *testing.T) {
	const owner = "S-1-5-21-1-2-3-1001"
	tr := NewSessionTracker(owner, true, 2)
	for _, ev := range []uint32{7 /* lock */, 8 /* unlock */, 4 /* remote disconnect */, 99} {
		if changed, present := tr.Apply(ev, 2, lookup(nil)); changed || !present {
			t.Errorf("notification %d changed the answer: %v %v", ev, changed, present)
		}
	}
}

func TestSessionTrackerIgnoresUnreadableSessions(t *testing.T) {
	tr := NewSessionTracker("S-1-5-21-1-2-3-1001", false, 0)
	if changed, _ := tr.Apply(SessionLogon, 5, lookup(nil)); changed {
		t.Error("a session nobody could be read from was taken for the owner")
	}
}

func TestUnderAny(t *testing.T) {
	dirs := []string{`C:\Program Files`, `C:\Program Files (x86)`, ""}
	for path, want := range map[string]bool{
		`C:\Program Files\EggBox\agentd.exe`:      true,
		`c:\program files\eggbox\agentd.exe`:      true,
		`C:\Program Files (x86)\X\agentd.exe`:     true,
		`C:\Program Files`:                        false, // the directory itself
		`C:\Program Files Evil\agentd.exe`:        false, // a sibling with the same prefix
		`C:\Users\me\Downloads\agentd.exe`:        false,
		`C:\Program Files\..\Users\me\agentd.exe`: false,
		`C:\Program Files\EggBox\..\..\evil.exe`:  false,
	} {
		if got := UnderAny(path, dirs); got != want {
			t.Errorf("UnderAny(%q) = %v, want %v", path, got, want)
		}
	}
}
