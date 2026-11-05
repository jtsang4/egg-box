package urltestgroup

import (
	"context"
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

// TypePreferred is the outbound type of a selector that has a stand-in.
const TypePreferred = "preferred"

// PreferredOptions configure a preferred group.
type PreferredOptions struct {
	// Outbounds are the members; the person (or the application on their
	// behalf) picks one of them, exactly as in a selector.
	Outbounds []string `json:"outbounds" reference:"outbound"`
	Default   string   `json:"default,omitempty" reference:"outbound"`
	// Fallbacks names, for some members, a group of the same configuration:
	// while that member is unhealthy, the group's current pick carries the
	// traffic instead. A member without an entry is used as it is.
	Fallbacks map[string]string `json:"fallbacks,omitempty"`
	// URL is the page a member is measured with, as in a urltest group.
	URL string `json:"url,omitempty"`
	// Interval is how often the selected member is measured while traffic
	// flows through the group and the member has a stand-in.
	Interval                  badoption.Duration `json:"interval,omitempty"`
	InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
}

// RegisterPreferred adds the group type to a registry.
func RegisterPreferred(registry *outbound.Registry) {
	outbound.Register[PreferredOptions](registry, TypePreferred, NewPreferred)
}

// DefaultPreferredInterval is how often a member with a stand-in is measured.
const DefaultPreferredInterval = time.Minute

var (
	_ adapter.OutboundGroup = (*Preferred)(nil)
	_ adapter.URLTestGroup  = (*Preferred)(nil)
	_ adapter.Referrer      = (*Preferred)(nil)
)

// Preferred is a selector whose choice may be a node that stops working. The
// router sends traffic to whatever Selected returns, so while the chosen node
// is unhealthy Selected returns the stand-in group instead, and the chosen
// node again as soon as a measurement through it succeeds. That happens in
// the agent, which keeps running when the application is closed.
//
// "Unhealthy" is the same signal the automatic groups use: the node has no
// entry in the URL test history. A failed connection through a group like
// this one deletes the entry (the core does that for every URL test group),
// and a failed measurement does too. A node nobody has measured yet counts as
// healthy until the first measurement, which starts as soon as the group is
// used.
//
// Measuring only happens while traffic flows through the group and only when
// the chosen member has a stand-in, so a group nobody uses, or one that has
// nothing to fall back on, costs nothing.
type Preferred struct {
	outbound.Adapter
	ctx     context.Context
	manager adapter.OutboundManager
	logger  log.ContextLogger
	history *urltest.HistoryStorage
	pause   pause.Manager

	tags              []string
	defaultTag        string
	fallbackTags      map[string]string
	link              string
	interval          time.Duration
	interruptExternal bool

	members   map[string]adapter.Outbound
	fallbacks map[string]adapter.Outbound
	selected  common.TypedValue[adapter.Outbound]
	interrupt *interrupt.Group

	probed     atomic.Bool
	effective  atomic.Value // tag of the outbound traffic went to last, to notice a change
	access     sync.Mutex
	started    bool
	ticker     *time.Ticker
	tickerStop chan struct{}
	pauseCb    *list.Element[pause.Callback]
	lastActive common.TypedValue[time.Time]
}

// NewPreferred builds the group.
func NewPreferred(ctx context.Context, _ adapter.Router, logger log.ContextLogger, tag string, options PreferredOptions) (adapter.Outbound, error) {
	if len(options.Outbounds) == 0 {
		return nil, E.New("missing tags")
	}
	for i, t := range options.Outbounds {
		if slices.Contains(options.Outbounds[:i], t) {
			return nil, E.New("duplicate tag: ", t)
		}
	}
	for member, standIn := range options.Fallbacks {
		if !slices.Contains(options.Outbounds, member) {
			return nil, E.New("fallback for ", member, ", which is not a member")
		}
		if !slices.Contains(options.Outbounds, standIn) {
			return nil, E.New("fallback group ", standIn, " is not a member")
		}
		if member == standIn {
			return nil, E.New("a member cannot stand in for itself: ", member)
		}
	}
	interval := time.Duration(options.Interval)
	if interval <= 0 {
		interval = DefaultPreferredInterval
	}
	history := service.PtrFromContext[urltest.HistoryStorage](ctx)
	if history == nil {
		return nil, E.New("missing URL test history storage")
	}
	return &Preferred{
		Adapter:           outbound.NewAdapter(TypePreferred, tag, nil, options.Outbounds),
		ctx:               ctx,
		manager:           service.FromContext[adapter.OutboundManager](ctx),
		logger:            logger,
		history:           history,
		pause:             service.FromContext[pause.Manager](ctx),
		tags:              options.Outbounds,
		defaultTag:        options.Default,
		fallbackTags:      options.Fallbacks,
		link:              options.URL,
		interval:          interval,
		interruptExternal: options.InterruptExistConnections,
		members:           map[string]adapter.Outbound{},
		fallbacks:         map[string]adapter.Outbound{},
		interrupt:         interrupt.NewGroup(),
	}, nil
}

// Network is the chosen member's.
func (p *Preferred) Network() []string {
	if sel := p.selected.Load(); sel != nil {
		return sel.Network()
	}
	return []string{N.NetworkTCP, N.NetworkUDP}
}

// Start resolves the members and picks the remembered choice (or the default).
func (p *Preferred) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	switch stage {
	case adapter.StartStateStart:
		for i, tag := range p.tags {
			detour, loaded := p.manager.Outbound(tag)
			if !loaded {
				return E.New("outbound ", i, " not found: ", tag)
			}
			p.members[tag] = detour
		}
		for member, standIn := range p.fallbackTags {
			p.fallbacks[member] = p.members[standIn]
		}
		if p.Tag() != "" {
			if cacheFile := service.FromContext[adapter.CacheFile](p.ctx); cacheFile != nil {
				if remembered := cacheFile.LoadSelected(p.Tag()); remembered != "" {
					if detour, loaded := p.members[remembered]; loaded {
						p.selected.Store(detour)
						return nil
					}
				}
			}
		}
		if p.defaultTag != "" {
			detour, loaded := p.members[p.defaultTag]
			if !loaded {
				return E.New("default outbound not found: ", p.defaultTag)
			}
			p.selected.Store(detour)
			return nil
		}
		p.selected.Store(p.members[p.tags[0]])
	case adapter.StartStateStarted:
		p.access.Lock()
		p.started = true
		p.lastActive.Store(time.Now())
		p.access.Unlock()
		scope.Add(p.close)
		go p.check(false)
	}
	return nil
}

