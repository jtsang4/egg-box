package macsys

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOwner(dir); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("an empty directory has no owner, got %v", err)
	}
	if err := SaveOwner(dir, Owner{UID: 501, Name: "alex"}); err != nil {
		t.Fatal(err)
	}
	o, err := LoadOwner(dir)
	if err != nil || o.UID != 501 || o.Name != "alex" {
		t.Fatalf("got %+v, %v", o, err)
	}
	if err := RemoveOwner(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOwner(dir); !errors.Is(err, ErrNoOwner) {
		t.Errorf("owner still there after removal: %v", err)
	}
	if err := RemoveOwner(dir); err != nil {
		t.Errorf("removing nobody must not fail: %v", err)
	}
}

func TestRootCannotOwn(t *testing.T) {
	if err := SaveOwner(t.TempDir(), Owner{UID: 0}); err == nil {
		t.Error("root was accepted as the authorized user")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, OwnerFile), []byte(`{"uid":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOwner(dir); err == nil {
		t.Error("a record naming root was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, OwnerFile), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOwner(dir); err == nil || errors.Is(err, ErrNoOwner) {
		t.Errorf("a damaged record must be an error of its own, got %v", err)
	}
}

func TestRequirementWithATeam(t *testing.T) {
	got, err := AppRequirement("ABCDE12345")
	if err != nil {
		t.Fatal(err)
	}
	want := `anchor apple generic and identifier "me.jtsang.singboxui" and certificate leaf[subject.OU] = "ABCDE12345"`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	agent, _ := AgentRequirement("ABCDE12345")
	if !strings.Contains(agent, `identifier "me.jtsang.singboxui.agent"`) {
		t.Errorf("the agent requirement names the wrong identifier: %s", agent)
	}
}

func TestRequirementWithoutATeamIsTheWeakDevelopmentForm(t *testing.T) {
	got, err := AppRequirement("")
	if err != nil || got != `identifier "me.jtsang.singboxui"` {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestRequirementRefusesInjection(t *testing.T) {
	for _, bad := range []struct{ id, team string }{
		{`x" or true or identifier "y`, ""},
		{"a b", ""},
		{"", ""},
		{"me.jtsang.singboxui", `X" or anchor apple or "`},
		{"me.jtsang.singboxui", "short"},
		{"me.jtsang.singboxui", "abcde12345"}, // team ids are upper case
	} {
		if got, err := Requirement(bad.id, bad.team); err == nil {
			t.Errorf("Requirement(%q, %q) = %q, want an error", bad.id, bad.team, got)
		}
	}
}

func TestConsoleTracker(t *testing.T) {
	owner, known := uint32(501), false
	tr := NewConsoleTracker(func() (uint32, bool) { return owner, known })

	// Before anybody is authorized, nobody is present.
	if changed, present := tr.Apply(501, true); changed || present {
		t.Errorf("an unauthorized console user counted as present: %v %v", changed, present)
	}
	known = true
	if changed, present := tr.Apply(502, true); changed || present {
		t.Errorf("another user at the screen: %v %v", changed, present)
	}
	if changed, present := tr.Apply(501, true); !changed || !present {
		t.Errorf("the owner arrives: %v %v", changed, present)
	}
	if changed, _ := tr.Apply(501, true); changed {
		t.Error("the same answer twice was a change")
	}
	// Another person switching in is the owner leaving.
	if changed, present := tr.Apply(502, true); !changed || present {
		t.Errorf("another user takes over: %v %v", changed, present)
	}
	if changed, present := tr.Apply(501, true); !changed || !present {
		t.Errorf("the owner returns: %v %v", changed, present)
	}
	// The login window.
	if changed, present := tr.Apply(0, false); !changed || present {
		t.Errorf("login window: %v %v", changed, present)
	}
	if tr.Present() {
		t.Error("Present() disagrees")
	}
}

func TestOwnerStoreClaimsOnceAndFollowsTheFile(t *testing.T) {
	dir := t.TempDir()
	st := NewOwnerStore(dir)
	if _, ok := st.UID(); ok {
		t.Fatal("an empty store has an owner")
	}
	if err := st.Claim(501, "alex"); err != nil {
		t.Fatal(err)
	}
	if uid, ok := st.UID(); !ok || uid != 501 {
		t.Errorf("owner %d %v", uid, ok)
	}
	if o, _ := st.Owner(); o.Name != "alex" {
		t.Errorf("name %q", o.Name)
	}
	if err := st.Claim(502, "sam"); err == nil {
		t.Error("an owner was replaced by a claim")
	}
	if uid, _ := st.UID(); uid != 501 {
		t.Errorf("the owner changed to %d", uid)
	}

	// An administrator hands the service over by removing the record: seen at once.
	if err := RemoveOwner(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.UID(); ok {
		t.Error("the removed owner is still believed")
	}
	if err := st.Claim(502, "sam"); err != nil {
		t.Errorf("claim after removal: %v", err)
	}
}

func TestOwnerStoreWithADamagedRecordAuthorizesNobodyAndCannotBeClaimed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, OwnerFile), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewOwnerStore(dir)
	if _, ok := st.UID(); ok {
		t.Error("a damaged record authorized somebody")
	}
	if err := st.Claim(501, ""); err == nil {
		t.Error("a damaged record was written over silently")
	}
}

func writeFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }
