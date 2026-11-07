package gui

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/catalog"
	"eggbox/internal/routing"
	"eggbox/internal/rulefmt"
)

// The tabs of the dialog: where the rules of a new item come from.
const (
	tabCatalog = iota
	tabLink
	tabPlans
	tabWrite
)

// addDialog adds an item to the plan in force, or (with target set) adds
// sources to an item that is already there. Where the rules come from is a
// choice of four, one tab each; a choice leads to the same confirming step.
type addDialog struct {
	// target is the item that gets the sources chosen; empty to make a new item.
	target string
	tabs   []int
	tab    int

	// catalog
	query   string
	rows    []addRow
	rowsSig string
	list    ui.ListState
	listSig string

	// link
	link      string
	pv        *app.RuleSetPreview
	pvFor     string
	pvBusy    bool
	pvErr     string
	pvDetail  string
	pvIsSub   bool
	keepOnAdd string

	// plans
	plan     int // index into the plans offered
	planList ui.ListState

	// hand-written
	hw   batchEditor
	kind int // 0 detects it, otherwise an index into entryKinds plus one

	// the confirming step
	draft *itemDraft
	busy  bool
	err   string
}

// itemDraft is an item about to be added.
type itemDraft struct {
	item routing.Item
	// name is what the person typed; origName is what the item was called
	// before (so a shipped item that is not renamed stays translatable).
	name, origName string
	note           string
	entry          *catalog.Entry
	alt            int // -1: the recommended source
	// posMode: 0 by the catalog's priority, 1 at the end, 2 before an item.
	posMode int
	before  string
}

// openAddItem opens the dialog to add an item to the plan.
func (a *App) openAddItem() { a.openDialog(newAddDialog("")) }

// openAddSource opens the dialog to add sources to an item. It opens over the
// dialog that asked, which it gives back to.
func (a *App) openAddSource(itemID string) { a.pushDialog(newAddDialog(itemID)) }

func newAddDialog(target string) *addDialog {
	d := &addDialog{target: target, plan: 0}
	d.tabs = []int{tabCatalog, tabLink, tabPlans, tabWrite}
	if target != "" {
		d.tabs = []int{tabCatalog, tabLink, tabWrite}
	}
	return d
}

func (d *addDialog) tabID() int {
	if d.tab < 0 || d.tab >= len(d.tabs) {
		d.tab = 0
	}
	return d.tabs[d.tab]
}

func (d *addDialog) frame(a *App, c *ui.Context) dlgFrame {
	if d.draft != nil {
		return d.confirmFrame(a, c)
	}
	title, sub := a.t("adddlg.title"), a.t("adddlg.sub")
	if d.target != "" {
		title = a.t("adddlg.source.title")
		if row, _, ok := a.rv().Row(d.target); ok {
			sub = a.itemName(row.Item)
		}
	}
	return dlgFrame{
		title: title, subtitle: sub, icon: "plus", tone: toneBlue, width: 660,
		body: func() {
			names := map[int]segItem{
				tabCatalog: {a.t("adddlg.tab.catalog"), "search"}, tabLink: {a.t("adddlg.tab.link"), "link"},
				tabPlans: {a.t("adddlg.tab.plans"), "layers"}, tabWrite: {a.t("adddlg.tab.write"), "pencil"},
			}
			items := make([]segItem, len(d.tabs))
			for i, id := range d.tabs {
				items[i] = names[id]
			}
			a.segmented(c, "add-tab", &d.tab, items, segOpts{small: true})
			// The tabs are as tall as the tallest, so that the dialog does not
			// jump when another is chosen.
			ui.Column(c).MinHeight(addTabHeight).Gap(14).Children(func() {
				switch d.tabID() {
				case tabCatalog:
					d.catalogTab(a, c)
				case tabLink:
					d.linkTab(a, c)
				case tabPlans:
					d.plansTab(a, c)
				case tabWrite:
					d.writeTab(a, c)
				}
			})
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			if d.tabID() == tabWrite {
				d.writeButton(a, c)
			}
		},
	}
}

