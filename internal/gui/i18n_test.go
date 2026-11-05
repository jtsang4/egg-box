package gui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var placeholder = regexp.MustCompile(`\{[a-z_]+\}`)

func placeholders(s string) string {
	found := placeholder.FindAllString(s, -1)
	sort.Strings(found)
	return strings.Join(found, ",")
}

// TestCatalogsAgree: both languages say the same things.
func TestCatalogsAgree(t *testing.T) {
	for k, en := range catalogEN {
		zh, ok := catalogZH[k]
		if !ok {
			t.Errorf("zh is missing %q", k)
			continue
		}
		if placeholders(en) != placeholders(zh) {
			t.Errorf("%q: placeholders differ: en %q zh %q", k, placeholders(en), placeholders(zh))
		}
		if strings.TrimSpace(en) == "" || strings.TrimSpace(zh) == "" {
			t.Errorf("%q is empty", k)
		}
	}
	for k := range catalogZH {
		if _, ok := catalogEN[k]; !ok {
			t.Errorf("en is missing %q", k)
		}
	}
}

// TestPluralsComplete: a counted phrase has both forms.
func TestPluralsComplete(t *testing.T) {
	for _, cat := range []map[string]string{catalogEN, catalogZH} {
		for k := range cat {
			switch {
			case strings.HasSuffix(k, ".one"):
				if _, ok := cat[strings.TrimSuffix(k, ".one")+".other"]; !ok {
					t.Errorf("%q has no .other", k)
				}
			case strings.HasSuffix(k, ".other"):
				base := strings.TrimSuffix(k, ".other")
				// .other doubles as the fallback of platform text.
				if _, ok := cat[base+".one"]; !ok && !strings.Contains(base, "service") && !strings.Contains(base, "boot") {
					if !platformBase(cat, base) {
						t.Errorf("%q has no .one", k)
					}
				}
			}
		}
	}
}

// platformBase: key.other next to key.darwin or key.windows is text per platform.
func platformBase(cat map[string]string, base string) bool {
	_, d := cat[base+".darwin"]
	_, w := cat[base+".windows"]
	return d || w
}

var keyShape = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// sourceLiterals returns the string literals of the package's own code.
func sourceLiterals(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "i18n_") && f != "i18n.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out[s] = true
				}
			}
			return true
		})
	}
	return out
}

// shellLiterals reads the program that hosts the interface, which words its
// menus and tray through Say. It returns every string literal (so a text it
// uses is not an orphan) and the literals it passes to say (which must exist).
func shellLiterals(t *testing.T) (all, said map[string]bool) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "cmd", "egg-box", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	all, said = map[string]bool{}, map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil {
						all[s] = true
					}
				}
			case *ast.CallExpr:
				id, ok := n.Fun.(*ast.Ident)
				if !ok || id.Name != "say" || len(n.Args) == 0 {
					return true
				}
				if lit, ok := n.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						said[s] = true
					}
				}
			}
			return true
		})
	}
	return all, said
}

// dynamicPrefixes are the keys built at run time from a known stem.
var dynamicPrefixes = []string{
	"notice.", "err.", "phase.", "entry.", "fmt.", "welcome.f", "welcome.sub.", "nav.", "time.",
	"mode.", "sort.", "region.", "tray.", "empty.", "hero.hint.", "icon.",
}

func isDynamic(k string) bool {
	for _, p := range dynamicPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// TestKeysExist: every key the code names is in the catalog.
func TestKeysExist(t *testing.T) {
	for lit := range sourceLiterals(t) {
		if !keyShape.MatchString(lit) || ignoredLiteral(lit) {
			continue
		}
		if known(lit) {
			continue
		}
		t.Errorf("the code uses the key %q, which has no text", lit)
	}
}

// TestShellKeysExist: every text the program's menus and tray ask for exists.
func TestShellKeysExist(t *testing.T) {
	all, said := shellLiterals(t)
	if len(said) == 0 {
		t.Fatal("found no say(...) calls in cmd/egg-box; has the shell moved?")
	}
	for lit := range said {
		if !known(lit) {
			t.Errorf("the program asks for the text %q, which does not exist", lit)
		}
	}
	// The navigation texts are named through a table; each page it lists
	// must have its text.
	for lit := range all {
		if strings.HasPrefix(lit, "nav.") && !known(lit) {
			t.Errorf("the program names the page text %q, which does not exist", lit)
		}
	}
}

// ignoredLiteral are dotted strings that are not keys.
func ignoredLiteral(s string) bool {
	switch {
	case strings.HasPrefix(s, "me."), strings.HasPrefix(s, "eggbox."), strings.HasPrefix(s, "example."), s == "clash.meta":
		return true
	case strings.HasSuffix(s, ".log"), strings.HasSuffix(s, ".json"), strings.HasSuffix(s, ".txt"), strings.HasSuffix(s, ".net"):
		return true
	}
	return false
}

// known reports whether a literal is a key, a stem of keys, or a stem with a
// platform or plural suffix.
func known(lit string) bool {
	if _, ok := catalogEN[lit]; ok {
		return true
	}
	for _, suf := range []string{".one", ".other", ".darwin", ".windows"} {
		if _, ok := catalogEN[lit+suf]; ok {
			return true
		}
	}
	for k := range catalogEN {
		if strings.HasPrefix(k, lit+".") {
			return true
		}
	}
	return false
}

// TestNoOrphanKeys: no text is left behind unused.
func TestNoOrphanKeys(t *testing.T) {
	lits := sourceLiterals(t)
	shell, _ := shellLiterals(t)
	for l := range shell {
		lits[l] = true
	}
	for k := range catalogEN {
		if isDynamic(k) {
			continue
		}
		stem := k
		for _, suf := range []string{".one", ".other", ".darwin", ".windows"} {
			stem = strings.TrimSuffix(stem, suf)
		}
		if lits[k] || lits[stem] {
			continue
		}
		// A key built from a stem and a suffix: key+".title".
		used := false
		for l := range lits {
			if keyShape.MatchString(l) && strings.HasPrefix(stem, l+".") {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("the text %q is never used", k)
		}
	}
}
