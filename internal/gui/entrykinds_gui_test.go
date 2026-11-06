package gui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// Every kind of entry a person can write is offered by the exception dialog
// under its name, with the hint and the example that belong to it, and what is
// kept is that kind: the list of the menu and the list of the kinds the
// engine takes are two lists, and only a person choosing from the menu shows
// that they run in step.
func TestExceptionDialogOffersEveryKindWithItsOwnHintAndExample(t *testing.T) {
	cases := []struct {
		kind routing.EntryKind
		typ  string // something of that kind that no exception has yet
	}{
		{routing.EntryDomainSuffix, "suffix.example.org"},
		{routing.EntryDomain, "www.exact.example.org"},
		{routing.EntryDomainKeyword, "kwzz"},
		{routing.EntryDomainRegex, `^zz[0-9]+\.example\.org$`},
		{routing.EntryIPCIDR, "203.0.113.0/24"},
		{routing.EntryPort, "8443"},
		{routing.EntryProcessName, "zzprog"},
		{routing.EntryProcessPath, "/opt/zz/bin/zzprog"},
	}
	if len(cases) != len(routing.EntryKinds) {
		t.Fatalf("this test tries %d kinds of entry and the plan has %d", len(cases), len(routing.EntryKinds))
	}
	h := exceptionsHarness(t)
	for at, c := range cases {
		kind := string(c.kind)
		h.click("Add")
		h.mustHave("Add an exception")
		// Back to the box of kinds. It opens on the first, which the dialog
		// starts with; the one wanted is as far down as the list of kinds says.
		h.ui.Key(ui.Shift, ui.KeyTab)
		h.ui.Key(0, ui.KeyEnter)
		h.ui.Advance(300 * time.Millisecond)
		for i := 0; i < at; i++ {
			h.ui.Key(0, ui.KeyDown)
		}
		h.ui.Key(0, ui.KeyEnter)
		h.ui.Advance(300 * time.Millisecond)
		h.settle()
		h.mustHave(catalogEN["entry."+kind], catalogEN["entry.hint."+kind], catalogEN["entry.example."+kind])
		for _, other := range routing.EntryKinds {
			if other != c.kind {
				h.mustNotHave(catalogEN["entry.hint."+string(other)])
			}
		}
		h.ui.Key(0, ui.KeyTab)
		h.typeText(c.typ)
		h.submitDialog("Add")
		h.mustNotHave("Add an exception")
		x, ok := h.exception(c.typ)
		if !ok || x.Kind != c.kind {
			t.Fatalf("asked for a %s %q; the plan keeps %+v (found %v)", kind, c.typ, x, ok)
		}
	}
}