// addTabHeight is the height of the area under the tabs: that of the catalog,
// the tallest.
const addTabHeight = 396

// ---------------------------------------------------------------- catalog

// addRow is a line of the catalog list.
type addRow struct {
	header string
	entry  *catalog.Entry
	site   *catalog.GeoSiteHit
	other  *catalog.OtherHit
	more   string
	// covered is the item of the plan that already is this entry.
	covered *routing.Item
}

// refreshRows searches the catalog again when the question or the plan changed.
func (d *addDialog) refreshRows(a *App) {
	sig := d.query + "|" + a.lang + "|" + strconv.FormatUint(a.rtc.ver, 10)
	if sig == d.rowsSig && d.rows != nil {
		return
	}
	d.rowsSig = sig
	items := a.rv().Items()
	res := catalog.Search(d.query, a.lang, 30)
	rows := []addRow{}
	var class catalog.Class
	for i := range res.Curated {
		e := res.Curated[i]
		if strings.TrimSpace(d.query) == "" {
			if e.Class != class {
				class = e.Class
				rows = append(rows, addRow{header: e.Class.Name(a.lang)})
			}
		} else if i == 0 {
			rows = append(rows, addRow{header: a.t("adddlg.recommended")})
		}
		row := addRow{entry: &e}
		if it, ok := catalog.Covers(items, e); ok && d.target == "" {
			row.covered = &it
		}
		rows = append(rows, row)
	}
	if len(res.GeoSite) > 0 {
		rows = append(rows, addRow{header: a.t("adddlg.geosite")})
		for i := range res.GeoSite {
			rows = append(rows, addRow{site: &res.GeoSite[i]})
		}
		if res.MoreGeoSite > 0 {
			rows = append(rows, addRow{more: a.tn("adddlg.more", res.MoreGeoSite)})
		}
	}
	if len(res.Other) > 0 {
		rows = append(rows, addRow{header: a.t("adddlg.rulesets")})
		for i := range res.Other {
			rows = append(rows, addRow{other: &res.Other[i]})
		}
		if res.MoreOther > 0 {
			rows = append(rows, addRow{more: a.tn("adddlg.more", res.MoreOther)})
		}
	}
	d.rows = rows
}

func (d *addDialog) catalogTab(a *App, c *ui.Context) {
	p := a.pl
	a.searchFirst(c, "add-q", &d.query, a.t("adddlg.search"), 0)
	d.refreshRows(a)
	if len(d.rows) == 0 {
		ui.Column(c).Height(330).Justify(ui.Center).Children(func() {
			a.emptyState(c, "search", a.t("adddlg.none.title"), a.t("adddlg.none.body"), nil)
		})
		return
	}
	jumpOnChange(&d.list, &d.listSig, d.query)
	ui.List(c.Key("add-list"), &d.list, len(d.rows), func(i int) {
		r := d.rows[i]
		switch {
		case r.header != "":
			a.eyebrow(c, strings.ToUpper(r.header)).Padding(12, 4, 4, 4)
		case r.more != "":
			a.txt(c, r.more, 12, 400, p.Text3).Padding(6, 4)
		case r.entry != nil:
			d.entryRow(a, c, r)
		case r.site != nil:
			d.plainRow(a, c, "add-s-"+r.site.Name, "globe", "geosite: "+r.site.Name, a.t("adddlg.geosite.note"), "", func() {
				d.pickSources(a, r.site.Name, "globe", []routing.Source{r.site.Source}, nil)
			})
		case r.other != nil:
			d.plainRow(a, c, "add-o-"+r.other.Name, "file-text", r.other.Name, r.other.Project+" · "+a.t("adddlg.rulesets.note"), "", func() {
				d.pickSources(a, r.other.Name, "file-text", []routing.Source{r.other.Source}, nil)
			})
		}
	}).Height(340).Gap(4).Padding(0, 0, 4, 0)
}

