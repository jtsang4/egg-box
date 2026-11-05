// Package app is the application's brain, with no interface in it: it keeps
// the connection to the privileged service, turns the user's library into
// configurations, runs the operations the interface offers (connect, select a
// node, refresh a subscription...) and publishes what the interface shows as
// immutable snapshots.
package app

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/ruledata"
	"eggbox/internal/subscription"
)

// Config wires a Service to its parts.
type Config struct {
	Library *library.Library
	Rules   *ruledata.Manager
	Agent   *agentclient.Client
	// Version is the application's version, for diagnostics.
	Version string
	// Clock is the time source (tests).
	Clock func() time.Time
	// HTTPClient downloads subscriptions (tests); nil uses the default.
	HTTPClient *http.Client
	// Notify is called, from any goroutine, after the snapshot changed. It must
	// return quickly: the interface invalidates its window here.
	Notify func()
	// ScheduleEvery is how often due refreshes are looked for (default 1 minute).
	ScheduleEvery time.Duration
	// ScheduleDelay is the wait before the first look (default 3 seconds).
	ScheduleDelay time.Duration
	// Mirrors chooses where rule data comes from for a mirror setting
	// ("auto", "github", "jsdelivr"). Nil uses the public mirrors; tests and
	// offline builds supply their own.
	Mirrors func(setting string) []ruledata.Mirror
}

// Service is safe for concurrent use.
type Service struct {
	cfg   Config
	lib   *library.Library
	rules *ruledata.Manager
	agent *agentclient.Client

	snap atomic.Pointer[Snapshot]

	mu         sync.Mutex // guards the fields below and serializes snapshot writers
	tagIndex   map[string]NodeRef
	keyToTag   map[string]string
	rawGroups  *agentapi.Groups
	noticeSeq  uint64
	lastSample time.Time
	conns      map[string]*Conn
	connCancel context.CancelFunc
	digest     string
	started    bool
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	opMu sync.Mutex // serializes connect, disconnect and apply
}

// New returns a service that is not running yet; call Run.
func New(cfg Config) *Service {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.ScheduleEvery <= 0 {
		cfg.ScheduleEvery = time.Minute
	}
	if cfg.ScheduleDelay <= 0 {
		cfg.ScheduleDelay = 3 * time.Second
	}
	s := &Service{
		cfg: cfg, lib: cfg.Library, rules: cfg.Rules, agent: cfg.Agent,
		tagIndex: map[string]NodeRef{}, keyToTag: map[string]string{}, conns: map[string]*Conn{},
	}
	first := &Snapshot{
		Link:  LinkInfo{State: agentclient.LinkConnecting},
		Modes: []string{configgen.ModeRule, configgen.ModeGlobal, configgen.ModeDirect},
	}
	s.snap.Store(first)
	return s
}

// Library exposes the persistent library for reading and for edits that need
// no follow-up (the service re-applies after the ones that do).
func (s *Service) Library() *library.Library { return s.lib }

// Snapshot returns the current view. The value must not be modified.
func (s *Service) Snapshot() *Snapshot { return s.snap.Load() }

// SetNotify replaces the change callback.
func (s *Service) SetNotify(fn func()) {
	s.mu.Lock()
	s.cfg.Notify = fn
	s.mu.Unlock()
}

// update replaces the snapshot with a modified copy.
func (s *Service) update(fn func(*Snapshot)) {
	s.mu.Lock()
	s.updateLocked(fn)
	notify := s.cfg.Notify
	s.mu.Unlock()
	if notify != nil {
		notify()
	}
}

func (s *Service) updateLocked(fn func(*Snapshot)) {
	next := *s.snap.Load()
	fn(&next)
	next.Phase = phaseOf(next.Link, next.Status)
	s.snap.Store(&next)
}

