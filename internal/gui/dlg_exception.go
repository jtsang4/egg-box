package gui

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/routing"
)

// ---------------------------------------------------------- one exception

// exceptionDialog adds or edits one exception: what it matches and where it goes.
type exceptionDialog struct {
	id    string // "" when adding
	on    bool
	kind  int
	value string
	dest  routing.Dest
	err   string
	busy  bool
}

// openException opens the dialog to add an exception, or to edit x.
func (a *App) openException(x *routing.Exception) {
	d := &exceptionDialog{on: true, dest: routing.Follow()}
	if x != nil {
		d.id, d.on, d.kind, d.value, d.dest = x.ID, x.Enabled, entryKindIndex(x.Kind), x.Value, x.Dest.Clone()
	}
	a.openDialog(d)
}

// openExceptionFor opens the dialog to add an exception that is already
// filled in, from a connection or from the result of a test.
func (a *App) openExceptionFor(e routing.Entry, dest routing.Dest) {
	a.openDialog(&exceptionDialog{on: true, kind: entryKindIndex(e.Kind), value: e.Value, dest: dest})
}

func (d *exceptionDialog) frame(a *App, c *ui.Context) dlgFrame {
	adding := d.id == ""
	title, action := a.t("exdlg.add.title"), a.t("common.add")
	if !adding {
		title, action = a.t("exdlg.edit.title"), a.t("common.save")
	}
	kind := entryKinds[d.kind]
	return dlgFrame{
		title: title, subtitle: a.t("exdlg.sub"), icon: "pin", tone: toneBlue, width: 500,
		body: func() {
			a.field(c, a.t("exdlg.kind"), "", func() {
				names := make([]string, len(entryKinds))
				for i, k := range entryKinds {
					names[i] = a.entryKindLabel(k)
				}
				a.dropdown(c, "ex-kind", names, d.kind, func(i int) { d.kind, d.err = i, "" })
			})
			a.field(c, a.t("exdlg.value"), a.entryHint(kind), func() {
				in := a.input(c, "ex-value", &d.value, a.entryExample(kind))
				in.Font("monospace")
				in.AutoFocus()
				if in.Changed() {
					d.err = ""
				}
				if in.Submitted() {
					d.submit(a)
				}
			})
			a.field(c, a.t("exdlg.dest"), a.t("exdlg.dest.hint"), func() {
				ui.Row(c).Children(func() {
					a.destButton(c, destOpts{key: "ex-dest", width: 260, title: a.t("exdlg.dest.for", "value", exceptionTitle(routing.Entry{Kind: kind, Value: strings.TrimSpace(d.value)}))},
						d.dest, func(x routing.Dest) { d.dest = x })
				})
			})
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: action, busy: d.busy, disabled: strings.TrimSpace(d.value) == ""}).Clicked() {
				d.submit(a)
			}
		},
	}
}

func (d *exceptionDialog) submit(a *App) {
	if d.busy {
		return
	}
	entry := routing.Entry{Kind: entryKinds[d.kind], Value: strings.TrimSpace(d.value)}
	if err := entry.Valid(); err != nil {
		d.err = a.explain(err)
		return
	}
	d.busy, d.err = true, ""
	id, on, dest := d.id, d.on, d.dest.Clone()
	a.editRouting(func(s *routing.State) error {
		if id == "" {
			_, err := s.UpsertException(entry, dest)
			return err
		}
		return s.UpdateException(id, routing.Exception{Kind: entry.Kind, Value: entry.Value, Dest: dest, Enabled: on})
	}, func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		if cur, ok := a.dlg.(*exceptionDialog); ok && cur == d {
			a.dlgOpen = false
		}
	})
}

// ------------------------------------------------------------ pasted lists

// batchEditor is a box for a pasted list and what was read from it.
type batchEditor struct {
	text string
	seen string
	read bool
	res  routing.BatchResult
}

// result reads the text, once per change.
func (b *batchEditor) result() *routing.BatchResult {
	if !b.read || b.seen != b.text {
		b.seen, b.read = b.text, true
		b.res = routing.ParseBatch(b.text)
	}
	return &b.res
}