// rowFace is the look of a row of a list in a dialog.
func (a *App) rowFace(b ui.Element, hov float32, disabled bool) {
	p := a.pl
	b.Background(p.Field.Mix(p.PanelHi, hov*boolf(!disabled))).Border(1, p.Line.Mix(p.LineHi, hov*boolf(!disabled)))
}

func (d *addDialog) entryRow(a *App, c *ui.Context, r addRow) {
	p := a.pl
	e := *r.entry
	// An entry the plan has is done with, unless its item is off: then a
	// click turns it on, rather than "added" saying it works when it does not.
	off := r.covered != nil && !r.covered.Enabled
	covered := r.covered != nil && !off
	b := ui.ButtonBase(c.Key("add-e-" + e.ID))
	b.Label(e.Name(a.lang))
	b.Height(58).Padding(0, 12).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(boolOr(covered, ui.CursorDefault, ui.CursorPointer)).Disabled(covered)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
	a.rowFace(b, hov, covered)
	switch {
	case covered:
		b.Tooltip(a.t("adddlg.covered.tip", "name", a.itemName(*r.covered)))
	case off:
		b.Tooltip(a.t("adddlg.off.tip", "name", a.itemName(*r.covered)))
	}
	b.Children(func() {
		a.iconTile(c, e.Icon, boolOr(covered, toneNeutral, toneBlue), 36)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, e.Name(a.lang), 13.5, 650, boolOr(covered, p.Text3, p.Text)).SingleLine()
			a.txt(c, e.Desc(a.lang), 12, 400, p.Text3).SingleLine()
		})
		switch {
		case covered:
			a.chip(c, a.t("adddlg.covered"), toneGood, "check")
		case off:
			a.chip(c, a.t("adddlg.off"), toneWarn, "")
		default:
			a.chip(c, e.Class.Name(a.lang), toneNeutral, "")
		}
	})
	if b.Clicked() {
		if off {
			d.turnOn(a, *r.covered)
			return
		}
		d.pickEntry(a, e)
	}
}

// turnOn turns on the item of the plan that already is the entry chosen.
func (d *addDialog) turnOn(a *App, it routing.Item) {
	if d.busy {
		return
	}
	d.busy, d.err = true, ""
	id, name := it.ID, a.itemName(it)
	a.enqueue(func(ctx context.Context) error {
		return a.be.EditRouting(ctx, func(s *routing.State) error { return s.SetItemEnabled(id, true) })
	}, func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		d.finish(a)
		a.toastInfo(a.t("adddlg.turned_on", "name", name))
	})
}

// plainRow is a row without a recommendation behind it: a name and a line.
func (d *addDialog) plainRow(a *App, c *ui.Context, key, iconName, title, sub, chip string, pick func()) {
	p := a.pl
	b := ui.ButtonBase(c.Key(key))
	b.Label(title)
	b.Height(50).Padding(0, 12).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
	a.rowFace(b, hov, false)
	b.Children(func() {
		a.iconTile(c, iconName, toneNeutral, 32)
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, title, 13.5, 600, p.Text).SingleLine()
			if sub != "" {
				a.txt(c, sub, 12, 400, p.Text3).SingleLine()
			}
		})
		if chip != "" {
			a.chip(c, chip, toneNeutral, "")
		}
	})
	if b.Clicked() {
		pick()
	}
}

// pickEntry takes a catalog entry: to the confirming step, or straight into
// the item that asked for sources.
func (d *addDialog) pickEntry(a *App, e catalog.Entry) {
	if d.target != "" {
		d.addSources(a, e.Sources(), "")
		return
	}
	it := e.Item(a.lang, -1)
	d.draft = &itemDraft{item: it, name: it.Name, origName: it.Name, entry: &e, alt: -1, note: e.Desc(a.lang)}
	d.err = ""
}