// Run starts the watchers and the scheduler and returns at once. They stop
// when ctx ends or Close is called.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	// The library's changes (a rule edited elsewhere, a refresh) repaint the window.
	s.lib.OnChange(func() { s.update(func(*Snapshot) {}) })

	spawn := func(fn func()) {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			fn()
		}()
	}
	spawn(func() { s.watchLink(ctx) })
	spawn(func() { s.agent.WatchStatus(ctx, s.onStatus) })
	spawn(func() { s.agent.WatchGroups(ctx, s.onGroups) })
	spawn(func() { s.agent.WatchTraffic(ctx, time.Second, s.onTraffic) })
	spawn(func() { s.agent.WatchLogs(ctx, 300, "", s.onLogs) })
	spawn(func() { s.trafficClock(ctx) })
	spawn(func() { s.schedule(ctx) })
}

// Close stops the background work.
func (s *Service) Close() {
	s.mu.Lock()
	cancel, conn := s.cancel, s.connCancel
	s.connCancel = nil
	s.mu.Unlock()
	if conn != nil {
		conn()
	}
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// ----------------------------------------------------------------- watchers

func (s *Service) watchLink(ctx context.Context) {
	var gen uint64
	for l := range s.agent.Updates(ctx) {
		info := LinkInfo{State: l.State, Generation: l.Generation}
		if l.Err != nil {
			info.Error = l.Err.Error()
		}
		if h := l.Hello; h != nil {
			info.Agent = AgentInfo{
				AgentVersion: h.GetAgentVersion(), CoreVersion: h.GetCoreVersion(),
				Platform: h.GetPlatform(), Arch: h.GetArch(), Driver: h.GetDriver(),
				Peer: h.GetPeer(), DevMode: h.GetDevMode(), InstanceID: h.GetInstanceId(),
			}
			if t := h.GetAgentStartedUnix(); t > 0 {
				info.Agent.StartedAt = time.Unix(t, 0)
			}
		}
		s.update(func(sn *Snapshot) {
			sn.Link = info
			if !info.Ready() {
				// What the service told us is stale once it is out of reach.
				sn.Status = nil
				sn.Traffic.Available, sn.Traffic.Up, sn.Traffic.Down = false, 0, 0
			}
		})
		if info.Ready() && info.Generation != gen {
			gen = info.Generation
			s.mu.Lock()
			s.digest = "" // the service may have lost or kept anything: re-apply when asked
			s.mu.Unlock()
			if settings, err := s.agent.Settings(ctx); err == nil {
				s.update(func(sn *Snapshot) { sn.AgentSettings = settings })
			}
		}
	}
}

func (s *Service) onStatus(st *agentapi.Status) {
	var prev *agentapi.Status
	s.update(func(sn *Snapshot) { prev, sn.Status = sn.Status, st })
	s.statusNotices(prev, st)
}

// statusNotices tells about what happens to the connection on its own, with
// nobody at the window: it breaks, mends or fails for good. What the user
// asks for (connect, disconnect) reports itself.
func (s *Service) statusNotices(prev, cur *agentapi.Status) {
	if prev == nil || cur == nil {
		return
	}
	was, is := prev.GetConnection(), cur.GetConnection()
	switch {
	case was == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED && is == agentapi.ConnectionState_CONNECTION_STATE_RECONNECTING:
		s.notice(LevelWarning, "reconnecting", "", nil)
	case was == agentapi.ConnectionState_CONNECTION_STATE_RECONNECTING && is == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED:
		s.notice(LevelSuccess, "reconnected", "", nil)
	case (was == agentapi.ConnectionState_CONNECTION_STATE_CONNECTED || was == agentapi.ConnectionState_CONNECTION_STATE_RECONNECTING) &&
		is == agentapi.ConnectionState_CONNECTION_STATE_FAILED:
		s.notice(LevelError, "connection_lost", cur.GetLastError(), nil)
	}
}

func (s *Service) onGroups(g *agentapi.Groups) {
	s.update(func(sn *Snapshot) {
		s.rawGroups = g
		sn.Groups = s.mapGroupsLocked(g)
	})
}

func (s *Service) mapGroupsLocked(g *agentapi.Groups) []Group {
	if g == nil {
		return nil
	}
	out := make([]Group, 0, len(g.GetGroups()))
	for _, grp := range g.GetGroups() {
		item := Group{Tag: grp.GetTag(), Type: grp.GetType(), Selectable: grp.GetSelectable(), Selected: grp.GetSelected()}
		for _, it := range grp.GetItems() {
			gi := GroupItem{Tag: it.GetTag(), Type: it.GetType(), DelayMS: int(it.GetDelayMs())}
			if t := it.GetTestedUnix(); t > 0 {
				gi.Tested = time.Unix(t, 0)
			}
			if ref, ok := s.tagIndex[it.GetTag()]; ok {
				r := ref
				gi.Node = &r
			}
			item.Items = append(item.Items, gi)
		}
		out = append(out, item)
	}
	return out
}

func (s *Service) onTraffic(t *agentapi.Traffic) {
	now := time.UnixMilli(t.GetTimeUnixMs())
	if t.GetTimeUnixMs() == 0 {
		now = s.cfg.Clock()
	}
	s.update(func(sn *Snapshot) {
		prev := sn.Traffic
		next := Traffic{
			Available: t.GetAvailable(), UpTotal: t.GetUplinkTotal(), DownTotal: t.GetDownlinkTotal(),
			Connections: int(t.GetConnections()), Memory: t.GetMemory(), History: prev.History, Peak: prev.Peak,
		}
		if !t.GetAvailable() {
			next.UpTotal, next.DownTotal = 0, 0
			s.lastSample = now
			sn.Traffic = next
			return
		}
		dt := 1.0
		if !s.lastSample.IsZero() {
			if d := now.Sub(s.lastSample).Seconds(); d >= 0.2 && d <= 15 {
				dt = d
			}
		}
		s.lastSample = now
		next.Up, next.Down = float64(t.GetUplink())/dt, float64(t.GetDownlink())/dt
		next.History, next.Peak = pushSample(prev.History, Sample{Up: next.Up, Down: next.Down})
		sn.Traffic = next
	})
}

// pushSample appends a sample to a copy of history, keeping HistoryLen of them.
func pushSample(history []Sample, sample Sample) ([]Sample, float64) {
	out := make([]Sample, 0, HistoryLen)
	if n := len(history); n >= HistoryLen {
		out = append(out, history[n-HistoryLen+1:]...)
	} else {
		out = append(out, history...)
	}
	out = append(out, sample)
	peak := 0.0
	for _, v := range out {
		peak = max(peak, v.Up, v.Down)
	}
	return out, peak
}

// trafficClock keeps the throughput history flowing while no samples arrive
// (the core is off), so the chart shows time passing at zero.
func (s *Service) trafficClock(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		idle := !s.snap.Load().Traffic.Available && s.cfg.Clock().Sub(s.lastSample) >= 1500*time.Millisecond
		s.mu.Unlock()
		if !idle {
			continue
		}
		s.update(func(sn *Snapshot) {
			tr := sn.Traffic
			tr.History, tr.Peak = pushSample(tr.History, Sample{})
			tr.Up, tr.Down = 0, 0
			sn.Traffic = tr
		})
	}
}

