// Package demo is a core that only pretends to be one. It reads the
// configuration it is given, shows the groups that configuration declares and
// invents the rest: delays, traffic, connections and log lines.
//
// It exists so that the whole application can be run, looked at and
// screenshotted on a machine without touching its network: nothing here opens
// a socket, a TUN device or a file. The hostnames and addresses in the
// invented connections are public documentation values, never anything taken
// from a real subscription.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"eggbox/internal/agent/engine"
	"eggbox/internal/agentapi"
)

// shown is a connection as it is now, for whoever it is handed to: the world
// goes on changing its own while the stream marshals what it was given.
func shown(c *agentapi.Connection) *agentapi.Connection {
	return proto.Clone(c).(*agentapi.Connection)
}

// Options tune the simulation.
type Options struct {
	// Seed makes the invented traffic repeatable; 0 picks one from the clock.
	Seed uint64
	// Tick is how often the world advances (default 250ms). Tests shorten it.
	Tick time.Duration
	// TunName is the interface name reported for configurations with a TUN
	// (default "utun-demo").
	TunName string
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// Engine implements engine.Engine.
type Engine struct {
	opt Options

	mu       sync.Mutex
	rng      *rand.Rand
	running  bool
	prepared engine.Prepared
	sink     engine.LogSink
	exited   chan engine.ExitEvent
	runDone  chan struct{}

	mode   string
	modes  []string
	router *router
	nodes  map[string]*node
	groups []*group
	byTag  map[string]*group

	groupSubs map[chan *agentapi.Groups]struct{}
	connSubs  map[chan *agentapi.ConnectionEvents]struct{}

	conns    map[string]*conn
	serial   uint64
	lastTick time.Time
	uplink   int64
	downlink int64
	phase    float64
	burstEnd time.Time
}

type node struct {
	tag, typ string
	delay    int32
	tested   int64
	down     bool // the node never answers
}

type group struct {
	tag, typ string
	selected string
	members  []string
	// standIns are, for a preferred group, the groups whose members take over
	// when the chosen node does not answer: the one that holds the chosen node
	// and has another that works.
	standIns []string
}

type conn struct {
	c       *agentapi.Connection
	endsAt  time.Time
	weight  float64
	closing bool
}

// New returns an engine that starts successfully.
func New(o Options) *Engine {
	if o.Tick <= 0 {
		o.Tick = 250 * time.Millisecond
	}
	if o.TunName == "" {
		o.TunName = "utun-demo"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	seed := o.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	return &Engine{
		opt:       o,
		rng:       rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
		exited:    make(chan engine.ExitEvent, 1),
		mode:      "Rule",
		modes:     []string{"Rule", "Global", "Direct"},
		groupSubs: map[chan *agentapi.Groups]struct{}{},
		connSubs:  map[chan *agentapi.ConnectionEvents]struct{}{},
		conns:     map[string]*conn{},
	}
}

var _ engine.Engine = (*Engine)(nil)

// Version names the simulation, not a real core.
func (e *Engine) Version() string { return "1.14.4-demo" }

// SetLogSink installs the receiver of the log lines.
func (e *Engine) SetLogSink(s engine.LogSink) {
	e.mu.Lock()
	e.sink = s
	e.mu.Unlock()
}

type rawConfig struct {
	Outbounds []struct {
		Type      string   `json:"type"`
		Tag       string   `json:"tag"`
		Outbounds []string `json:"outbounds"`
		Default   string   `json:"default"`
		StandIns  []string `json:"stand_ins"`
	} `json:"outbounds"`
	Endpoints []struct {
		Type string `json:"type"`
		Tag  string `json:"tag"`
	} `json:"endpoints"`
	Route struct {
		RuleSet []rawRuleSet `json:"rule_set"`
		Rules   []rawRule    `json:"rules"`
		Final   string       `json:"final"`
	} `json:"route"`
}

// parse reads the groups and nodes a configuration declares.
func parse(raw []byte) (*rawConfig, error) {
	var cfg rawConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("demo core: the configuration is not valid JSON: %w", err)
	}
	if len(cfg.Outbounds) == 0 {
		return nil, fmt.Errorf("demo core: the configuration declares no outbounds")
	}
	return &cfg, nil
}

// Check reads the configuration without starting anything, rule sets
// included.
func (e *Engine) Check(_ context.Context, p engine.Prepared) error {
	cfg, err := parse(p.JSON)
	if err != nil {
		return err
	}
	_, err = loadRouter(cfg)
	return err
}

// Start builds the world from the configuration and begins the simulation.
func (e *Engine) Start(ctx context.Context, p engine.Prepared) error {
	cfg, err := parse(p.JSON)
	if err != nil {
		return err
	}
	rt, err := loadRouter(cfg)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return fmt.Errorf("demo core: start while running")
	}
	e.build(cfg)
	e.router = rt
	e.prepared = p
	e.running = true
	e.runDone = make(chan struct{})
	e.conns = map[string]*conn{}
	e.uplink, e.downlink = 0, 0
	e.lastTick = e.opt.Now()
	e.mode = "Rule"
	go e.world(e.runDone)
	go e.firstTest(e.runDone)

	e.logLocked("info", "router: loaded %d outbounds, %d groups, %d rules, %d rule sets", len(e.nodes), len(e.groups), len(rt.rules), len(rt.sets))
	if p.HasTun {
		e.logLocked("info", "inbound/tun[%s]: started at %s", p.TunTag, e.opt.TunName)
	}
	e.logLocked("info", "sing-box started (demonstration core, nothing leaves this computer)")
	return nil
}

