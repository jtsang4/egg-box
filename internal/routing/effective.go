package routing

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Errors returned by the operations.
var (
	ErrNoPlan        = errors.New("there is no such plan")
	ErrNoItem        = errors.New("there is no such item")
	ErrLocked        = errors.New("this item belongs to a plan that is kept up to date elsewhere; save the plan as your own to change it")
	ErrDuplicateName = errors.New("another item already has this name")
	ErrNoName        = errors.New("the item needs a name")
	ErrNotMine       = errors.New("only a plan of your own can be changed this way")
)

// Origin says where a row of the effective plan comes from.
type Origin int

const (
	// OriginPlan: an item of the plan itself.
	OriginPlan Origin = iota
	// OriginOverlay: an item the user added to a plan that is kept up to date elsewhere.
	OriginOverlay
	// OriginMine: an item of a plan of the user's own.
	OriginMine
)

// Row is one item of the effective plan, with the user's choices applied.
type Row struct {
	Item   Item
	Origin Origin
	// DefaultDest and DefaultOn are what the plan itself says, which "restore
	// default" goes back to; for the user's own items they are the item's own.
	DefaultDest Dest
	DefaultOn   bool
	// Overridden reports a choice of the user's laid over the plan.
	Overridden bool
	// Locked rows cannot be moved or removed.
	Locked bool
	// Extra are the ids of the sources the user added to an item the plan
	// itself owns; the others cannot be removed from it.
	Extra []string
}

// View is the plan in force, ready to show or to turn into a configuration.
type View struct {
	Plan    *Plan
	Overlay *Overlay
	Rows    []Row
	// Fallback is where traffic no row claims goes.
	Fallback           Dest
	FallbackDefault    Dest
	FallbackOverridden bool
}

// Rows returns just the items.
func (v View) Items() []Item {
	out := make([]Item, len(v.Rows))
	for i, r := range v.Rows {
		out[i] = r.Item
	}
	return out
}

// Row finds a row by item id.
func (v View) Row(id string) (Row, int, bool) {
	for i, r := range v.Rows {
		if r.Item.ID == id {
			return r, i, true
		}
	}
	return Row{}, -1, false
}