// All lists the members.
func (p *Preferred) All() []string { return p.tags }

// Selected is where traffic goes now: the chosen member, or its stand-in
// while it is unhealthy.
func (p *Preferred) Selected(string) adapter.Outbound {
	sel := p.selected.Load()
	if sel == nil {
		return nil
	}
	return p.resolve(sel, true)
}

func (p *Preferred) resolve(sel adapter.Outbound, touch bool) adapter.Outbound {
	standIn := p.fallbacks[sel.Tag()]
	if standIn == nil {
		return sel
	}
	if touch {
		p.touch()
	}
	if p.healthy(sel) {
		return sel
	}
	if t, ok := standIn.(adapter.URLTestGroup); ok {
		// A group of this kind only moves to another member when it is told to
		// look again; the unhealthy node has just lost its measurement.
		t.PerformUpdateCheck()
	}
	if g, ok := standIn.(adapter.OutboundGroup); ok {
		if alt := g.Selected(N.NetworkTCP); alt == nil || group.RealTag(alt, N.NetworkTCP) == group.RealTag(sel, N.NetworkTCP) {
			return sel
		}
	}
	return standIn
}

func (p *Preferred) healthy(sel adapter.Outbound) bool {
	if !p.probed.Load() {
		return true
	}
	tag := group.RealTag(sel, N.NetworkTCP)
	return tag != "" && p.history.LoadURLTestHistory(tag) != nil
}

// SelectOutbound picks a member, as a selector does. It reports false when
// tag is not a member.
func (p *Preferred) SelectOutbound(tag string) bool {
	detour, loaded := p.members[tag]
	if !loaded {
		return false
	}
	if p.selected.Swap(detour) == detour {
		return true
	}
	if p.Tag() != "" {
		if cacheFile := service.FromContext[adapter.CacheFile](p.ctx); cacheFile != nil {
			if err := cacheFile.StoreSelected(p.Tag(), tag); err != nil {
				p.logger.Error("store selected: ", err)
			}
		}
	}
	p.probed.Store(false)
	p.interrupt.Interrupt(p.interruptExternal)
	p.history.NotifyUpdated()
	if p.fallbacks[tag] != nil {
		go p.check(true)
	}
	return true
}

// AttachConnection registers a connection routed through the group, so that a
// change of the traffic's destination can interrupt it.
func (p *Preferred) AttachConnection(closer io.Closer) func() {
	p.touch()
	return p.interrupt.Add(closer, true)
}

