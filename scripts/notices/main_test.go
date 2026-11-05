package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLicenseFilesAreFoundByName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"LICENSE", "LICENSE.md", "LICENSE-MIT", "COPYING", "NOTICE", "PATENTS", "license.txt", "UNLICENSE",
		"README.md", "license.go", "licenses.json", "main.go", "go.mod"} {
		write(t, dir, name, "x")
	}
	if err := os.Mkdir(filepath.Join(dir, "LICENSES"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := licenseFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"COPYING", "LICENSE", "LICENSE-MIT", "LICENSE.md", "NOTICE", "PATENTS", "UNLICENSE", "license.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("license files = %v, want %v", got, want)
	}
}

func TestReadLicensesJoinsFilesAndNormalisesLineEnds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "LICENSE", "terms\r\nmore terms\r\n\r\n")
	write(t, dir, "PATENTS", "a patent grant\n")
	text, ok, err := readLicenses(dir)
	if err != nil || !ok {
		t.Fatalf("readLicenses = %v, %v", ok, err)
	}
	if want := "terms\nmore terms\n\na patent grant"; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}

	_, ok, err = readLicenses(t.TempDir())
	if err != nil || ok {
		t.Fatalf("a folder with no license reported ok=%v err=%v", ok, err)
	}
}

func TestGroupPrintsEachTextOnce(t *testing.T) {
	got := group(map[string]string{
		"b v1": "GPL text",
		"a v1": "MIT text",
		"c v2": "GPL text",
	})
	if len(got) != 2 {
		t.Fatalf("groups = %+v, want 2", got)
	}
	if got[0].Text != "MIT text" || !reflect.DeepEqual(got[0].Covers, []string{"a v1"}) {
		t.Fatalf("first group = %+v", got[0])
	}
	if got[1].Text != "GPL text" || !reflect.DeepEqual(got[1].Covers, []string{"b v1", "c v2"}) {
		t.Fatalf("second group = %+v", got[1])
	}
}

func TestRenderNamesEveryModuleAndKeepsTheText(t *testing.T) {
	out := render("Product", []entry{
		{Covers: []string{"example.com/a v1.0.0", "example.com/b v2.0.0"}, Text: "Permission is hereby granted"},
	}, []string{"A note."})
	for _, want := range []string{"Product is built from", "A note.", "example.com/a v1.0.0", "example.com/b v2.0.0", "Permission is hereby granted"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("the file does not end with a newline")
	}
}

func TestWintunNoteReadsTheVersionTheModuleCarries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "wintun"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "internal", "wintun"), "README.md", "# wintun\n\nDLL version: 0.14.1\n")
	note := wintunNote([]module{{Path: "example.com/other", Dir: t.TempDir()}, {Path: "github.com/sagernet/sing-tun", Dir: dir}})
	if !strings.Contains(note, "Wintun 0.14.1") {
		t.Fatalf("note = %q", note)
	}
	if got := wintunNote([]module{{Path: "example.com/other", Dir: dir}}); got != "" {
		t.Fatalf("a program without sing-tun got the note %q", got)
	}
}