// PlanByID finds a plan: the shipped one, or one of the stored ones.
func (s State) PlanByID(id string) *Plan {
	if id == "" || id == BuiltinID {
		return Builtin()
	}
	for _, p := range s.Plans {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// ActivePlan is the plan in force; the shipped plan when the active one is gone.
func (s State) ActivePlan() *Plan {
	if p := s.PlanByID(s.Active); p != nil {
		return p
	}
	return Builtin()
}

// View is the effective active plan.
func (s State) View() View { return s.ViewOf(s.ActivePlan().ID) }

// ViewOf is the effective form of a plan: its items with the user's choices
// applied and the user's own items in place.
func (s State) ViewOf(id string) View {
	p := s.PlanByID(id)
	if p == nil {
		p = Builtin()
	}
	if p.Kind == PlanMine {
		v := View{Plan: p, Overlay: &Overlay{}, Fallback: p.Fallback, FallbackDefault: p.Fallback}
		for _, it := range p.Items {
			v.Rows = append(v.Rows, Row{Item: it, Origin: OriginMine, DefaultDest: it.Dest, DefaultOn: it.Enabled})
		}
		return v
	}
	ov := s.Overlays[p.ID]
	if ov == nil {
		ov = &Overlay{}
	}
	v := View{Plan: p, Overlay: ov, Fallback: p.Fallback, FallbackDefault: p.Fallback}
	if ov.Fallback != nil {
		v.Fallback, v.FallbackOverridden = *ov.Fallback, true
	}
	var base []Row
	for _, it := range p.Items {
		row := Row{Item: it, Origin: OriginPlan, DefaultDest: it.Dest, DefaultOn: it.Enabled, Locked: true}
		if d, ok := lookup(ov.Dest, it); ok {
			row.Item.Dest, row.Overridden = d, true
		}
		if on, ok := lookup(ov.Enabled, it); ok {
			row.Item.Enabled, row.Overridden = on, true
		}
		if extra, ok := lookup(ov.Extra, it); ok && len(extra) > 0 {
			row.Item.Sources = append(cloneSources(it.Sources), cloneSources(extra)...)
			for _, e := range extra {
				row.Extra = append(row.Extra, e.ID)
			}
			row.Overridden = true
		}
		base = append(base, row)
	}
	placed := make([]bool, len(ov.Items))
	for _, b := range base {
		keys := b.Item.Keys()
		for i, o := range ov.Items {
			if !placed[i] && o.Anchor != "" && slices.Contains(keys, o.Anchor) {
				v.Rows = append(v.Rows, overlayRow(o))
				placed[i] = true
			}
		}
		v.Rows = append(v.Rows, b)
	}
	for i, o := range ov.Items {
		if !placed[i] {
			v.Rows = append(v.Rows, overlayRow(o))
		}
	}
	return v
}

func overlayRow(o Item) Row {
	return Row{Item: o, Origin: OriginOverlay, DefaultDest: o.Dest, DefaultOn: o.Enabled}
}

// lookup finds the user's choice for a plan item under any name it answers to.
func lookup[T any](m map[string]T, it Item) (T, bool) {
	var zero T
	if len(m) == 0 {
		return zero, false
	}
	for _, k := range it.Keys() {
		if v, ok := m[k]; ok {
			return v, true
		}
	}
	return zero, false
}

func primary(it Item) string { return NormName(it.Name) }

// overlayOf returns the overlay of the active followed plan, creating it.
func (s *State) overlayOf(id string) *Overlay {
	if s.Overlays == nil {
		s.Overlays = map[string]*Overlay{}
	}
	ov := s.Overlays[id]
	if ov == nil {
		ov = &Overlay{}
		s.Overlays[id] = ov
	}
	return ov
}

// mineOf returns the stored plan of the user's own with that id.
func (s *State) mineOf(id string) *Plan {
	for _, p := range s.Plans {
		if p.ID == id && p.Kind == PlanMine {
			return p
		}
	}
	return nil
}

// activeEdit says where the active plan's items can be changed: the plan itself
// when it is the user's own, otherwise the overlay.
func (s *State) activeEdit() (mine *Plan, ov *Overlay, base *Plan) {
	base = s.ActivePlan()
	if base.Kind == PlanMine {
		return s.mineOf(base.ID), nil, base
	}
	return nil, s.overlayOf(base.ID), base
}

// SetItemDest changes where an item of the active plan goes. For an item of a
// plan kept up to date elsewhere the choice is laid over it, so it outlives a
// refresh and a plan switch.
func (s *State) SetItemDest(itemID string, d Dest) error {
	if err := d.Valid(false); err != nil {
		return err
	}
	return s.editItem(itemID, func(it *Item) { it.Dest = d.Clone() },
		func(ov *Overlay, it Item) {
			if ov.Dest == nil {
				ov.Dest = map[string]Dest{}
			}
			if d.Equal(it.Dest) {
				delete(ov.Dest, primary(it))
				for _, k := range it.Keys() {
					delete(ov.Dest, k)
				}
				return
			}
			clearKeys(ov.Dest, it)
			ov.Dest[primary(it)] = d.Clone()
		})
}

// SetItemEnabled turns an item of the active plan on or off.
func (s *State) SetItemEnabled(itemID string, on bool) error {
	return s.editItem(itemID, func(it *Item) { it.Enabled = on },
		func(ov *Overlay, it Item) {
			if ov.Enabled == nil {
				ov.Enabled = map[string]bool{}
			}
			clearKeys(ov.Enabled, it)
			if on != it.Enabled {
				ov.Enabled[primary(it)] = on
			}
		})
}

func clearKeys[T any](m map[string]T, it Item) {
	for _, k := range it.Keys() {
		delete(m, k)
	}
}

// editItem applies a change to an item of the active plan: directly when the
// item is the user's own (an item of their plan or one they added), as an
// overlay entry when it belongs to the plan.
func (s *State) editItem(id string, own func(*Item), over func(*Overlay, Item)) error {
	mine, ov, base := s.activeEdit()
	if mine != nil {
		for i := range mine.Items {
			if mine.Items[i].ID == id {
				own(&mine.Items[i])
				return nil
			}
		}
		return ErrNoItem
	}
	for i := range ov.Items {
		if ov.Items[i].ID == id {
			own(&ov.Items[i])
			return nil
		}
	}
	for _, it := range base.Items {
		if it.ID == id {
			over(ov, it)
			return nil
		}
	}
	return ErrNoItem
}

// SetFallback changes where traffic no item claims goes.
func (s *State) SetFallback(d Dest) error {
	if err := d.Valid(false); err != nil {
		return err
	}
	mine, ov, base := s.activeEdit()
	if mine != nil {
		mine.Fallback = d.Clone()
		return nil
	}
	if d.Equal(base.Fallback) {
		ov.Fallback = nil
	} else {
		c := d.Clone()
		ov.Fallback = &c
	}
	return nil
}

// SetMain changes the main exit.
func (s *State) SetMain(d Dest) error {
	if err := d.Valid(true); err != nil {
		return err
	}
	s.Main = d.Clone()
	if d.Kind == DestNode && d.Node != nil {
		s.remember(*d.Node)
	}
	return nil
}

// remember puts a node first among the recently chosen ones.
func (s *State) remember(id NodeID) {
	out := make([]NodeID, 0, MaxRecent)
	out = append(out, id)
	for _, r := range s.Recent {
		if !r.Same(id) && len(out) < MaxRecent {
			out = append(out, r)
		}
	}
	s.Recent = out
}

// RestoreDefault returns the active plan's items and fallback to what the plan
// itself says. The items the user added stay.
func (s *State) RestoreDefault() error {
	mine, ov, _ := s.activeEdit()
	if mine != nil {
		return ErrNotMine
	}
	ov.Dest, ov.Enabled, ov.Fallback = nil, nil, nil
	return nil
}

// ------------------------------------------------------- adding and moving

// AddItem adds an item to the active plan, before the item beforeID ("" puts it
// last). The item gets an id when it has none.
func (s *State) AddItem(it Item, beforeID string) (Item, error) {
	it.Name = strings.TrimSpace(it.Name)
	if it.Name == "" {
		return Item{}, ErrNoName
	}
	if err := it.Dest.Valid(false); err != nil {
		return Item{}, err
	}
	v := s.View()
	for _, r := range v.Rows {
		if SameName(r.Item, it) && r.Item.ID != it.ID {
			return Item{}, ErrDuplicateName
		}
	}
	if it.ID == "" {
		it.ID = NewID("i")
	}
	it.Anchor = ""
	// An item made to go direct is made of local services; it stays so when
	// it is sent through a node later.
	it.Domestic = it.Domestic || it.Dest.Kind == DestDirect
	mine, ov, _ := s.activeEdit()
	if mine != nil {
		mine.Items = append(mine.Items, it)
	} else {
		ov.Items = append(ov.Items, it)
	}
	order := make([]string, 0, len(v.Rows)+1)
	inserted := false
	for _, r := range v.Rows {
		if r.Item.ID == beforeID {
			order = append(order, it.ID)
			inserted = true
		}
		order = append(order, r.Item.ID)
	}
	if !inserted {
		order = append(order, it.ID)
	}
	if err := s.Reorder(order); err != nil {
		return Item{}, err
	}
	return it, nil
}

// DeleteItem removes an item the user owns from the active plan.
func (s *State) DeleteItem(id string) error {
	mine, ov, _ := s.activeEdit()
	if mine != nil {
		for i := range mine.Items {
			if mine.Items[i].ID == id {
				mine.Items = slices.Delete(mine.Items, i, i+1)
				return nil
			}
		}
		return ErrNoItem
	}
	for i := range ov.Items {
		if ov.Items[i].ID == id {
			ov.Items = slices.Delete(ov.Items, i, i+1)
			return nil
		}
	}
	for _, it := range s.ActivePlan().Items {
		if it.ID == id {
			return ErrLocked
		}
	}
	return ErrNoItem
}

// Move shifts an item one place up (-1) or down (+1) among the items it may
// pass.
func (s *State) Move(id string, delta int) error {
	v := s.View()
	row, at, ok := v.Row(id)
	if !ok {
		return ErrNoItem
	}
	if row.Locked {
		return ErrLocked
	}
	to := at + delta
	if to < 0 || to >= len(v.Rows) {
		return nil
	}
	order := make([]string, len(v.Rows))
	for i, r := range v.Rows {
		order[i] = r.Item.ID
	}
	order[at], order[to] = order[to], order[at]
	return s.Reorder(order)
}

// MoveBefore puts an item right before another one ("" puts it last).
func (s *State) MoveBefore(id, beforeID string) error {
	v := s.View()
	row, _, ok := v.Row(id)
	if !ok {
		return ErrNoItem
	}
	if row.Locked {
		return ErrLocked
	}
	var order []string
	for _, r := range v.Rows {
		if r.Item.ID != id {
			order = append(order, r.Item.ID)
		}
	}
	at := slices.Index(order, beforeID)
	if at < 0 {
		at = len(order)
	}
	order = slices.Insert(order, at, id)
	return s.Reorder(order)
}

// Reorder sets the order of the active plan's items, given as item ids. In a
// plan kept up to date elsewhere the plan's own items keep their order and only
// the user's items move around them.
func (s *State) Reorder(order []string) error {
	mine, ov, base := s.activeEdit()
	if mine != nil {
		pos := map[string]int{}
		for i, id := range order {
			pos[id] = i
		}
		if len(order) != len(mine.Items) {
			return ErrNoItem
		}
		items := make([]Item, len(mine.Items))
		for _, it := range mine.Items {
			i, ok := pos[it.ID]
			if !ok {
				return ErrNoItem
			}
			items[i] = it
		}
		mine.Items = items
		return nil
	}
	var baseIDs []string
	for _, it := range base.Items {
		baseIDs = append(baseIDs, it.ID)
	}
	var seenBase []string
	for _, id := range order {
		if slices.Contains(baseIDs, id) {
			seenBase = append(seenBase, id)
		}
	}
	if !slices.Equal(baseIDs, seenBase) {
		return ErrLocked
	}
	own := map[string]Item{}
	for _, it := range ov.Items {
		own[it.ID] = it
	}
	byID := map[string]Item{}
	for _, it := range base.Items {
		byID[it.ID] = it
	}
	var items []Item
	for i, id := range order {
		o, ok := own[id]
		if !ok {
			continue
		}
		// The anchor is the next item of the plan itself, if any.
		o.Anchor = ""
		for _, later := range order[i+1:] {
			if b, ok := byID[later]; ok {
				o.Anchor = primary(b)
				break
			}
		}
		items = append(items, o)
	}
	if len(items) != len(ov.Items) {
		return ErrNoItem
	}
	ov.Items = items
	return nil
}

// ---------------------------------------------------------------- plans

// Switch makes another plan the active one. What the person chose for an item
// carries over to the item of the same name in the plan they switch to (when
// that plan is kept up to date elsewhere; a plan of their own keeps its own
// choices).
func (s *State) Switch(id string) error {
	if id == "" {
		id = BuiltinID
	}
	target := s.PlanByID(id)
	if target == nil {
		return ErrNoPlan
	}
	if s.ActiveID() == id {
		s.Active = id
		return nil
	}
	old := s.View()
	s.Active = id
	if target.Kind == PlanMine {
		return nil
	}
	ov := s.overlayOf(target.ID)
	for _, row := range old.Rows {
		explicit := row.Origin == OriginMine || row.Overridden
		if !explicit || row.Origin == OriginOverlay {
			continue
		}
		keys := row.Item.Keys()
		for _, it := range target.Items {
			if !slices.ContainsFunc(it.Keys(), func(k string) bool { return slices.Contains(keys, k) }) {
				continue
			}
			clearKeys(ov.Dest, it)
			clearKeys(ov.Enabled, it)
			if !row.Item.Dest.Equal(it.Dest) {
				if ov.Dest == nil {
					ov.Dest = map[string]Dest{}
				}
				ov.Dest[primary(it)] = row.Item.Dest.Clone()
			}
			if row.Item.Enabled != it.Enabled {
				if ov.Enabled == nil {
					ov.Enabled = map[string]bool{}
				}
				ov.Enabled[primary(it)] = row.Item.Enabled
			}
			carryExtras(ov, it, row)
			break
		}
	}
	if old.FallbackOverridden || old.Plan.Kind == PlanMine {
		if old.Fallback.Equal(target.Fallback) {
			ov.Fallback = nil
		} else {
			f := old.Fallback.Clone()
			ov.Fallback = &f
		}
	}
	return nil
}

// carryExtras gives the item of the plan being switched to the sources the
// person added to the item of the same name in the plan being left, unless it
// has them already.
func carryExtras(ov *Overlay, it Item, from Row) {
	if len(from.Extra) == 0 {
		return
	}
	have := map[string]bool{}
	for _, s := range it.Sources {
		have[sourceKey(s)] = true
	}
	for _, s := range ov.Extra[primary(it)] {
		have[sourceKey(s)] = true
	}
	for _, s := range from.Item.Sources {
		if !slices.Contains(from.Extra, s.ID) || have[sourceKey(s)] {
			continue
		}
		if ov.Extra == nil {
			ov.Extra = map[string][]Source{}
		}
		ov.Extra[primary(it)] = append(ov.Extra[primary(it)], s.clone())
	}
}

// SaveAsMine keeps the effective plan as a plan of the user's own and
// switches to it.
func (s *State) SaveAsMine(name string) (*Plan, error) { return s.NewPlan(name, s.ActiveID()) }

// NewPlan makes a plan of the user's own and switches to it: a copy of the plan
// from as the person sees it (their choices and items included), or, when from
// is "", one with no items, where everything but the exceptions goes the way
// of the fallback until items are added.
func (s *State) NewPlan(name, from string) (*Plan, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrNoName
	}
	p := &Plan{ID: NewID("p"), Kind: PlanMine, Name: name, Fallback: Follow()}
	if from != "" {
		if s.PlanByID(from) == nil {
			return nil, ErrNoPlan
		}
		v := s.ViewOf(from)
		p.Fallback, p.Origin = v.Fallback.Clone(), v.Plan.Name
		if v.Plan.Kind == PlanMine {
			p.Origin = v.Plan.Origin
		}
		for _, r := range v.Rows {
			it := r.Item.Clone()
			it.Anchor = ""
			p.Items = append(p.Items, it)
		}
	}
	s.Plans = append(s.Plans, p)
	s.Active = p.ID
	return p, nil
}

// DeletePlan removes a plan the user keeps. When it was the active one the
// shipped plan takes over.
func (s *State) DeletePlan(id string) error {
	for i, p := range s.Plans {
		if p.ID != id {
			continue
		}
		if p.Kind != PlanMine {
			return ErrNotMine
		}
		s.Plans = slices.Delete(s.Plans, i, i+1)
		delete(s.Overlays, id)
		if s.ActiveID() == id {
			s.Active = BuiltinID
		}
		return nil
	}
	return ErrNoPlan
}

// RenamePlan renames a plan of the user's own.
func (s *State) RenamePlan(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrNoName
	}
	p := s.mineOf(id)
	if p == nil {
		return ErrNotMine
	}
	p.Name = name
	return nil
}

