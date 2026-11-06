package gui

import (
	"context"
	"net/netip"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/routing"
)

// testDialog says where an address would go: the item or exception that
// claims it, the destination, the node it would use now. Next to the answer are
// the one-click ways to change it, which make an exception.
type testDialog struct {
	input  string
	tested string
	busy   bool
	res    *app.TestURLResult
	err    string
	scope  int
	cands  []app.Candidate
}

// openTestURL opens the test, running it at once when there is an address.
func (a *App) openTestURL(addr string) {
	d := &testDialog{input: addr}
	a.openDialog(d)
	if strings.TrimSpace(addr) != "" {
		d.run(a)
	}
}

func (d *testDialog) run(a *App) {
	raw := strings.TrimSpace(d.input)
	if raw == "" || d.busy {
		return
	}
	d.busy, d.err = true, ""
	var res *app.TestURLResult
	a.job("test-url", func(ctx context.Context) error {
		var err error
		res, err = a.be.TestURL(ctx, raw)
		return err
	}, func(err error) {
		d.busy = false
		if err != nil {
			d.res, d.err = nil, a.explain(err)
			return
		}
		d.res, d.tested, d.scope = res, raw, 0
		conn := app.Conn{Host: res.Host}
		if len(res.Addrs) > 0 && res.Host == res.Addrs[0].String() {
			conn.Destination = netip.AddrPortFrom(res.Addrs[0], uint16(res.Port)).String()
		}
		d.cands = app.ConnCandidates(conn)
		// The whole domain is the useful default; the exact name narrows it.
		for i, cd := range d.cands {
			if cd.Scope == app.ScopeDomain {
				d.scope = i
			}
		}
	})
}

func (d *testDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	return dlgFrame{
		title: a.t("testdlg.title"), subtitle: a.t("testdlg.sub"), icon: "compass", tone: toneBlue, width: 600,
		body: func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				in := a.input(c, "tu-in", &d.input, a.t("testdlg.placeholder"))
				in.Grow(1)
				if d.res == nil && !d.busy {
					in.AutoFocus()
				}
				if in.Submitted() {
					d.run(a)
				}
				if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("testdlg.go"), busy: d.busy, disabled: strings.TrimSpace(d.input) == ""}).Clicked() {
					d.run(a)
				}
			})
			if d.err != "" {
				a.errorNote(c, d.err)
			}
			if d.res == nil {
				if d.err == "" && !d.busy {
					a.txt(c, a.t("testdlg.help"), 12, 400, p.Text3).LineHeight(1.5)
				}
				return
			}
			d.answer(a, c)
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("common.done")}).Clicked() {
				a.dlgOpen = false
			}
		},
	}
}

// whyText words what decided, in the person's terms.
func (a *App) whyText(w app.Why) string {
	switch w.Kind {
	case app.WhyItem:
		name := w.Name
		if row, _, ok := a.rv().Row(w.ID); ok {
			name = a.itemName(row.Item)
		}
		return a.t("why.item", "name", name)
	case app.WhyException:
		return a.t("why.exception", "n", strconv.Itoa(w.Index))
	case app.WhyFallback:
		return a.t("why.fallback")
	case app.WhyLAN:
		return a.t("why.lan")
	case app.WhyMode:
		return a.t("why.mode")
	case app.WhyUnresolved:
		return a.t("why.unresolved")
	}
	return a.t("why.unknown")
}

// sourceText words which source of an item matched.
func (a *App) sourceText(s *app.TestSource) string {
	if s == nil {
		return ""
	}
	if s.Entry != nil {
		return a.t("testdlg.source.entry", "kind", a.entryKindLabel(s.Entry.Kind), "value", exceptionTitle(*s.Entry))
	}
	label := s.Name
	if label == "" {
		label = a.t("testdlg.source.file")
	}
	if s.Project != "" {
		label = s.Project + " · " + label
	}
	return a.t("testdlg.source", "source", label)
}

