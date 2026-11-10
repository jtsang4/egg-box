package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"eggbox/internal/agentapi"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/planimport"
	"eggbox/internal/redact"
	"eggbox/internal/routing"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
)

// ---------------------------------------------------------------- runtime ops

// Select makes a group item the live groups show (a node's tag, a region
// group or "Auto") the main exit, and remembers the choice.
func (s *Service) Select(ctx context.Context, tag string) error {
	s.mu.Lock()
	ref, isNode := s.tagIndex[tag]
	s.mu.Unlock()
	var d routing.Dest
	switch {
	case isNode:
		d = routing.ToNode(ref.ID)
	case tag == configgen.TagAuto:
		d = routing.Auto()
	case strings.HasPrefix(tag, configgen.RegionPrefix):
		d = routing.ToRegion(strings.TrimPrefix(tag, configgen.RegionPrefix))
	default:
		return fmt.Errorf("%q cannot be the main exit", tag)
	}
	return s.SetMainExit(ctx, d)
}

// SetMainExit chooses the main exit: the fastest node, the fastest node of a
// region or one node. It works while disconnected too, the choice then applies
// to the next connection.
func (s *Service) SetMainExit(ctx context.Context, d routing.Dest) error {
	err := s.EditRouting(ctx, func(st *routing.State) error { return st.SetMain(d) })
	if err != nil {
		s.report("select_failed", err)
	}
	return err
}

// EditRouting changes the routing state (the plan in force and everything laid
// over it, the exceptions, the choices made about nodes) with fn, which works
// on a copy: when it fails nothing is kept. The result is applied when the
// proxy is on; choices that only move a selection do not restart the core.
func (s *Service) EditRouting(ctx context.Context, fn func(*routing.State) error) error {
	if err := s.lib.UpdateRouting(fn); err != nil {
		return err
	}
	s.kickPlan()
	s.reapplyIfOnline(ctx)
	return nil
}

// Routing returns the routing state.
func (s *Service) Routing() routing.State { return s.lib.Routing() }

// TestLatency measures the delay of a node or a whole group (by tag).
func (s *Service) TestLatency(ctx context.Context, tag string) error {
	s.setBusy(func(b *Busy) { b.Testing[tag] = true })
	defer s.setBusy(func(b *Busy) { delete(b.Testing, tag) })
	if _, err := s.agent.URLTest(ctx, tag); err != nil {
		s.report("test_failed", err)
		return err
	}
	return nil
}

// SetMode switches between Rule, Global and Direct while connected.
func (s *Service) SetMode(ctx context.Context, mode string) error {
	if _, err := s.agent.SetMode(ctx, mode); err != nil {
		s.report("mode_failed", err)
		return err
	}
	return nil
}

// CloseConnection drops one tracked connection.
func (s *Service) CloseConnection(ctx context.Context, id string) error {
	_, err := s.agent.CloseConnection(ctx, id)
	return err
}

// CloseAllConnections drops every tracked connection.
func (s *Service) CloseAllConnections(ctx context.Context) error {
	_, err := s.agent.CloseAllConnections(ctx)
	return err
}

// RetryCleanup asks the service to run its resource cleanup again.
func (s *Service) RetryCleanup(ctx context.Context) error {
	_, err := s.agent.RetryCleanup(ctx)
	if err != nil {
		s.report("cleanup_failed", err)
	}
	return err
}

// SetAgentSettings changes the service's own settings.
func (s *Service) SetAgentSettings(ctx context.Context, fn func(*agentapi.UpdateSettingsRequest)) error {
	req := &agentapi.UpdateSettingsRequest{}
	fn(req)
	settings, err := s.agent.UpdateSettings(ctx, req)
	if err != nil {
		return err
	}
	s.update(func(sn *Snapshot) { sn.AgentSettings = settings })
	return nil
}

// ------------------------------------------------------------- subscriptions

