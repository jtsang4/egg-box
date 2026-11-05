package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jtsang4/sing-box-ui/internal/agent/routecheck"
	"github.com/jtsang4/sing-box-ui/internal/agent/store"
	"github.com/jtsang4/sing-box-ui/internal/agentapi"
	"github.com/jtsang4/sing-box-ui/internal/brand"
	"github.com/jtsang4/sing-box-ui/internal/redact"
)

const (
	diagnosticsSchema    = 1
	diagnosticsLogLines  = 300
	diagnosticsMaxRoutes = 300
)

// diagnostics is the report layout. It deliberately has no field that could
// hold a credential: the configuration appears as a shape (counts and types)
// and the ledger without the tokens it uses to prove ownership. The whole
// document additionally passes through the redactor before it leaves.
type diagnostics struct {
	Schema    int             `json:"schema"`
	Generated time.Time       `json:"generated"`
	Agent     diagAgent       `json:"agent"`
	Status    json.RawMessage `json:"status"`
	Settings  store.Settings  `json:"settings"`
	Intent    store.Intent    `json:"intent"`
	Revisions diagRevisions   `json:"revisions"`
	Config    *diagConfig     `json:"config,omitempty"`
	Ledger    []diagLedger    `json:"ledger"`
	Network   diagNetwork     `json:"network"`
	Logs      []diagLog       `json:"logs"`
}

