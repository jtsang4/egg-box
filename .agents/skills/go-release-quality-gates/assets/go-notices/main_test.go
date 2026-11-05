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
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
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

func TestNotesFlagCollectsEveryUse(t *testing.T) {
	var n notes
	_ = n.Set("one")
	_ = n.Set("two")
	if !reflect.DeepEqual([]string(n), []string{"one", "two"}) {
		t.Fatalf("notes = %v", n)
	}
}

// app builds a throwaway module that imports a throwaway dependency through a
// replace directive, so that `go list` runs without a network. The dependency
// has the given files.
func app(t *testing.T, depFiles map[string]string) (appDir string) {
	t.Helper()
	root := t.TempDir()
	dep := filepath.Join(root, "dep")
	appDir = filepath.Join(root, "app")
	write(t, dep, "go.mod", "module example.invalid/dep\n\ngo 1.21\n")
	write(t, dep, "dep.go", "package dep\n\nfunc Hello() string { return \"hello\" }\n")
	for name, text := range depFiles {
		write(t, dep, name, text)
	}
	write(t, appDir, "go.mod", "module example.invalid/app\n\ngo 1.21\n\nrequire example.invalid/dep v0.0.0\n\nreplace example.invalid/dep => ../dep\n")
	write(t, appDir, "cmd/app/main.go", "package main\n\nimport \"example.invalid/dep\"\n\nfunc main() { println(dep.Hello()) }\n")
	return appDir
}

// inDir runs the rest of the test with dir as the working directory.
func inDir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
}

func TestRunCoversTheModulesABuildContainsAndTheStandardLibrary(t *testing.T) {
	dir := app(t, map[string]string{"LICENSE": "The dependency's terms.\n"})
	inDir(t, dir)
	out := filepath.Join(t.TempDir(), "NOTICES.txt")
	err := run(options{product: "Demo", goos: "linux", goarchs: []string{"amd64", "arm64"}, out: out, pkgs: []string{"./cmd/..."}, notes: []string{"Also carries a driver."}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"Demo is built from", "example.invalid/dep v0.0.0", "The dependency's terms.", "The Go standard library go", "Also carries a driver."} {
		if !strings.Contains(text, want) {
			t.Errorf("notices miss %q", want)
		}
	}
	if strings.Contains(text, "example.invalid/app") {
		t.Error("the product's own module is listed among its dependencies")
	}
}

func TestRunStopsOnAModuleWithoutALicense(t *testing.T) {
	dir := app(t, nil)
	inDir(t, dir)
	err := run(options{product: "Demo", goos: "linux", goarchs: []string{"amd64"}, out: filepath.Join(t.TempDir(), "N.txt"), pkgs: []string{"./cmd/..."}})
	if err == nil || !strings.Contains(err.Error(), "example.invalid/dep v0.0.0") {
		t.Fatalf("err = %v, want a refusal that names the module", err)
	}
}
