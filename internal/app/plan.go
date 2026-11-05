package app

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
	"eggbox/internal/ruledata"
	"eggbox/internal/subscription"
)

// planView is one compilation of the plan in force: what it needs, what came
// of it, and the names to explain the core's rules with. Views are replaced,
// never changed, so readers need no lock.
type planView struct {
	state  routing.State
	res    *plancompile.Result
	out    *configgen.Output // nil when there is nothing to connect through
	active []library.ActiveNode
	// names are the names of the items by id; exceptions are described by what
	// they match.
	names map[string]string
	// owners map the rule-set tags of the configuration to what they came from.
	owners map[string]configgen.Owner
	// wanted are the nodes somebody chose, by owner: "main", "fallback" or the
	// id of an item.
	wanted map[string]routing.NodeID
}

// compile turns the plan into the generator's input.
func (s *Service) compile(st routing.State, prefs configgen.Prefs, active []library.ActiveNode) (*plancompile.Result, error) {
	nodes := make([]plancompile.Node, len(active))
	for i, a := range active {
		nodes[i] = plancompile.Node{Sub: a.SubID, Node: a.Node, Auto: a.Auto}
	}
	return s.compiler.Compile(plancompile.Options{State: st, Nodes: nodes, Data: s.rules, Prefs: prefs})
}

// buildView compiles the plan in force and writes the configuration it leads
// to, without involving the service.
func (s *Service) buildView() (*planView, error) {
	active, err := s.lib.ActiveNodes()
	if err != nil {
		return nil, err
	}
	st, prefs := s.lib.Routing(), s.lib.Prefs()
	res, err := s.compile(st, prefs, active)
	if err != nil {
		return nil, err
	}
	v := &planView{state: st, res: res, active: active, owners: map[string]configgen.Owner{}}
	if len(active) > 0 {
		if out, err := configgen.Generate(res.Input); err == nil {
			v.out = out
			v.owners = out.Owners
		}
	}
	v.names, v.wanted = namesAndPins(st)
	return v, nil
}

func namesAndPins(st routing.State) (names map[string]string, wanted map[string]routing.NodeID) {
	names, wanted = map[string]string{}, map[string]routing.NodeID{}
	view := st.View()
	for _, r := range view.Rows {
		names[r.Item.ID] = r.Item.Name
		if r.Item.Dest.Kind == routing.DestNode && r.Item.Dest.Node != nil {
			wanted[r.Item.ID] = *r.Item.Dest.Node
		}
	}
	for _, x := range st.Exceptions {
		names[x.ID] = x.Value
	}
	if st.Main.Kind == routing.DestNode && st.Main.Node != nil {
		wanted["main"] = *st.Main.Node
	}
	if view.Fallback.Kind == routing.DestNode && view.Fallback.Node != nil {
		wanted["fallback"] = *view.Fallback.Node
	}
	return names, wanted
}

// kickPlan asks for the plan to be looked at again.
func (s *Service) kickPlan() {
	select {
	case s.planKick <- struct{}{}:
	default:
	}
}

// planLoop keeps the snapshot's view of the plan current. It never touches
// the service, so it works while disconnected too.
func (s *Service) planLoop(ctx context.Context) {
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.planKick:
		}
		// Changes come in bursts (an import, a drag).
		select {
		case <-ctx.Done():
			return
		case <-time.After(120 * time.Millisecond):
		}
		select {
		case <-s.planKick:
		default:
		}
		v, err := s.buildView()
		if err != nil {
			continue
		}
		seq++
		s.view.Store(v)
		info := s.planInfo(v, seq)
		s.update(func(sn *Snapshot) {
			sn.Plan = info
			sn.Plan.Updating = s.fetching.Load()
			sn.Exits = s.exitsLocked(sn.Groups)
		})
		s.fetchMissing(ctx, v.res.Missing)
		s.refreshIdentities(v)
	}
}

