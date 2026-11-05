package policy

import (
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzNormalize feeds the gate arbitrary documents. The gate decides what the
// GUI may make a root / SYSTEM process run, so the interesting question is never
// "does it reject this", it is "what does it let through". Whatever it accepts
// is audited here by a walk of the whole output that shares no code with the
// gate's own, section by section, logic: a path-like field, a management
// surface or a listener that escaped the rules would show up as a failure.
//
// With no -fuzz flag the seeds run as an ordinary test.
func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{
		minimal,
		`{"inbounds":[{"type":"tun","tag":"tun-in"}],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"final":"direct"}}`,
		`{"inbounds":[{"type":"mixed","listen":"0.0.0.0","listen_port":1080}],"outbounds":[{"type":"direct","tag":"d"}]}`,
		`{"outbounds":[{"type":"vless","tag":"v","server":"s.example.invalid","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","tls":{"enabled":true,"ca_file":"/etc/passwd"}}]}`,
		`{"outbounds":[{"type":"vless","tag":"v","server":"s.example.invalid","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","tls":{"enabled":true,"certificate_path":"res:ca.pem"}}]}`,
		`{"outbounds":[{"type":"vmess","tag":"w","server":"s.example.invalid","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","transport":{"type":"ws","path":"/ws"}}]}`,
		`{"experimental":{"clash_api":{"external_controller":"0.0.0.0:9090"}}}`,
		`{"experimental":{"cache_file":{"enabled":true,"path":"/etc/shadow"}}}`,
		`{"log":{"output":"/tmp/x","level":"info"}}`,
		`{"services":[{"type":"ssm-api"}]}`,
		`{"route":{"rule_set":[{"type":"local","tag":"r","format":"binary","path":"res:geosite-cn.srs"}]}}`,
		`{"route":{"rule_set":[{"type":"remote","tag":"r","format":"binary","url":"https://example.invalid/r.srs"}]}}`,
		`{"dns":{"servers":[{"type":"local","tag":"l"},{"type":"hosts","tag":"h","path":["/etc/hosts"]}]}}`,
		`{"endpoints":[{"type":"wireguard","tag":"wg","private_key":"x","peers":[]}]}`,
		`{"outbounds":[{"type":"direct","tag":"a","tag":"b"}]}`,
		`{"a":{"b":{"c":{}}}}`,
		`[]`, `null`, `{}`, ``,
	} {
		f.Add([]byte(seed))
	}
	p := prod()
	sandbox := testEnv
	sandbox.SandboxPort = 17890
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, env := range []Env{testEnv, sandbox} {
			res := p.Normalize(raw, env)
			if res.HasErrors() {
				if len(res.JSON) != 0 {
					t.Fatalf("a refused configuration must not come with something to run:\n%s", res.JSON)
				}
				continue
			}
			auditAccepted(t, p, env, res)
		}
	})
}