// pickSources takes sources found elsewhere (a name of the geosite list, a
// category of another project, a file the person pasted).
func (d *addDialog) pickSources(a *App, name, iconName string, srcs []routing.Source, keep func() string) {
	k := ""
	if keep != nil {
		k = keep()
	}
	if d.target != "" {
		d.addSources(a, srcs, k)
		return
	}
	it := routing.Item{Name: name, Icon: iconName, Sources: srcs, Dest: routing.Follow(), Enabled: true}
	d.draft = &itemDraft{item: it, name: name, origName: name, alt: -1}
	d.keepOnAdd = k
	d.err = ""
}

// addSources gives the item that asked for sources what was chosen.
func (d *addDialog) addSources(a *App, srcs []routing.Source, keep string) {
	if d.busy || len(srcs) == 0 {
		return
	}
	d.busy, d.err = true, ""
	target := d.target
	a.enqueue(func(ctx context.Context) error {
		if keep != "" {
			if err := a.be.KeepRuleSet(ctx, keep); err != nil {
				return err
			}
		}
		return a.be.EditRouting(ctx, func(s *routing.State) error {
			added := 0
			var dup error
			for _, src := range srcs {
				switch err := s.AddSource(target, src); {
				case err == nil:
					added++
				case errors.Is(err, routing.ErrDuplicateSource):
					dup = err
				default:
					return err
				}
			}
			if added == 0 && dup != nil {
				return dup
			}
			return nil
		})
	}, func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		d.finish(a)
	})
}

// finish closes the dialog when it is still the one in front.
func (d *addDialog) finish(a *App) {
	if cur, ok := a.dlg.(*addDialog); ok && cur == d {
		a.dlgOpen = false
	}
}

// ------------------------------------------------------------------- link

// fetch downloads the file at the address and says what it holds.
func (d *addDialog) fetch(a *App) {
	link := strings.TrimSpace(d.link)
	if link == "" || d.pvBusy {
		return
	}
	d.pvBusy, d.pv, d.pvErr, d.pvDetail, d.pvIsSub = true, nil, "", "", false
	d.pvFor = d.link
	var pv *app.RuleSetPreview
	a.job("ruleset-preview", func(ctx context.Context) error {
		var err error
		pv, err = a.be.PreviewRuleSet(ctx, link)
		return err
	}, func(err error) {
		d.pvBusy = false
		if err != nil {
			if errors.Is(err, app.ErrLooksLikeSubscription) {
				d.pvIsSub = true
				return
			}
			d.pvErr = a.explain(err)
			if ex := app.Explain(err); ex.Code == "unknown" || ex.Code == "rule_http_status" {
				d.pvDetail = truncate(ex.Detail, 240)
			}
			return
		}
		d.pv = pv
	})
}

func (d *addDialog) linkTab(a *App, c *ui.Context) {
	p := a.pl
	a.field(c, a.t("adddlg.link"), a.t("adddlg.link.hint"), func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			in := a.input(c, "add-link", &d.link, "https://")
			in.Grow(1)
			if d.link == "" {
				in.AutoFocus()
			}
			if in.Submitted() {
				d.fetch(a)
			}
			if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("subdlg.paste"), icon: "copy"}).Clicked() {
				d.link = strings.TrimSpace(c.ReadClipboard())
				d.fetch(a)
			}
			if a.btn(c, btnSpec{kind: btnSoft, label: a.t("adddlg.check"), busy: d.pvBusy, disabled: strings.TrimSpace(d.link) == ""}).Clicked() {
				d.fetch(a)
			}
		})
	})
	if d.pvFor != d.link {
		// What was typed since no longer describes the answer.
		d.pv, d.pvErr, d.pvDetail, d.pvIsSub = nil, "", "", false
	}
	switch {
	case d.pvBusy:
		ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(8, 0).Children(func() {
			a.spinner(c, 16, p.BlueText)
			a.txt(c, a.t("adddlg.fetching"), 13, 500, p.Text2)
		})
	case d.pvIsSub:
		ui.Column(c).Gap(10).Padding(14).Radius(12).Background(p.Warn.Alpha(0.1)).Border(1, p.Warn.Alpha(0.26)).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icon("alert")).Size(16, 16).TextColor(p.Warn)
				a.txt(c, a.t("adddlg.issub"), 13, 600, p.Text).Grow(1).Basis(0)
			})
			link := strings.TrimSpace(d.link)
			ui.Row(c).Children(func() {
				if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("adddlg.issub.add"), icon: "rss"}).Clicked() {
					a.openAddSubscriptionWith(link)
				}
			})
		})
	case d.pvErr != "":
		a.errorNote(c, d.pvErr)
		if d.pvDetail != "" {
			a.txt(c, d.pvDetail, 11.5, 400, p.Text3).Font("monospace").LineHeight(1.45)
		}
		a.txt(c, a.t("adddlg.retry"), 12, 400, p.Text3).LineHeight(1.5)
	case d.pv != nil:
		d.previewCard(a, c)
	}
}