// effectiveLocked is what a group sends traffic to: its choice, or for a
// preferred group whose chosen node does not answer, the node standing in.
func (e *Engine) effectiveLocked(g *group) string {
	if g.typ == "preferred" {
		if n := e.nodes[g.selected]; n != nil && n.down {
			if alt := e.standInLocked(g); alt != "" {
				return alt
			}
		}
	}
	return g.selected
}

// standInLocked is the fastest working node among the members of the stand-in
// group that holds the chosen node.
func (e *Engine) standInLocked(g *group) string {
	for _, tag := range g.standIns {
		sg := e.byTag[tag]
		if sg == nil || !contains(sg.members, g.selected) {
			continue
		}
		best, bestDelay := "", int32(math.MaxInt32)
		for _, m := range sg.members {
			n := e.nodes[m]
			if n == nil || n.down || m == g.selected {
				continue
			}
			// Nodes not tested yet count as slow, so a tested one is preferred.
			d := n.delay
			if d <= 0 {
				d = math.MaxInt32 - 1
			}
			if d < bestDelay {
				best, bestDelay = m, d
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// build derives nodes and groups from a configuration.
func (e *Engine) build(cfg *rawConfig) {
	e.nodes = map[string]*node{}
	e.groups = nil
	e.byTag = map[string]*group{}
	for _, o := range cfg.Outbounds {
		switch o.Type {
		case "selector", "urltest", "preferred":
			g := &group{tag: o.Tag, typ: o.Type, members: append([]string(nil), o.Outbounds...), standIns: o.StandIns}
			if o.Default != "" && contains(g.members, o.Default) {
				g.selected = o.Default
			} else if len(g.members) > 0 {
				g.selected = g.members[0]
			}
			e.groups = append(e.groups, g)
			e.byTag[g.tag] = g
		case "direct", "block", "dns":
		default:
			e.nodes[o.Tag] = &node{tag: o.Tag, typ: o.Type, down: unreachable(o.Tag)}
		}
	}
	e.nodes["direct"] = &node{tag: "direct", typ: "direct"}
	for _, ep := range cfg.Endpoints {
		e.nodes[ep.Tag] = &node{tag: ep.Tag, typ: ep.Type, down: unreachable(ep.Tag)}
	}
}

// unreachable picks, repeatably, the rare node that never answers.
func unreachable(tag string) bool { return hash(tag)%23 == 0 }

func hash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// delayFor is a plausible latency for a node, repeatable with some drift.
func (e *Engine) delayFor(n *node) int32 {
	if n.down {
		return 0
	}
	base := 48 + int32(hash(n.tag)%230)
	jitter := int32(e.rng.IntN(30)) - 12
	return max(18, base+jitter)
}

// firstTest tests every node a little after the start, the way the core's
// automatic group does.
func (e *Engine) firstTest(done <-chan struct{}) {
	e.mu.Lock()
	tags := e.nodeTagsLocked()
	e.mu.Unlock()
	for _, tag := range tags {
		select {
		case <-done:
			return
		case <-time.After(time.Duration(2+e.rngInt(18)) * e.opt.Tick / 50):
		}
		e.mu.Lock()
		if n := e.nodes[tag]; n != nil && e.running {
			e.testLocked(n)
			e.reselectLocked()
			e.publishGroupsLocked()
		}
		e.mu.Unlock()
	}
}

func (e *Engine) rngInt(n int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rng.IntN(n)
}

func (e *Engine) nodeTagsLocked() []string {
	var tags []string
	seen := map[string]bool{}
	for _, g := range e.groups {
		for _, m := range g.members {
			if _, ok := e.nodes[m]; ok && m != "direct" && !seen[m] {
				seen[m] = true
				tags = append(tags, m)
			}
		}
	}
	return tags
}

func (e *Engine) testLocked(n *node) {
	n.delay = e.delayFor(n)
	n.tested = e.opt.Now().Unix()
}

// reselectLocked lets the automatic groups pick their fastest member.
func (e *Engine) reselectLocked() {
	for _, g := range e.groups {
		if g.typ != "urltest" {
			continue
		}
		best, bestDelay := "", int32(math.MaxInt32)
		for _, m := range g.members {
			n := e.nodes[m]
			if n == nil {
				if sub := e.byTag[m]; sub != nil && sub.selected != "" {
					n = e.nodes[sub.selected]
				}
			}
			if n != nil && n.delay > 0 && n.delay < bestDelay {
				best, bestDelay = m, n.delay
			}
		}
		if best != "" {
			g.selected = best
		}
	}
}

// Stop ends the simulation.
func (e *Engine) Stop(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return nil
	}
	e.logLocked("info", "sing-box stopped")
	e.running = false
	close(e.runDone)
	e.runDone = nil
	e.prepared = engine.Prepared{}
	return nil
}

// Running reports whether the simulation runs.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// Exited never fires: the simulation only stops when asked.
func (e *Engine) Exited() <-chan engine.ExitEvent { return e.exited }

// TunInterface is the name of the pretend device.
func (e *Engine) TunInterface() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running && e.prepared.HasTun {
		return e.opt.TunName
	}
	return ""
}

func (e *Engine) requireRunning() error {
	if !e.running {
		return engine.ErrNotRunning
	}
	return nil
}

// logLocked sends a line to the sink. The sink only appends to the agent's
// log buffer and never calls back into the engine, so calling it with the
// lock held keeps the lines in order.
func (e *Engine) logLocked(level, format string, args ...any) {
	if e.sink == nil {
		return
	}
	e.sink(level, fmt.Sprintf(format, args...))
}

// ------------------------------------------------------------------ groups

func (e *Engine) snapshotLocked() *agentapi.Groups {
	out := &agentapi.Groups{}
	for _, g := range e.groups {
		ag := &agentapi.Group{Tag: g.tag, Type: g.typ, Selectable: g.typ == "selector" || g.typ == "preferred", Selected: e.effectiveLocked(g)}
		for _, m := range g.members {
			item := &agentapi.GroupItem{Tag: m}
			switch {
			case e.nodes[m] != nil:
				n := e.nodes[m]
				item.Type, item.DelayMs, item.TestedUnix = n.typ, n.delay, n.tested
			case e.byTag[m] != nil:
				sub := e.byTag[m]
				item.Type = sub.typ
				if n := e.nodes[sub.selected]; n != nil {
					item.DelayMs, item.TestedUnix = n.delay, n.tested
				}
			default:
				item.Type = "direct"
			}
			ag.Items = append(ag.Items, item)
		}
		out.Groups = append(out.Groups, ag)
	}
	return out
}

func (e *Engine) publishGroupsLocked() {
	snap := e.snapshotLocked()
	for ch := range e.groupSubs {
		offer(ch, snap)
	}
}

// offer hands a picture of the groups to a watcher that may be slow. When the
// watcher's buffer is full the oldest picture goes to make room, never the
// newest: whoever fell behind ends up holding the groups as they stand now,
// not an old picture that nothing will ever correct (a choice made while the
// watcher lagged would otherwise never reach it). Every send is made with the
// engine's lock held and watchers only receive, so the room that was made
// cannot be taken by anyone else.
func offer[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

// WatchGroups streams the groups: the current ones first, then every change.
func (e *Engine) WatchGroups(ctx context.Context) (<-chan *agentapi.Groups, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return nil, err
	}
	ch := make(chan *agentapi.Groups, 8)
	ch <- e.snapshotLocked()
	e.groupSubs[ch] = struct{}{}
	done := e.runDone
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		e.mu.Lock()
		delete(e.groupSubs, ch)
		close(ch)
		e.mu.Unlock()
	}()
	return ch, nil
}

// SelectOutbound chooses the member of a selector.
func (e *Engine) SelectOutbound(group, outbound string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	g := e.byTag[group]
	if g == nil {
		return fmt.Errorf("%w: group %s", engine.ErrNotFound, group)
	}
	if g.typ != "selector" && g.typ != "preferred" {
		return fmt.Errorf("%w: %s is not a selector", engine.ErrInvalid, group)
	}
	if !contains(g.members, outbound) {
		return fmt.Errorf("%w: %s is not in %s", engine.ErrNotFound, outbound, group)
	}
	g.selected = outbound
	e.logLocked("info", "outbound/selector[%s]: selected %s", group, outbound)
	e.publishGroupsLocked()
	return nil
}

// URLTest measures a group's members, or one outbound, taking a moment so
// that the interface shows its waiting state and fills the delays in turn.
func (e *Engine) URLTest(ctx context.Context, tag string) error {
	e.mu.Lock()
	if err := e.requireRunning(); err != nil {
		e.mu.Unlock()
		return err
	}
	var targets []*node
	if g := e.byTag[tag]; g != nil {
		seen := map[string]bool{}
		var walk func(*group)
		walk = func(g *group) {
			for _, m := range g.members {
				if n := e.nodes[m]; n != nil && m != "direct" && !seen[m] {
					seen[m] = true
					targets = append(targets, n)
				} else if sub := e.byTag[m]; sub != nil && !seen["g:"+m] {
					seen["g:"+m] = true
					walk(sub)
				}
			}
		}
		walk(g)
	} else if n := e.nodes[tag]; n != nil {
		targets = []*node{n}
	} else {
		e.mu.Unlock()
		return fmt.Errorf("%w: %s", engine.ErrNotFound, tag)
	}
	step := e.opt.Tick / 12
	e.mu.Unlock()

	for _, n := range targets {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(step):
		}
		e.mu.Lock()
		if e.running {
			e.testLocked(n)
			e.reselectLocked()
			e.publishGroupsLocked()
		}
		e.mu.Unlock()
	}
	return nil
}

