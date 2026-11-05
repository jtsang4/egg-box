package rulefmt

import (
	"testing"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

func TestInspectAndHostRules(t *testing.T) {
	host := def(option.DefaultHeadlessRule{DomainSuffix: []string{"a.test"}})
	ip := def(option.DefaultHeadlessRule{IPCIDR: []string{"10.0.0.0/8"}})
	proc := def(option.DefaultHeadlessRule{ProcessName: []string{"curl"}})
	mixed := def(option.DefaultHeadlessRule{DomainSuffix: []string{"b.test"}, Port: []uint16{443}})
	inverted := def(option.DefaultHeadlessRule{DomainSuffix: []string{"c.test"}, Invert: true})
	logical := option.HeadlessRule{Type: C.RuleTypeLogical, LogicalOptions: option.LogicalHeadlessRule{
		Mode: C.LogicalTypeAnd, Rules: []option.HeadlessRule{host, proc},
	}}

	cases := []struct {
		name  string
		rules []option.HeadlessRule
		want  Traits
	}{
		{"none", nil, Traits{}},
		{"host", []option.HeadlessRule{host}, Traits{Host: true}},
		{"ip", []option.HeadlessRule{ip}, Traits{Addr: true}},
		{"process", []option.HeadlessRule{proc}, Traits{Process: true}},
		{"mixed", []option.HeadlessRule{host, ip, proc}, Traits{Host: true, Addr: true, Process: true}},
		{"inside a logical rule", []option.HeadlessRule{logical}, Traits{Host: true, Process: true}},
	}
	for _, c := range cases {
		if got := Inspect(c.rules); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}

	got := HostRules([]option.HeadlessRule{host, ip, proc, mixed, inverted, logical})
	if len(got) != 1 || got[0].DefaultOptions.DomainSuffix[0] != "a.test" {
		t.Errorf("HostRules kept %d rules, want only the plain host rule", len(got))
	}
}