// previewCard says what the file at an address holds, in the terms of 5.3 of
// the design: the format, the counts, what was skipped.
func (d *addDialog) previewCard(a *App, c *ui.Context) {
	p := a.pl
	pv := d.pv
	ui.Column(c).Gap(10).Padding(14).Radius(14).Background(p.Field).Border(1, p.Line).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon("check-circle")).Size(18, 18).TextColor(p.Good)
			ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
				a.txt(c, a.t("adddlg.recognized", "format", a.formatName(pv.Format)), 13.5, 650, p.Text)
				a.txt(c, pv.Display, 11.5, 400, p.Text3).Font("monospace").SingleLine()
			})
			if pv.Project != "" {
				a.chip(c, pv.Project, toneBlue, "")
			}
		})
		st := pv.Stats
		a.txt(c, a.countsLine(st.Domain, st.IP, st.Other), 12.5, 450, p.Text2).LineHeight(1.5)
		if n := st.UnsupportedTotal(); n > 0 {
			a.txt(c, a.t("adddlg.unsupported", "n", strconv.Itoa(n), "types", unsupportedTypes(st)), 12, 400, p.Warn).LineHeight(1.5)
		}
		if pv.Stripped {
			a.txt(c, a.t("adddlg.stripped"), 12, 400, p.Text3).LineHeight(1.5)
		}
		if pv.Converted {
			a.txt(c, a.t("adddlg.converted"), 12, 400, p.Text3).LineHeight(1.5)
		}
		if pv.Swapped {
			a.txt(c, a.t("adddlg.swapped"), 12, 400, p.Text3).LineHeight(1.5)
		}
		ui.Row(c).Justify(ui.End).Children(func() {
			label := a.t("adddlg.use")
			if d.target != "" {
				label = a.t("adddlg.add_source")
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: label, busy: d.busy}).Clicked() {
				src := routing.URLSource(pv.URL, pv.Name, pv.Project)
				name := pv.Name
				if name == "" {
					name = app.RuleSetName(pv.URL)
				}
				url := pv.URL
				d.pickSources(a, name, "file-text", []routing.Source{src}, func() string { return url })
			}
		})
	})
}