// ------------------------------------------------------------------- modes

// Mode returns the current mode and the available ones.
func (e *Engine) Mode() (string, []string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return "", nil, err
	}
	return e.mode, append([]string(nil), e.modes...), nil
}

// SetMode switches the mode.
func (e *Engine) SetMode(mode string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	if !contains(e.modes, mode) {
		return fmt.Errorf("%w: unknown mode %q", engine.ErrInvalid, mode)
	}
	e.mode = mode
	e.logLocked("info", "router: mode switched to %s", mode)
	return nil
}

// --------------------------------------------------------------- world

// world advances the simulation until the run ends.
func (e *Engine) world(done <-chan struct{}) {
	t := time.NewTicker(e.opt.Tick)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			e.mu.Lock()
			if e.running && e.runDone == done {
				e.advanceLocked()
			}
			e.mu.Unlock()
		}
	}
}

// rateLocked is the downlink rate in bytes per second at this moment: a quiet
// base with slow waves, and now and then a burst, like someone starting a video.
func (e *Engine) rateLocked(now time.Time) float64 {
	e.phase += 0.11
	rate := 180e3 + 90e3*math.Sin(e.phase) + 60e3*math.Sin(e.phase*2.7+1)
	if now.After(e.burstEnd) && e.rng.Float64() < 0.012 {
		e.burstEnd = now.Add(time.Duration(5+e.rng.IntN(9)) * time.Second)
	}
	if now.Before(e.burstEnd) {
		rate += 2.4e6 + 900e3*e.rng.Float64()
	}
	rate *= 0.75 + 0.5*e.rng.Float64()
	return max(rate, 8e3)
}