// batchPreview shows what a pasted list came to, before anything is added:
// how many lines were read, of which kinds, and which were not understood.
func (a *App) batchPreview(c *ui.Context, b *batchEditor) {
	p := a.pl
	if strings.TrimSpace(b.text) == "" {
		a.txt(c, a.t("batch.help"), 12, 400, p.Text3).LineHeight(1.5)
		return
	}
	res := b.result()
	ui.Column(c).Gap(10).Padding(12, 14).Radius(12).Background(p.Field).Border(1, p.Line).Children(func() {
		n := len(res.Lines)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ic, t := "check-circle", toneGood
			if n == 0 {
				ic, t = "alert-circle", toneWarn
			}
			ui.Icon(c, icon(ic)).Size(16, 16).TextColor(a.toneColor(t))
			a.txt(c, boolOr(n > 0, a.tn("batch.recognized", n), a.t("batch.none")), 13, 650, p.Text).Grow(1).Basis(0)
		})
		if n > 0 {
			a.kindChips(c, res.Counts)
		}
		if bad := res.Unrecognized; len(bad) > 0 {
			a.hr(c)
			a.txt(c, a.tn("batch.unrecognized", len(bad)), 12.5, 600, p.Warn)
			a.badLines(c, bad)
		}
	})
}

// kindChips says how many entries there are of each kind.
func (a *App) kindChips(c *ui.Context, counts map[routing.EntryKind]int) {
	ui.Row(c).Gap(6).Wrap().Children(func() {
		for _, k := range routing.EntryKinds {
			if cnt := counts[k]; cnt > 0 {
				a.chip(c, a.entryKindLabel(k)+"  "+strconv.Itoa(cnt), toneNeutral, "")
			}
		}
	})
}

// badLines lists the first few lines of a list that cannot be used, each
// with why.
func (a *App) badLines(c *ui.Context, bad []routing.BatchBad) {
	p := a.pl
	for i, l := range bad {
		if i == 6 {
			a.txt(c, a.tn("batch.more", len(bad)-6), 12, 400, p.Text3)
			break
		}
		ui.Row(c).Gap(8).Children(func() {
			a.txt(c, a.t("batch.line", "n", strconv.Itoa(l.Line)), 11.5, 600, p.Text3).FontFeatures("tnum").SingleLine().Shrink(0)
			ui.Column(c).Grow(1).Basis(0).Gap(1).Children(func() {
				a.txt(c, l.Text, 12, 450, p.Text).Font("monospace").SingleLine()
				a.txt(c, a.batchReason(l), 11.5, 400, p.Text3).LineHeight(1.35)
			})
		})
	}
}

// batchReason says in the person's language why a line cannot be used.
func (a *App) batchReason(l routing.BatchBad) string {
	switch l.Code {
	case routing.BadType:
		return a.t("batch.bad.type", "type", l.Arg)
	case routing.BadDuplicate:
		return a.t("exedit.bad.duplicate", "n", l.Arg)
	case routing.BadTarget:
		return a.t("exedit.bad.target", "target", l.Arg)
	case routing.BadAmbiguous:
		if len(l.Choices) == 0 {
			return a.t("exedit.bad.ambiguous_none", "name", l.Arg)
		}
		return a.t("exedit.bad.ambiguous", "name", l.Arg, "choices", strings.Join(l.Choices, a.t("exedit.or")))
	case routing.BadNoNode:
		return a.t("exedit.bad.no_node", "name", l.Arg)
	case routing.BadRegion:
		return a.t("exedit.bad.region", "code", l.Arg)
	}
	return a.t("batch.bad.generic")
}

// ------------------------------------------------- the exceptions as text

// exceptionsEditor edits all the exceptions at once, as text: one per line,
// in order, each with its destination. Saving replaces the list.
type exceptionsEditor struct {
	doc  routing.ExceptionsText
	text string
	// The text is read once per change of it, of the nodes it can name or of
	// the plan in force.
	seen    string
	seenVer [2]uint64
	read    bool
	draft   routing.ExceptionsDraft
	busy    bool
	err     string
}

