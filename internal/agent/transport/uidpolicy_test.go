package transport

import (
	"errors"
	"testing"
)

type uidFixture struct {
	owner   uint32
	hasOwn  bool
	console uint32
	hasCon  bool
	saveErr error
	saved   []uint32
}

func (f *uidFixture) policy() UIDPolicy {
	return UIDPolicy{
		Owner: func() (uint32, bool) { return f.owner, f.hasOwn },
		SetOwner: func(uid uint32) error {
			if f.saveErr != nil {
				return f.saveErr
			}
			f.saved = append(f.saved, uid)
			f.owner, f.hasOwn = uid, true
			return nil
		},
		Console: func() (uint32, bool) { return f.console, f.hasCon },
	}
}

func TestUIDPolicyAdmitsOnlyTheOwner(t *testing.T) {
	f := &uidFixture{owner: 501, hasOwn: true, console: 502, hasCon: true}
	p := f.policy()
	if _, err := p.Check(501, 99); err != nil {
		t.Errorf("the owner was refused: %v", err)
	}
	if _, err := p.Check(502, 99); err == nil {
		t.Error("another user was admitted, even though they are at the screen")
	}
	if _, err := p.Check(0, 1); err == nil {
		t.Error("root was admitted")
	}
	if len(f.saved) != 0 {
		t.Errorf("the owner was rewritten: %v", f.saved)
	}
}

func TestUIDPolicyFirstUserAtTheScreenBecomesTheOwner(t *testing.T) {
	f := &uidFixture{console: 501, hasCon: true}
	p := f.policy()

	// A background process of another account cannot get in first.
	if _, err := p.Check(502, 7); err == nil {
		t.Error("a user who is not at the screen claimed the service")
	}
	if len(f.saved) != 0 {
		t.Fatalf("a refused caller was recorded: %v", f.saved)
	}

	peer, err := p.Check(501, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.saved) != 1 || f.saved[0] != 501 {
		t.Errorf("owner not recorded: %v", f.saved)
	}
	if peer.Description == "" {
		t.Error("no description for the log")
	}
	// And from then on the same rule as always.
	if _, err := p.Check(502, 9); err == nil {
		t.Error("another user got in after the owner was set")
	}
}

func TestUIDPolicyNobodyAtTheScreen(t *testing.T) {
	f := &uidFixture{}
	if _, err := f.policy().Check(501, 1); err == nil {
		t.Error("the service was claimed while the login window was showing")
	}
}

func TestUIDPolicyReportsAFailureToRecord(t *testing.T) {
	f := &uidFixture{console: 501, hasCon: true, saveErr: errors.New("disk full")}
	if _, err := f.policy().Check(501, 1); err == nil {
		t.Error("the caller was admitted though the owner could not be recorded")
	}
}
