package rulefmt

import (
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// Traits say what deciding a list of rules takes.
type Traits struct {
	// Host: some rule matches host names.
	Host bool
	// Addr: some rule needs the destination address, so the core has to
	// resolve the name first.
	Addr bool
	// Process: some rule matches the program that opened the connection, which
	// the core only knows when asked to look it up.
	Process bool
}

// Inspect reports what a list of rules depends on.
func Inspect(rules []option.HeadlessRule) Traits {
	var t Traits
	for _, r := range rules {
		inspect(r, &t)
	}
	return t
}

func inspect(r option.HeadlessRule, t *Traits) {
	if r.Type == C.RuleTypeLogical {
		for _, sub := range r.LogicalOptions.Rules {
			inspect(sub, t)
		}
		return
	}
	d := r.DefaultOptions
	if hostConditions(d) {
		t.Host = true
	}
	if len(d.IPCIDR) > 0 || d.IPSet != nil {
		t.Addr = true
	}
	if len(d.ProcessName) > 0 || len(d.ProcessPath) > 0 || len(d.ProcessPathRegex) > 0 {
		t.Process = true
	}
}

func hostConditions(d option.DefaultHeadlessRule) bool {
	return len(d.Domain)+len(d.DomainSuffix)+len(d.DomainKeyword)+len(d.DomainRegex)+len(d.AdGuardDomain) > 0
}

// HostRules keeps the rules that host names alone decide. DNS routing has
// nothing else to go by: a rule that also asks for a port, a program or an
// address would behave differently there than for the connection itself, and
// an inverted one would send every other name the wrong way.
func HostRules(rules []option.HeadlessRule) []option.HeadlessRule {
	var out []option.HeadlessRule
	for _, r := range rules {
		if r.Type == C.RuleTypeLogical {
			continue
		}
		d := r.DefaultOptions
		if d.Invert || !hostConditions(d) || len(d.IPCIDR) > 0 || d.IPSet != nil || hasOtherConditions(d) {
			continue
		}
		out = append(out, r)
	}
	return out
}
