package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func stageAndPromote(t *testing.T, s *Store, expected uint64, cfg string, res ...Resource) Revision {
	t.Helper()
	st, err := s.Stage(expected, "p1", "Profile", res)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := st.WriteConfig([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	rev, err := s.Promote(st)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	return rev
}

func TestFirstRevisionIsOne(t *testing.T) {
	s, _ := open(t)
	if _, ok := s.Current(); ok {
		t.Fatal("empty store has no current revision")
	}
	rev := stageAndPromote(t, s, 0, `{"a":1}`)
	if rev.N != 1 || s.CurrentN() != 1 {
		t.Fatalf("revision = %d", rev.N)
	}
	got, err := os.ReadFile(rev.ConfigPath())
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("config = %q, %v", got, err)
	}
}

func TestStageDetectsRevisionConflict(t *testing.T) {
	s, _ := open(t)
	stageAndPromote(t, s, 0, `{}`)
	if _, err := s.Stage(0, "", "", nil); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale writer must conflict, got %v", err)
	}
	if _, err := s.Stage(5, "", "", nil); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("future writer must conflict, got %v", err)
	}
	if _, err := s.Stage(1, "", "", nil); err != nil {
		t.Fatalf("correct writer must pass: %v", err)
	}
}

func TestRevisionsSurviveReopen(t *testing.T) {
	s, root := open(t)
	stageAndPromote(t, s, 0, `{"v":1}`)
	if err := s.MarkGood(1); err != nil {
		t.Fatal(err)
	}
	again, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cur, ok := again.Current()
	if !ok || cur.N != 1 || cur.ProfileName != "Profile" {
		t.Fatalf("current = %+v ok=%v", cur, ok)
	}
	good, ok := again.LastGood()
	if !ok || good.N != 1 {
		t.Fatalf("last good = %+v ok=%v", good, ok)
	}
}

func TestPromoteKeepsPreviousUntilNewIsGood(t *testing.T) {
	s, _ := open(t)
	stageAndPromote(t, s, 0, `{"v":1}`)
	_ = s.MarkGood(1)
	stageAndPromote(t, s, 1, `{"v":2}`)

	cur, _ := s.Current()
	good, _ := s.LastGood()
	if cur.N != 2 || good.N != 1 {
		t.Fatalf("current=%d lastGood=%d", cur.N, good.N)
	}
	if _, err := os.Stat(good.ConfigPath()); err != nil {
		t.Fatal("the last good revision must stay on disk until the new one proves itself")
	}

	_ = s.MarkGood(2)
	if _, err := os.Stat(good.Dir); !os.IsNotExist(err) {
		t.Fatal("the superseded revision should be pruned once the new one is good")
	}
}

func TestRevertRestoresPreviousAndDropsFailed(t *testing.T) {
	s, _ := open(t)
	stageAndPromote(t, s, 0, `{"v":1}`)
	_ = s.MarkGood(1)
	failed := stageAndPromote(t, s, 1, `{"v":2}`)

	if err := s.Revert(1); err != nil {
		t.Fatal(err)
	}
	if s.CurrentN() != 1 {
		t.Fatalf("current = %d", s.CurrentN())
	}
	if _, err := os.Stat(failed.Dir); !os.IsNotExist(err) {
		t.Fatal("failed revision must be deleted")
	}
	// The burned number can be used again, and a fresh stage must succeed.
	rev := stageAndPromote(t, s, 1, `{"v":3}`)
	if rev.N != 2 {
		t.Fatalf("revision number after revert = %d", rev.N)
	}
}