// AddSubscription adds a link, downloads it and, when the proxy is on, applies
// the new nodes. A link that cannot be downloaded or understood is not kept.
func (s *Service) AddSubscription(ctx context.Context, name, link string, opts library.RemoteOptions) (library.Subscription, *library.RefreshResult, error) {
	sub, err := s.lib.AddRemote(name, link, opts)
	if err != nil {
		return library.Subscription{}, nil, err
	}
	res, err := s.refresh(ctx, sub.ID)
	if err != nil {
		_ = s.lib.Remove(sub.ID)
		s.report("subscription_add_failed", err)
		return library.Subscription{}, nil, err
	}
	sub, _ = s.lib.Subscription(sub.ID)
	s.notice(LevelSuccess, "subscription_added", "", map[string]string{"name": sub.Name, "nodes": strconv.Itoa(res.NodeCount)})
	s.reapplyIfOnline(ctx)
	return sub, res, nil
}

// ImportNodes adds nodes from pasted text: share links, a Clash profile or a
// sing-box configuration.
func (s *Service) ImportNodes(ctx context.Context, name, text string) (library.Subscription, error) {
	res, err := subscription.Parse([]byte(text))
	if err != nil {
		s.report("import_failed", err)
		return library.Subscription{}, err
	}
	sub, err := s.lib.AddLocal(name, res)
	if err != nil {
		s.report("import_failed", err)
		return library.Subscription{}, err
	}
	// A pasted profile may carry its routing too.
	if imp, ierr := planimport.Import([]byte(text), planimport.Options{SubID: sub.ID}); ierr == nil {
		if _, err := s.lib.SyncPlan(sub.ID, imp); err != nil {
			s.report("import_failed", err)
		}
	}
	s.notice(LevelSuccess, "nodes_imported", "", map[string]string{"nodes": strconv.Itoa(len(res.Nodes))})
	s.deliverLibraryNotices()
	s.reapplyIfOnline(ctx)
	return sub, nil
}

// EditImportedNode replaces the node whose key is key in an imported
// subscription with the one a share link describes (see library.EditNode),
// and applies the change when connected. It returns the node as stored.
func (s *Service) EditImportedNode(ctx context.Context, subID, key, link string) (subscription.Node, error) {
	stored, err := s.lib.EditNode(subID, key, link)
	if err != nil {
		return subscription.Node{}, err
	}
	s.reapplyIfOnline(ctx)
	return stored, nil
}

// refreshFlight is a refresh under way and, once done, what came of it.
type refreshFlight struct {
	done chan struct{}
	res  *library.RefreshResult
	err  error
}