func (e *Engine) advanceLocked() {
	now := e.opt.Now()
	dt := now.Sub(e.lastTick).Seconds()
	e.lastTick = now
	if dt <= 0 {
		return
	}

	var events []*agentapi.ConnectionEvent

	// New connections.
	spawn := e.rng.Float64() < 1-math.Exp(-1.6*dt)
	if len(e.conns) < 6 {
		spawn = true
	}
	if spawn && len(e.conns) < 64 {
		if c := e.newConnLocked(now); c != nil {
			events = append(events, &agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_NEW, Id: c.c.Id, Connection: shown(c.c)})
		}
	}

	// Traffic: the rate is shared among the open connections by weight.
	down := e.rateLocked(now) * dt
	up := down * (0.05 + 0.07*e.rng.Float64())
	var total float64
	for _, c := range e.conns {
		total += c.weight
	}
	if total > 0 {
		ids := make([]string, 0, len(e.conns))
		for id := range e.conns {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			c := e.conns[id]
			if c.closing {
				continue
			}
			dd := int64(down * c.weight / total)
			du := int64(up * c.weight / total)
			c.c.DownlinkTotal += dd
			c.c.UplinkTotal += du
			if dd+du > 0 {
				events = append(events, &agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_UPDATE, Id: id, UplinkDelta: du, DownlinkDelta: dd})
			}
		}
	}
	e.downlink += int64(down)
	e.uplink += int64(up)

	// Connections that ended.
	for id, c := range e.conns {
		if !now.Before(c.endsAt) {
			c.c.ClosedUnixMs = now.UnixMilli()
			events = append(events, &agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_CLOSED, Id: id, Connection: shown(c.c)})
			delete(e.conns, id)
		}
	}
	e.emitLocked(events)
}

