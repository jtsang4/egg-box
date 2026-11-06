package gui

import (
	"fmt"
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

// The words of a text hold names in braces ("{name} added {count} items") that
// the code fills in when it asks for the text. TestCatalogsAgree sees that both
// languages name the same things; nothing else sees that the code that asks
// gives each of them a value, and a name nobody filled in reaches the screen as
// it is written, braces and all. This reads every place that asks for a text by
// its key and holds the names it fills in against the names in the text.

var braced = regexp.MustCompile(`\{([a-z_]+)\}`)

func bracedNames(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range braced.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

// ask is a place in the code that asks for a text with a key written out.
type ask struct {
	where string
	key   string
	forms []string // the endings the text may take: "" or ".one", ".other" or a platform's
	// given are the names the code fills in; implicit those that asking in
	// the plural fills in by itself.
	given, implicit map[string]bool
	// dynamic is set when the names that are filled in are not all written out.
	dynamic bool
}

// asks finds the places in the interface's code that ask for a text.
func asks(t *testing.T) []ask {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	fset := token.NewFileSet()
	var out []ask
	str := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "i18n_") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var keyAt, kvAt int
			var forms []string
			var implicit []string
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				switch fn.Sel.Name {
				case "t":
					keyAt, kvAt, forms = 0, 1, []string{""}
				case "tn":
					keyAt, kvAt, forms, implicit = 0, 2, []string{".one", ".other"}, []string{"n"}
				case "pk":
					keyAt, kvAt, forms = 0, 1, []string{".darwin", ".windows", ".other"}
				default:
					return true
				}
			case *ast.Ident:
				if fn.Name != "translate" {
					return true
				}
				keyAt, kvAt, forms = 1, 2, []string{""}
			default:
				return true
			}
			if len(call.Args) <= keyAt {
				return true
			}
			key, ok := str(call.Args[keyAt])
			if !ok || !keyShape.MatchString(key) {
				return true
			}
			a := ask{
				where: fmt.Sprintf("%s:%d", f, fset.Position(call.Pos()).Line),
				key:   key, forms: forms, given: map[string]bool{}, implicit: map[string]bool{},
			}
			for _, n := range implicit {
				a.implicit[n] = true
			}
			if len(call.Args) > kvAt {
				if call.Ellipsis != token.NoPos {
					a.dynamic = true
				}
				for i := kvAt; i < len(call.Args); i += 2 {
					name, ok := str(call.Args[i])
					if !ok {
						a.dynamic = true
						break
					}
					a.given[name] = true
				}
			}
			out = append(out, a)
			return true
		})
	}
	return out
}

func TestEveryNameInATextIsFilledWhereTheTextIsAsked(t *testing.T) {
	all := asks(t)
	if len(all) < 500 {
		t.Fatalf("found only %d places that ask for a text; has the way of asking changed?", len(all))
	}
	var withNames int
	for _, a := range all {
		if a.dynamic {
			continue
		}
		wanted := map[string]bool{}
		any := false
		for _, form := range a.forms {
			for lang, cat := range map[string]map[string]string{"en": catalogEN, "zh": catalogZH} {
				text, ok := cat[a.key+form]
				if !ok {
					continue
				}
				any = true
				for name := range bracedNames(text) {
					wanted[name] = true
					if !a.given[name] && !a.implicit[name] {
						t.Errorf("%s asks for %q but does not fill {%s} (%s: %q)", a.where, a.key+form, name, lang, text)
					}
				}
			}
		}
		if !any {
			t.Errorf("%s asks for %q, which no text answers", a.where, a.key)
		}
		if len(wanted) > 0 {
			withNames++
		}
		for name := range a.given {
			if !wanted[name] {
				t.Errorf("%s fills {%s} of %q, which no text of it has", a.where, name, a.key)
			}
		}
	}
	if withNames < 100 {
		t.Fatalf("only %d places fill names; has the check lost its reach?", withNames)
	}
}
