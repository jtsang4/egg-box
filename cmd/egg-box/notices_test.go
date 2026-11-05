package main

import (
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("licenses"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNoticesAreFoundWhereEachInstallerPutsThem(t *testing.T) {
	root := t.TempDir()

	// macOS: the program is in Contents/MacOS, the notices in Contents/Resources.
	contents := filepath.Join(root, "EggBox.app", "Contents")
	macExe := filepath.Join(contents, "MacOS", "egg-box")
	put(t, macExe)
	if got := noticesFor(macExe, "darwin"); got != "" {
		t.Fatalf("found %q in a bundle that has none", got)
	}
	put(t, filepath.Join(contents, "Resources", noticesFile))
	if got, want := noticesFor(macExe, "darwin"), filepath.Join(contents, "Resources", noticesFile); got != want {
		t.Fatalf("macOS: %q, want %q", got, want)
	}

	// Windows: next to the program.
	dir := filepath.Join(root, "Program Files", "EggBox")
	winExe := filepath.Join(dir, "EggBox.exe")
	put(t, winExe)
	if got := noticesFor(winExe, "windows"); got != "" {
		t.Fatalf("found %q in an installation that has none", got)
	}
	put(t, filepath.Join(dir, noticesFile))
	if got, want := noticesFor(winExe, "windows"), filepath.Join(dir, noticesFile); got != want {
		t.Fatalf("Windows: %q, want %q", got, want)
	}

	// Each looks only where its own installer puts the file.
	if got := noticesFor(macExe, "windows"); got != "" {
		t.Fatalf("the bundle's resources are not where Windows looks: %q", got)
	}
}

func TestADirectoryIsNotTheNotices(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "EggBox.exe")
	put(t, exe)
	if err := os.Mkdir(filepath.Join(dir, noticesFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := noticesFor(exe, "windows"); got != "" {
		t.Fatalf("a folder was taken for the notices: %q", got)
	}
}