type diagAgent struct {
	Version  string `json:"version"`
	Core     string `json:"core"`
	Protocol int    `json:"protocol"`
	Instance string `json:"instance"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Go       string `json:"go"`
	Driver   string `json:"driver"`
	Sandbox  bool   `json:"sandbox"`
}

type diagRevisions struct {
	Current  uint64    `json:"current"`
	LastGood uint64    `json:"last_good"`
	Profile  string    `json:"profile,omitempty"`
	Created  time.Time `json:"created"`
}

type diagTagged struct {
	Type string `json:"type"`
	Tag  string `json:"tag,omitempty"`
}

type diagConfig struct {
	Revision   uint64         `json:"revision"`
	Inbounds   []diagTagged   `json:"inbounds"`
	Outbounds  map[string]int `json:"outbounds_by_type"`
	RouteRules int            `json:"route_rules"`
	RuleSets   int            `json:"rule_sets"`
	Final      string         `json:"route_final,omitempty"`
	DNSServers int            `json:"dns_servers"`
	DNSRules   int            `json:"dns_rules"`
	Error      string         `json:"error,omitempty"`
}

type diagLedger struct {
	Kind string `json:"kind"`
	// Resource names what was created (an interface, a resolver entry). The
	// word "key" is avoided on purpose: the redactor treats it as a credential.
	Resource string    `json:"resource"`
	Created  time.Time `json:"created"`
}

type diagNetwork struct {
	Online      bool        `json:"online"`
	Fingerprint string      `json:"fingerprint"`
	Interfaces  []diagIface `json:"interfaces"`
	Routes      []diagRoute `json:"routes"`
	RouteCount  int         `json:"route_count"`
	Error       string      `json:"error,omitempty"`
}

type diagIface struct {
	Name  string   `json:"name"`
	Up    bool     `json:"up"`
	Kind  string   `json:"kind"`
	Addrs []string `json:"addrs"`
	Ours  bool     `json:"ours,omitempty"`
}

type diagRoute struct {
	Dest    string `json:"dest"`
	Gateway string `json:"gateway,omitempty"`
	If      string `json:"if"`
}

type diagLog struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

// GetDiagnostics returns a report that is safe to attach to a bug report. It is
// read-only, so it does not wait in the operation queue: it must keep working
// while the agent is busy or in trouble, because that is when it is needed.
func (c *Controller) GetDiagnostics(ctx context.Context) (*agentapi.DiagnosticsReport, error) {
	now := time.Now().UTC()
	status, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(c.Status())
	if err != nil {
		return nil, fmt.Errorf("encode status: %w", err)
	}

	d := diagnostics{
		Schema:    diagnosticsSchema,
		Generated: now,
		Agent: diagAgent{
			Version:  c.cfg.Version,
			Core:     c.cfg.Engine.Version(),
			Protocol: brand.ProtocolVersion,
			Instance: c.cfg.InstanceID,
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
			Go:       runtime.Version(),
			Driver:   c.cfg.Driver.Name(),
			Sandbox:  c.cfg.Driver.Sandbox(),
		},
		Status:   status,
		Settings: c.cfg.Settings.Load(),
		Intent:   c.cfg.Intent.Load(),
		Ledger:   []diagLedger{},
		Logs:     []diagLog{},
	}

	if rev, ok := c.cfg.Store.Current(); ok {
		d.Revisions.Current = rev.N
		d.Revisions.Profile = rev.ProfileName
		d.Revisions.Created = rev.Created
		d.Config = configShape(rev)
	}
	if rev, ok := c.cfg.Store.LastGood(); ok {
		d.Revisions.LastGood = rev.N
	}

	for _, e := range c.cfg.Ledger.Entries() {
		d.Ledger = append(d.Ledger, diagLedger{Kind: string(e.Kind), Resource: e.Key, Created: e.Created})
	}

	d.Network = c.networkInfo(ctx)

	for _, l := range c.cfg.Hub.Tail(diagnosticsLogLines, "debug") {
		d.Logs = append(d.Logs, diagLog{Time: time.UnixMilli(l.TimeUnixMs).UTC(), Level: l.Level, Source: l.Source, Message: l.Message})
	}

	raw, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode report: %w", err)
	}
	// The application's own identifiers look like host names but are not secret
	// and are needed to read the report.
	out, err := redact.New(brand.AppID).JSON(raw)
	if err != nil {
		return nil, fmt.Errorf("redact report: %w", err)
	}
	c.logAgent("info", "diagnostics report generated")
	return &agentapi.DiagnosticsReport{
		SuggestedFilename: "sing-box-ui-diagnostics-" + now.Format("20060102-150405") + ".json",
		Json:              out,
		GeneratedUnixMs:   now.UnixMilli(),
	}, nil
}

func (c *Controller) networkInfo(ctx context.Context) diagNetwork {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n := diagNetwork{Online: c.cfg.Driver.Online(ctx), Interfaces: []diagIface{}, Routes: []diagRoute{}}
	if fp, err := c.cfg.Driver.Fingerprint(ctx); err == nil {
		n.Fingerprint = fp
	}
	snap, err := c.cfg.Driver.Snapshot(ctx)
	if err != nil {
		n.Error = "snapshot: " + err.Error()
		return n
	}
	for _, i := range snap.Interfaces {
		item := diagIface{Name: i.Name, Up: i.Up, Kind: i.Kind.String(), Ours: i.Ours, Addrs: []string{}}
		for _, a := range i.Addrs {
			item.Addrs = append(item.Addrs, a.String())
		}
		n.Interfaces = append(n.Interfaces, item)
	}
	n.RouteCount = len(snap.Routes)
	routes := append([]routecheck.Route(nil), snap.Routes...)
	// Default and broad routes are what explain a conflict, so list them first.
	sort.SliceStable(routes, func(a, b int) bool { return routes[a].Dest.Bits() < routes[b].Dest.Bits() })
	for i, r := range routes {
		if i >= diagnosticsMaxRoutes {
			break
		}
		item := diagRoute{Dest: r.Dest.String(), If: r.IfName}
		if r.Gateway.IsValid() {
			item.Gateway = r.Gateway.String()
		}
		n.Routes = append(n.Routes, item)
	}
	return n
}

// configShape summarizes a stored configuration without reproducing anything
// that identifies a server or an account.
func configShape(rev store.Revision) *diagConfig {
	shape := &diagConfig{Revision: rev.N, Inbounds: []diagTagged{}, Outbounds: map[string]int{}}
	data, err := os.ReadFile(rev.ConfigPath())
	if err != nil {
		shape.Error = "read: " + err.Error()
		return shape
	}
	var doc struct {
		Inbounds  []diagTagged `json:"inbounds"`
		Outbounds []struct {
			Type string `json:"type"`
		} `json:"outbounds"`
		Route struct {
			Rules   []json.RawMessage `json:"rules"`
			RuleSet []json.RawMessage `json:"rule_set"`
			Final   string            `json:"final"`
		} `json:"route"`
		DNS struct {
			Servers []json.RawMessage `json:"servers"`
			Rules   []json.RawMessage `json:"rules"`
		} `json:"dns"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		shape.Error = "parse: " + err.Error()
		return shape
	}
	if doc.Inbounds != nil {
		shape.Inbounds = doc.Inbounds
	}
	for _, o := range doc.Outbounds {
		shape.Outbounds[o.Type]++
	}
	shape.RouteRules = len(doc.Route.Rules)
	shape.RuleSets = len(doc.Route.RuleSet)
	shape.Final = doc.Route.Final
	shape.DNSServers = len(doc.DNS.Servers)
	shape.DNSRules = len(doc.DNS.Rules)
	return shape
}
