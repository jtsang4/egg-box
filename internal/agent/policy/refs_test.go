package policy

import (
	"strings"
	"testing"
)

const refBase = `{
  "dns": {
    "servers": [
      {"type": "udp", "tag": "dns-direct", "server": "223.5.5.5"},
      {"type": "https", "tag": "dns-remote", "server": "1.1.1.1", "detour": "proxy"}
    ],
    "rules": [{"rule_set": ["ads"], "server": "dns-direct"}],
    "final": "dns-remote"
  },
  "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen_port": 7890}],
  "outbounds": [
    {"type": "shadowsocks", "tag": "node-a", "server": "203.0.113.5", "server_port": 8388, "method": "aes-128-gcm", "password": "x", "domain_resolver": "dns-direct"},
    {"type": "direct", "tag": "direct"},
    {"type": "selector", "tag": "proxy", "outbounds": ["node-a", "direct"], "default": "node-a"},
    {"type": "urltest", "tag": "auto", "outbounds": ["node-a"], "url": "https://www.gstatic.com/generate_204"}
  ],
  "route": {
    "rule_set": [{"type": "inline", "tag": "ads", "rules": [{"domain_suffix": ["ads.example"]}]}],
    "rules": [
      {"action": "sniff"},
      {"rule_set": ["ads"], "action": "reject"},
      {"clash_mode": "Direct", "outbound": "direct"},
      {"domain_suffix": ["lan"], "action": "route", "outbound": "direct"},
      {"type": "logical", "mode": "or", "rules": [{"domain": ["a.example"]}, {"domain": ["b.example"]}], "outbound": "auto"}
    ],
    "final": "proxy",
    "default_domain_resolver": {"server": "dns-direct", "strategy": "prefer_ipv4"}
  }
}`

func TestReferencesInAValidConfigPass(t *testing.T) {
	mustPass(t, prod(), refBase)
}

