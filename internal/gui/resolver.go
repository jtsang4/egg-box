package gui

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/dnsspec"
	"eggbox/internal/routing"
)

// The ways a rule's names can be looked up, in the order the control offers
// them.
const (
	resolverDefault = iota
	resolverServer
	resolverExit
)

// resolverEdit is what a resolver control shows: the way chosen and the
// server being typed. It shows the resolver in force when that changes (an
// edit landing, or a change made elsewhere), not in between: an edit is
// applied in the background, and the control keeps the person's choice
// meanwhile.
type resolverEdit struct {
	init bool
	seen string // the resolver in force when the control last looked
	mode int
	// server is what is typed for a server.
	server string
	// touched is set once the person changed the control.
	touched bool
	err     string
}

func resolverModeOf(r string) int {
	switch r {
	case "":
		return resolverDefault
	case dnsspec.Exit:
		return resolverExit
	}
	return resolverServer
}

// sync shows r when it is not what the control last saw.
func (e *resolverEdit) sync(r string) {
	if e.init && r == e.seen {
		return
	}
	e.init, e.seen = true, r
	e.mode = resolverModeOf(r)
	e.server = ""
	if e.mode == resolverServer {
		e.server = r
	}
}

// reset makes the control show the resolver in force again, after an edit
// that failed.
func (e *resolverEdit) reset() { e.init = false }

// value is the resolver the control says now.
func (e *resolverEdit) value() string {
	switch e.mode {
	case resolverServer:
		return strings.TrimSpace(e.server)
	case resolverExit:
		return dnsspec.Exit
	}
	return ""
}

// resolverLabel names a resolver in the person's words.
func (a *App) resolverLabel(r string) string {
	switch r {
	case "":
		return a.t("resolver.default")
	case dnsspec.Exit:
		return a.t("resolver.exit")
	case dnsspec.System:
		return a.t("resolver.system")
	}
	return a.t("resolver.uses", "server", r)
}

// resolverControl lets the person choose how a rule's names are looked up.
// With apply, a choice takes effect at once (a server when it is saved);
// without it the dialog reads e.value() when it is submitted. cur is the
// resolver in force.
func (a *App) resolverControl(c *ui.Context, key string, e *resolverEdit, cur string, apply func(string)) {
	p := a.pl
	if apply != nil {
		e.sync(cur)
	}
	save := func() {
		v := e.value()
		if err := routing.ValidResolver(v); err != nil || v == "" {
			e.err = a.t("err.resolver_invalid")
			return
		}
		e.err = ""
		if apply != nil && routing.NormalizeResolver(v) != cur {
			apply(v)
		}
	}
	ui.Column(c.Key(key)).Gap(6).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(196).Shrink(0).Children(func() {
				modes := []string{a.t("resolver.default"), a.t("resolver.server"), a.t("resolver.exit")}
				a.dropdown(c, key+"-mode", modes, e.mode, func(i int) {
					e.mode, e.err, e.touched = i, "", true
					if apply == nil || i == resolverServer {
						return
					}
					if v := e.value(); v != cur {
						apply(v)
					}
				})
			})
			if e.mode == resolverServer {
				in := a.input(c, key+"-server", &e.server, a.t("resolver.placeholder"))
				in.Font("monospace").Grow(1).Basis(0)
				if in.Changed() {
					e.err, e.touched = "", true
				}
				if apply != nil {
					if in.Submitted() {
						save()
					}
					v := strings.TrimSpace(e.server)
					if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("common.save"), disabled: v == "" || routing.NormalizeResolver(v) == cur}).Clicked() {
						save()
					}
				}
			}
		})
		hint := []string{"resolver.hint.default", "resolver.hint.server", "resolver.hint.exit"}[e.mode]
		a.txt(c, a.t(hint), 12, 400, p.Text3).LineHeight(1.45)
		if e.err != "" {
			a.errorNote(c, e.err)
		}
	})
}

// shadowNote says which hand-written entries of an item exceptions already
// decide, destination and lookup alike.
func (a *App) shadowNote(c *ui.Context, shadows []routing.Shadow) {
	if len(shadows) == 0 {
		return
	}
	p := a.pl
	ui.Column(c.Key("id-shadow")).Gap(6).Padding(10, 12).Radius(12).Background(p.Field).Border(1, p.Line).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Icon(c, icon("info")).Size(14, 14).TextColor(p.Warn).Shrink(0)
			a.txt(c, a.tn("itemdlg.shadow", len(shadows)), 12.5, 500, p.Text2).Grow(1).Basis(0).LineHeight(1.5)
		})
		for i, s := range shadows {
			if i == 5 {
				a.txt(c, a.tn("list.more", len(shadows)-5), 12, 400, p.Text3)
				break
			}
			a.txt(c, a.t("itemdlg.shadow.line", "entry", routing.EntryLine(s.Entry), "n", strconv.Itoa(s.Index)), 12, 450, p.Text2).Font("monospace").SingleLine()
		}
	})
}

// asksNames reports whether an item may match host names, so that how they
// are looked up matters. Once the item is compiled its rules say; before, an
// item made only of hand-written addresses, ports or programs looks nothing
// up, and one with a rule file may.
func (a *App) asksNames(it routing.Item) bool {
	if st, ok := a.itemStatus(it.ID); ok && (st.State == app.ItemReady || st.State == app.ItemPartial) {
		return st.Host > 0
	}
	return asksNames(it)
}

func asksNames(it routing.Item) bool {
	for _, src := range it.Sources {
		if src.Kind != routing.SourceInline {
			return true
		}
		for _, e := range src.Entries {
			if e.Kind.IsHost() {
				return true
			}
		}
	}
	return false
}
