package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
	"eggbox/internal/ruledata"
	"eggbox/internal/subscription"
)

// ApplyReport says what an apply did.
type ApplyReport struct {
	// Skipped: the service already has this exact configuration.
	Skipped bool
	// Switched: only selections differed, and they were made without
	// restarting the core.
	Switched bool
	Revision uint64
	Nodes    int
	Dropped  []Dropped
	Warnings []Warning
}

// maxHealRounds bounds how many times nodes the service rejects are dropped
// before giving up.
const maxHealRounds = 4

// firstFetchWait is how long a connection waits for rule data that is not
// there yet. Past it, the items that lack data are left out and the person is
// told, instead of holding the connection hostage to a slow mirror.
const firstFetchWait = 25 * time.Second

// prepared is a configuration ready to hand to the service.
type prepared struct {
	view    *planView
	out     *configgen.Output
	active  []library.ActiveNode
	dropped []Dropped
}

// prepare compiles the plan, writes the configuration and has the service
// check it. Nodes the service rejects are dropped, so one bad node never keeps
// the good ones from working.
func (s *Service) prepare(ctx context.Context) (*prepared, error) {
	active, err := s.lib.ActiveNodes()
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, ErrNoNodes
	}
	st, prefs := s.lib.Routing(), s.lib.Prefs()

	res, err := s.compile(st, prefs, active)
	if err != nil {
		return nil, err
	}
	if len(res.Missing) > 0 {
		fctx, cancel := context.WithTimeout(ctx, firstFetchWait)
		s.setBusy(func(b *Busy) { b.RuleData = true })
		s.fetchRuleData(fctx, res.Missing, ruledata.UpdateOptions{})
		s.setBusy(func(b *Busy) { b.RuleData = false })
		cancel()
		if res, err = s.compile(st, prefs, active); err != nil {
			return nil, err
		}
	}

	in := res.Input
	var dropped []Dropped
	for round := 0; ; round++ {
		out, err := configgen.Generate(in)
		if err != nil {
			return nil, err
		}
		if s.wasValidated(out.Digest) {
			// Only selections differ from a configuration the service
			// accepted: there is nothing new to check.
			return s.finishPrepare(st, res, out, active, dropped), nil
		}
		resp, err := s.agent.Validate(ctx, bundleOf(out))
		if err != nil {
			return nil, err
		}
		if resp.GetValid() {
			s.markValidated(out.Digest)
			return s.finishPrepare(st, res, out, active, dropped), nil
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

func (s *Service) finishPrepare(st routing.State, res *plancompile.Result, out *configgen.Output, active []library.ActiveNode, dropped []Dropped) *prepared {
	for _, d := range out.Dropped {
		dropped = append(dropped, Dropped{Name: d.Name, Reason: d.Reason})
	}
	v := &planView{state: st, res: res, out: out, active: active, owners: out.Owners}
	v.names, v.wanted = namesAndPins(st)
	return &prepared{view: v, out: out, active: active, dropped: dropped}
}

const maxValidated = 16

func (s *Service) wasValidated(digest string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.validated, digest)
}

func (s *Service) markValidated(digest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.Contains(s.validated, digest) {
		s.validated = append(s.validated, digest)
		if len(s.validated) > maxValidated {
			s.validated = s.validated[len(s.validated)-maxValidated:]
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

// pushDigest identifies what is handed to the service: the configuration and
// its resources, but not the selections, which are made through the service's
// selection call.
func pushDigest(out *configgen.Output) string { return out.Digest }

// Apply regenerates the configuration and hands it to the service: stored
// when the proxy is off, hot-applied when it is on (the service rolls back to
// the previous configuration if the new one fails). When only selections
// changed, they are made on the running core and nothing restarts.
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
	online := s.Snapshot().Phase.Online()

	s.mu.Lock()
	prev := s.applied
	same := !force && s.pushed == pushDigest(p.out)
	s.mu.Unlock()

	// A running core that differs from the new configuration only in what its
	// groups have selected is switched, not restarted.
	if online && !force {
		if switches, ok := configgen.Switches(prev, p.out); ok {
			if err := s.makeSwitches(ctx, switches); err != nil {
				return nil, err
			}
			s.mu.Lock()
			s.applied = p.out
			s.mu.Unlock()
			report.Switched = true
			st := s.Snapshot().Status
			if st != nil {
				report.Revision = st.GetStoredRevision()
			}
			s.setGenerated(p, report, report.Revision)
			return report, nil
		}
	}
	if same {
		if st := s.Snapshot().Status; st != nil && st.GetHasConfig() && !online {
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
	s.pushed = pushDigest(p.out)
	s.applied = nil
	s.mu.Unlock()
	report.Revision = res.GetRevision()
	if online {
		// The core started afresh: what it remembers of earlier choices must
		// give way to the plan's.
		s.reassert(ctx, p.out)
		s.mu.Lock()
		s.applied = p.out
		s.mu.Unlock()
	}
	s.setGenerated(p, report, res.GetRevision())
	return report, nil
}

// makeSwitches puts groups on the members the plan wants.
func (s *Service) makeSwitches(ctx context.Context, switches map[string]string) error {
	tags := make([]string, 0, len(switches))
	for tag := range switches {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		if _, err := s.agent.SelectOutbound(ctx, tag, switches[tag]); err != nil {
			return err
		}
	}
	return nil
}

// reassert puts every group the plan decides on the member it wants. The
// core keeps its own memory of what was selected, from earlier runs and
// earlier plans, and that memory must not outvote the plan.
func (s *Service) reassert(ctx context.Context, out *configgen.Output) {
	tags := make([]string, 0, len(out.Selections))
	for tag := range out.Selections {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	// Main exit first: items that follow it see the right node at once.
	sort.SliceStable(tags, func(i, j int) bool { return tags[i] == configgen.TagProxy && tags[j] != configgen.TagProxy })
	for _, tag := range tags {
		if _, err := s.agent.SelectOutbound(ctx, tag, out.Selections[tag]); err != nil {
			s.RecordServiceLog("warn", fmt.Sprintf("Could not select the member of %s: %v", tag, err))
		}
	}
}

// setGenerated records what the last configuration contains, so groups can be
// shown with their nodes' details, and keeps the view of the plan the
// configuration came from.
func (s *Service) setGenerated(p *prepared, report *ApplyReport, revision uint64) {
	byKey := make(map[string]library.ActiveNode, len(p.active))
	names := map[string]map[string]bool{} // name → subscriptions that have it
	for _, a := range p.active {
		byKey[a.Key()] = a
		if names[a.Node.Name] == nil {
			names[a.Node.Name] = map[string]bool{}
		}
		names[a.Node.Name][a.SubID] = true
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
			SubID: a.SubID, SubName: a.SubName, SubAlias: a.SubAlias,
			ID:        routing.NodeID{Sub: a.SubID, Name: a.Node.Name, Hash: key, Region: regionFor(p.view.state, a)},
			Ambiguous: len(names[a.Node.Name]) > 1,
		}
	}
	s.view.Store(p.view)
	applied := AppliedInfo{At: s.cfg.Clock(), Revision: revision, Nodes: len(p.active) - len(report.Dropped), Dropped: report.Dropped, Warnings: report.Warnings}
	s.update(func(sn *Snapshot) {
		s.tagIndex, s.keyToTag = index, keyToTag
		sn.Applied = applied
		sn.Groups = s.mapGroupsLocked(s.rawGroups)
		sn.Exits = s.exitsLocked(sn.Groups)
	})
	s.kickPlan()
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
	if v := s.view.Load(); v != nil && v.out != nil {
		s.reassert(ctx, v.out)
		s.mu.Lock()
		s.applied = v.out
		s.mu.Unlock()
	}
	s.connectNotices()
	s.notice(LevelSuccess, "connected", "", nil)
	return nil
}

// connectNotices tells, once, what the plan could not use: items that are on
// but have no data, and chosen nodes that are not there.
func (s *Service) connectNotices() {
	v := s.view.Load()
	if v == nil {
		return
	}
	waiting := 0
	for _, row := range v.state.View().Rows {
		if row.Item.Enabled && v.res.Items[row.Item.ID].State == plancompile.ItemUnavailable {
			waiting++
		}
	}
	if waiting > 0 {
		s.tellOnce("waiting:"+strconv.Itoa(waiting), LevelWarning, "items_waiting", "", map[string]string{"count": strconv.Itoa(waiting)})
	}
	for _, n := range v.res.Notices {
		if n.Code == plancompile.NoticePinGone {
			s.tellOnce("gone:"+string(n.Owner)+n.ID+n.Node.Key(), LevelWarning, "pin_gone", "", map[string]string{"node": n.Node.Name, "for": v.names[n.ID]})
		}
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
	s.mu.Lock()
	s.applied = nil
	s.mu.Unlock()
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