func TestDanglingReferencesAreRejectedWithTheirLocation(t *testing.T) {
	cases := []struct {
		name, from, to string
		path           string
	}{
		{"route rule outbound", `{"clash_mode": "Direct", "outbound": "direct"}`, `{"clash_mode": "Direct", "outbound": "nowhere"}`, "$.route.rules[2].outbound"},
		{"route rule outbound in a logical rule", `"outbound": "auto"`, `"outbound": "gone"`, "$.route.rules[4].outbound"},
		{"final outbound", `"final": "proxy"`, `"final": "gone"`, "$.route.final"},
		{"selector member", `"outbounds": ["node-a", "direct"]`, `"outbounds": ["node-a", "ghost"]`, "$.outbounds[2].outbounds[1]"},
		{"url-test member", `"outbounds": ["node-a"], "url"`, `"outbounds": ["ghost"], "url"`, "$.outbounds[3].outbounds[0]"},
		{"selector default", `"default": "node-a"`, `"default": "direct-2"`, "$.outbounds[2].default"},
		{"dns server detour", `"detour": "proxy"`, `"detour": "gone"`, "$.dns.servers[1].detour"},
		{"dns rule server", `"server": "dns-direct"}],
    "final"`, `"server": "dns-missing"}],
    "final"`, "$.dns.rules[0].server"},
		{"dns final", `"final": "dns-remote"`, `"final": "dns-missing"`, "$.dns.final"},
		{"outbound domain resolver", `"domain_resolver": "dns-direct"`, `"domain_resolver": "dns-missing"`, "$.outbounds[0].domain_resolver"},
		{"default domain resolver", `{"server": "dns-direct", "strategy": "prefer_ipv4"}`, `{"server": "dns-missing", "strategy": "prefer_ipv4"}`, "$.route.default_domain_resolver"},
	}
	for _, c := range cases {
		if !strings.Contains(refBase, c.from) {
			t.Fatalf("%s: the fixture does not contain %q", c.name, c.from)
		}
		cfg := strings.Replace(refBase, c.from, c.to, 1)
		res := mustFail(t, prod(), cfg, "reference_missing")
		found := false
		for _, is := range res.Errors() {
			if is.Code == "reference_missing" && is.Path == c.path {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no reference_missing at %s:\n%s", c.name, c.path, dump(res))
		}
	}
}

// The group of a node the person pinned (the member they chose, and the groups
// that stand in for it while it is unhealthy) is allowed, and checked like the
// others: what it names must exist, its default must be one of its members, and
// a stand-in must be a group, not a single node.
func TestAPreferredGroupIsAllowedAndWhatItNamesMustExist(t *testing.T) {
	const (
		urltest = `{"type": "urltest", "tag": "auto", "outbounds": ["node-a"], "url": "https://www.gstatic.com/generate_204"}`
		pinned  = `{"type": "preferred", "tag": "pin", "outbounds": ["node-a"], "default": "node-a", "stand_ins": ["auto"], "url": "https://www.gstatic.com/generate_204", "interval": "1m"}`
	)
	if strings.Count(refBase, urltest) != 1 {
		t.Fatal("the fixture does not hold the group this test builds on")
	}
	with := func(groups ...string) string {
		cfg := strings.Replace(refBase, urltest, urltest+", "+strings.Join(groups, ", "), 1)
		// Something is sent to the group, and the person's selector offers it.
		cfg = strings.Replace(cfg, `{"action": "sniff"},`, `{"action": "sniff"}, {"domain": ["pinned.example"], "outbound": "pin"},`, 1)
		return strings.Replace(cfg, `"outbounds": ["node-a", "direct"]`, `"outbounds": ["node-a", "direct", "pin"]`, 1)
	}
	mustPass(t, prod(), with(pinned))
	// A stand-in is any group: a selector, or another group of the same kind.
	mustPass(t, prod(), with(strings.Replace(pinned, `"stand_ins": ["auto"]`, `"stand_ins": ["proxy"]`, 1)))
	mustPass(t, prod(), with(pinned, `{"type": "preferred", "tag": "pin2", "outbounds": ["direct"], "default": "direct"}`,
		`{"type": "preferred", "tag": "pin3", "outbounds": ["node-a"], "stand_ins": ["pin2"]}`))

	for _, c := range []struct {
		name, from, to, code, path string
	}{
		{"a member that is not there", `"outbounds": ["node-a"], "default": "node-a", "stand_ins"`, `"outbounds": ["ghost"], "default": "ghost", "stand_ins"`, "reference_missing", "$.outbounds[4].outbounds[0]"},
		{"a default that is not a member", `"default": "node-a", "stand_ins"`, `"default": "direct", "stand_ins"`, "reference_missing", "$.outbounds[4].default"},
		{"a stand-in that is not there", `"stand_ins": ["auto"]`, `"stand_ins": ["ghost"]`, "reference_missing", "$.outbounds[4].stand_ins[0]"},
		{"a stand-in that is a single node", `"stand_ins": ["auto"]`, `"stand_ins": ["node-a"]`, "reference_invalid", "$.outbounds[4].stand_ins[0]"},
	} {
		if !strings.Contains(pinned, c.from) {
			t.Fatalf("%s: the fixture does not contain %q", c.name, c.from)
		}
		res := mustFail(t, prod(), with(strings.Replace(pinned, c.from, c.to, 1)), c.code)
		found := false
		for _, is := range res.Errors() {
			found = found || (is.Code == c.code && is.Path == c.path)
		}
		if !found {
			t.Errorf("%s: no %s at %s:\n%s", c.name, c.code, c.path, dump(res))
		}
	}
}

func TestResolveActionNeedsAKnownServer(t *testing.T) {
	ok := strings.Replace(refBase, `{"action": "sniff"},`, `{"action": "sniff"}, {"action": "resolve", "server": "dns-direct"},`, 1)
	mustPass(t, prod(), ok)
	bad := strings.Replace(refBase, `{"action": "sniff"},`, `{"action": "sniff"}, {"action": "resolve", "server": "dns-missing"},`, 1)
	mustFail(t, prod(), bad, "reference_missing")
}

func TestEndpointsCountAsOutbounds(t *testing.T) {
	cfg := `{
	  "inbounds": [{"type": "mixed", "tag": "m", "listen_port": 7890}],
	  "endpoints": [{"type": "wireguard", "tag": "wg", "address": ["10.0.0.2/32"], "private_key": "k", "peers": [{"address": "203.0.113.9", "port": 51820, "public_key": "p", "allowed_ips": ["0.0.0.0/0"]}]}],
	  "outbounds": [{"type": "direct", "tag": "direct"}, {"type": "selector", "tag": "proxy", "outbounds": ["wg", "direct"]}],
	  "route": {"rules": [{"domain": ["x.example"], "outbound": "wg"}], "final": "proxy"}
	}`
	mustPass(t, prod(), cfg)
}

func TestReferencesWithoutDNSServersUseTheBuiltInResolver(t *testing.T) {
	// No dns.servers: the core provides one resolver tagged "local".
	cfg := `{
	  "inbounds": [{"type": "mixed", "tag": "m", "listen_port": 7890}],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {"rules": [{"action": "resolve", "server": "local"}], "final": "direct"}
	}`
	mustPass(t, prod(), cfg)
	mustFail(t, prod(), strings.Replace(cfg, `"server": "local"`, `"server": "other"`, 1), "reference_missing")
}

func TestEmptyAndForeignValuesAreNotReferences(t *testing.T) {
	// Empty tags and non-string values are the core's business, not a dangling reference.
	cfg := `{
	  "inbounds": [{"type": "mixed", "tag": "m", "listen_port": 7890}],
	  "outbounds": [{"type": "direct", "tag": "direct", "detour": ""}],
	  "route": {"final": "", "rules": [{"action": "sniff"}, {"action": "hijack-dns", "port": [53]}]}
	}`
	mustPass(t, prod(), cfg)
}

func TestReferencesAreCheckedBeforeTheTunIsRewritten(t *testing.T) {
	cfg := `{
	  "inbounds": [{"type": "tun", "tag": "tun-in"}],
	  "outbounds": [{"type": "direct", "tag": "direct"}],
	  "route": {"rules": [{"inbound": ["tun-in"], "outbound": "missing"}], "final": "direct"}
	}`
	mustFail(t, prod(), cfg, "reference_missing")
	// The same in the development sandbox, where the TUN becomes a loopback proxy.
	env := testEnv
	env.SandboxPort = 17890
	res, _ := normalize(t, dev(), cfg, env)
	found := false
	for _, is := range res.Errors() {
		found = found || is.Code == "reference_missing"
	}
	if !found {
		t.Fatalf("sandbox mode must validate references too:\n%s", dump(res))
	}
}

func TestRelayDetoursAndBlockedOutboundAreValidated(t *testing.T) {
	cfg := `{
	  "inbounds": [{"type": "mixed", "tag": "m", "listen_port": 7890}],
	  "endpoints": [
	    {"type": "wireguard", "tag": "wg-landing", "detour": "relay-pin-1", "address": ["10.0.0.2/32"], "private_key": "k", "peers": [{"address": "203.0.113.9", "port": 51820, "public_key": "p", "allowed_ips": ["0.0.0.0/0"]}]}
	  ],
	  "outbounds": [
	    {"type": "shadowsocks", "tag": "plain-1", "server": "203.0.113.5", "server_port": 8388, "method": "aes-128-gcm", "password": "x"},
	    {"type": "block", "tag": "relay-blocked"},
	    {"type": "urltest", "tag": "relay-region-HK", "outbounds": ["plain-1"], "url": "https://www.gstatic.com/generate_204"},
	    {"type": "preferred", "tag": "relay-pin-1", "outbounds": ["plain-1"], "default": "plain-1", "stand_ins": ["relay-region-HK"], "url": "https://www.gstatic.com/generate_204"},
	    {"type": "shadowsocks", "tag": "landing-1", "server": "203.0.113.6", "server_port": 8388, "method": "aes-128-gcm", "password": "y", "detour": "relay-pin-1"},
	    {"type": "shadowsocks", "tag": "landing-blocked", "server": "203.0.113.7", "server_port": 8388, "method": "aes-128-gcm", "password": "z", "detour": "relay-blocked"},
	    {"type": "direct", "tag": "direct"},
	    {"type": "selector", "tag": "proxy", "outbounds": ["plain-1", "landing-1", "landing-blocked", "wg-landing", "direct"], "default": "landing-1"}
	  ],
	  "route": {"final": "proxy"}
	}`
	mustPass(t, prod(), cfg)
	// Dangling outbound detour must fail with reference_missing.
	mustFail(t, prod(), strings.Replace(cfg, `"detour": "relay-blocked"`, `"detour": "no-such-relay"`, 1), "reference_missing")
	// Dangling endpoint detour must fail with reference_missing.
	mustFail(t, prod(), strings.Replace(cfg, `"tag": "wg-landing", "detour": "relay-pin-1"`, `"tag": "wg-landing", "detour": "ghost-relay"`, 1), "reference_missing")
}
