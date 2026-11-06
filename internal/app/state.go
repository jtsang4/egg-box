package app

import (
	"time"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/plancompile"
	"eggbox/internal/routing"
	"eggbox/internal/sysproxy"
)

// Phase is where the connection stands, for the interface.
type Phase int

const (
	// PhaseNoAgent: the privileged service cannot be reached (not installed,
	// not running, refused us or speaks another version).
	PhaseNoAgent Phase = iota
	// PhaseDisconnected: the service is ready and the proxy is off.
	PhaseDisconnected
	PhaseConnecting
	PhaseConnected
	// PhaseReconnecting: the user wants to be connected and the core is being
	// re-established (network change, wake, crash).
	PhaseReconnecting
	PhaseDisconnecting
	// PhaseFailed: the last attempt failed and the proxy is off.
	PhaseFailed
)

func (p Phase) String() string {
	return [...]string{"no-agent", "disconnected", "connecting", "connected", "reconnecting", "disconnecting", "failed"}[p]
}

// Online reports whether traffic is (or should be) flowing through the proxy.
func (p Phase) Online() bool { return p == PhaseConnected || p == PhaseReconnecting }

// Transitional reports whether the phase is a change in progress.
func (p Phase) Transitional() bool {
	return p == PhaseConnecting || p == PhaseReconnecting || p == PhaseDisconnecting
}

// AgentInfo describes the service behind the link.
type AgentInfo struct {
	AgentVersion string
	CoreVersion  string
	Platform     string
	Arch         string
	Driver       string
	Peer         string
	DevMode      bool
	StartedAt    time.Time
	InstanceID   string
}

// LinkInfo is the state of the connection to the privileged service.
type LinkInfo struct {
	State agentclient.LinkState
	// Error explains an unusable link, in the client's words.
	Error string
	Agent AgentInfo
	// Generation changes whenever the link was re-established.
	Generation uint64
}

// Ready reports whether calls go through.
func (l LinkInfo) Ready() bool { return l.State == agentclient.LinkReady }

// NodeRef is what is known about the node behind a group item.
type NodeRef struct {
	// Key identifies the node's configuration (a hash); it changes when the
	// provider rotates a password or an address.
	Key string
	// ID is the node's identity for routing choices: subscription and name,
	// with the hash and region as a fallback.
	ID        routing.NodeID
	Name      string
	Protocol  string
	Transport string
	Region    string
	Server    string
	Port      int
	SubID     string
	SubName   string
	// SubAlias is the short name the person gave the subscription, if any.
	SubAlias string
	// Ambiguous is set when another subscription offers a node with the same
	// name, so the name alone does not say which one it is.
	Ambiguous bool
}

// Label is the node's name, with its subscription when the name alone would
// not tell it apart.
func (n NodeRef) Label() string {
	if !n.Ambiguous {
		return n.Name
	}
	sub := n.SubAlias
	if sub == "" {
		sub = n.SubName
	}
	if sub == "" {
		return n.Name
	}
	return n.Name + " · " + sub
}

// GroupItem is a member of a group: a node or another group.
type GroupItem struct {
	Tag     string
	Type    string
	DelayMS int
	Tested  time.Time
	// Node is set for items that are nodes; nil for groups and "direct".
	Node *NodeRef
}

// Group is a selector or a fastest-node group.
type Group struct {
	Tag        string
	Type       string
	Selectable bool
	Selected   string
	Items      []GroupItem
}

// Sample is the throughput of one interval, in bytes per second.
type Sample struct {
	Up, Down float64
}

// HistoryLen is how many throughput samples are kept (one per second).
const HistoryLen = 90

// Traffic is the throughput the core reports.
type Traffic struct {
	Available   bool
	Up, Down    float64 // bytes per second
	UpTotal     int64
	DownTotal   int64
	Connections int
	Memory      uint64
	// History is oldest first, at most HistoryLen samples.
	History []Sample
	// Peak is the largest sample in History.
	Peak float64
}

// WhyKind says what decided where a connection went.
type WhyKind int

