//go:build !windows

package macsys

import (
	"os"
	"path/filepath"
	"testing"
)

func me() uint32 { return uint32(os.Getuid()) }

func TestEnsureProtectedDirCreatesAndAccepts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "EggBox", "agent")
	if err := EnsureProtectedDir(dir, me()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("%v %v", fi, err)
	}
	if err := EnsureProtectedDir(dir, me()); err != nil {
		t.Errorf("a second call must accept what the first made: %v", err)
	}
}

func TestEnsureProtectedDirRefusesWhatOthersCanChange(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o770, 0o707, 0o777, 0o720} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := EnsureProtectedDir(dir, me()); err == nil {
			t.Errorf("mode %o was accepted", mode)
		}
	}
	// The folder holding it counts too.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(base, 0o700)
	if err := EnsureProtectedDir(dir, me()); err == nil {
		t.Error("a parent that anybody can write in was accepted")
	}
}

func TestEnsureProtectedDirRefusesAnotherOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	if err := EnsureProtectedDir(dir, me()+1); err == nil {
		t.Error("a folder of somebody else was accepted")
	}
}

func TestEnsureProtectedDirRefusesLinksAndFiles(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProtectedDir(link, me()); err == nil {
		t.Error("a link was accepted")
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProtectedDir(file, me()); err == nil {
		t.Error("a file was accepted")
	}
}

func TestCheckInstallLocation(t *testing.T) {
	// The temporary folder is the test's own, standing in for root's.
	base := t.TempDir()
	exe := filepath.Join(base, "App.app", "Contents", "MacOS", "agentd")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The folders of the machine above the temporary one belong to whoever
	// they belong to: check from the temporary folder down, as if this user
	// were root.
	if err := checkLocation(exe, base, me()); err != nil {
		t.Fatalf("a closed location was refused: %v", err)
	}

	for _, c := range []struct {
		what string
		path string
		mode os.FileMode
	}{
		{"a program anybody can replace", exe, 0o757},
		{"a folder anybody can write in", filepath.Join(base, "App.app", "Contents", "MacOS"), 0o777},
		{"a folder of a group", filepath.Join(base, "App.app", "Contents"), 0o775},
	} {
		orig, _ := os.Stat(c.path)
		if err := os.Chmod(c.path, c.mode); err != nil {
			t.Fatal(err)
		}
		if err := checkLocation(exe, base, me()); err == nil {
			t.Errorf("%s was accepted", c.what)
		}
		_ = os.Chmod(c.path, orig.Mode().Perm())
	}

	if err := checkLocation(exe, base, me()+1); err == nil {
		t.Error("a program of a user who is not root was accepted")
	}
}

func TestCheckInstallLocationFollowsLinks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real, 0o757); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := checkLocation(link, base, me()); err == nil {
		t.Error("a link to a program anybody can replace was accepted")
	}
}

func TestCheckInstallLocationOfAMissingFile(t *testing.T) {
	if err := CheckInstallLocation(filepath.Join(t.TempDir(), "nothing"), me()); err == nil {
		t.Error("a file that is not there was accepted")
	}
}
