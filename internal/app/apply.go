package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/subscription"
)

// ApplyReport says what an apply did.
type ApplyReport struct {
	// Skipped: the service already has this exact configuration.
	Skipped  bool
	Revision uint64
	Nodes    int
	Dropped  []Dropped
	Warnings []Warning
}

// maxHealRounds bounds how many times nodes the service rejects are dropped
// before giving up.
const maxHealRounds = 4

// prepared is a configuration ready to hand to the service.
type prepared struct {
	out     *configgen.Output
	active  []library.ActiveNode
	dropped []Dropped
	digest  string
}

// prepare generates a configuration from the library and has the service check
// it. Nodes the service rejects are dropped, so one bad node never keeps the
// good ones from working.
func (s *Service) prepare(ctx context.Context) (*prepared, error) {
	active, err := s.lib.ActiveNodes()
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, ErrNoNodes
	}
	prefs, rules, settings := s.lib.Prefs(), s.lib.Rules(), s.lib.Settings()

	needed := configgen.RequiredRuleSets(prefs)
	sets, missing := s.rules.Load(needed...)
	if len(missing) > 0 {
		// First run, or the data was lost: try to fetch it, but do not hold the
		// connection hostage to a slow mirror; the generator degrades with a warning.
		fctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		s.rules.SetMirrors(s.mirrorsFor(settings.RuleMirror))
		s.rules.Update(fctx, missing, ruledataOpts(false))
		cancel()
		sets, _ = s.rules.Load(needed...)
	}

	nodes := make([]subscription.Node, len(active))
	for i, a := range active {
		nodes[i] = a.Node
	}
	in := configgen.Input{Nodes: nodes, Prefs: prefs, Rules: rules, RuleSets: sets, Selected: settings.SelectedNode}

	var dropped []Dropped
	for round := 0; ; round++ {
		out, err := configgen.Generate(in)
		if err != nil {
			return nil, err
		}
		resp, err := s.agent.Validate(ctx, bundleOf(out))
		if err != nil {
			return nil, err
		}
		if resp.GetValid() {
			for _, d := range out.Dropped {
				dropped = append(dropped, Dropped{Name: d.Name, Reason: d.Reason})
			}
			// The digest ignores the preselected node: choosing another one at
			// runtime must not look like a configuration change.
			base := in
			base.Selected = ""
			baseOut, err := configgen.Generate(base)
			if err != nil {
				return nil, err
			}
			return &prepared{out: out, active: active, dropped: dropped, digest: digestOf(baseOut)}, nil
		}
		var issues []configgen.PathIssue
		verr := &ValidationError{}
		for _, i := range resp.GetIssues() {
			verr.Issues = append(verr.Issues, Issue{Path: i.GetPath(), Code: i.GetCode(), Message: i.GetMessage(), Warning: i.GetSeverity() == agentapi.Issue_SEVERITY_WARNING})
			if i.GetSeverity() != agentapi.Issue_SEVERITY_WARNING {
				issues = append(issues, configgen.PathIssue{Path: i.GetPath(), Message: i.GetMessage()})
			}
		}
		healed, drops, ok := configgen.Heal(in, out, issues)
		if !ok || round >= maxHealRounds {
			return nil, verr
		}
		for _, d := range drops {
			dropped = append(dropped, Dropped{Name: d.Name, Reason: d.Reason})
		}
		in = healed
		if len(in.Nodes) == 0 {
			return nil, ErrNoNodes
		}
	}
}

func bundleOf(out *configgen.Output) *agentapi.ConfigBundle {
	b := &agentapi.ConfigBundle{ConfigJson: out.JSON, ProfileId: "default", ProfileName: "EggBox"}
	for _, r := range out.Resources {
		sum := sha256.Sum256(r.Data)
		b.Resources = append(b.Resources, &agentapi.Resource{Name: r.Name, Data: r.Data, Sha256: hex.EncodeToString(sum[:])})
	}
	return b
}

