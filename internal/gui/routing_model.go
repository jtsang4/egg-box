package gui

import (
	"context"
	"strings"

	"eggbox/internal/app"
	"eggbox/internal/routing"
	"eggbox/internal/ruleurl"
)

// ------------------------------------------------------- the edit queue

// editJob is a change to the routing waiting for its turn.
type editJob struct {
	run  func(ctx context.Context) error
	done func(err error)
}

// editRouting changes what the person chose about routing with fn, which works
// on a copy: when it fails nothing is kept. Changes made while another one is
// being applied wait their turn, so a quick second click is not lost. With a
// done function the caller reports the failure; without one a toast does.
func (a *App) editRouting(fn func(*routing.State) error, done func(err error)) {
	a.enqueue(func(ctx context.Context) error { return a.be.EditRouting(ctx, fn) }, done)
}

// setMain chooses where the main exit leads.
func (a *App) setMain(d routing.Dest) {
	a.enqueue(func(ctx context.Context) error { return a.be.SetMainExit(ctx, d) }, nil)
}

func (a *App) enqueue(run func(ctx context.Context) error, done func(err error)) {
	a.rte.queue = append(a.rte.queue, editJob{run, done})
	a.pumpEdits()
}

func (a *App) pumpEdits() {
	if a.rte.running || len(a.rte.queue) == 0 {
		return
	}
	j := a.rte.queue[0]
	a.rte.queue = a.rte.queue[1:]
	a.rte.running = true
	a.job("routing-edit", j.run, func(err error) {
		a.rte.running = false
		if err != nil && j.done == nil {
			a.fail(err)
		}
		if j.done != nil {
			j.done(err)
		}
		a.pumpEdits()
	})
}

// editing reports whether a change is being applied or waits to be.
func (a *App) editing() bool { return a.rte.running || len(a.rte.queue) > 0 }

// ----------------------------------------------------------------- names

// itemName is an item's name in the person's language: a shipped item's name
// is translated, the others are as they were written.
func (a *App) itemName(it routing.Item) string {
	if it.Key != "" {
		if n := routing.BuiltinName(it.Key, a.lang); n != "" {
			return n
		}
	}
	return it.Name
}

// planName is a plan's name as the person reads it.
func (a *App) planName(p *routing.Plan) string {
	switch p.Kind {
	case routing.PlanBuiltin:
		return a.t("plan.builtin")
	case routing.PlanSubscription:
		name := p.Name
		if s, ok := a.subscriptionByID(p.SubID); ok {
			name = s.Name
		}
		return a.t("plan.of", "name", name)
	}
	return p.Name
}

// originName words where a plan of the person's own was made from.
func (a *App) originName(origin string) string {
	if origin == routing.Builtin().Name {
		return a.t("plan.builtin")
	}
	return origin
}

// planNote says where a plan comes from and how fresh it is.
func (a *App) planNote(p *routing.Plan) (string, tone) {
	switch p.Kind {
	case routing.PlanBuiltin:
		return a.t("plan.note.builtin"), toneNeutral
	case routing.PlanSubscription:
		s, ok := a.subscriptionByID(p.SubID)
		switch {
		case !ok:
			return a.t("plan.note.sub_gone"), toneWarn
		case !s.Enabled:
			return a.t("plan.note.sub_off", "name", s.Name), toneWarn
		}
		return a.t("plan.note.sub", "name", s.Name, "when", a.ago(p.UpdatedAt)), toneNeutral
	}
	if p.Origin != "" {
		return a.t("plan.note.mine_from", "origin", a.originName(p.Origin)), toneNeutral
	}
	return a.t("plan.note.mine"), toneNeutral
}

// ----------------------------------------------------------- destinations

// destLabel words a destination.
func (a *App) destLabel(d routing.Dest) string {
	switch d.Kind {
	case routing.DestFollow:
		return a.t("dest.follow")
	case routing.DestDirect:
		return a.t("dest.direct")
	case routing.DestReject:
		return a.t("dest.reject")
	case routing.DestAuto:
		return a.t("dest.auto")
	case routing.DestRegion:
		return a.t("dest.region", "name", a.regionName(d.Region))
	case routing.DestNode:
		if d.Node == nil {
			return a.t("dest.node.none")
		}
		if n, m := a.nodeFor(*d.Node); m != routing.MatchNone {
			return n.Label()
		}
		return a.t("dest.node.gone", "name", d.Node.Name)
	}
	return string(d.Kind)
}

// destIcon is the picture that goes with a destination.
func destIcon(d routing.Dest) string {
	switch d.Kind {
	case routing.DestDirect:
		return "arrow-right"
	case routing.DestReject:
		return "shield-off"
	case routing.DestAuto:
		return "zap"
	case routing.DestRegion:
		return "globe"
	case routing.DestNode:
		return "server"
	}
	return "route"
}

// destTone is the colour of a destination: blocking is red, going direct is
// plain, anything through a node is blue.
func destTone(d routing.Dest) tone {
	switch d.Kind {
	case routing.DestReject:
		return toneBad
	case routing.DestDirect:
		return toneNeutral
	}
	return toneBlue
}

// destMissing reports a destination that names a node no subscription offers.
func (a *App) destMissing(d routing.Dest) bool {
	if d.Kind != routing.DestNode || d.Node == nil {
		return false
	}
	_, m := a.nodeFor(*d.Node)
	return m == routing.MatchNone
}