// refresh downloads a subscription again. When the same subscription is being
// downloaded already (a due one is picked up by the schedule at the moment it
// is added, or a click comes while the schedule is at it) the caller waits for
// that download and shares its outcome, so nothing is fetched twice and the
// add of a new subscription is not undone by a collision.
func (s *Service) refresh(ctx context.Context, id string) (*library.RefreshResult, error) {
	s.refreshMu.Lock()
	if f, ok := s.refreshing[id]; ok {
		s.refreshMu.Unlock()
		select {
		case <-f.done:
			return f.res, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// A refresh that dies half way leaves its waiters an error, not an empty result.
	f := &refreshFlight{done: make(chan struct{}), err: errors.New("the update did not finish")}
	s.refreshing[id] = f
	s.refreshMu.Unlock()
	s.setBusy(func(b *Busy) { b.Refreshing[id] = true })

	defer func() {
		s.setBusy(func(b *Busy) { delete(b.Refreshing, id) })
		s.refreshMu.Lock()
		delete(s.refreshing, id)
		s.refreshMu.Unlock()
		close(f.done)
	}()
	f.res, f.err = s.lib.Refresh(ctx, id, library.RefreshOptions{Client: s.cfg.HTTPClient})
	if f.err == nil {
		s.deliverLibraryNotices()
	}
	return f.res, f.err
}

// refreshed reports whether a refresh changed anything the configuration is
// made of: the nodes or the plan.
func refreshed(res *library.RefreshResult) bool {
	return res.Changed || res.Plan.Changed
}

// RefreshSubscription downloads a subscription again.
func (s *Service) RefreshSubscription(ctx context.Context, id string) (*library.RefreshResult, error) {
	sub, ok := s.lib.Subscription(id)
	if !ok {
		return nil, library.ErrNotFound
	}
	res, err := s.refresh(ctx, id)
	if err != nil {
		s.report("subscription_refresh_failed", err)
		return nil, err
	}
	args := map[string]string{"name": sub.Name, "nodes": strconv.Itoa(res.NodeCount), "added": strconv.Itoa(res.Added), "removed": strconv.Itoa(res.Removed)}
	switch {
	case res.NotModified:
		s.notice(LevelInfo, "subscription_current", "", args)
	default:
		s.notice(LevelSuccess, "subscription_updated", "", args)
	}
	if refreshed(res) {
		s.reapplyIfRefreshed(ctx)
	}
	return res, nil
}

// RefreshAll refreshes every enabled remote subscription, one after another.
func (s *Service) RefreshAll(ctx context.Context) (updated int, firstErr error) {
	changed := false
	for _, sub := range s.lib.Subscriptions() {
		if sub.Kind != library.KindRemote || !sub.Enabled {
			continue
		}
		res, err := s.refresh(ctx, sub.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		updated++
		changed = changed || refreshed(res)
	}
	if firstErr != nil {
		s.report("subscription_refresh_failed", firstErr)
	} else {
		s.notice(LevelSuccess, "subscriptions_updated", "", map[string]string{"count": strconv.Itoa(updated)})
	}
	if changed {
		s.reapplyIfRefreshed(ctx)
	}
	return updated, firstErr
}

// EditSubscription changes a subscription's settings and applies the result
// when the proxy is on.
func (s *Service) EditSubscription(ctx context.Context, id string, fn func(*library.Subscription)) error {
	if err := s.lib.Edit(id, fn); err != nil {
		return err
	}
	s.reapplyIfOnline(ctx)
	return nil
}

// RemoveSubscription deletes a subscription.
func (s *Service) RemoveSubscription(ctx context.Context, id string) error {
	if err := s.lib.Remove(id); err != nil {
		return err
	}
	s.reapplyIfOnline(ctx)
	return nil
}

// reapplyIfRefreshed applies refreshed nodes to a running connection, when the
// user wants that.
func (s *Service) reapplyIfRefreshed(ctx context.Context) {
	if s.lib.Settings().ReapplyAfterRefresh {
		s.reapplyIfOnline(ctx)
	}
}

func (s *Service) reapplyIfOnline(ctx context.Context) {
	if !s.Snapshot().Phase.Online() {
		return
	}
	if _, err := s.Apply(ctx); err != nil && !isNoNodes(err) && err != ErrNoNodes {
		s.report("apply_failed", err)
	}
}

// ----------------------------------------------------------------- prefs

// SavePrefs stores routing preferences and applies them when the proxy is on.
func (s *Service) SavePrefs(ctx context.Context, p configgen.Prefs) (adjusted []string, err error) {
	adjusted, err = s.lib.SetPrefs(p)
	if err != nil {
		return nil, err
	}
	s.kickPlan()
	s.reapplyIfOnline(ctx)
	return adjusted, nil
}

// -------------------------------------------------------------- diagnostics

// ExportDiagnostics builds a report for a bug report: the service's own
// redacted report plus a summary of the application's state. It contains no
// links, credentials, server addresses or configuration content.
func (s *Service) ExportDiagnostics(ctx context.Context) (filename string, data []byte, err error) {
	snap := s.Snapshot()
	subs := s.lib.Subscriptions()
	type subInfo struct {
		Kind      string `json:"kind"`
		Enabled   bool   `json:"enabled"`
		Nodes     int    `json:"nodes"`
		Format    string `json:"format,omitempty"`
		LastError string `json:"lastError,omitempty"`
		Age       string `json:"sinceUpdate,omitempty"`
	}
	var infos []subInfo
	for _, sub := range subs {
		si := subInfo{Kind: string(sub.Kind), Enabled: sub.Enabled, Nodes: sub.NodeCount, Format: sub.Format, LastError: sub.LastError}
		if !sub.UpdatedAt.IsZero() {
			si.Age = s.cfg.Clock().Sub(sub.UpdatedAt).Round(time.Minute).String()
		}
		infos = append(infos, si)
	}
	prefs := s.lib.Prefs()
	st := s.lib.Routing()
	view := st.View()
	states := map[string]int{}
	dests := map[string]int{}
	enabled := 0
	for _, row := range view.Rows {
		if row.Item.Enabled {
			enabled++
		}
		dests[string(row.Item.Dest.Kind)]++
		switch snap.Plan.Items[row.Item.ID].State {
		case ItemReady:
			states["ready"]++
		case ItemOff:
			states["off"]++
		case ItemUnavailable:
			states["unavailable"]++
		case ItemPartial:
			states["partial"]++
		}
	}
	// Only counts and classes: the names of a provider's groups are its
	// content and stay out of reports.
	planDoc := map[string]any{
		"kind": string(st.ActivePlan().Kind), "items": len(view.Rows), "enabled": enabled,
		"states": states, "destinations": dests, "exceptions": len(st.Exceptions),
		"plans": len(st.Plans) + 1, "main": string(st.Main.Kind), "fallback": string(view.Fallback.Kind),
		"gone": len(snap.Plan.Gone), "missingFiles": snap.Plan.Missing,
	}
	var urls []string
	for u := range snap.Plan.Sources {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	var rulesInfo []map[string]any
	for _, u := range urls {
		src := snap.Plan.Sources[u]
		rulesInfo = append(rulesInfo, map[string]any{
			"url": ruleurl.Display(u), "present": src.Present, "format": src.Format, "size": src.Size,
			"updated": src.Updated, "checked": src.Checked, "domain": src.Domain, "ip": src.IP,
			"unsupported": src.Unsupported, "error": src.Err, "mirror": src.Mirror,
		})
	}
	app := map[string]any{
		"version": s.cfg.Version,
		"os":      runtime.GOOS, "arch": runtime.GOARCH,
		"link":          snap.Link.State.String(),
		"linkError":     snap.Link.Error,
		"phase":         snap.Phase.String(),
		"subscriptions": infos,
		"plan":          planDoc,
		"lanDirect":     prefs.LANDirect,
		"ipv6":          prefs.IPv6,
		"tunStack":      prefs.TunStack,
		"ruleData":      rulesInfo,
		"applied":       map[string]any{"nodes": snap.Applied.Nodes, "dropped": len(snap.Applied.Dropped), "warnings": len(snap.Applied.Warnings)},
	}
	doc := map[string]any{"app": app}
	var logTail []string
	for _, l := range snap.Logs[max(0, len(snap.Logs)-150):] {
		logTail = append(logTail, fmt.Sprintf("%s %s %s %s", l.Time.Format("15:04:05"), l.Level, l.Source, l.Message))
	}
	doc["recentLog"] = logTail
	if snap.Link.Ready() {
		if rep, aerr := s.agent.Diagnostics(ctx); aerr == nil {
			filename = rep.GetSuggestedFilename()
			var agentDoc any
			if json.Unmarshal(rep.GetJson(), &agentDoc) == nil {
				doc["agent"] = agentDoc
			}
		} else {
			doc["agentError"] = aerr.Error()
		}
	}
	if filename == "" {
		filename = "egg-box-diagnostics-" + s.cfg.Clock().Format("20060102-150405") + ".json"
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", nil, err
	}
	// One more pass over everything: nothing sensitive may slip in through a
	// log line or an error message.
	clean, err := redact.New().JSON(raw)
	if err != nil {
		return "", nil, err
	}
	return filename, clean, nil
}
