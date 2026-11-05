package app

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"eggbox/internal/agentapi"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/redact"
	"eggbox/internal/ruledata"
	"eggbox/internal/subscription"
)

// ---------------------------------------------------------------- runtime ops

// Select puts the Proxy group on a group item the live groups show (a node's
// tag, a region group or "Auto") and remembers the choice for next time.
func (s *Service) Select(ctx context.Context, tag string) error {
	s.mu.Lock()
	ref, isNode := s.tagIndex[tag]
	s.mu.Unlock()
	key := tag // a region group
	switch {
	case isNode:
		key = ref.Key
	case tag == configgen.TagAuto:
		key = ""
	}
	return s.SelectKey(ctx, key)
}

// SelectKey chooses what the Proxy group uses: a node by its key, a region
// group by its tag, or "" for Auto. It works while disconnected too, the choice
// then applies to the next connection.
func (s *Service) SelectKey(ctx context.Context, key string) error {
	if err := s.lib.UpdateSettings(func(st *library.Settings) { st.SelectedNode = key }); err != nil {
		return err
	}
	if !s.Snapshot().Phase.Online() {
		return nil
	}
	s.mu.Lock()
	tag, ok := s.keyToTag[key]
	s.mu.Unlock()
	switch {
	case ok:
	case key == "":
		tag = configgen.TagAuto
	default:
		tag = key
	}
	if _, err := s.agent.SelectOutbound(ctx, configgen.TagProxy, tag); err != nil {
		s.report("select_failed", err)
		return err
	}
	return nil
}

// SelectedTag returns the tag the Proxy group is on: what the core reports
// while connected, otherwise what the user chose.
func (s *Service) SelectedTag() string {
	for _, g := range s.Snapshot().Groups {
		if g.Tag == configgen.TagProxy && g.Selected != "" {
			return g.Selected
		}
	}
	sel := s.lib.Settings().SelectedNode
	s.mu.Lock()
	defer s.mu.Unlock()
	if tag, ok := s.keyToTag[sel]; ok {
		return tag
	}
	if sel == "" {
		return configgen.TagAuto
	}
	return sel
}

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
	s.notice(LevelSuccess, "nodes_imported", "", map[string]string{"nodes": strconv.Itoa(len(res.Nodes))})
	s.reapplyIfOnline(ctx)
	return sub, nil
}

// beginRefresh marks a subscription as being refreshed; false if it already is.
func (s *Service) beginRefresh(id string) bool {
	if s.Snapshot().Busy.Refreshing[id] {
		return false
	}
	s.setBusy(func(b *Busy) { b.Refreshing[id] = true })
	return true
}

func (s *Service) refresh(ctx context.Context, id string) (*library.RefreshResult, error) {
	if !s.beginRefresh(id) {
		return nil, fmt.Errorf("this subscription is already being updated")
	}
	defer s.setBusy(func(b *Busy) { delete(b.Refreshing, id) })
	return s.lib.Refresh(ctx, id, library.RefreshOptions{Client: s.cfg.HTTPClient})
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
	if res.Changed {
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
		changed = changed || res.Changed
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

// ----------------------------------------------------------- rules and prefs

// SavePrefs stores routing preferences and applies them when the proxy is on.
func (s *Service) SavePrefs(ctx context.Context, p configgen.Prefs) (adjusted []string, err error) {
	adjusted, err = s.lib.SetPrefs(p)
	if err != nil {
		return nil, err
	}
	s.reapplyIfOnline(ctx)
	return adjusted, nil
}

// SaveRules replaces the custom rules and applies them when the proxy is on.
func (s *Service) SaveRules(ctx context.Context, rules []configgen.CustomRule) error {
	if err := s.lib.SetRules(rules); err != nil {
		return err
	}
	s.reapplyIfOnline(ctx)
	return nil
}

// RuleData describes the rule sets the application keeps.
type RuleData struct {
	ID      string
	Present bool
	Needed  bool
	Updated time.Time
	Checked time.Time
	Size    int64
	Source  string
}

// RuleDataCatalog is every rule set the presets can use.
var RuleDataCatalog = []string{configgen.RuleSetCN, configgen.RuleSetCNIP, configgen.RuleSetForeign, configgen.RuleSetAds}

// RuleDataStatus reports the state of the rule sets.
func (s *Service) RuleDataStatus() []RuleData {
	needed := map[string]bool{}
	for _, id := range configgen.RequiredRuleSets(s.lib.Prefs()) {
		needed[id] = true
	}
	var out []RuleData
	for _, st := range s.rules.Statuses(RuleDataCatalog...) {
		out = append(out, RuleData{ID: st.ID, Present: st.Present, Needed: needed[st.ID], Updated: st.Meta.UpdatedAt, Checked: st.Meta.CheckedAt, Size: st.Meta.Size, Source: st.Meta.Source})
	}
	return out
}

func (s *Service) mirrorsFor(setting string) []ruledata.Mirror {
	if s.cfg.Mirrors != nil {
		return s.cfg.Mirrors(setting)
	}
	switch setting {
	case "github":
		return []ruledata.Mirror{ruledata.GitHub}
	case "jsdelivr":
		return []ruledata.Mirror{ruledata.JSDelivr}
	}
	return ruledata.DefaultMirrors()
}

func ruledataOpts(force bool) ruledata.UpdateOptions { return ruledata.UpdateOptions{Force: force} }

// UpdateRuleData downloads the rule sets the preferences need (and any the
// application already holds).
func (s *Service) UpdateRuleData(ctx context.Context, force bool) error {
	s.setBusy(func(b *Busy) { b.RuleData = true })
	defer s.setBusy(func(b *Busy) { b.RuleData = false })

	settings := s.lib.Settings()
	s.rules.SetMirrors(s.mirrorsFor(settings.RuleMirror))
	ids := configgen.RequiredRuleSets(s.lib.Prefs())
	for _, st := range s.rules.Statuses(RuleDataCatalog...) {
		if st.Present && !contains(ids, st.ID) {
			ids = append(ids, st.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	results := s.rules.Update(ctx, ids, ruledata.UpdateOptions{Force: force})
	var updated, failed int
	var firstErr error
	for _, r := range results {
		switch r.Outcome {
		case ruledata.Updated:
			updated++
		case ruledata.Failed:
			failed++
			if firstErr == nil {
				firstErr = r.Err
			}
		}
	}
	args := map[string]string{"updated": strconv.Itoa(updated), "failed": strconv.Itoa(failed)}
	switch {
	case failed > 0:
		s.notice(LevelWarning, "rule_data_failed", Explain(firstErr).Detail, args)
	case updated > 0:
		s.notice(LevelSuccess, "rule_data_updated", "", args)
	default:
		s.notice(LevelInfo, "rule_data_current", "", args)
	}
	if updated > 0 {
		s.reapplyIfOnline(ctx)
	}
	return firstErr
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
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
	var rulesInfo []map[string]any
	for _, st := range s.RuleDataStatus() {
		rulesInfo = append(rulesInfo, map[string]any{"id": st.ID, "present": st.Present, "needed": st.Needed, "size": st.Size, "source": st.Source, "updated": st.Updated})
	}
	app := map[string]any{
		"version": s.cfg.Version,
		"os":      runtime.GOOS, "arch": runtime.GOARCH,
		"link":          snap.Link.State.String(),
		"linkError":     snap.Link.Error,
		"phase":         snap.Phase.String(),
		"subscriptions": infos,
		"customRules":   len(s.lib.Rules()),
		"preset":        string(prefs.Preset),
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