func (e *Engine) emitLocked(events []*agentapi.ConnectionEvent) {
	if len(events) == 0 {
		return
	}
	msg := &agentapi.ConnectionEvents{Events: events}
	var reset *agentapi.ConnectionEvents
	for ch := range e.connSubs {
		select {
		case ch <- msg:
			continue
		default:
		}
		// The watcher fell behind. Events are changes, so losing some would
		// leave it with a list that stays wrong; what it has not read yet is
		// replaced by the whole list as it stands now. Only this function
		// sends, with the lock held, so the room made here is not taken.
		for drained := false; !drained; {
			select {
			case <-ch:
			default:
				drained = true
			}
		}
		if reset == nil {
			reset = e.resetLocked()
		}
		ch <- reset
	}
}

// resetLocked is every open connection, as the one message that replaces
// whatever its receiver knew.
func (e *Engine) resetLocked() *agentapi.ConnectionEvents {
	reset := &agentapi.ConnectionEvents{Reset_: true}
	ids := make([]string, 0, len(e.conns))
	for id := range e.conns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		reset.Events = append(reset.Events, &agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_NEW, Id: id, Connection: shown(e.conns[id].c)})
	}
	return reset
}

// WatchTraffic reports the rates and totals every interval.
func (e *Engine) WatchTraffic(ctx context.Context, interval time.Duration) (<-chan *agentapi.Traffic, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return nil, err
	}
	out := make(chan *agentapi.Traffic, 1)
	done := e.runDone
	go func() {
		defer close(out)
		t := time.NewTicker(interval)
		defer t.Stop()
		var lastUp, lastDown int64
		e.mu.Lock()
		lastUp, lastDown = e.uplink, e.downlink
		e.mu.Unlock()
		for {
			e.mu.Lock()
			msg := &agentapi.Traffic{
				Uplink: e.uplink - lastUp, Downlink: e.downlink - lastDown,
				UplinkTotal: e.uplink, DownlinkTotal: e.downlink,
				Connections: int32(len(e.conns)),
				Memory:      uint64(52e6 + 9e6*math.Sin(e.phase/4) + float64(len(e.conns))*120e3),
				Goroutines:  int32(96 + len(e.conns)*3),
				Available:   true,
				TimeUnixMs:  e.opt.Now().UnixMilli(),
			}
			lastUp, lastDown = e.uplink, e.downlink
			e.mu.Unlock()
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			case <-done:
				return
			}
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			case <-done:
				return
			}
		}
	}()
	return out, nil
}