func TestDiscardRemovesPendingDirectory(t *testing.T) {
	s, root := open(t)
	st, err := s.Stage(0, "", "", []Resource{{Name: "a.srs", Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	s.Discard(st)
	matches, _ := filepath.Glob(filepath.Join(root, "config", "rev-*"))
	if len(matches) != 0 {
		t.Fatalf("leftover dirs: %v", matches)
	}
	if _, err := s.Promote(st); err == nil {
		t.Fatal("a discarded revision must not be promotable")
	}
}

func TestOpenSweepsOrphanedDirectories(t *testing.T) {
	s, root := open(t)
	stageAndPromote(t, s, 0, `{}`)
	orphan := filepath.Join(root, "config", "rev-000009.pending")
	if err := os.MkdirAll(filepath.Join(orphan, "resources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphaned pending directory should be swept on open")
	}
}

func TestResourcesAreStoredWithRestrictivePermissions(t *testing.T) {
	s, _ := open(t)
	rev := stageAndPromote(t, s, 0, `{}`, Resource{Name: "geosite-cn.srs", Data: []byte("rules")})
	p := filepath.Join(rev.ResourceDir(), "geosite-cn.srs")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("resource is accessible to others: %v", info.Mode())
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, []byte("rules")) {
		t.Fatalf("resource content = %q", got)
	}
}

func TestResourceValidation(t *testing.T) {
	good := sha256.Sum256([]byte("data"))
	cases := []struct {
		name string
		res  []Resource
		ok   bool
	}{
		{"plain", []Resource{{Name: "a.srs", Data: []byte("x")}}, true},
		{"dots and dashes", []Resource{{Name: "geo-site_cn.v2.srs", Data: nil}}, true},
		{"path traversal", []Resource{{Name: "../evil", Data: nil}}, false},
		{"subdirectory", []Resource{{Name: "a/b", Data: nil}}, false},
		{"backslash", []Resource{{Name: `a\b`, Data: nil}}, false},
		{"dot", []Resource{{Name: ".", Data: nil}}, false},
		{"dotdot", []Resource{{Name: "..", Data: nil}}, false},
		{"empty", []Resource{{Name: "", Data: nil}}, false},
		{"too long", []Resource{{Name: strings.Repeat("a", 65), Data: nil}}, false},
		{"duplicate ignoring case", []Resource{{Name: "A", Data: nil}, {Name: "a", Data: nil}}, false},
		{"too big", []Resource{{Name: "big", Data: make([]byte, MaxResourceBytes+1)}}, false},
		{"good hash", []Resource{{Name: "h", Data: []byte("data"), SHA256: hex.EncodeToString(good[:])}}, true},
		{"bad hash", []Resource{{Name: "h", Data: []byte("tampered"), SHA256: hex.EncodeToString(good[:])}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, root := open(t)
			st, err := s.Stage(0, "", "", tc.res)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				s.Discard(st)
				return
			}
			if !errors.Is(err, ErrInvalidResource) {
				t.Fatalf("expected ErrInvalidResource, got %v", err)
			}
			// Nothing may have been written outside the sandbox either.
			if _, err := os.Stat(filepath.Join(root, "..", "evil")); err == nil {
				t.Fatal("traversal wrote outside the data root")
			}
		})
	}
}

func TestTooManyResources(t *testing.T) {
	s, _ := open(t)
	var rs []Resource
	for i := 0; i <= MaxResources; i++ {
		rs = append(rs, Resource{Name: "r" + strings.Repeat("x", i%10) + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	if _, err := s.Stage(0, "", "", rs); !errors.Is(err, ErrInvalidResource) {
		t.Fatalf("got %v", err)
	}
}

func TestWriteConfigLimits(t *testing.T) {
	s, _ := open(t)
	st, _ := s.Stage(0, "", "", nil)
	if err := st.WriteConfig(nil); err == nil {
		t.Fatal("empty config must be rejected")
	}
	if err := st.WriteConfig(make([]byte, MaxConfigBytes+1)); err == nil {
		t.Fatal("oversized config must be rejected")
	}
}

func TestPromoteWithoutConfigFails(t *testing.T) {
	s, _ := open(t)
	st, _ := s.Stage(0, "", "", nil)
	if _, err := s.Promote(st); err == nil {
		t.Fatal("promote without a configuration must fail")
	}
}

func TestSettingsFileDefaultsAndCorruption(t *testing.T) {
	dir := t.TempDir()
	f, err := OpenFile(dir, "settings.json", DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Load(); !got.ResumeAfterCrash || got.AutoConnect || got.LogLevel != "info" {
		t.Fatalf("defaults = %+v", got)
	}
	got, err := f.Update(func(s *Settings) error { s.AutoConnect = true; return nil })
	if err != nil || !got.AutoConnect {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if !f.Load().AutoConnect {
		t.Fatal("update was not persisted")
	}

	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.Load(); got.AutoConnect {
		t.Fatalf("a corrupt file must fall back to defaults, got %+v", got)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "settings.json.corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("corrupt settings should be kept for inspection: %v", matches)
	}
}

func TestUpdateRollsBackOnError(t *testing.T) {
	f, _ := OpenFile(t.TempDir(), "intent.json", Intent{})
	_, err := f.Update(func(i *Intent) error { i.Connected = true; return errors.New("nope") })
	if err == nil {
		t.Fatal("expected the callback error")
	}
	if f.Load().Connected {
		t.Fatal("a failed update must not persist")
	}
}

func TestValidLogLevel(t *testing.T) {
	for _, l := range []string{"trace", "debug", "info", "warn", "error"} {
		if !ValidLogLevel(l) {
			t.Fatalf("%s should be valid", l)
		}
	}
	for _, l := range []string{"", "INFO", "fatal", "panic"} {
		if ValidLogLevel(l) {
			t.Fatalf("%q should be invalid", l)
		}
	}
}

func TestScratchHoldsResourcesAndCleansUp(t *testing.T) {
	s, root := open(t)
	sc, err := s.Scratch([]Resource{{Name: "a.srs", Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(sc.ResourceDir(), "a.srs"))
	if err != nil || string(got) != "x" {
		t.Fatalf("resource = %q, %v", got, err)
	}
	sc.Close()
	if _, err := os.Stat(sc.ResourceDir()); !os.IsNotExist(err) {
		t.Fatalf("scratch dir must be gone, stat err = %v", err)
	}

	// A leftover from a crash is swept on the next Open.
	left, err := s.Scratch(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left.ResourceDir()); !os.IsNotExist(err) {
		t.Fatalf("leftover scratch dir must be swept, stat err = %v", err)
	}
}

func TestScratchValidatesResources(t *testing.T) {
	s, _ := open(t)
	if _, err := s.Scratch([]Resource{{Name: "../evil", Data: []byte("x")}}); !errors.Is(err, ErrInvalidResource) {
		t.Fatalf("err = %v", err)
	}
}

func TestPendingResourceDirExistsBeforePromote(t *testing.T) {
	s, _ := open(t)
	st, err := s.Stage(0, "p", "P", []Resource{{Name: "a.srs", Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(st.PendingResourceDir(), "a.srs")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.FinalResourceDir()); !os.IsNotExist(err) {
		t.Fatal("final dir must not exist before Promote")
	}
	s.Discard(st)
}