// openExceptionsEditor opens the exceptions as text.
func (a *App) openExceptionsEditor() {
	doc := routing.WriteExceptions(a.rt().Exceptions, a.exceptionNames())
	a.openDialog(&exceptionsEditor{doc: doc, text: doc.Text})
}

// exceptionNames is what the text of the exceptions can name.
func (a *App) exceptionNames() routing.Names {
	nodes := a.liveNodes()
	ns := routing.Names{Nodes: make([]routing.NamedNode, len(nodes)), Rows: a.rv().Rows}
	regions := map[string]bool{}
	for i, n := range nodes {
		ns.Nodes[i] = routing.NamedNode{ID: n.ID, SubName: n.SubName, SubAlias: n.SubAlias}
		regions[n.Region] = true
	}
	ns.Region = func(code string) bool {
		_, known := regionNames[code]
		return known || regions[code]
	}
	ns.Subs = make(map[string]string, len(a.subs))
	for _, s := range a.subs {
		ns.Subs[s.ID] = boolOr(s.Alias != "", s.Alias, s.Name)
	}
	return ns
}

// exceptionRule is an exception as a line of the text.
func (a *App) exceptionRule(x routing.Exception) string {
	return routing.ExceptionLine(x, routing.FormatTarget(x.Dest, a.exceptionNames()))
}

func (d *exceptionsEditor) result(a *App) *routing.ExceptionsDraft {
	a.refreshNodes()
	ver := [2]uint64{a.nodesVer, a.rtc.ver}
	if !d.read || d.seen != d.text || d.seenVer != ver {
		if d.read && d.seen != d.text {
			d.err = ""
		}
		d.seen, d.seenVer, d.read = d.text, ver, true
		d.draft = d.doc.Read(d.text, a.exceptionNames())
	}
	return &d.draft
}

func (d *exceptionsEditor) frame(a *App, c *ui.Context) dlgFrame {
	dr := d.result(a)
	return dlgFrame{
		title: a.t("exedit.title"), subtitle: a.t("exedit.sub"), icon: "file-text", tone: toneBlue, width: 640,
		body: func() {
			area := a.area(c, "exed-text", &d.text, a.t("exedit.placeholder"), 8, 14)
			area.AutoFocus()
			a.txt(c, a.t("exedit.help"), 12, 400, a.pl.Text3).LineHeight(1.5)
			a.exceptionsDraftView(c, d, dr)
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("common.save"), busy: d.busy, disabled: !dr.Ready()}).Clicked() {
				d.submit(a)
			}
		},
	}
}

// exceptionsDraftView shows what the text comes to: what changes, what each
// destination stands for, what goes and what is wrong.
func (a *App) exceptionsDraftView(c *ui.Context, d *exceptionsEditor, dr *routing.ExceptionsDraft) {
	p := a.pl
	ui.Column(c).Gap(10).Padding(12, 14).Radius(12).Background(p.Field).Border(1, p.Line).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ic, t, msg := "check-circle", toneGood, ""
			switch {
			case len(dr.Problems) > 0:
				ic, t, msg = "alert-circle", toneWarn, a.tn("exedit.problems", len(dr.Problems))
			case dr.TooMany:
				ic, t, msg = "alert-circle", toneWarn, a.t("err.too_many_exceptions")
			case !dr.Changes():
				ic, t, msg = "info", toneNeutral, a.t("exedit.unchanged")
			default:
				msg = a.exceptionsChanges(dr)
				if len(dr.Removed) > 0 {
					t = toneWarn
				}
			}
			ui.Icon(c, icon(ic)).Size(16, 16).TextColor(a.toneColor(t))
			a.txt(c, msg, 13, 650, p.Text).Grow(1).Basis(0).LineHeight(1.35)
		})
		if bad := dr.Problems; len(bad) > 0 {
			a.badLines(c, bad)
			strip := map[int]bool{}
			for _, l := range bad {
				if l.Code == routing.BadTarget {
					strip[l.Line] = true
				}
			}
			if len(strip) > 0 {
				ui.Row(c).Children(func() {
					if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("exedit.strip")}).Clicked() {
						d.text = routing.StripTargets(d.text, strip)
					}
				})
			}
		}
		if gone := dr.Removed; len(gone) > 0 {
			if len(dr.Problems) > 0 {
				a.hr(c)
			}
			a.txt(c, a.t("exedit.removing"), 12.5, 600, p.Warn)
			for i, x := range gone {
				if i == 6 {
					a.txt(c, a.tn("batch.more", len(gone)-6), 12, 400, p.Text3)
					break
				}
				a.txt(c, a.exceptionRule(x), 12, 450, p.Text2).Font("monospace").SingleLine()
			}
		}
		if len(dr.Problems) > 0 || len(dr.Removed) > 0 {
			a.hr(c)
		}
		if n := len(dr.List); n > 0 {
			a.txt(c, a.tn("exedit.count", n), 12, 500, p.Text2)
			a.kindChips(c, dr.Counts)
		} else {
			a.txt(c, a.t("exedit.none"), 12, 500, p.Text2)
		}
		if ts := dr.Targets; len(ts) > 0 {
			ui.Column(c).Gap(3).Children(func() {
				for i, m := range ts {
					if i == 8 {
						a.txt(c, a.tn("batch.more", len(ts)-8), 12, 400, p.Text3)
						break
					}
					a.txt(c, "“"+m.Target+"”  →  "+a.destLabel(m.Dest), 12, 450, p.Text2).SingleLine()
				}
			})
		}
	})
}