// refreshIdentities lets chosen nodes follow a rename that left the node's
// configuration alone, and records the region they were last seen in.
func (s *Service) refreshIdentities(v *planView) {
	live := make([]routing.LiveNode, 0, len(v.active))
	for _, a := range v.active {
		live = append(live, routing.LiveNode{Sub: a.SubID, Name: a.Node.Name, Hash: a.Node.Key(), Region: regionFor(v.state, a)})
	}
	_ = s.lib.UpdateRouting(func(st *routing.State) error {
		if !st.RefreshIdentities(live) {
			return errNothingChanged
		}
		return nil
	})
}

// errNothingChanged lets an edit that found nothing to do keep the library
// from being written.
var errNothingChanged = errors.New("nothing changed")

// RegionOf is the region a node counts as: the person's correction, else what
// its name says. The interface shows the same one the configuration uses.
func RegionOf(st routing.State, a library.ActiveNode) string { return regionFor(st, a) }

// regionFor is the region a node counts as: the person's correction, else
// what its name says.
func regionFor(st routing.State, a library.ActiveNode) string {
	id := routing.NodeID{Sub: a.SubID, Name: a.Node.Name}
	if r := st.NodePref(id).Region; r != "" {
		return strings.ToUpper(r)
	}
	return strings.ToUpper(subscription.Region(a.Node.Name))
}

// planInfo describes the compiled plan for the interface.
func (s *Service) planInfo(v *planView, seq uint64) PlanInfo {
	info := PlanInfo{
		Seq: seq, Ready: true, Missing: len(v.res.Missing),
		Items: map[string]ItemStatus{}, Sources: map[string]SourceStatus{},
	}
	view := v.state.View()
	var urls []string
	seen := map[string]bool{}
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	for _, row := range view.Rows {
		for _, src := range row.Item.Sources {
			if src.Kind != routing.SourceURL {
				continue
			}
			add(src.URL)
			for _, d := range s.rules.Deps(src.URL) {
				add(d)
			}
		}
	}
	s.mu.Lock()
	failed := make(map[string]string, len(s.fetchErrs))
	for u, e := range s.fetchErrs {
		failed[u] = e
	}
	s.mu.Unlock()
	for _, st := range s.rules.Statuses(urls...) {
		info.Sources[st.URL] = sourceStatus(st, failed[st.URL])
	}
	for _, row := range view.Rows {
		ci := v.res.Items[row.Item.ID]
		is := ItemStatus{State: ci.State, Missing: ci.Missing, Unsupported: ci.Unsupported, Domain: ci.Domain, IP: ci.IP, Sources: len(row.Item.Sources)}
		for _, src := range row.Item.Sources {
			if src.Kind != routing.SourceURL {
				continue
			}
			st, ok := info.Sources[src.URL]
			if !ok {
				continue
			}
			if st.Err != "" {
				is.Failing = true
			}
			if st.Present && (is.Oldest.IsZero() || st.Checked.Before(is.Oldest)) {
				is.Oldest = st.Checked
			}
		}
		info.Items[row.Item.ID] = is
	}
	for _, n := range v.res.Notices {
		if n.Code != plancompile.NoticePinGone {
			continue
		}
		info.Gone = append(info.Gone, Pin{Owner: n.Owner, ID: n.ID, Name: v.names[n.ID], Node: n.Node})
	}
	return info
}

func sourceStatus(st ruledata.Status, failure string) SourceStatus {
	out := SourceStatus{URL: st.URL, Present: st.Present, Err: failure}
	if st.Present {
		m := st.Meta
		out.Format, out.Size, out.Updated, out.Checked = m.Format, m.Size, m.UpdatedAt, m.CheckedAt
		out.Domain, out.IP, out.Other = m.Stats.Domain, m.Stats.IP, m.Stats.Other
		out.Unsupported = m.Stats.Unsupported
		out.Mirror = m.Mirror
		if m.LastError != "" {
			out.Err = m.LastError
		}
	}
	return out
}

// ------------------------------------------------------------------- exits

// exitsLocked works out where traffic of the main exit, the fallback and every
// item leaves right now, from the groups the core reports.
func (s *Service) exitsLocked(groups []Group) map[string]ExitInfo {
	v := s.view.Load()
	if v == nil || v.out == nil {
		return nil
	}
	out := make(map[string]ExitInfo, len(v.out.Selectors)+1)
	out["main"] = s.exitOfLocked(groups, configgen.TagProxy, v.wanted["main"])
	for id, tag := range v.out.Selectors {
		out[id] = s.exitOfLocked(groups, tag, v.wanted[id])
	}
	return out
}