// References are the outbounds in use, so idle connections of the others can
// be released.
func (p *Preferred) References() []string {
	sel := p.selected.Load()
	if sel == nil {
		return p.tags[:1]
	}
	return []string{p.resolve(sel, false).Tag()}
}

// DialContext and ListenPacket serve the group when it is used as a detour;
// routed connections go to the outbound Selected names and never come here.
func (p *Preferred) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	out := p.Selected(network)
	if out == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := out.DialContext(ctx, network, destination)
	if err != nil {
		p.history.DeleteURLTestHistory(group.RealTag(out, N.NetworkTCP))
		return nil, err
	}
	return p.interrupt.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (p *Preferred) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	out := p.Selected(N.NetworkUDP)
	if out == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := out.ListenPacket(ctx, destination)
	if err != nil {
		p.history.DeleteURLTestHistory(group.RealTag(out, N.NetworkUDP))
		return nil, err
	}
	return p.interrupt.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

// URLTest measures every member (the daemon's "test" button on a group).
func (p *Preferred) URLTest(ctx context.Context) (map[string]uint16, error) {
	members := make([]adapter.Outbound, 0, len(p.tags))
	for _, tag := range p.tags {
		if m := p.members[tag]; m != nil {
			members = append(members, m)
		}
	}
	result := URLTestOutbounds(ctx, p.manager, p.history, p.logger, members, p.link, p.interval, true)
	p.PerformUpdateCheck()
	return result, nil
}

// PerformUpdateCheck notices a change of the stand-in after a measurement.
func (p *Preferred) PerformUpdateCheck() {
	sel := p.selected.Load()
	if sel == nil {
		return
	}
	p.noteEffective(p.resolve(sel, false))
}

// noteEffective tells the rest of the core when traffic started going
// somewhere else because of health, and cuts the connections that went to the
// old place if the configuration says so.
func (p *Preferred) noteEffective(now adapter.Outbound) {
	tag := now.Tag()
	if previous, _ := p.effective.Swap(tag).(string); previous != "" && previous != tag {
		p.interrupt.Interrupt(p.interruptExternal)
		p.history.NotifyUpdated()
	}
}

// ---------------------------------------------------------------- measuring

func (p *Preferred) touch() {
	p.access.Lock()
	defer p.access.Unlock()
	if !p.started {
		return
	}
	p.lastActive.Store(time.Now())
	if p.ticker != nil {
		return
	}
	ticker := time.NewTicker(p.interval)
	stop := make(chan struct{})
	p.ticker, p.tickerStop = ticker, stop
	p.pauseCb = pause.RegisterTicker(p.pause, ticker, p.interval, nil)
	go p.loop(ticker, stop)
}

func (p *Preferred) loop(ticker *time.Ticker, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		if time.Since(p.lastActive.Load()) > 3*p.interval {
			p.access.Lock()
			if p.ticker == ticker {
				p.stopTickerLocked()
			}
			p.access.Unlock()
			return
		}
		p.check(false)
	}
}

func (p *Preferred) stopTickerLocked() {
	if p.ticker == nil {
		return
	}
	p.ticker.Stop()
	p.ticker = nil
	p.pause.UnregisterCallback(p.pauseCb)
	p.pauseCb = nil
	close(p.tickerStop)
}

func (p *Preferred) close() error {
	p.access.Lock()
	defer p.access.Unlock()
	p.stopTickerLocked()
	return nil
}

// check measures the chosen member when it has a stand-in, and records the
// outcome the way the automatic groups do.
func (p *Preferred) check(force bool) {
	sel := p.selected.Load()
	if sel == nil || p.fallbacks[sel.Tag()] == nil {
		return
	}
	leaf := group.RealTag(sel, N.NetworkTCP)
	if leaf == "" {
		return
	}
	if !force && p.probed.Load() {
		if h := p.history.LoadURLTestHistory(leaf); h != nil && time.Since(h.Time) < p.interval/2 {
			return
		}
	}
	ctx, cancel := context.WithTimeout(p.ctx, C.TCPTimeout)
	defer cancel()
	delay, err := Measure(ctx, p.link, sel)
	if p.ctx.Err() != nil {
		return
	}
	if p.selected.Load() != sel {
		// The choice changed while measuring; the new choice is measured by its own check.
		return
	}
	if err != nil {
		p.logger.Debug("preferred ", p.Tag(), ": ", leaf, " unavailable: ", err)
		p.history.DeleteURLTestHistory(leaf)
	} else {
		p.history.StoreURLTestHistory(leaf, &adapter.URLTestHistory{Time: time.Now(), Delay: delay})
	}
	p.probed.Store(true)
	p.PerformUpdateCheck()
}