// CopyItemFrom copies an item of another plan into the active one, before
// beforeID ("" puts it last). The copy keeps the item's sources and the
// destination and switch the other plan has for it.
func (s *State) CopyItemFrom(planID, itemID, beforeID string) (Item, error) {
	if s.PlanByID(planID) == nil {
		return Item{}, ErrNoPlan
	}
	row, _, ok := s.ViewOf(planID).Row(itemID)
	if !ok {
		return Item{}, ErrNoItem
	}
	it := row.Item.Clone()
	it.ID, it.Anchor = "", ""
	return s.AddItem(it, beforeID)
}

// PlanChange says what a refresh of a plan changed.
type PlanChange struct {
	Added   []string
	Removed []string
}

// Empty reports whether nothing was added or removed.
func (c PlanChange) Empty() bool { return len(c.Added) == 0 && len(c.Removed) == 0 }

// SyncPlan stores the fresh content of a plan that follows a subscription. The
// person's choices stay (they are kept by item name); the result lists the
// items the refresh added and removed since the plan was last looked at. The
// first time a plan is stored nothing counts as added.
func (s *State) SyncPlan(p *Plan) PlanChange {
	var change PlanChange
	found := false
	for i, old := range s.Plans {
		if old.ID == p.ID {
			s.Plans[i] = p
			found = true
			break
		}
	}
	if !found {
		s.Plans = append(s.Plans, p)
	}
	ov := s.overlayOf(p.ID)
	cur := make([]string, 0, len(p.Items))
	curKeys := make([]string, 0, len(p.Items))
	for _, it := range p.Items {
		cur = append(cur, it.Name)
		curKeys = append(curKeys, primary(it))
	}
	if len(ov.Seen) > 0 {
		seenKeys := make([]string, len(ov.Seen))
		for i, n := range ov.Seen {
			seenKeys[i] = NormName(n)
		}
		for i, it := range p.Items {
			if !slices.Contains(seenKeys, curKeys[i]) {
				change.Added = append(change.Added, it.Name)
			}
		}
		for i, n := range ov.Seen {
			if !slices.Contains(curKeys, seenKeys[i]) {
				change.Removed = append(change.Removed, n)
			}
		}
	}
	ov.Seen = cur
	return change
}