const maxLogLines = 600

func (s *Service) onLogs(b *agentapi.LogBatch) {
	s.update(func(sn *Snapshot) {
		var lines []LogLine
		if !b.GetReset_() {
			lines = append(lines, sn.Logs...)
		}
		for _, e := range b.GetEntries() {
			lines = append(lines, LogLine{Time: time.UnixMilli(e.GetTimeUnixMs()), Level: e.GetLevel(), Source: e.GetSource(), Message: e.GetMessage()})
		}
		if n := len(lines); n > maxLogLines {
			lines = append([]LogLine(nil), lines[n-maxLogLines:]...)
		}
		sn.Logs = lines
	})
}

// WatchConnections starts or stops following the connection list. The list is
// only kept while something looks at it.
func (s *Service) WatchConnections(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on {
		if s.connCancel != nil {
			s.connCancel()
			s.connCancel = nil
		}
		return
	}
	if s.connCancel != nil || !s.started {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.connCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.agent.WatchConnections(ctx, time.Second, s.onConnections)
	}()
}

const (
	maxOpenConns   = 2000
	maxClosedConns = 150
)

func (s *Service) onConnections(ev *agentapi.ConnectionEvents) {
	s.update(func(sn *Snapshot) {
		if ev.GetReset_() {
			s.conns = map[string]*Conn{}
		}
		now := s.cfg.Clock()
		for _, e := range ev.GetEvents() {
			switch e.GetType() {
			case agentapi.ConnectionEvent_TYPE_NEW:
				if c := e.GetConnection(); c != nil {
					cc := connOf(c)
					s.conns[cc.ID] = &cc
				}
			case agentapi.ConnectionEvent_TYPE_UPDATE:
				if c := s.conns[e.GetId()]; c != nil {
					c.Up += e.GetUplinkDelta()
					c.Down += e.GetDownlinkDelta()
					c.UpRate, c.DownRate = float64(e.GetUplinkDelta()), float64(e.GetDownlinkDelta())
				}
			case agentapi.ConnectionEvent_TYPE_CLOSED:
				c := s.conns[e.GetId()]
				if c == nil {
					if cn := e.GetConnection(); cn != nil {
						cc := connOf(cn)
						c = &cc
						s.conns[cc.ID] = c
					}
				}
				if c != nil {
					if cn := e.GetConnection(); cn != nil {
						c.Up, c.Down = cn.GetUplinkTotal(), cn.GetDownlinkTotal()
					}
					c.Closed = now
					c.UpRate, c.DownRate = 0, 0
				}
			}
		}
		// Forget the oldest closed connections.
		var closed []*Conn
		for _, c := range s.conns {
			if !c.Open() {
				closed = append(closed, c)
			}
		}
		if len(closed) > maxClosedConns {
			sort.Slice(closed, func(i, j int) bool { return closed[i].Closed.After(closed[j].Closed) })
			for _, c := range closed[maxClosedConns:] {
				delete(s.conns, c.ID)
			}
		}
		list := make([]Conn, 0, len(s.conns))
		for _, c := range s.conns {
			list = append(list, *c)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Open() != list[j].Open() {
				return list[i].Open()
			}
			return list[i].Created.After(list[j].Created)
		})
		if len(list) > maxOpenConns+maxClosedConns {
			list = list[:maxOpenConns+maxClosedConns]
		}
		sn.Conns = list
	})
}

