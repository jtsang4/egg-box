package gui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/routing"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
)

// iconChoices are the pictures an item of the person's own can wear.
var iconChoices = []string{
	"route", "globe", "sparkles", "film", "music", "gamepad", "chat", "users", "code", "cloud", "card", "bag",
	"book", "building", "windows", "apple", "send", "tv", "shield-check", "lock", "heart", "server",
}

// itemDialog shows what an item is made of, source by source, and lets the
// person add to it, take away from it and write entries into it.
type itemDialog struct {
	id      string
	name    string
	named   bool // name holds what is being typed
	editing string
	edit    batchEditor
	err     string
	busy    bool
	scroll  ui.ScrollState
}

// openItem opens the detail of an item.
func (a *App) openItem(id string) { a.openDialog(&itemDialog{id: id}) }

func (d *itemDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	row, _, ok := a.rv().Row(d.id)
	if !ok {
		a.dlgOpen = false
		return dlgFrame{title: "", body: func() {}}
	}
	it := row.Item
	if !d.named {
		d.name, d.named = a.itemName(it), true
	}
	own := !row.Locked
	return dlgFrame{
		title: a.itemName(it), subtitle: a.planName(a.rv().Plan), icon: boolOr(it.Icon != "", it.Icon, "route"), tone: toneBlue, width: 620,
		body: func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				a.txt(c, a.t("itemdlg.goes"), 12.5, 600, p.Text2).SingleLine()
				a.destButton(c, destOpts{key: "id-dest", width: 240, title: a.itemName(it)}, it.Dest, func(x routing.Dest) {
					id := it.ID
					a.editRouting(func(s *routing.State) error { return s.SetItemDest(id, x) }, nil)
				})
				ui.Spacer(c)
				on := it.Enabled
				a.txt(c, boolOr(on, a.t("itemdlg.on"), a.t("itemdlg.off")), 12.5, 600, p.Text2).SingleLine()
				if a.toggle(c, "id-on", &on, a.itemName(it)) {
					id := it.ID
					a.editRouting(func(s *routing.State) error { return s.SetItemEnabled(id, on) }, nil)
				}
			})
			if own {
				d.ownerControls(a, c, row)
			}
			d.importNotes(a, c, it)
			a.hr(c)
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				a.heading(c, a.t("itemdlg.sources")).Grow(1)
				if a.btn(c, btnSpec{kind: btnSoft, small: true, label: a.t("itemdlg.add_source"), icon: "plus"}).Clicked() {
					a.openAddSource(it.ID)
				}
			})
			ui.Scroll(c.Key("id-scroll")).TrackScroll(&d.scroll).MaxHeight(360).Children(func() {
				ui.Column(c).Gap(10).Padding(0, 2, 2, 0).Children(func() {
					if len(it.Sources) == 0 {
						a.txt(c, a.t("itemdlg.no_sources"), 13, 400, p.Text3)
					}
					for _, src := range it.Sources {
						d.sourceCard(a, c, row, src)
					}
				})
			})
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("common.done")}).Clicked() {
				a.dlgOpen = false
			}
		},
	}
}

// importNotes say what making this item from a provider's plan changed where
// the person could not tell: the plan had it differently.
func (d *itemDialog) importNotes(a *App, c *ui.Context, it routing.Item) {
	p := a.pl
	var lines []string
	for _, n := range it.Notes {
		switch n {
		case routing.NoteReordered:
			lines = append(lines, a.t("itemdlg.note.reordered"))
		case routing.NoteMultiRegionAuto:
			// The note explains why the item follows the main exit; once the
			// person chose something else there is nothing to explain.
			if it.Dest.Kind == routing.DestFollow {
				lines = append(lines, a.t("itemdlg.note.multi_region"))
			}
		}
	}
	if len(lines) == 0 {
		return
	}
	ui.Column(c.Key("id-notes")).Gap(8).Padding(10, 12).Radius(12).Background(p.Field).Border(1, p.Line).Children(func() {
		for _, line := range lines {
			ui.Row(c).Gap(8).Children(func() {
				ui.Icon(c, icon("info")).Size(14, 14).TextColor(p.Text3).Shrink(0)
				a.txt(c, line, 12.5, 400, p.Text2).Grow(1).Basis(0).LineHeight(1.5)
			})
		}
	})
}