// DetachPlan turns a plan that followed a subscription into one of the user's
// own, with the user's choices folded in, for when the subscription is gone. It
// returns the new plan, which takes the old plan's place as the active one.
func (s *State) DetachPlan(planID, origin string) (*Plan, error) {
	src := s.PlanByID(planID)
	if src == nil || src.Kind == PlanBuiltin {
		return nil, ErrNoPlan
	}
	if src.Kind == PlanMine {
		return src, nil
	}
	v := s.ViewOf(planID)
	p := &Plan{ID: NewID("p"), Kind: PlanMine, Name: src.Name, Fallback: v.Fallback.Clone(), Origin: origin}
	for _, r := range v.Rows {
		it := r.Item.Clone()
		it.Anchor = ""
		p.Items = append(p.Items, it)
	}
	active := s.ActiveID() == planID
	for i, q := range s.Plans {
		if q.ID == planID {
			s.Plans = slices.Delete(s.Plans, i, i+1)
			break
		}
	}
	delete(s.Overlays, planID)
	s.Plans = append(s.Plans, p)
	if active {
		s.Active = p.ID
	}
	return p, nil
}

// String describes a destination for logs and tests, never for people.
func (d Dest) String() string {
	switch d.Kind {
	case DestRegion:
		return "region:" + d.Region
	case DestNode:
		if d.Node != nil {
			return fmt.Sprintf("node:%s/%s", d.Node.Sub, d.Node.Name)
		}
	}
	return string(d.Kind)
}

// Customized reports whether the person has chosen anything of their own yet:
// an exception, a plan of their own, a main exit other than the automatic one,
// or a change laid over a plan. A subscription's plan is only adopted for them
// while this is false.
func (s *State) Customized() bool {
	if len(s.Exceptions) > 0 || s.Main.Kind != DestAuto {
		return true
	}
	for _, p := range s.Plans {
		if p.Kind == PlanMine {
			return true
		}
	}
	for _, o := range s.Overlays {
		if o != nil && (len(o.Dest) > 0 || len(o.Enabled) > 0 || len(o.Items) > 0 || o.Fallback != nil) {
			return true
		}
	}
	return false
}
