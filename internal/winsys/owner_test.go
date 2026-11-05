package winsys

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOwner(dir); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("an empty directory has no owner, got %v", err)
	}
	want := Owner{SID: "S-1-5-21-1004336348-1177238915-682003330-1001", Name: `PC\ada`}
	if err := SaveOwner(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOwner(dir)
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	// Replacing the owner leaves one record, and no temporary file behind.
	next := Owner{SID: "S-1-5-21-1004336348-1177238915-682003330-1002"}
	if err := SaveOwner(dir, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadOwner(dir); got != next {
		t.Errorf("got %+v", got)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("%d files in the directory", len(ents))
	}
}

func TestOwnerRefusesGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := SaveOwner(dir, Owner{SID: "everyone"}); err == nil {
		t.Error("saved a name that is not a SID")
	}
	for _, body := range []string{"", "{", `{"sid":""}`, `{"sid":"S-1-5-21-1)(A;;GA;;;WD"}`} {
		if err := os.WriteFile(filepath.Join(dir, OwnerFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOwner(dir); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
}