// ownerControls are the parts of an item that only an item of the person's own
// has: its name and its picture.
func (d *itemDialog) ownerControls(a *App, c *ui.Context, row routing.Row) {
	p := a.pl
	it := row.Item
	a.field(c, a.t("itemdlg.name"), "", func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			in := a.input(c, "id-name", &d.name, a.t("adddlg.name.placeholder"))
			in.Grow(1)
			save := func() {
				name := strings.TrimSpace(d.name)
				if name == "" || name == a.itemName(it) {
					return
				}
				id := it.ID
				d.err = ""
				a.editRouting(func(s *routing.State) error { return s.RenameItem(id, name) }, func(err error) {
					if err != nil {
						d.err = a.explain(err)
					}
				})
			}
			if in.Submitted() {
				save()
			}
			if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("common.save"), disabled: strings.TrimSpace(d.name) == "" || strings.TrimSpace(d.name) == a.itemName(it)}).Clicked() {
				save()
			}
		})
	})
	ui.Row(c).Gap(6).Wrap().Children(func() {
		for _, name := range iconChoices {
			b := ui.ButtonBase(c.Key("id-icon-" + name))
			b.Label(a.t("icon." + name))
			sel := it.Icon == name || it.Icon == "" && name == "route"
			b.Size(34, 34).Radius(10).Center().Cursor(ui.CursorPointer)
			hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 110 * time.Millisecond})
			b.Background(p.Field.Mix(p.PanelHi, hov).Mix(p.Blue.Alpha(0.2), boolf(sel)))
			b.Border(1, p.Line.Mix(p.Blue.Alpha(0.7), boolf(sel)))
			b.Children(func() {
				ui.Icon(c, icon(name)).Size(17, 17).TextColor(boolOr(sel, p.BlueText, p.Text2))
			})
			if b.Clicked() && !sel {
				id, ic := it.ID, name
				a.editRouting(func(s *routing.State) error { return s.SetItemIcon(id, ic) }, nil)
			}
		}
	})
}

// canRemove says whether a source can be taken away from the item.
func canRemove(row routing.Row, src routing.Source) bool {
	if row.Origin != routing.OriginPlan && len(row.Item.Sources) > 1 {
		return true
	}
	for _, id := range row.Extra {
		if id == src.ID {
			return true
		}
	}
	return false
}

// sourceCard is one source of the item: what it holds and where it came from.
func (d *itemDialog) sourceCard(a *App, c *ui.Context, row routing.Row, src routing.Source) {
	p := a.pl
	it := row.Item
	inline := src.Kind == routing.SourceInline
	ui.Column(c.Key("src-"+src.ID)).Gap(8).Padding(12, 14).Radius(14).Background(p.Field).Border(1, p.Line).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			a.iconTile(c, boolOr(inline, "pencil", "file-text"), toneNeutral, 30)
			ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
				a.txt(c, a.sourceTitle(src), 13.5, 650, p.Text).SingleLine()
				if !inline {
					a.txt(c, ruleurl.Display(src.URL), 11.5, 400, p.Text3).Font("monospace").SingleLine()
				}
			})
			if src.Project != "" {
				a.chip(c, src.Project, toneBlue, "")
			}
			if !inline {
				if a.btn(c, btnSpec{kind: btnGhost, icon: "copy", iconOnly: true, small: true, tip: a.t("itemdlg.copy_link")}).Clicked() {
					c.WriteClipboard(src.URL)
				}
			}
			if canRemove(row, src) {
				if a.btn(c, btnSpec{kind: btnGhost, icon: "trash", iconOnly: true, small: true, tip: a.t("itemdlg.remove")}).Clicked() {
					id, sid := it.ID, src.ID
					d.err = ""
					a.editRouting(func(s *routing.State) error { return s.RemoveSource(id, sid) }, func(err error) {
						if err != nil {
							d.err = a.explain(err)
						}
					})
				}
			}
		})
		if inline {
			d.inlineBody(a, c, row, src)
			return
		}
		st, ok := a.snap.Plan.Sources[src.URL]
		switch {
		case !ok || !a.snap.Plan.Ready:
		case !st.Present:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon("clock")).Size(14, 14).TextColor(p.Warn)
				a.txt(c, a.t("source.missing"), 12.5, 500, p.Warn)
			})
			if st.Err != "" {
				a.txt(c, truncate(st.Err, 200), 11.5, 400, p.Text3).Font("monospace").LineHeight(1.4)
			}
		default:
			a.txt(c, a.t("source.counts", "format", a.formatName(rulefmt.Format(st.Format)), "counts", a.countsLine(st.Domain, st.IP, st.Other)), 12.5, 450, p.Text2).LineHeight(1.5)
			if n := unsupportedCount(st); n > 0 {
				a.txt(c, a.t("source.unsupported", "n", strconv.Itoa(n), "types", unsupportedKinds(st.Unsupported)), 12, 400, p.Text3).LineHeight(1.5)
			}
			when := a.t("source.updated", "when", a.ago(st.Updated))
			if !st.Checked.IsZero() && st.Checked.After(st.Updated) {
				when += " · " + a.t("source.checked", "when", a.ago(st.Checked))
			}
			a.txt(c, when, 11.5, 400, p.Text3)
			if st.Err != "" {
				ui.Row(c).Gap(8).Children(func() {
					ui.Icon(c, icon("alert")).Size(14, 14).TextColor(p.Warn).Shrink(0)
					a.txt(c, a.t("source.failing", "err", truncate(st.Err, 160)), 12, 400, p.Warn).Grow(1).Basis(0).LineHeight(1.4)
				})
			}
		}
	})
}

