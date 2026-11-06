package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"eggbox/internal/routing"
	"eggbox/internal/ruledata"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
)

// ErrLooksLikeSubscription means an address given as a rule list leads to a
// subscription: a list of nodes, not of rules.
var ErrLooksLikeSubscription = errors.New("this looks like a subscription, not a list of rules")

// mirrors is where rule data is downloaded from, in the order they are tried.
func (s *Service) mirrors() []ruledata.Mirror {
	st := s.lib.Settings()
	if s.cfg.Mirrors != nil {
		return s.cfg.Mirrors(st.RuleMirror, st.RuleMirrorPrefix)
	}
	return ruledata.MirrorsFor(st.RuleMirror, st.RuleMirrorPrefix)
}

// urlsOf lists the rule files the items of a plan use.
func urlsOf(rows []routing.Row) []string {
	var out []string
	seen := map[string]bool{}
	for _, row := range rows {
		for _, src := range row.Item.Sources {
			if src.Kind == routing.SourceURL && !seen[src.URL] {
				seen[src.URL] = true
				out = append(out, src.URL)
			}
		}
	}
	return out
}

// NeededRuleData lists the rule files the plan in force uses and the files
// those refer to.
func (s *Service) neededRuleData(st routing.State) []string {
	urls := urlsOf(st.View().Rows)
	for _, u := range slices.Clone(urls) {
		for _, d := range s.rules.Deps(u) {
			if !slices.Contains(urls, d) {
				urls = append(urls, d)
			}
		}
	}
	return urls
}

// keptRuleData lists every rule file any plan or layer still names, so that
// switching plans does not mean downloading again.
func (s *Service) keptRuleData(st routing.State) map[string]bool {
	keep := map[string]bool{}
	add := func(items []routing.Item) {
		for _, it := range items {
			for _, src := range it.Sources {
				if src.Kind != routing.SourceURL {
					continue
				}
				keep[src.URL] = true
				for _, d := range s.rules.Deps(src.URL) {
					keep[d] = true
				}
			}
		}
	}
	add(routing.Builtin().Items)
	for _, p := range st.Plans {
		add(p.Items)
	}
	for _, o := range st.Overlays {
		if o != nil {
			add(o.Items)
		}
	}
	return keep
}

// PruneRuleData forgets the rule files nothing refers to any more.
func (s *Service) PruneRuleData() int {
	return s.rules.Prune(s.keptRuleData(s.lib.Routing()))
}

// fetchRuleData downloads files and remembers why a download failed.
func (s *Service) fetchRuleData(ctx context.Context, urls []string, opts ruledata.UpdateOptions) []ruledata.Result {
	s.rules.SetMirrors(s.mirrors())
	results := s.rules.Update(ctx, urls, opts)
	s.mu.Lock()
	for _, r := range results {
		if r.Outcome == ruledata.Failed {
			s.fetchErrs[r.URL] = ruledata.Describe(r.Err)
		} else {
			delete(s.fetchErrs, r.URL)
		}
	}
	s.mu.Unlock()
	return results
}

// summarize counts the outcomes of an update.
func summarize(results []ruledata.Result) (updated, failed int, first error) {
	for _, r := range results {
		switch r.Outcome {
		case ruledata.Updated:
			updated++
		case ruledata.Failed:
			failed++
			if first == nil {
				first = r.Err
			}
		}
	}
	return updated, failed, first
}

// retryAfter is how long a file that could not be fetched for the plan is
// left alone before it is tried again on its own.
const retryAfter = 10 * time.Minute

// fetchMissing downloads, in the background, the files the plan needs and
// does not have, so that adding an item or switching plans needs no further
// step. A failing mirror is not asked again on every change.
func (s *Service) fetchMissing(ctx context.Context, missing []string) {
	if len(missing) == 0 {
		return
	}
	now := s.cfg.Clock()
	var todo []string
	s.mu.Lock()
	for _, u := range missing {
		if t, ok := s.tried[u]; !ok || now.Sub(t) >= retryAfter {
			todo = append(todo, u)
		}
	}
	s.mu.Unlock()
	if len(todo) == 0 || !s.fetching.CompareAndSwap(false, true) {
		return
	}
	s.mu.Lock()
	for _, u := range todo {
		s.tried[u] = now
	}
	s.mu.Unlock()
	s.setBusy(func(b *Busy) { b.RuleData = true })
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.fetching.Store(false)
		defer s.setBusy(func(b *Busy) { b.RuleData = false })
		fctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		updated, failed, first := summarize(s.fetchRuleData(fctx, todo, ruledata.UpdateOptions{}))
		if ctx.Err() != nil {
			return
		}
		args := map[string]string{"updated": strconv.Itoa(updated), "failed": strconv.Itoa(failed)}
		switch {
		case failed > 0:
			s.notice(LevelWarning, "rule_data_failed", ruledata.Describe(first), args)
		case updated > 0:
			s.notice(LevelSuccess, "rule_data_updated", "", args)
		}
		s.kickPlan()
		if updated > 0 {
			s.reapplyIfOnline(ctx)
		}
	}()
}