// unsupportedTypes lists the kinds of rule the core cannot use, as the file
// names them.
func unsupportedTypes(st rulefmt.Stats) string {
	var out []string
	for k := range st.Unsupported {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > 4 {
		out = append(out[:4], "…")
	}
	return strings.Join(out, ", ")
}

// formatName is a file format in words.
func (a *App) formatName(f rulefmt.Format) string { return a.t("fmt." + string(f)) }

// ------------------------------------------------------------------ plans

// otherPlans are the plans an item can be copied from: all but the one in force.
func (a *App) otherPlans() []*routing.Plan {
	active := a.rt().ActiveID()
	var out []*routing.Plan
	for _, p := range a.allPlans() {
		if p.ID != active {
			out = append(out, p)
		}
	}
	return out
}

// allPlans lists the plans a person can switch to: the shipped one, then
// those that came with subscriptions, then their own.
func (a *App) allPlans() []*routing.Plan {
	out := []*routing.Plan{routing.Builtin()}
	for _, kind := range []routing.PlanKind{routing.PlanSubscription, routing.PlanMine} {
		for _, p := range a.rt().Plans {
			if p.Kind == kind {
				out = append(out, p)
			}
		}
	}
	return out
}

func (d *addDialog) plansTab(a *App, c *ui.Context) {
	p := a.pl
	plans := a.otherPlans()
	if len(plans) == 0 {
		ui.Column(c).Height(330).Justify(ui.Center).Children(func() {
			a.emptyState(c, "layers", a.t("adddlg.plans.none.title"), a.t("adddlg.plans.none.body"), nil)
		})
		return
	}
	if d.plan >= len(plans) {
		d.plan = 0
	}
	names := make([]string, len(plans))
	for i, pl := range plans {
		names[i] = a.planName(pl)
	}
	a.field(c, a.t("adddlg.plans.from"), "", func() {
		a.dropdown(c, "add-plan", names, d.plan, func(i int) { d.plan = i })
	})
	rows := a.rt().ViewOf(plans[d.plan].ID).Rows
	have := map[string]string{}
	for _, r := range a.rv().Rows {
		for _, k := range r.Item.Names() {
			have[k] = a.itemName(r.Item)
		}
	}
	ui.List(c.Key("add-plan-list"), &d.planList, len(rows), func(i int) {
		r := rows[i]
		var dup string
		for _, k := range r.Item.Names() {
			if n, ok := have[k]; ok {
				dup = n
				break
			}
		}
		covered := dup != ""
		b := ui.ButtonBase(c.Key("add-pi-" + r.Item.ID))
		b.Label(a.itemName(r.Item))
		b.Height(54).Padding(0, 12).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(boolOr(covered, ui.CursorDefault, ui.CursorPointer)).Disabled(covered)
		hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
		a.rowFace(b, hov, covered)
		b.Children(func() {
			a.iconTile(c, boolOr(r.Item.Icon != "", r.Item.Icon, "route"), boolOr(covered, toneNeutral, toneBlue), 34)
			ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
				a.txt(c, a.itemName(r.Item), 13.5, 650, boolOr(covered, p.Text3, p.Text)).SingleLine()
				a.txt(c, a.itemSummaryOf(r.Item), 12, 400, p.Text3).SingleLine()
			})
			if covered {
				a.chip(c, a.t("adddlg.plans.have"), toneNeutral, "check")
			} else {
				a.chip(c, a.destLabel(r.Item.Dest), destTone(r.Item.Dest), destIcon(r.Item.Dest))
			}
		})
		if b.Clicked() {
			it := r.Item.Clone()
			it.ID, it.Anchor = "", ""
			name := a.itemName(it)
			d.draft = &itemDraft{item: it, name: name, origName: name, alt: -1, note: a.t("adddlg.plans.note", "plan", names[d.plan])}
			d.err = ""
		}
	}).Height(280).Gap(4).Padding(0, 0, 4, 0)
}

// itemSummaryOf is a short account of what an item is made of, for lists of
// items of plans that are not in force.
func (a *App) itemSummaryOf(it routing.Item) string {
	return a.itemSummary(routing.Row{Item: it})
}

// ------------------------------------------------------------ hand-written

// typedBatch reads a list in which every line is of one kind.
func typedBatch(text string, kind routing.EntryKind) routing.BatchResult {
	res := routing.BatchResult{Counts: map[routing.EntryKind]int{}}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e := routing.Entry{Kind: kind, Value: line}.Normalize()
		if err := e.Valid(); err != nil {
			res.Unrecognized = append(res.Unrecognized, routing.BatchBad{Line: i + 1, Text: truncate(raw, 120), Reason: err.Error()})
			continue
		}
		res.Lines = append(res.Lines, routing.BatchLine{Line: i + 1, Entry: e})
		res.Counts[kind]++
	}
	return res
}

