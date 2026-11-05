package library

import (
	"reflect"
	"strconv"

	"eggbox/internal/planimport"
	"eggbox/internal/routing"
)

// PlanSync says what storing the plan a subscription came with did.
type PlanSync struct {
	// Stored is false when the subscription brought no plan.
	Stored bool
	// Items is the number of items the plan has.
	Items int
	// Change lists what a refresh added to and removed from a plan that was
	// stored before.
	Change routing.PlanChange
	// Changed is set when what the plan holds differs from what was stored.
	Changed bool
	// Adopted is set when the plan became the one in force: it is the first
	// subscription, and nothing of the person's own was chosen yet.
	Adopted bool
	// Report counts what the import found, for diagnostics.
	Report planimport.Report
}

// SyncPlan stores the routing a subscription came with (see planimport),
// keeping what the person chose for its items.
//
// The first subscription's plan is adopted at once, with the main exit it
// starts on, because that is what makes importing a subscription enough to
// have a sensible routing. Later subscriptions' plans are only stored: the
// person switches to one when they want to.
func (l *Library) SyncPlan(subID string, res *planimport.Result) (PlanSync, error) {
	var ps PlanSync
	if res == nil || res.Plan == nil {
		return ps, nil
	}
	err := l.mutate(func() error {
		s := l.find(subID)
		if s == nil {
			return ErrNotFound
		}
		plan := res.Plan.Clone()
		plan.ID, plan.Kind, plan.SubID, plan.Name = routing.SubPlanID(subID), routing.PlanSubscription, subID, s.Name
		old := l.data.Routing.PlanByID(plan.ID)
		known := old != nil
		ps = PlanSync{Stored: true, Items: len(plan.Items), Report: res.Report, Changed: !known || !reflect.DeepEqual(old, plan)}
		ps.Change = l.data.Routing.SyncPlan(plan)

		if !known && len(l.data.Subscriptions) == 1 && l.data.Routing.ActiveID() == routing.BuiltinID && !l.data.Routing.Customized() {
			if err := l.data.Routing.Switch(plan.ID); err == nil {
				ps.Adopted = true
				if plan.Main != nil {
					_ = l.data.Routing.SetMain(*plan.Main)
				}
				l.addNoticeLocked("plan_adopted", map[string]string{"name": s.Name})
			}
		}
		if n := len(ps.Change.Added); n > 0 {
			l.addNoticeLocked("plan_items_added", map[string]string{"name": s.Name, "count": strconv.Itoa(n)})
		}
		if n := len(ps.Change.Removed); n > 0 {
			l.addNoticeLocked("plan_items_removed", map[string]string{"name": s.Name, "count": strconv.Itoa(n)})
		}
		return nil
	})
	return ps, err
}