func (d *testDialog) answer(a *App, c *ui.Context) {
	p := a.pl
	res := d.res
	ui.Column(c).Gap(10).Padding(16).Radius(14).Background(p.Field).Border(1, p.Line).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			a.txt(c, d.tested, 12.5, 500, p.Text3).Font("monospace").SingleLine().Grow(1).Basis(0)
		})
		a.txt(c, a.whyText(res.Why), 16, 700, p.Text).LetterSpacing(-0.2).LineHeight(1.35)
		if s := a.sourceText(res.Source); s != "" {
			a.txt(c, s, 12.5, 400, p.Text2).LineHeight(1.45)
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon("arrow-right")).Size(16, 16).TextColor(p.Text3)
			a.chip(c, a.destLabel(res.Dest), destTone(res.Dest), destIcon(res.Dest))
		})
		if ex := res.Exit; ex != nil && ex.Node != nil {
			line := a.t("testdlg.now", "node", ex.Node.Label())
			if ex.DelayMS > 0 {
				line += " · " + fmtMillis(ex.DelayMS)
			}
			a.txt(c, line, 12.5, 500, p.Text2).LineHeight(1.45)
		} else if res.Dest.Kind != routing.DestDirect && res.Dest.Kind != routing.DestReject && !a.snap.Phase.Online() {
			a.txt(c, a.t("testdlg.offline"), 12, 400, p.Text3).LineHeight(1.45)
		}
		if res.Resolved {
			if res.ResolveErr != "" {
				a.txt(c, a.t("testdlg.resolve_failed"), 12, 400, p.Warn).LineHeight(1.45)
			} else {
				a.txt(c, a.t("testdlg.resolved", "addrs", addrList(res.Addrs)), 12, 400, p.Text3).LineHeight(1.45)
			}
		}
		if res.Skipped > 0 {
			a.txt(c, a.tn("testdlg.skipped", res.Skipped), 12, 400, p.Text3).LineHeight(1.45)
		}
		if res.Waiting > 0 {
			a.txt(c, a.tn("testdlg.waiting", res.Waiting), 12, 400, p.Warn).LineHeight(1.45)
		}
	})
	d.actions(a, c)
}

// addrList writes a few addresses.
func addrList(addrs []netip.Addr) string {
	var out []string
	for i, ad := range addrs {
		if i == 3 {
			out = append(out, "…")
			break
		}
		out = append(out, ad.String())
	}
	return strings.Join(out, ", ")
}

// actions are the ways to change the result: each makes an exception.
func (d *testDialog) actions(a *App, c *ui.Context) {
	p := a.pl
	if len(d.cands) == 0 {
		return
	}
	a.hr(c)
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		a.txt(c, a.t("testdlg.make"), 12.5, 600, p.Text2).SingleLine().Shrink(0)
		names := make([]string, len(d.cands))
		for i, cd := range d.cands {
			names[i] = a.scopeLabel(cd)
		}
		if d.scope >= len(d.cands) {
			d.scope = 0
		}
		ui.Box(c).Grow(1).Basis(0).Children(func() {
			a.dropdown(c, "tu-scope", names, d.scope, func(i int) { d.scope = i })
		})
	})
	cand := d.cands[d.scope]
	apply := func(dest routing.Dest) {
		e := cand.Entry
		a.editRouting(func(s *routing.State) error {
			_, err := s.UpsertException(e, dest)
			return err
		}, func(err error) {
			if err != nil {
				d.err = a.explain(err)
				return
			}
			a.rte.exOpen = true
			d.run(a)
		})
	}
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("testdlg.direct"), icon: "arrow-right"}).Clicked() {
			apply(routing.Direct())
		}
		if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("testdlg.follow"), icon: "route"}).Clicked() {
			apply(routing.Follow())
		}
		if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("testdlg.reject"), icon: "shield-off"}).Clicked() {
			apply(routing.Reject())
		}
		a.btn(c, btnSpec{kind: btnGhost, small: true, label: a.t("testdlg.more"), icon: "chevron-down"}).
			PopoverMenuButton(a.destMenu(destOpts{title: a.t("testdlg.title")}, d.res.Dest, apply))
	})
}

// scopeLabel words how wide an exception made from a candidate is.
func (a *App) scopeLabel(cd app.Candidate) string {
	switch cd.Scope {
	case app.ScopeHost:
		return a.t("scope.host", "value", cd.Entry.Value)
	case app.ScopeDomain:
		return a.t("scope.domain", "value", cd.Entry.Value)
	case app.ScopeIP:
		return a.t("scope.ip", "value", cd.Entry.Value)
	case app.ScopeProcess:
		return a.t("scope.process", "value", app.ProcessName(cd.Entry.Value))
	}
	return cd.Entry.Value
}