// entries reads the box as the chosen kind asks.
func (d *addDialog) entries() routing.BatchResult {
	if d.kind > 0 && d.kind <= len(entryKinds) {
		if !d.hw.read || d.hw.seen != d.hw.text+"\x00"+strconv.Itoa(d.kind) {
			d.hw.seen, d.hw.read = d.hw.text+"\x00"+strconv.Itoa(d.kind), true
			d.hw.res = typedBatch(d.hw.text, entryKinds[d.kind-1])
		}
		return d.hw.res
	}
	if !d.hw.read || d.hw.seen != d.hw.text+"\x00auto" {
		d.hw.seen, d.hw.read = d.hw.text+"\x00auto", true
		d.hw.res = routing.ParseBatch(d.hw.text)
	}
	return d.hw.res
}

func (d *addDialog) writeTab(a *App, c *ui.Context) {
	p := a.pl
	res := d.entries()
	a.txt(c, a.t("adddlg.write.help"), 12.5, 400, p.Text2).LineHeight(1.5)
	a.field(c, a.t("adddlg.write.kind"), "", func() {
		names := []string{a.t("adddlg.write.auto")}
		for _, k := range entryKinds {
			names = append(names, a.entryKindLabel(k))
		}
		a.dropdown(c, "add-kind", names, d.kind, func(i int) { d.kind = i })
	})
	a.area(c, "add-write", &d.hw.text, a.t("batch.placeholder"), 6, 9).AutoFocus()
	// The preview reads the box as it is now.
	shown := batchEditor{text: d.hw.text, read: true, seen: d.hw.text, res: res}
	a.batchPreview(c, &shown)
}

// writeButton is the footer's button of the hand-written tab: on to the
// confirming step, or (for an item that is there already) adding the lines.
func (d *addDialog) writeButton(a *App, c *ui.Context) {
	res := d.entries()
	n := len(res.Lines)
	label := a.t("adddlg.write.next")
	if d.target != "" {
		label = a.tn("adddlg.write.add", n)
	}
	if !a.btn(c, btnSpec{kind: btnPrimary, label: label, busy: d.busy, disabled: n == 0}).Clicked() {
		return
	}
	entries := make([]routing.Entry, 0, n)
	for _, l := range res.Lines {
		entries = append(entries, l.Entry)
	}
	src := routing.InlineSource(entries)
	if d.target != "" {
		d.addSources(a, []routing.Source{src}, "")
		return
	}
	it := routing.Item{Icon: "edit-rule", Sources: []routing.Source{src}, Dest: routing.Follow(), Enabled: true}
	// All lines naming one destination (DIRECT, say) suggest it.
	if ts := res.Targets(); len(ts) == 1 {
		if dest, ok := routing.ResolveTarget(ts[0], a.rv().Rows); ok {
			it.Dest = dest
		}
	}
	d.draft = &itemDraft{item: it, alt: -1, note: a.tn("adddlg.write.note", n)}
	d.err = ""
}

// ------------------------------------------------------------- confirming

