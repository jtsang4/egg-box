package app

import (
	"time"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
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
	Key       string
	Name      string
	Protocol  string
	Transport string
	Region    string
	Server    string
	Port      int
	SubID     string
	SubName   string
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

// Conn is one tracked connection.
type Conn struct {
	ID          string
	Network     string
	Source      string
	Destination string
	Host        string
	Protocol    string
	Rule        string
	Outbound    string
	Chain       []string
	Process     string
	Created     time.Time
	Closed      time.Time // zero while open
	Up, Down    int64
	UpRate      float64
	DownRate    float64
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