func connOf(c *agentapi.Connection) Conn {
	out := Conn{
		ID: c.GetId(), Network: c.GetNetwork(), Source: c.GetSource(), Destination: c.GetDestination(),
		Host: c.GetDomain(), Protocol: c.GetProtocol(), Rule: c.GetRule(), Outbound: c.GetOutbound(),
		Chain: c.GetChain(), Created: time.UnixMilli(c.GetCreatedUnixMs()),
		Up: c.GetUplinkTotal(), Down: c.GetDownlinkTotal(),
	}
	if p := c.GetProcess(); p != nil {
		out.Process = p.GetPath()
	}
	return out
}

// ------------------------------------------------------------------- notices

const maxNotices = 20

// notice records a notice for the interface to show.
func (s *Service) notice(level Level, code, detail string, args map[string]string) {
	s.update(func(sn *Snapshot) {
		s.noticeSeq++
		n := Notice{ID: s.noticeSeq, Time: s.cfg.Clock(), Level: level, Code: code, Args: args, Detail: detail}
		list := append(append([]Notice(nil), sn.Notices...), n)
		if len(list) > maxNotices {
			list = list[len(list)-maxNotices:]
		}
		sn.Notices = list
	})
}

// Report records an error as a notice.
func (s *Service) report(code string, err error) {
	ex := Explain(err)
	s.notice(LevelError, code, ex.Detail, map[string]string{"reason": ex.Code})
}

// setBusy changes the busy flags.
func (s *Service) setBusy(fn func(*Busy)) {
	s.update(func(sn *Snapshot) {
		b := sn.Busy
		b.Refreshing = copySet(b.Refreshing)
		b.Testing = copySet(b.Testing)
		fn(&b)
		sn.Busy = b
	})
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	for k, v := range m {
		if v {
			out[k] = true
		}
	}
	return out
}

// ErrNoNodes means the library holds no usable node to connect through.
var ErrNoNodes = errors.New("there are no nodes to connect through")

func isNoNodes(err error) bool {
	return errors.Is(err, configgen.ErrNoNodes) || errors.Is(err, subscription.ErrNoNodes)
}