const (
	// WhyUnknown: the core's rule is not one the application wrote.
	WhyUnknown WhyKind = iota
	// WhyItem: an item of the plan in force.
	WhyItem
	// WhyException: one of the person's exceptions.
	WhyException
	// WhyFallback: nothing matched.
	WhyFallback
	// WhyLAN: the local network.
	WhyLAN
	// WhyMode: the Global or Direct mode.
	WhyMode
	// WhyUnresolved: the plan asks where a name leads, and the name leads
	// nowhere, so the core turns the connection away (only a test of an
	// address says so: such a connection is not in the list).
	WhyUnresolved
)

// Why is the reason a connection took the route it did, in the person's terms
// instead of the core's rule text.
type Why struct {
	Kind WhyKind
	// ID is the item or exception that matched.
	ID string
	// Name is the item's name, or what the exception matches.
	Name string
	// Index is the exception's place in the list, from 1.
	Index int
}

// Conn is one tracked connection.
type Conn struct {
	ID          string
	Network     string
	Source      string
	Destination string
	Host        string
	Protocol    string
	// Rule is the core's own text; use Why for anything the person reads.
	Rule     string
	Why      Why
	Outbound string
	Chain    []string
	Process  string
	Created  time.Time
	Closed   time.Time // zero while open
	Up, Down int64
	UpRate   float64
	DownRate float64
}

// Open reports whether the connection is still open.
func (c Conn) Open() bool { return c.Closed.IsZero() }

// LogLine is one log entry.
type LogLine struct {
	Time    time.Time
	Level   string
	Source  string
	Message string
	local   bool // application diagnostics survive a replay from the agent
}

// Level of a notice.
type Level string

