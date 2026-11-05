// Command notices writes the notices that travel with a build: the license of
// every module compiled into the programs, and of the Go standard library.
// Nearly all of those licenses require whoever hands the program on to keep
// them with it, so the installers carry the file this writes.
//
//	go run ./scripts/notices -goos windows -goarch amd64 -tags "$TAGS" -out FILE
//
// A module with no license file in its root stops the run: somebody has to
// look at it before a build goes out, which beats shipping it unmentioned.
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
	Text    string
	Covers  []string
	Missing bool
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

// goList lists the modules the programs are built from for one target.
func goList(goos, goarch, tags string, pkgs []string) ([]module, error) {
	args := []string{"list", "-deps", "-f", `{{if .Module}}{{.Module.Path}}{{"\t"}}{{.Module.Version}}{{"\t"}}{{.Module.Dir}}{{"\t"}}{{.Module.Main}}{{end}}`}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, pkgs...)
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %v\n%s", err, stderr.String())
	}
	var mods []module
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 || f[3] == "true" {
			continue
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

// wintunNote says which Wintun the Windows programs carry. Its license is not
// in the module that embeds it, so the note names it and where the terms are.
func wintunNote(mods []module) string {
	for _, m := range mods {
		if m.Path != "github.com/sagernet/sing-tun" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.Dir, "internal", "wintun", "README.md"))
		if err != nil {
			return ""
		}
		version := regexp.MustCompile(`DLL version:\s*(\S+)`).FindSubmatch(b)
		if version == nil {
			return ""
		}
		return fmt.Sprintf("The Windows programs also carry Wintun %s (wintun.dll), a network adapter driver by WireGuard LLC\n"+
			"used unmodified under the terms of its prebuilt-binaries license: https://www.wintun.net/", version[1])
	}
	return ""
}

func main() {
	var (
		goos    = flag.String("goos", "", "target operating system (darwin or windows)")
		goarch  = flag.String("goarch", "", "target architectures, separated by commas")
		tags    = flag.String("tags", "", "build tags")
		out     = flag.String("out", "", "file to write")
		product = flag.String("product", "Sing-Box UI", "name of the product")
	)
	flag.Parse()
	pkgs := flag.Args()
	if len(pkgs) == 0 {
		pkgs = []string{"./cmd/agentd", "./cmd/sing-box-ui"}
	}
	if *goos == "" || *goarch == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: notices -goos OS -goarch ARCH[,ARCH] [-tags TAGS] -out FILE [packages]")
		os.Exit(2)
	}
	if err := run(*product, *goos, strings.Split(*goarch, ","), *tags, *out, pkgs); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(product, goos string, goarchs []string, tags, out string, pkgs []string) error {
	byPath := map[string]module{}
	for _, arch := range goarchs {
		mods, err := goList(goos, arch, tags, pkgs)
		if err != nil {
			return err
		}
		for _, m := range mods {
			byPath[m.Path] = m
		}
	}
	if len(byPath) == 0 {
		return errors.New("no modules found; run it from the repository")
	}
	all := make([]module, 0, len(byPath))
	for _, m := range byPath {
		all = append(all, m)
	}

	covers := map[string]string{}
	var missing []string
	for _, m := range all {
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
		return fmt.Errorf("no license file at the root of:\n  %s\nlook at what they require, then name them in this program", strings.Join(missing, "\n  "))
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

	var notes []string
	if goos == "windows" {
		if n := wintunNote(all); n != "" {
			notes = append(notes, n)
		}
	}
	return os.WriteFile(out, []byte(render(product, group(covers), notes)), 0o644)
}