// WatchConnections streams the open connections: a reset with all of them,
// then the events of the world.
func (e *Engine) WatchConnections(ctx context.Context, _ time.Duration) (<-chan *agentapi.ConnectionEvents, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return nil, err
	}
	out := make(chan *agentapi.ConnectionEvents, 1)
	out <- e.resetLocked()
	live := make(chan *agentapi.ConnectionEvents, 64)
	e.connSubs[live] = struct{}{}
	done := e.runDone
	go func() {
		defer close(out)
		defer func() {
			e.mu.Lock()
			delete(e.connSubs, live)
			e.mu.Unlock()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case ev := <-live:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}
	}()
	return out, nil
}

// Open invents one connection to a destination of the world right now, given
// by its host name or its address, and routes it like any other. It returns
// nil when the rules refuse the connection, which then shows in the log only.
// It exists so that tests and demonstrations can ask what the configuration
// does with a particular destination without waiting for chance.
func (e *Engine) Open(dest string) (*agentapi.Connection, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return nil, err
	}
	for _, t := range targets {
		if t.host != dest && t.ip != dest {
			continue
		}
		c := e.openLocked(e.opt.Now(), t, programs[e.rng.IntN(len(programs))])
		if c == nil {
			return nil, nil
		}
		e.emitLocked([]*agentapi.ConnectionEvent{{Type: agentapi.ConnectionEvent_TYPE_NEW, Id: c.c.Id, Connection: shown(c.c)}})
		return shown(c.c), nil
	}
	return nil, fmt.Errorf("%w: the world has no destination %q", engine.ErrNotFound, dest)
}

// CloseConnection closes one open connection.
func (e *Engine) CloseConnection(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	c, ok := e.conns[id]
	if !ok {
		return fmt.Errorf("%w: connection %s", engine.ErrNotFound, id)
	}
	c.c.ClosedUnixMs = e.opt.Now().UnixMilli()
	delete(e.conns, id)
	e.emitLocked([]*agentapi.ConnectionEvent{{Type: agentapi.ConnectionEvent_TYPE_CLOSED, Id: id, Connection: shown(c.c)}})
	return nil
}

// CloseAllConnections closes every open connection.
func (e *Engine) CloseAllConnections() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.requireRunning(); err != nil {
		return err
	}
	now := e.opt.Now().UnixMilli()
	ids := make([]string, 0, len(e.conns))
	for id := range e.conns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var events []*agentapi.ConnectionEvent
	for _, id := range ids {
		c := e.conns[id]
		c.c.ClosedUnixMs = now
		events = append(events, &agentapi.ConnectionEvent{Type: agentapi.ConnectionEvent_TYPE_CLOSED, Id: id, Connection: shown(c.c)})
	}
	e.conns = map[string]*conn{}
	e.emitLocked(events)
	return nil
}