func (d *addDialog) confirmFrame(a *App, c *ui.Context) dlgFrame {
	dr := d.draft
	rows := a.rv().Rows
	return dlgFrame{
		title: a.t("adddlg.confirm.title"), subtitle: dr.note, icon: boolOr(dr.item.Icon != "", dr.item.Icon, "route"), tone: toneBlue, width: 560,
		body: func() {
			a.field(c, a.t("adddlg.name"), "", func() {
				in := a.input(c, "add-name", &dr.name, a.t("adddlg.name.placeholder"))
				if dr.name == "" {
					in.AutoFocus()
				}
				if in.Changed() {
					d.err = ""
				}
				if in.Submitted() {
					d.submit(a)
				}
			})
			if dr.entry != nil {
				if alts := dr.entry.Alternatives(); len(alts) > 0 {
					names := []string{a.t("adddlg.src.recommended", "project", catalog.ProjectMetaCubeX)}
					for _, alt := range alts {
						names = append(names, alt.Project+boolOr(alt.Note != "", " · "+alt.Note, ""))
					}
					a.field(c, a.t("adddlg.src"), "", func() {
						a.dropdown(c, "add-alt", names, dr.alt+1, func(i int) {
							dr.alt = i - 1
							dest := dr.item.Dest
							dr.item = dr.entry.Item(a.lang, dr.alt)
							dr.item.Dest = dest
						})
					})
				}
			}
			a.field(c, a.t("adddlg.dest"), a.t("adddlg.dest.hint"), func() {
				ui.Row(c).Children(func() {
					a.destButton(c, destOpts{key: "add-dest", width: 280, title: boolOr(dr.name != "", dr.name, a.t("adddlg.confirm.title"))},
						dr.item.Dest, func(x routing.Dest) { dr.item.Dest = x })
				})
			})
			a.field(c, a.t("adddlg.pos"), "", func() {
				names := []string{d.autoPosition(a, dr, rows), a.t("adddlg.pos.end")}
				for _, r := range rows {
					names = append(names, a.t("adddlg.pos.before", "name", a.itemName(r.Item)))
				}
				cur := dr.posMode
				if dr.posMode == 2 {
					cur = 1
					for i, r := range rows {
						if r.Item.ID == dr.before {
							cur = 2 + i
						}
					}
				}
				a.dropdown(c, "add-pos", names, cur, func(i int) {
					switch {
					case i == 0:
						dr.posMode, dr.before = 0, ""
					case i == 1:
						dr.posMode, dr.before = 1, ""
					default:
						dr.posMode, dr.before = 2, rows[i-2].Item.ID
					}
				})
			})
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.back")}).Clicked() {
				d.draft, d.err, d.keepOnAdd = nil, "", ""
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("routing.add"), busy: d.busy, disabled: strings.TrimSpace(dr.name) == ""}).Clicked() {
				d.submit(a)
			}
		},
	}
}

// autoPosition words where the catalog's own priority puts the item.
func (d *addDialog) autoPosition(a *App, dr *itemDraft, rows []routing.Row) string {
	if dr.entry == nil {
		return a.t("adddlg.pos.auto_end")
	}
	id := catalog.InsertBefore(a.rv().Items(), *dr.entry)
	if id == "" {
		return a.t("adddlg.pos.auto_end")
	}
	for _, r := range rows {
		if r.Item.ID == id {
			return a.t("adddlg.pos.auto", "name", a.itemName(r.Item))
		}
	}
	return a.t("adddlg.pos.auto_end")
}

func (d *addDialog) submit(a *App) {
	dr := d.draft
	if d.busy || dr == nil {
		return
	}
	name := strings.TrimSpace(dr.name)
	if name == "" {
		return
	}
	d.busy, d.err = true, ""
	it := dr.item.Clone()
	it.ID = ""
	if name != dr.origName {
		it.Key, it.Name = "", name
	} else if it.Key == "" {
		it.Name = name
	}
	posMode, before, entry, keep := dr.posMode, dr.before, dr.entry, d.keepOnAdd
	a.enqueue(func(ctx context.Context) error {
		if keep != "" {
			if err := a.be.KeepRuleSet(ctx, keep); err != nil {
				return err
			}
		}
		return a.be.EditRouting(ctx, func(s *routing.State) error {
			at := ""
			switch {
			case posMode == 2:
				at = before
			case posMode == 0 && entry != nil:
				at = catalog.InsertBefore(s.View().Items(), *entry)
			}
			_, err := s.AddItem(it, at)
			return err
		})
	}, func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		d.finish(a)
		a.toastInfo(a.t("adddlg.added", "name", name))
	})
}