// exceptionsChanges sums up what saving the text changes.
func (a *App) exceptionsChanges(dr *routing.ExceptionsDraft) string {
	var parts []string
	if dr.Added > 0 {
		parts = append(parts, a.t("exedit.added", "n", strconv.Itoa(dr.Added)))
	}
	if n := len(dr.Removed); n > 0 {
		parts = append(parts, a.t("exedit.removed", "n", strconv.Itoa(n)))
	}
	if dr.Changed > 0 {
		parts = append(parts, a.t("exedit.changed", "n", strconv.Itoa(dr.Changed)))
	}
	if dr.Reordered {
		parts = append(parts, a.t("exedit.reordered"))
	}
	return strings.Join(parts, " · ")
}

func (d *exceptionsEditor) submit(a *App) {
	dr := d.result(a)
	if d.busy || !dr.Ready() {
		return
	}
	d.busy, d.err = true, ""
	base, list, sent := d.doc.Base, dr.List, d.text
	var after []routing.Exception
	a.editRouting(func(s *routing.State) error {
		err := s.ReplaceExceptions(base, list)
		if err == nil || errors.Is(err, routing.ErrExceptionsChanged) {
			after = slices.Clone(s.Exceptions)
			for i := range after {
				after[i].Dest = after[i].Dest.Clone()
			}
		}
		return err
	}, func(err error) {
		d.busy = false
		switch {
		case errors.Is(err, routing.ErrExceptionsChanged):
			// Carry the text over to the list as it is now: what the person
			// changed stays, the rest takes the latest form.
			d.doc, d.text = d.doc.Rebase(d.text, after, a.exceptionNames())
			d.read = false
			d.result(a)
			d.err = a.t("exedit.rebased")
			return
		case err != nil:
			d.err = a.explain(err)
			return
		}
		a.rte.exOpen = true
		a.toastInfo(a.t("exedit.done"))
		if d.text != sent {
			// Written to while it was being saved: what is new stays to be saved.
			d.doc = routing.WriteExceptions(after, a.exceptionNames())
			d.read = false
			return
		}
		if cur, ok := a.dlg.(*exceptionsEditor); ok && cur == d {
			a.dlgOpen = false
		}
	})
}

// ----------------------------------------------- an exception from a connection

// openThenGo is the choice "from now on, go ...", made on a connection: how
// wide the exception is and where it goes. It adds the exception at once.
func (a *App) exceptionFromConn(cn app.Conn, cand app.Candidate, dest routing.Dest) {
	e := cand.Entry
	a.editRouting(func(s *routing.State) error {
		_, err := s.UpsertException(e, dest)
		return err
	}, func(err error) {
		if err != nil {
			a.fail(err)
			return
		}
		a.toastInfo(a.t("exception.made", "what", exceptionTitle(e), "dest", a.destLabel(dest)))
	})
}