// auditAccepted checks the promises everything else relies on, on a
// configuration the gate said could run.
func auditAccepted(t *testing.T, p *Policy, env Env, res *Result) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf(format+"\n--- accepted configuration ---\n%s", append(args, res.JSON)...)
	}
	// What runs is one document with no duplicate keys, so no two readers can
	// disagree about which value counts.
	root, err := decodeStrict(res.JSON)
	if err != nil {
		fail("the accepted configuration is not a strict JSON object: %v", err)
	}

	allowedTop := set("log", "dns", "ntp", "certificate", "http_clients", "endpoints", "inbounds", "outbounds", "route", "experimental")
	for k := range root {
		if !allowedTop[k] {
			fail("top-level section %q must not survive", k)
		}
	}

	// The only management-looking thing is the cache file, and it is the agent's.
	if exp, ok := root["experimental"].(map[string]any); ok {
		for k := range exp {
			if k != "cache_file" {
				fail("experimental.%s must not survive", k)
			}
		}
		if cache, ok := exp["cache_file"].(map[string]any); ok && cache["path"] != env.CachePath {
			fail("the cache file must be the agent's own, got %v", cache["path"])
		}
	}
	if lg, ok := root["log"].(map[string]any); ok {
		if _, has := lg["output"]; has {
			fail("the core must not write its log to a path the GUI chose")
		}
	}

	// Listeners: only a TUN the agent configured, or a loopback proxy.
	inbounds, _ := root["inbounds"].([]any)
	for i, raw := range inbounds {
		in, _ := raw.(map[string]any)
		switch typ, _ := in["type"].(string); typ {
		case "tun":
			if env.SandboxPort != 0 {
				fail("inbounds[%d]: a sandbox must never create a TUN", i)
			}
			if in["auto_route"] != true {
				fail("inbounds[%d]: the TUN's routing belongs to the agent", i)
			}
			if got := toStrings(in["address"]); strings.Join(got, ",") != strings.Join(env.Tun.Addresses, ",") {
				fail("inbounds[%d]: the TUN's addresses belong to the agent, got %v", i, got)
			}
		case "mixed":
			if !loopback(in["listen"]) {
				fail("inbounds[%d]: a proxy listener must be on loopback, got %v", i, in["listen"])
			}
		default:
			fail("inbounds[%d]: type %v is not allowed to listen", i, in["type"])
		}
	}

	auditTree(root, "$", env, fail)
}

// auditTree walks everything that will run. It knows nothing of the gate's
// sections: where a "path" sits is read from the position in the output alone.
func auditTree(v any, path string, env Env, fail func(string, ...any)) {
	switch t := v.(type) {
	case []any:
		for i, e := range t {
			auditTree(e, indexPath(path, i), env, fail)
		}
	case map[string]any:
		ownType, _ := t["type"].(string)
		for k, val := range t {
			p := keyPath(path, k)
			if alwaysForbidden[k] && !isEmpty(val) && val != false {
				fail("%s: %q must never be set", p, k)
			}
			if pathKeyRe.MatchString(k) && !matcherKeys[k] && !coveredBySectionDeny[k] && holdsString(val) {
				for _, s := range toStrings(val) {
					if s == "" {
						continue
					}
					switch {
					case resourceKeys[k]:
						if !strictlyUnder(s, env.ResourceDir) {
							fail("%s: %q must point into the resource folder, got %q", p, k, s)
						}
					case k == "path":
						auditPath(path, ownType, s, p, env, fail)
					default:
						fail("%s: %q looks like a reference to a file or a program and was let through (%q)", p, k, s)
					}
				}
			}
			auditTree(val, p, env, fail)
		}
	}
}

// auditPath says whether a "path" may hold s where it sits: a URL path in the
// places that are about URLs, the agent's own cache file, or a rule set the
// gate pointed into the resource folder. Anywhere else it would be a file.
func auditPath(parent, parentType, s, at string, env Env, fail func(string, ...any)) {
	isOutbound := strings.HasPrefix(parent, "$.outbounds[") && !strings.Contains(parent[len("$.outbounds["):], ".")
	isDNSServer := strings.HasPrefix(parent, "$.dns.servers[") && !strings.Contains(parent[len("$.dns.servers["):], ".")
	switch {
	case strings.HasSuffix(parent, ".transport") && (parentType == "http" || parentType == "ws" || parentType == "httpupgrade"):
	case isOutbound && parentType == "http":
	case isDNSServer && (parentType == "https" || parentType == "h3"):
	case parent == "$.experimental.cache_file":
		if s != env.CachePath {
			fail("%s: the cache file must be the agent's own, got %q", at, s)
		}
	case strings.HasPrefix(parent, "$.route.rule_set[") && parentType == "local":
		if !strictlyUnder(s, env.ResourceDir) {
			fail("%s: a local rule set must be one of the resources, got %q", at, s)
		}
	default:
		fail("%s: a path where only a URL path or the agent's own files are allowed: %q", at, s)
	}
}

func strictlyUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && filepath.IsLocal(rel)
}

func loopback(v any) bool {
	s, _ := v.(string)
	if s == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(s)
	return err == nil && a.IsLoopback()
}

func toStrings(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		out = append(out, t)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