// inlineBody shows the entries of a hand-written source, and edits them.
func (d *itemDialog) inlineBody(a *App, c *ui.Context, row routing.Row, src routing.Source) {
	p := a.pl
	it := row.Item
	if d.editing != src.ID {
		shown := src.Entries
		if len(shown) > 6 {
			shown = shown[:6]
		}
		ui.Column(c).Gap(2).Children(func() {
			for _, e := range shown {
				ui.Row(c).Gap(10).Children(func() {
					a.txt(c, a.entryKindLabel(e.Kind), 11.5, 500, p.Text3).Width(164).SingleLine()
					a.txt(c, exceptionTitle(e), 12.5, 500, p.Text).Font("monospace").SingleLine().Grow(1).Basis(0)
				})
			}
			if n := len(src.Entries) - len(shown); n > 0 {
				a.txt(c, a.tn("batch.more", n), 12, 400, p.Text3)
			}
		})
		editable := !row.Locked || containsString(row.Extra, src.ID)
		if editable {
			ui.Row(c).Children(func() {
				if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("itemdlg.edit_entries"), icon: "pencil"}).Clicked() {
					d.editing = src.ID
					lines := make([]string, len(src.Entries))
					for i, e := range src.Entries {
						lines[i] = entryLine(e)
					}
					d.edit = batchEditor{text: strings.Join(lines, "\n")}
					d.err = ""
				}
			})
		}
		return
	}
	a.area(c, "id-entries", &d.edit.text, a.t("batch.placeholder"), 5, 10).AutoFocus()
	a.batchPreview(c, &d.edit, nil, false)
	res := d.edit.result()
	ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
		if a.btn(c, btnSpec{kind: btnGhost, small: true, label: a.t("common.cancel")}).Clicked() {
			d.editing = ""
		}
		if a.btn(c, btnSpec{kind: btnPrimary, small: true, label: a.t("common.save"), busy: d.busy, disabled: len(res.Lines) == 0}).Clicked() {
			entries := make([]routing.Entry, 0, len(res.Lines))
			for _, l := range res.Lines {
				entries = append(entries, l.Entry)
			}
			id, sid := it.ID, src.ID
			d.busy, d.err = true, ""
			a.editRouting(func(s *routing.State) error { return s.SetEntries(id, sid, entries) }, func(err error) {
				d.busy = false
				if err != nil {
					d.err = a.explain(err)
					return
				}
				d.editing = ""
			})
		}
	})
}

// entryLine writes an entry the way the paste box reads it, with its kind.
func entryLine(e routing.Entry) string {
	if t, ok := clashTypeOf[e.Kind]; ok {
		return t + "," + e.Value
	}
	return e.Value
}

var clashTypeOf = map[routing.EntryKind]string{
	routing.EntryDomain:        "DOMAIN",
	routing.EntryDomainSuffix:  "DOMAIN-SUFFIX",
	routing.EntryDomainKeyword: "DOMAIN-KEYWORD",
	routing.EntryDomainRegex:   "DOMAIN-REGEX",
	routing.EntryIPCIDR:        "IP-CIDR",
	routing.EntryPort:          "DST-PORT",
	routing.EntryProcessName:   "PROCESS-NAME",
	routing.EntryProcessPath:   "PROCESS-PATH",
}

func unsupportedCount(st app.SourceStatus) int {
	n := 0
	for _, c := range st.Unsupported {
		n += c
	}
	return n
}

// unsupportedKinds lists the kinds of rule skipped.
func unsupportedKinds(m map[string]int) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > 4 {
		out = append(out[:4], "…")
	}
	return strings.Join(out, ", ")
}