// UpdateRuleData checks the rule files the plan in force uses and downloads
// what changed; force downloads them all again.
func (s *Service) UpdateRuleData(ctx context.Context, force bool) error {
	s.setBusy(func(b *Busy) { b.RuleData = true })
	defer s.setBusy(func(b *Busy) { b.RuleData = false })

	urls := s.neededRuleData(s.lib.Routing())
	if len(urls) == 0 {
		return nil
	}
	updated, failed, first := summarize(s.fetchRuleData(ctx, urls, ruledata.UpdateOptions{Force: force}))
	args := map[string]string{"updated": strconv.Itoa(updated), "failed": strconv.Itoa(failed)}
	switch {
	case failed > 0:
		s.notice(LevelWarning, "rule_data_failed", Explain(first).Detail, args)
	case updated > 0:
		s.notice(LevelSuccess, "rule_data_updated", "", args)
	default:
		s.notice(LevelInfo, "rule_data_current", "", args)
	}
	s.kickPlan()
	if updated > 0 {
		s.reapplyIfOnline(ctx)
	}
	return first
}

// updateScheduledRuleData checks the files that are due, quietly: it tells
// the person only when a file changed or could not be reached.
func (s *Service) updateScheduledRuleData(ctx context.Context, maxAge time.Duration) {
	urls := s.neededRuleData(s.lib.Routing())
	if len(urls) == 0 {
		return
	}
	// Files the plan lacks are being fetched already, and that run asks for
	// the same ones: the next round looks again.
	if !s.fetching.CompareAndSwap(false, true) {
		return
	}
	defer s.fetching.Store(false)
	s.setBusy(func(b *Busy) { b.RuleData = true })
	defer s.setBusy(func(b *Busy) { b.RuleData = false })
	updated, failed, first := summarize(s.fetchRuleData(ctx, urls, ruledata.UpdateOptions{MaxAge: maxAge}))
	args := map[string]string{"updated": strconv.Itoa(updated), "failed": strconv.Itoa(failed)}
	if failed > 0 {
		s.notice(LevelWarning, "rule_data_failed", Explain(first).Detail, args)
	} else if updated > 0 {
		s.notice(LevelInfo, "rule_data_updated", "", args)
	}
	s.kickPlan()
	if updated > 0 {
		s.reapplyIfRefreshed(ctx)
	}
	s.PruneRuleData()
}

// ruleDataStale reports whether a file the plan needs is missing or was last
// checked longer ago than maxAge.
func (s *Service) ruleDataStale(maxAge time.Duration) bool {
	now := s.cfg.Clock()
	urls := s.neededRuleData(s.lib.Routing())
	for _, st := range s.rules.Statuses(urls...) {
		if !st.Present || now.Sub(st.Meta.CheckedAt) >= maxAge {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ preview

// RuleSetPreview is what a rule file holds, shown before it is added.
type RuleSetPreview struct {
	// URL is the file's canonical address.
	URL string
	// Display is the address with anything after a "?" hidden.
	Display string
	// Project is the project the address was recognised as ("" if none).
	Project string
	// Name is a name for the file, from its address.
	Name string
	// Stripped is set when a mirror's prefix was taken off the address.
	Stripped bool
	// Converted is set when the address was changed to the file's own (a GitHub
	// page, or a CDN's copy).
	Converted bool
	// Swapped is set when a mihomo .mrs address was replaced by its sing-box twin.
	Swapped bool
	// Format is the format the content was recognised as.
	Format rulefmt.Format
	// Stats counts what the file holds.
	Stats rulefmt.Stats
	// Mirror is where it was downloaded from.
	Mirror string
}

// PreviewRuleSet downloads the file at an address the person pasted and says
// what it holds. Nothing is kept until KeepRuleSet.
func (s *Service) PreviewRuleSet(ctx context.Context, raw string) (*RuleSetPreview, error) {
	s.rules.SetMirrors(s.mirrors())
	f, err := s.rules.Fetch(ctx, raw)
	if err != nil {
		var pe *ruledata.ParseError
		if errors.As(err, &pe) {
			if res, perr := subscription.Parse(pe.Body); perr == nil && len(res.Nodes) > 0 {
				return nil, ErrLooksLikeSubscription
			}
		}
		return nil, err
	}
	s.mu.Lock()
	if len(s.previews) >= 4 {
		s.previews = map[string]*ruledata.Fetched{}
	}
	s.previews[f.Info.URL] = f
	s.mu.Unlock()
	return &RuleSetPreview{
		URL: f.Info.URL, Display: ruleurl.Display(f.Info.URL), Project: f.Info.Project, Name: f.Info.Name,
		Stripped: f.Info.Stripped, Converted: f.Info.Converted, Swapped: f.Info.Swapped, Format: f.Parsed.Format, Stats: f.Parsed.Stats, Mirror: f.Mirror,
	}, nil
}

// KeepRuleSet stores a file PreviewRuleSet fetched, so that an item using it
// works at once. A file that was not previewed (or is gone from memory) is
// downloaded again.
func (s *Service) KeepRuleSet(ctx context.Context, canonical string) error {
	s.mu.Lock()
	f := s.previews[canonical]
	delete(s.previews, canonical)
	s.mu.Unlock()
	if f != nil {
		if err := s.rules.Keep(f); err != nil {
			return err
		}
		s.kickPlan()
		return nil
	}
	_, failed, first := summarize(s.fetchRuleData(ctx, []string{canonical}, ruledata.UpdateOptions{}))
	if failed > 0 {
		return first
	}
	s.kickPlan()
	return nil
}

// RuleSetName suggests a name for an item made of the file at an address.
func RuleSetName(canonical string) string {
	info, err := ruleurl.Canonical(canonical)
	if err != nil || info.Name == "" {
		return "rules"
	}
	return info.Name
}
