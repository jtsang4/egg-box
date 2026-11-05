// Command notices writes the third-party notices that travel with a build: the
// license of every module compiled into the programs, and of the Go standard
// library. Nearly all of those licenses require whoever hands a program on to
// keep them with it, so an installer or archive carries the file this writes.
//
//	go run ./scripts/notices -product "My App" -goos windows -goarch amd64,arm64 \
//	    -tags "$TAGS" -out THIRD-PARTY-NOTICES.txt ./cmd/...
//
// The modules are those `go list -deps` finds for the given packages, target
// systems, architectures and build tags, so what is listed is what that build
// contains (not everything in go.mod). A module with no license file in its
// root stops the run: somebody has to read what it requires before a build goes
// out, which beats shipping it unmentioned.
//
// Run it from the module's directory. It needs no network when the modules are
// in the module cache. It does not support vendor mode (set GOFLAGS=-mod=mod).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// module is one dependency that ends up in the programs.
type module struct {
	Path, Version, Dir string
}

// licenseName matches the files in which a module states its terms, and the
// patent grants that sit beside them.
var licenseName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|patents|unlicense)([-_.].*)?$`)

// notText are the extensions of files that merely have a license-like name:
// a source file called license.go is not the license.
var notText = map[string]bool{
	".go": true, ".mod": true, ".sum": true, ".yaml": true, ".yml": true, ".json": true, ".toml": true,
	".sh": true, ".py": true, ".js": true, ".ts": true, ".html": true, ".css": true, ".c": true, ".h": true,
	".s": true, ".proto": true, ".gz": true, ".zip": true,
}

// licenseFiles returns the license files at the root of dir, by name.
func licenseFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && licenseName.MatchString(name) && !notText[strings.ToLower(filepath.Ext(name))] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// readLicenses returns the text of a module's license files, one after the
// other. The second result is false when it has none.
func readLicenses(dir string) (string, bool, error) {
	names, err := licenseFiles(dir)
	if err != nil || len(names) == 0 {
		return "", false, err
	}
	var parts []string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", false, err
		}
		parts = append(parts, strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")))
	}
	return strings.Join(parts, "\n\n"), true, nil
}

// entry is a license text and the software it covers.
type entry struct {
	Text   string
	Covers []string
}

// group puts together what shares a text, so the GPL is not printed once for
// each of the modules that carry it.
func group(covers map[string]string) []entry {
	byText := map[string]*entry{}
	for name, text := range covers {
		e := byText[text]
		if e == nil {
			e = &entry{Text: text}
			byText[text] = e
		}
		e.Covers = append(e.Covers, name)
	}
	out := make([]entry, 0, len(byText))
	for _, e := range byText {
		sort.Strings(e.Covers)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Covers[0] < out[j].Covers[0] })
	return out
}

// render writes the file's text.
func render(product string, entries []entry, notes []string) string {
	var b strings.Builder
	rule := strings.Repeat("=", 78)
	fmt.Fprintf(&b, "%s is built from the software below. Each part is distributed under its own\n", product)
	b.WriteString("license, which is reproduced here.\n")
	for _, n := range notes {
		b.WriteString("\n" + n + "\n")
	}
	for _, e := range entries {
		b.WriteString("\n" + rule + "\n")
		for _, c := range e.Covers {
			b.WriteString(c + "\n")
		}
		b.WriteString(rule + "\n\n")
		b.WriteString(e.Text + "\n")
	}
	return b.String()
}

// target is what one build is made for.
type target struct {
	goos, goarch, tags string
	cgo                bool
}

// goList lists the modules the programs are built from for one target.
func goList(t target, pkgs []string) ([]module, error) {
	args := []string{"list", "-deps", "-f", `{{if .Module}}{{.Module.Path}}{{"\t"}}{{.Module.Version}}{{"\t"}}{{.Module.Dir}}{{"\t"}}{{.Module.Main}}{{end}}`}
	if t.tags != "" {
		args = append(args, "-tags", t.tags)
	}
	args = append(args, pkgs...)
	cgo := "CGO_ENABLED=0"
	if t.cgo {
		cgo = "CGO_ENABLED=1"
	}
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS="+t.goos, "GOARCH="+t.goarch, cgo)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %v\n%s", err, stderr.String())
	}
	var mods []module
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 || f[3] == "true" { // the main module is the product itself
			continue
		}
		if f[2] == "" {
			return nil, fmt.Errorf("%s %s has no directory (vendor mode?); run with GOFLAGS=-mod=mod", f[0], f[1])
		}
		mods = append(mods, module{Path: f[0], Version: f[1], Dir: f[2]})
	}
	return mods, nil
}

// goEnv returns one of the go command's settings.
func goEnv(name string) (string, error) {
	out, err := exec.Command("go", "env", name).Output()
	return strings.TrimSpace(string(out)), err
}

// notes are free text added after the introduction: for what a build carries
// that is not a Go module (a driver, a font, a binary blob) and so is in no
// list the tool can read. The flag may be repeated.
type notes []string

func (n *notes) String() string     { return strings.Join(*n, "\n") }
func (n *notes) Set(s string) error { *n = append(*n, s); return nil }

type options struct {
	product string
	goos    string
	goarchs []string
	tags    string
	cgo     bool
	out     string
	pkgs    []string
	notes   []string
}

func main() {
	var (
		goos    = flag.String("goos", "", "target operating system (required)")
		goarch  = flag.String("goarch", "", "target architectures, separated by commas (required)")
		tags    = flag.String("tags", "", "build tags")
		cgo     = flag.Bool("cgo", false, "list as a build with cgo enabled (off by default)")
		out     = flag.String("out", "", "file to write (required)")
		product = flag.String("product", "", "name of the product (required)")
		extra   notes
	)
	flag.Var(&extra, "note", "text to add after the introduction (repeatable)")
	flag.Parse()
	pkgs := flag.Args()
	if len(pkgs) == 0 {
		pkgs = []string{"./cmd/..."}
	}
	if *goos == "" || *goarch == "" || *out == "" || *product == "" {
		fmt.Fprintln(os.Stderr, "usage: notices -product NAME -goos OS -goarch ARCH[,ARCH] [-tags TAGS] [-cgo] [-note TEXT]... -out FILE [packages]")
		os.Exit(2)
	}
	o := options{product: *product, goos: *goos, goarchs: strings.Split(*goarch, ","), tags: *tags, cgo: *cgo, out: *out, pkgs: pkgs, notes: extra}
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(o options) error {
	// One build may be made for several architectures; the file has to cover
	// all of them, so the lists are put together.
	byPath := map[string]module{}
	for _, arch := range o.goarchs {
		mods, err := goList(target{goos: o.goos, goarch: arch, tags: o.tags, cgo: o.cgo}, o.pkgs)
		if err != nil {
			return err
		}
		for _, m := range mods {
			byPath[m.Path] = m
		}
	}
	if len(byPath) == 0 {
		return errors.New("no third-party modules found; run it from the module's directory with the packages of the programs")
	}

	covers := map[string]string{}
	var missing []string
	for _, m := range byPath {
		text, ok, err := readLicenses(m.Dir)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, m.Path+" "+m.Version)
			continue
		}
		covers[m.Path+" "+m.Version] = text
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("no license file at the root of:\n  %s\nread what they require, then deal with them here (a -note, or a decision not to ship them)", strings.Join(missing, "\n  "))
	}

	root, err := goEnv("GOROOT")
	if err != nil {
		return err
	}
	version, err := goEnv("GOVERSION")
	if err != nil {
		return err
	}
	text, ok, err := readLicenses(root)
	if err != nil || !ok {
		return fmt.Errorf("the license of the Go standard library is not at %s (%v)", root, err)
	}
	covers["The Go standard library "+version] = text

	return os.WriteFile(o.out, []byte(render(o.product, group(covers), o.notes)), 0o644)
}