// mainBrief says where the main exit leads, for the words "follow the main
// exit": the choice, and the node it ends on while connected.
func (a *App) mainBrief() string {
	main := a.rt().Main
	label := a.destLabel(main)
	if main.Kind == routing.DestNode || !a.snap.Phase.Online() {
		return label
	}
	if ex, ok := a.mainExitNow(); ok && ex.Node != nil {
		return label + " · " + ex.Node.Label()
	}
	return label
}

// ------------------------------------------------------------------ items

// itemStatus is what became of an item when the plan was compiled.
func (a *App) itemStatus(id string) (app.ItemStatus, bool) {
	if !a.snap.Plan.Ready {
		return app.ItemStatus{}, false
	}
	st, ok := a.snap.Plan.Items[id]
	return st, ok
}

// sourceCount counts what an item is made of: files and hand-written entries.
func sourceCount(it routing.Item) (files, entries int) {
	for _, s := range it.Sources {
		if s.Kind == routing.SourceInline {
			entries += len(s.Entries)
		} else {
			files++
		}
	}
	return files, entries
}

// itemSummary is the line under an item's name: where its rules come from and
// how fresh they are ("MetaCubeX · 2 sources · updated today").
func (a *App) itemSummary(row routing.Row) string {
	it := row.Item
	var parts []string
	var projects []string
	for _, s := range it.Sources {
		if s.Project != "" && !containsString(projects, s.Project) {
			projects = append(projects, s.Project)
		}
	}
	switch {
	case len(projects) > 2:
		parts = append(parts, strings.Join(projects[:2], ", ")+"…")
	case len(projects) > 0:
		parts = append(parts, strings.Join(projects, ", "))
	}
	files, entries := sourceCount(it)
	switch {
	case files > 0 && entries > 0:
		parts = append(parts, a.tn("item.sources", len(it.Sources)))
	case files > 0:
		parts = append(parts, a.tn("item.sources", files))
	case entries > 0:
		parts = append(parts, a.tn("item.entries", entries))
	default:
		parts = append(parts, a.t("item.no_sources"))
	}
	if st, ok := a.itemStatus(it.ID); ok && !st.Oldest.IsZero() {
		parts = append(parts, a.t("item.updated", "when", a.ago(st.Oldest)))
	}
	return strings.Join(parts, " · ")
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// itemChip is a problem an item shows on its row.
type itemChip struct {
	text, tip string
	tone      tone
	icon      string
}

// itemChips lists what is wrong with an item, nothing when all is well: the
// states the person has to know about, and no others.
func (a *App) itemChips(row routing.Row) []itemChip {
	if !row.Item.Enabled {
		return nil
	}
	st, ok := a.itemStatus(row.Item.ID)
	if !ok {
		return nil
	}
	var out []itemChip
	switch st.State {
	case app.ItemUnavailable:
		out = append(out, itemChip{a.t("item.unavailable"), a.t("item.unavailable.tip"), toneWarn, "clock"})
	case app.ItemPartial:
		out = append(out, itemChip{a.t("item.partial"), a.tn("item.partial.tip", st.Missing), toneWarn, "alert"})
	}
	if st.Failing && st.State != app.ItemUnavailable {
		days := 0
		if !st.Oldest.IsZero() {
			days = int(a.now.Sub(st.Oldest).Hours() / 24)
		}
		if days >= 1 {
			out = append(out, itemChip{a.tn("item.stale", days), a.t("item.stale.tip"), toneWarn, "clock"})
		} else {
			out = append(out, itemChip{a.t("item.update_failed"), a.t("item.stale.tip"), toneWarn, "clock"})
		}
	}
	if st.Unsupported > 0 {
		out = append(out, itemChip{a.t("item.unsupported"), a.tn("item.unsupported.tip", st.Unsupported), toneNeutral, "info"})
	}
	return out
}

// sourceTitle names a source for a list: its name, or what a hand-written one
// holds.
func (a *App) sourceTitle(s routing.Source) string {
	if s.Kind == routing.SourceInline {
		return a.tn("source.inline", len(s.Entries))
	}
	if s.Name != "" {
		return s.Name
	}
	return ruleurl.Display(s.URL)
}

// ------------------------------------------------------------ exceptions

// entryKindLabel names what an entry matches.
func (a *App) entryKindLabel(k routing.EntryKind) string { return a.t("entry." + string(k)) }

// entryPlaceholder is an example of an entry's value.
func (a *App) entryExample(k routing.EntryKind) string { return a.t("entry.example." + string(k)) }

// countsLine says what a rule file holds: each kind of rule it has, as many as
// it has.
func (a *App) countsLine(domain, ip, other int) string {
	var parts []string
	if domain > 0 {
		parts = append(parts, a.tn("counts.domain", domain))
	}
	if ip > 0 {
		parts = append(parts, a.tn("counts.ip", ip))
	}
	if other > 0 {
		parts = append(parts, a.tn("counts.other", other))
	}
	if len(parts) == 0 {
		return a.t("counts.none")
	}
	return strings.Join(parts, " · ")
}

// entryHint explains what an entry matches.
func (a *App) entryHint(k routing.EntryKind) string { return a.t("entry.hint." + string(k)) }

// exceptionTitle is what an exception matches, as the person reads it: a
// program's path is shown as its name.
func exceptionTitle(e routing.Entry) string {
	if e.Kind == routing.EntryProcessPath {
		return app.ProcessName(e.Value)
	}
	return e.Value
}

// entryKinds are the kinds of entry a single entry can be.
var entryKinds = routing.EntryKinds

func entryKindIndex(k routing.EntryKind) int {
	for i, x := range entryKinds {
		if x == k {
			return i
		}
	}
	return 0
}