func digestOf(out *configgen.Output) string {
	h := sha256.New()
	h.Write(out.JSON)
	for _, r := range out.Resources {
		h.Write([]byte{0})
		h.Write([]byte(r.Name))
		h.Write(r.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Apply regenerates the configuration and hands it to the service: stored
// when the proxy is off, hot-applied when it is on (the service rolls back to
// the previous configuration if the new one fails).
func (s *Service) Apply(ctx context.Context) (*ApplyReport, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.setBusy(func(b *Busy) { b.Apply = true })
	defer s.setBusy(func(b *Busy) { b.Apply = false })
	return s.applyLocked(ctx, false)
}

func (s *Service) applyLocked(ctx context.Context, force bool) (*ApplyReport, error) {
	if err := s.requireLink(ctx); err != nil {
		return nil, err
	}
	p, err := s.prepare(ctx)
	if err != nil {
		return nil, err
	}
	report := &ApplyReport{Nodes: len(p.active), Dropped: p.dropped}
	for _, w := range p.out.Warnings {
		report.Warnings = append(report.Warnings, Warning{Code: w.Code, Detail: w.Detail})
	}

	s.mu.Lock()
	same := !force && s.digest == p.digest
	s.mu.Unlock()
	if same {
		if st := s.Snapshot().Status; st != nil && st.GetHasConfig() {
			s.setGenerated(p, report, st.GetStoredRevision())
			report.Skipped, report.Revision = true, st.GetStoredRevision()
			return report, nil
		}
	}

	var res *agentapi.OperationResult
	for attempt := 0; attempt < 2; attempt++ {
		st, err := s.agent.Status(ctx)
		if err != nil {
			return nil, err
		}
		res, err = s.agent.ApplyConfig(ctx, st.GetStoredRevision(), bundleOf(p.out), agentapi.ApplyMode_APPLY_MODE_AUTO)
		if err == nil {
			break
		}
		// Another controller stored a revision since we asked: look again once.
		if agentclient.CodeOf(err) == agentapi.ErrorCode_ERROR_CODE_REVISION_CONFLICT && attempt == 0 {
			continue
		}
		return nil, err
	}
	s.mu.Lock()
	s.digest = p.digest
	s.mu.Unlock()
	report.Revision = res.GetRevision()
	s.setGenerated(p, report, res.GetRevision())
	return report, nil
}

// setGenerated records what the last configuration contains, so groups can be
// shown with their nodes' details.
func (s *Service) setGenerated(p *prepared, report *ApplyReport, revision uint64) {
	byKey := make(map[string]library.ActiveNode, len(p.active))
	for _, a := range p.active {
		byKey[a.Key()] = a
	}
	index := make(map[string]NodeRef, len(p.out.Tags))
	keyToTag := make(map[string]string, len(p.out.Tags))
	for key, tag := range p.out.Tags {
		a, ok := byKey[key]
		if !ok {
			continue
		}
		keyToTag[key] = tag
		index[tag] = NodeRef{
			Key: key, Name: a.Node.Name, Protocol: a.Node.Protocol(), Transport: a.Node.Transport(),
			Region: subscription.Region(a.Node.Name), Server: a.Node.Server, Port: a.Node.Port,
			SubID: a.SubID, SubName: a.SubName,
		}
	}
	applied := AppliedInfo{At: s.cfg.Clock(), Revision: revision, Nodes: len(p.active) - len(report.Dropped), Dropped: report.Dropped, Warnings: report.Warnings}
	s.update(func(sn *Snapshot) {
		s.tagIndex, s.keyToTag = index, keyToTag
		sn.Applied = applied
		sn.Groups = s.mapGroupsLocked(s.rawGroups)
	})
}

// ConnectOptions are the choices of a connect request.
type ConnectOptions struct {
	// IgnoreRouteConflicts takes priority over another VPN's default routes.
	// Set it only after the user confirmed the takeover.
	IgnoreRouteConflicts bool
}

// Connect brings the proxy up with the current library.
func (s *Service) Connect(ctx context.Context, opts ConnectOptions) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.setBusy(func(b *Busy) { b.Connect = true })
	defer s.setBusy(func(b *Busy) { b.Connect = false })

	report, err := s.applyLocked(ctx, false)
	if err != nil {
		s.report("connect_failed", err)
		return err
	}
	if len(report.Dropped) > 0 {
		s.notice(LevelWarning, "nodes_dropped", "", map[string]string{"count": fmt.Sprint(len(report.Dropped))})
	}
	if _, err := s.agent.Connect(ctx, agentclient.ConnectOptions{IgnoreRouteConflicts: opts.IgnoreRouteConflicts}); err != nil {
		if agentclient.CodeOf(err) != agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT {
			s.report("connect_failed", err)
		}
		return err
	}
	s.reassertSelection(ctx)
	s.notice(LevelSuccess, "connected", "", nil)
	return nil
}

// reassertSelection puts the Proxy group back on the node the user chose; the
// core's own memory of it may be gone (new cache, new installation).
func (s *Service) reassertSelection(ctx context.Context) {
	sel := s.lib.Settings().SelectedNode
	if sel == "" {
		return
	}
	s.mu.Lock()
	tag, ok := s.keyToTag[sel]
	s.mu.Unlock()
	if !ok {
		tag = sel // a region group's tag
	}
	if _, err := s.agent.SelectOutbound(ctx, configgen.TagProxy, tag); err != nil {
		// The node may have left the subscription; Auto is the sane fallback.
		_ = s.lib.UpdateSettings(func(st *library.Settings) {
			if st.SelectedNode == sel {
				st.SelectedNode = ""
			}
		})
	}
}

// Disconnect takes the proxy down.
func (s *Service) Disconnect(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.setBusy(func(b *Busy) { b.Connect = true })
	defer s.setBusy(func(b *Busy) { b.Connect = false })
	if err := s.requireLink(ctx); err != nil {
		return err
	}
	if _, err := s.agent.Disconnect(ctx); err != nil {
		s.report("disconnect_failed", err)
		return err
	}
	s.notice(LevelInfo, "disconnected", "", nil)
	return nil
}

// Toggle connects when the proxy is off and disconnects when it is on.
func (s *Service) Toggle(ctx context.Context) error {
	if s.Snapshot().Phase.Online() || s.Snapshot().Phase == PhaseConnecting {
		return s.Disconnect(ctx)
	}
	return s.Connect(ctx, ConnectOptions{})
}

// linkGrace is how long an operation waits for a link that is just coming
// back before it tells the user the service is not there.
const linkGrace = 2 * time.Second

// requireLink fails fast when the service cannot be reached. The client alone
// would keep retrying until the caller's deadline, which for a button press
// means a long silence.
func (s *Service) requireLink(ctx context.Context) error {
	if s.agent.Link().State == agentclient.LinkReady {
		return nil
	}
	wctx, cancel := context.WithTimeout(ctx, linkGrace)
	defer cancel()
	if err := s.agent.WaitReady(wctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w (%s)", agentclient.ErrNotReady, s.agent.Link().State)
	}
	return nil
}
