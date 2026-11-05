package gui

import (
	"context"

	"github.com/jtsang4/sing-box-ui/internal/agentapi"
	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/configgen"
	"github.com/jtsang4/sing-box-ui/internal/library"
)

// Backend is what the interface needs from the application layer. It is
// defined here, by its consumer, so that the interface can be driven by a
// fixture in tests and screenshots. *app.Service satisfies it through
// ServiceBackend.
//
// Reads are cheap and never block: the interface calls them every frame.
// Operations may take a long time (they talk to the service or the network);
// the interface runs them off the main thread.
type Backend interface {
	// Snapshot is an immutable view of the live state.
	Snapshot() *app.Snapshot
	// SetNotify registers the function to call, from any goroutine, after the
	// snapshot or the library changed.
	SetNotify(fn func())

	Subscriptions() []library.Subscription
	// ActiveNodes are the nodes of the enabled subscriptions, filtered and
	// deduplicated, as the configuration will use them. It reads files: the
	// interface calls it when the subscriptions change, not every frame.
	ActiveNodes() ([]library.ActiveNode, error)
	Settings() library.Settings
	UpdateSettings(fn func(*library.Settings)) error
	Prefs() configgen.Prefs
	Rules() []configgen.CustomRule
	RuleDataStatus() []app.RuleData

	Connect(ctx context.Context, o app.ConnectOptions) error
	Disconnect(ctx context.Context) error
	SelectKey(ctx context.Context, key string) error
	Select(ctx context.Context, tag string) error
	TestLatency(ctx context.Context, tag string) error
	SetMode(ctx context.Context, mode string) error
	CloseConnection(ctx context.Context, id string) error
	CloseAllConnections(ctx context.Context) error
	WatchConnections(on bool)
	RetryCleanup(ctx context.Context) error
	SetAgentSettings(ctx context.Context, fn func(*agentapi.UpdateSettingsRequest)) error

	AddSubscription(ctx context.Context, name, link string, o library.RemoteOptions) (library.Subscription, *library.RefreshResult, error)
	ImportNodes(ctx context.Context, name, text string) (library.Subscription, error)
	RefreshSubscription(ctx context.Context, id string) (*library.RefreshResult, error)
	RefreshAll(ctx context.Context) (int, error)
	EditSubscription(ctx context.Context, id string, fn func(*library.Subscription)) error
	RemoveSubscription(ctx context.Context, id string) error
	MoveSubscription(id string, index int) error
	// Link reveals a remote subscription's link; SetLink replaces it.
	Link(id string) (string, error)
	SetLink(id, link string) error

	SavePrefs(ctx context.Context, p configgen.Prefs) ([]string, error)
	SaveRules(ctx context.Context, rules []configgen.CustomRule) error
	UpdateRuleData(ctx context.Context, force bool) error

	ExportDiagnostics(ctx context.Context) (filename string, data []byte, err error)
}

// ServiceBackend adapts the application service to Backend.
type ServiceBackend struct{ *app.Service }

// Subscriptions lists the library's subscriptions.
func (b ServiceBackend) Subscriptions() []library.Subscription {
	return b.Library().Subscriptions()
}

// ActiveNodes lists the nodes the configuration will use.
func (b ServiceBackend) ActiveNodes() ([]library.ActiveNode, error) {
	return b.Library().ActiveNodes()
}

// Settings returns the application's preferences.
func (b ServiceBackend) Settings() library.Settings { return b.Library().Settings() }

// UpdateSettings changes the application's preferences.
func (b ServiceBackend) UpdateSettings(fn func(*library.Settings)) error {
	return b.Library().UpdateSettings(fn)
}

// Prefs returns the routing preferences.
func (b ServiceBackend) Prefs() configgen.Prefs { return b.Library().Prefs() }

// Rules returns the custom rules.
func (b ServiceBackend) Rules() []configgen.CustomRule { return b.Library().Rules() }

// MoveSubscription changes a subscription's position.
func (b ServiceBackend) MoveSubscription(id string, index int) error {
	return b.Library().Move(id, index)
}

// Link returns a remote subscription's link, from the secret store.
func (b ServiceBackend) Link(id string) (string, error) { return b.Library().Link(id) }

// SetLink replaces a remote subscription's link.
func (b ServiceBackend) SetLink(id, link string) error { return b.Library().SetLink(id, link) }