// exitOfLocked follows a group's selection down to a node.
func (s *Service) exitOfLocked(groups []Group, tag string, wanted routing.NodeID) ExitInfo {
	info := ExitInfo{Group: tag}
	cur := tag
	for hop := 0; hop < 8; hop++ {
		var g *Group
		for i := range groups {
			if groups[i].Tag == cur {
				g = &groups[i]
				break
			}
		}
		if g == nil {
			if ref, ok := s.tagIndex[cur]; ok {
				r := ref
				info.Node = &r
			}
			break
		}
		if g.Selected == "" {
			break
		}
		if hop == 0 {
			info.Member = g.Selected
		}
		for _, it := range g.Items {
			if it.Tag == g.Selected {
				info.DelayMS = it.DelayMS
			}
		}
		cur = g.Selected
	}
	if !wanted.IsZero() {
		info.Wanted = &wanted
		info.StandIn = info.Node != nil && !wanted.Same(info.Node.ID)
	}
	return info
}

// ExitOf says where an item's traffic ("fallback" for the fallback, "main"
// for the main exit) leaves now. ok is false when nothing is known, which is
// the case while no configuration was generated and for blocked items.
func (s *Service) ExitOf(id string) (ExitInfo, bool) {
	e, ok := s.Snapshot().Exits[id]
	return e, ok
}

// --------------------------------------------------------------------- why

// whyOf explains a connection's rule in the person's terms. rule is the
// core's text; the rule sets the application wrote are found in it by tag.
func (s *Service) whyOf(rule, outbound string) Why {
	v := s.view.Load()
	if v == nil {
		return Why{}
	}
	for _, tok := range strings.FieldsFunc(rule, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.')
	}) {
		o, ok := v.owners[tok]
		if !ok {
			continue
		}
		switch o.Kind {
		case configgen.OwnerException:
			return Why{Kind: WhyException, ID: o.ID, Name: v.names[o.ID], Index: v.state.ExceptionIndex(o.ID)}
		case configgen.OwnerItem:
			return Why{Kind: WhyItem, ID: o.ID, Name: v.names[o.ID]}
		}
	}
	lower := strings.ToLower(rule)
	switch {
	case strings.Contains(lower, "ip_is_private"):
		return Why{Kind: WhyLAN}
	case strings.Contains(lower, "clash_mode"):
		return Why{Kind: WhyMode}
	case strings.Contains(lower, "domain_suffix=[local lan localdomain"):
		return Why{Kind: WhyLAN}
	case strings.TrimSpace(rule) == "" || strings.HasPrefix(lower, "final"):
		if outbound != "" {
			return Why{Kind: WhyFallback}
		}
	}
	return Why{}
}

// ------------------------------------------------------- notices of the library

// libraryNoticeLevels give the level of the notices the library queues.
var libraryNoticeLevels = map[string]Level{
	"plan_adopted":          LevelSuccess,
	"plan_items_added":      LevelInfo,
	"plan_items_removed":    LevelInfo,
	"migrate_rules_dropped": LevelWarning,
	"migrate_node_lost":     LevelWarning,
}

// deliverLibraryNotices shows what the library queued while nobody was looking
// (a migration, a refresh in the background) and takes it off the queue.
func (s *Service) deliverLibraryNotices() {
	for _, n := range s.lib.PendingNotices() {
		level, ok := libraryNoticeLevels[n.Code]
		if !ok {
			level = LevelInfo
		}
		s.notice(level, n.Code, "", n.Args)
		_ = s.lib.DismissNotice(n.ID)
	}
}

// tellOnce records a notice only the first time in a session.
func (s *Service) tellOnce(key string, level Level, code, detail string, args map[string]string) {
	s.mu.Lock()
	if s.told[key] {
		s.mu.Unlock()
		return
	}
	s.told[key] = true
	s.mu.Unlock()
	s.notice(level, code, detail, args)
}
