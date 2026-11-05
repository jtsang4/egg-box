package gui

import (
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

// batchBox is the text area a list is pasted into.
func (a *App) batchBox(c *ui.Context, key string, b *batchEditor) ui.Element {
	area := a.area(c, key, &b.text, a.t("batch.placeholder"), 6, 10)
	return area
}

// batchPreview shows what a pasted list came to, before anything is added:
// how many lines were read, of which kinds, which were not understood and, for
// lists whose lines name a destination, what each name stands for.
func (a *App) batchPreview(c *ui.Context, b *batchEditor, rows []routing.Row, targets bool) {
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
			ui.Row(c).Gap(6).Wrap().Children(func() {
				for _, k := range routing.EntryKinds {
					if cnt := res.Counts[k]; cnt > 0 {
						a.chip(c, a.entryKindLabel(k)+"  "+strconv.Itoa(cnt), toneNeutral, "")
					}
				}
			})
		}
		if targets {
			for _, name := range res.Targets() {
				dest, ok := routing.ResolveTarget(name, rows)
				var meaning string
				if ok {
					meaning = a.destLabel(dest)
				} else {
					meaning = a.t("batch.target.chosen")
				}
				a.txt(c, "“"+name+"”  →  "+meaning, 12, 450, p.Text2).SingleLine()
			}
		}
		if bad := res.Unrecognized; len(bad) > 0 {
			a.hr(c)
			a.txt(c, a.tn("batch.unrecognized", len(bad)), 12.5, 600, p.Warn)
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
	})
}

// batchReason says in the person's language why a line was not understood.
func (a *App) batchReason(l routing.BatchBad) string {
	if typ, ok := strings.CutSuffix(l.Reason, " rules are not supported here"); ok {
		return a.t("batch.bad.type", "type", typ)
	}
	return a.t("batch.bad.generic")
}

// ----------------------------------------------------------- many exceptions

// batchDialog adds many exceptions from a pasted list.
type batchDialog struct {
	ed   batchEditor
	dest routing.Dest
	busy bool
	err  string
}

func (a *App) openBatchException(text string) {
	a.openDialog(&batchDialog{ed: batchEditor{text: text}, dest: routing.Follow()})
}

func (d *batchDialog) frame(a *App, c *ui.Context) dlgFrame {
	res := d.ed.result()
	n := len(res.Lines)
	rows := a.rv().Rows
	return dlgFrame{
		title: a.t("batchdlg.title"), subtitle: a.t("batchdlg.sub"), icon: "pin", tone: toneBlue, width: 560,
		body: func() {
			a.batchBox(c, "bx-text", &d.ed).AutoFocus()
			a.batchPreview(c, &d.ed, rows, true)
			a.field(c, a.t("batchdlg.dest"), a.t("batchdlg.dest.hint"), func() {
				ui.Row(c).Children(func() {
					a.destButton(c, destOpts{key: "bx-dest", width: 260, title: a.t("batchdlg.title")}, d.dest, func(x routing.Dest) { d.dest = x })
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
			label := a.t("common.add")
			if n > 0 {
				label = a.tn("batchdlg.add", n)
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: label, busy: d.busy, disabled: n == 0}).Clicked() {
				d.submit(a)
			}
		},
	}
}

func (d *batchDialog) submit(a *App) {
	if d.busy {
		return
	}
	lines := append([]routing.BatchLine(nil), d.ed.result().Lines...)
	if len(lines) == 0 {
		return
	}
	d.busy, d.err = true, ""
	dest := d.dest.Clone()
	a.editRouting(func(s *routing.State) error {
		rows := s.View().Rows
		for _, l := range lines {
			to := dest
			if l.Target != "" {
				if t, ok := routing.ResolveTarget(l.Target, rows); ok {
					to = t
				}
			}
			if _, err := s.UpsertException(l.Entry, to); err != nil {
				return err
			}
		}
		return nil
	}, func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		if cur, ok := a.dlg.(*batchDialog); ok && cur == d {
			a.dlgOpen = false
		}
		a.rte.exOpen = true
		a.toastInfo(a.tn("batchdlg.done", len(lines)))
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
