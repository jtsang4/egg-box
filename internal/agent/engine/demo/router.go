package demo

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"

	"eggbox/internal/rulefmt"
)

// stringList reads a JSON string or a list of strings.
type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

// rawRule is a routing rule as far as the demonstration understands one: the
// conditions and actions the application's configurations use. A rule with
// anything else in it never matches, rather than matching everything.
type rawRule struct {
	Action       string     `json:"action"`
	Outbound     string     `json:"outbound"`
	Protocol     stringList `json:"protocol"`
	ClashMode    string     `json:"clash_mode"`
	IPIsPrivate  bool       `json:"ip_is_private"`
	DomainSuffix stringList `json:"domain_suffix"`
	RuleSet      stringList `json:"rule_set"`

	foreign bool // has a condition the demonstration cannot evaluate
}

var knownRuleKeys = map[string]bool{
	"action": true, "outbound": true, "protocol": true, "clash_mode": true,
	"ip_is_private": true, "domain_suffix": true, "rule_set": true, "strategy": true,
}

func (r *rawRule) UnmarshalJSON(b []byte) error {
	type plain rawRule
	if err := json.Unmarshal(b, (*plain)(r)); err != nil {
		return err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return err
	}
	for k := range keys {
		if !knownRuleKeys[k] {
			r.foreign = true
		}
	}
	return nil
}

// rawRuleSet is a rule set of the configuration: a file, or rules in place.
type rawRuleSet struct {
	Type  string            `json:"type"`
	Tag   string            `json:"tag"`
	Path  string            `json:"path"`
	Rules []json.RawMessage `json:"rules"`
}

// router decides, for an invented connection, which rule catches it, the way
// the core's router does: the rules in order, the first that matches wins,
// and what nothing matches goes to the final outbound.
type router struct {
	rules []rawRule
	sets  map[string]*rulefmt.Matcher
	final string
}

// loadRouter reads the rules and the rule sets they use. The files are the
// ones the agent's policy let through: it has already pointed every "res:"
// reference into its protected resource directory.
func loadRouter(cfg *rawConfig) (*router, error) {
	rt := &router{rules: cfg.Route.Rules, final: cfg.Route.Final, sets: map[string]*rulefmt.Matcher{}}
	for _, rs := range cfg.Route.RuleSet {
		var data []byte
		switch rs.Type {
		case "local":
			if strings.HasPrefix(rs.Path, "res:") {
				return nil, fmt.Errorf("demo core: rule set %s still points at %s: the policy resolves such references", rs.Tag, rs.Path)
			}
			b, err := os.ReadFile(rs.Path)
			if err != nil {
				return nil, fmt.Errorf("demo core: rule set %s: %w", rs.Tag, err)
			}
			data = b
		case "inline":
			b, err := json.Marshal(map[string]any{"version": 3, "rules": rs.Rules})
			if err != nil {
				return nil, fmt.Errorf("demo core: rule set %s: %w", rs.Tag, err)
			}
			data = b
		default:
			return nil, fmt.Errorf("demo core: rule set %s is of type %q, which the demonstration does not know", rs.Tag, rs.Type)
		}
		p, err := rulefmt.Parse(data, rulefmt.Hint{})
		if err != nil {
			return nil, fmt.Errorf("demo core: rule set %s: %w", rs.Tag, err)
		}
		m, err := rulefmt.NewMatcher(p.Rules)
		if err != nil {
			return nil, fmt.Errorf("demo core: rule set %s: %w", rs.Tag, err)
		}
		rt.sets[rs.Tag] = m
	}
	return rt, nil
}

// flow is what a rule can ask about a connection.
type flow struct {
	host        string // "" for a connection to a bare address
	addr        netip.Addr
	port        uint16
	network     string
	proto       string
	process     string
	processPath string
	mode        string
}

// verdict is the outcome of routing a connection.
type verdict struct {
	// rule is the text the core shows for the rule that caught it, empty when
	// nothing did.
	rule string
	// action is "route", "reject" or "hijack-dns".
	action   string
	outbound string
}

func (rt *router) route(f flow) verdict {
	for i := range rt.rules {
		r := &rt.rules[i]
		switch r.Action {
		case "sniff", "resolve":
			continue
		}
		if r.foreign || !rt.matches(r, f) {
			continue
		}
		switch r.Action {
		case "reject":
			return verdict{rule: r.describe() + " => reject", action: "reject"}
		case "hijack-dns":
			return verdict{rule: r.describe() + " => hijack-dns", action: "hijack-dns", outbound: "direct"}
		case "", "route":
			return verdict{rule: r.describe() + " => route(" + r.Outbound + ")", action: "route", outbound: r.Outbound}
		}
	}
	return verdict{action: "route", outbound: rt.final}
}

func (rt *router) matches(r *rawRule, f flow) bool {
	if len(r.Protocol) == 0 && r.ClashMode == "" && !r.IPIsPrivate && len(r.DomainSuffix) == 0 && len(r.RuleSet) == 0 {
		return false
	}
	if len(r.Protocol) > 0 && !slices.Contains(r.Protocol, f.proto) {
		return false
	}
	if r.ClashMode != "" && !strings.EqualFold(r.ClashMode, f.mode) {
		return false
	}
	if r.IPIsPrivate && !(f.addr.IsPrivate() || f.addr.IsLoopback() || f.addr.IsLinkLocalUnicast()) {
		return false
	}
	if len(r.DomainSuffix) > 0 && !hasSuffix(f.host, r.DomainSuffix) {
		return false
	}
	if len(r.RuleSet) > 0 {
		q := rulefmt.Query{Domain: f.host, Port: f.port, Network: f.network, Process: f.process, ProcessPath: f.processPath}
		if f.addr.IsValid() {
			q.Addrs = []netip.Addr{f.addr}
		}
		hit := false
		for _, tag := range r.RuleSet {
			if m := rt.sets[tag]; m != nil && m.Match(q) == rulefmt.Match {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func hasSuffix(host string, suffixes []string) bool {
	if host == "" {
		return false
	}
	for _, s := range suffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// describe writes the conditions the way the core does, so that what the
// interface makes of a connection's rule is exercised against the real
// format: "rule_set=tag", "rule_set=[a b]", "clash_mode=Direct",
// "ip_is_private=true", "domain_suffix=[a b c...]".
func (r *rawRule) describe() string {
	var parts []string
	list := func(name string, items []string, cut bool) {
		switch {
		case len(items) == 0:
		case len(items) == 1:
			parts = append(parts, name+"="+items[0])
		case cut && len(items) > 3:
			parts = append(parts, name+"=["+strings.Join(items[:3], " ")+"...]")
		default:
			parts = append(parts, name+"=["+strings.Join(items, " ")+"]")
		}
	}
	list("protocol", r.Protocol, false)
	if r.ClashMode != "" {
		parts = append(parts, "clash_mode="+r.ClashMode)
	}
	list("domain_suffix", r.DomainSuffix, true)
	if r.IPIsPrivate {
		parts = append(parts, "ip_is_private=true")
	}
	list("rule_set", r.RuleSet, false)
	return strings.Join(parts, " ")
}