const (
	LevelInfo    Level = "info"
	LevelSuccess Level = "success"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

// Notice is something the user should be told about, with a stable code the
// interface translates.
type Notice struct {
	ID    uint64
	Time  time.Time
	Level Level
	Code  string
	// Args fill the placeholders of the translated text.
	Args map[string]string
	// Detail is the raw explanation, in English, for the details line.
	Detail string
}

// Busy lists what the application is doing right now.
type Busy struct {
	Connect    bool
	Apply      bool
	RuleData   bool
	Refreshing map[string]bool // subscription ids
	Testing    map[string]bool // group tags
}

// Any reports whether anything is in progress.
func (b Busy) Any() bool {
	return b.Connect || b.Apply || b.RuleData || len(b.Refreshing) > 0 || len(b.Testing) > 0
}

// Snapshot is an immutable view of everything the interface shows that comes
// from outside it. The service replaces the whole value on every change and
// never modifies one in place, so readers need no locks.
type Snapshot struct {
	Link   LinkInfo
	Status *agentapi.Status
	Phase  Phase
	// AgentSettings are the service's own settings (auto-connect...).
	AgentSettings *agentapi.Settings

	Groups  []Group
	Traffic Traffic
	// Conns are the tracked connections, newest first (only while watched).
	Conns []Conn
	Logs  []LogLine
	Busy  Busy
	// Notices are the most recent notices, oldest first.
	Notices []Notice
	// Modes are the routing modes the core offers.
	Modes []string
	// Applied describes the configuration last pushed to the service.
	Applied AppliedInfo
	// Plan describes how the plan in force came out when it was compiled: what
	// each item can use, what is missing.
	Plan PlanInfo
	// Exits tells where traffic of the main exit ("main"), the fallback
	// ("fallback") and each item (by id) ends up right now.
	Exits map[string]ExitInfo
	// ProxyIssues are the system proxy settings that point at a local port
	// that is not this application's, found while connected: the ones that
	// lead nowhere cut off every program that uses the system proxy. The
	// most pressing comes first.
	ProxyIssues []sysproxy.Issue
}

// ItemState is what became of an item when the plan was compiled.
type ItemState = plancompile.ItemState

// Item states.
const (
	ItemReady       = plancompile.ItemReady
	ItemOff         = plancompile.ItemOff
	ItemUnavailable = plancompile.ItemUnavailable
	ItemPartial     = plancompile.ItemPartial
)

// ItemStatus describes one row of the plan for the interface.
type ItemStatus struct {
	State ItemState
	// Missing counts sources (and the files they refer to) with no usable data.
	Missing int
	// Unsupported counts rules the core cannot evaluate.
	Unsupported int
	// Domain and IP count the conditions that made it into the configuration.
	Domain, IP int
	// Sources is the number of sources the item has.
	Sources int
	// Oldest is the oldest successful check among the item's files; zero when
	// the item has no downloaded file (hand-written items never do).
	Oldest time.Time
	// Failing is set when the last attempt to update one of its files failed.
	Failing bool
}

// SourceStatus describes one downloaded rule file.
type SourceStatus struct {
	URL     string
	Present bool
	// Format is what the file was recognised as.
	Format  string
	Size    int64
	Updated time.Time
	Checked time.Time
	// Domain, IP and Other count what the file holds; Unsupported counts by
	// the name of the kind what the core cannot evaluate.
	Domain, IP, Other int
	Unsupported       map[string]int
	// Err is why the last update failed, in words for people.
	Err    string
	Mirror string
}

// Pin says a node somebody chose is not what carries their traffic.
type Pin struct {
	// Owner is "main", "fallback", "item" or "exception".
	Owner plancompile.OwnerKind
	ID    string
	// Name is the item's name, or what the exception matches.
	Name string
	Node routing.NodeID
}

// PlanInfo is how the plan in force compiled.
type PlanInfo struct {
	// Seq changes with every compilation.
	Seq uint64
	// Ready is false until the plan was compiled once.
	Ready bool
	// Items are the rows of the plan by item id.
	Items map[string]ItemStatus
	// Sources are the rule files the plan uses by canonical address.
	Sources map[string]SourceStatus
	// Missing counts the files the plan needs and does not have.
	Missing int
	// Gone are chosen nodes no subscription offers any more.
	Gone []Pin
	// Updating is set while rule data is being downloaded.
	Updating bool
}

// ExitInfo says where traffic leaves right now.
type ExitInfo struct {
	// Group is the tag of the group the traffic goes through; empty for
	// traffic that is blocked.
	Group string
	// Member is what that group has selected: "Proxy", "direct", "Auto", a
	// region group or a node's tag.
	Member string
	// Node is the node the traffic ends up on, when there is one.
	Node *NodeRef
	// DelayMS is that node's last measured delay; 0 when not measured.
	DelayMS int
	// Wanted is the node somebody chose for this exit, if they chose one.
	Wanted *routing.NodeID
	// StandIn is set when the node in use is not the one that was chosen: the
	// chosen one does not answer, or no subscription offers it any more.
	StandIn bool
}

// AppliedInfo describes the last configuration the application generated.
type AppliedInfo struct {
	At       time.Time
	Revision uint64
	Nodes    int
	Dropped  []Dropped
	Warnings []Warning
}

// Dropped is a node the configuration leaves out, with the reason.
type Dropped struct {
	Name   string
	Reason string
}

// Warning is a generator warning, translated by code.
type Warning struct {
	Code   string
	Detail string
}

// phaseOf derives the interface phase from the link and the service status.
func phaseOf(l LinkInfo, st *agentapi.Status) Phase {
	if !l.Ready() || st == nil {
		return PhaseNoAgent
	}
	switch st.GetConnection() {
	case agentapi.ConnectionState_CONNECTION_STATE_CONNECTING:
		return PhaseConnecting
	case agentapi.ConnectionState_CONNECTION_STATE_CONNECTED:
		return PhaseConnected
	case agentapi.ConnectionState_CONNECTION_STATE_RECONNECTING:
		return PhaseReconnecting
	case agentapi.ConnectionState_CONNECTION_STATE_DISCONNECTING:
		return PhaseDisconnecting
	case agentapi.ConnectionState_CONNECTION_STATE_FAILED:
		return PhaseFailed
	}
	return PhaseDisconnected
}
