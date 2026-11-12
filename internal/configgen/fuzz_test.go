package configgen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eggbox/internal/agent/engine/singbox"
	"eggbox/internal/agent/policy"
	"eggbox/internal/configgen"
	"eggbox/internal/subscription"
	"eggbox/internal/testsupport/srs"
)

// FuzzProviderContent follows what a provider sends all the way to the agent:
// whatever the bytes are, reading them must not panic, and what is made of them
// must end up as a configuration the agent runs, or as a plain "no usable
// nodes". The one thing the agent may refuse is a single node, which is then
// left out (Heal), as it is in the application; it must never refuse something
// that no node explains, and the repair must not go on forever.
//
// With no -fuzz flag the seeds run as an ordinary test.
func FuzzProviderContent(f *testing.F) {
	f.Add([]byte(strings.Join(links(), "\n")))
	f.Add([]byte(b64(strings.Join(links(), "\n"))))
	f.Add([]byte(`proxies:
  - {name: "A", type: ss, server: a.example.com, port: 8388, cipher: aes-256-gcm, password: pw}
  - {name: "B", type: vmess, server: b.example.com, port: 443, uuid: ` + uuid + `, alterId: 0, cipher: auto, tls: true, network: ws, ws-opts: {path: /ws}}
  - {name: "C", type: trojan, server: c.example.com, port: 443, password: pw, sni: c.example.com}
`))
	f.Add([]byte(`{"outbounds":[
  {"type":"trojan","tag":"T","server":"t.example.com","server_port":443,"password":"pw","tls":{"enabled":true,"server_name":"t.example.com"}},
  {"type":"vless","tag":"E","server":"e.example.com","server_port":443,"uuid":"` + uuid + `","tls":{"enabled":true,"certificate_path":"/etc/passwd"}},
  {"type":"selector","tag":"S","outbounds":["T","E"]}]}`))
	f.Add([]byte(`proxies:
  - {name: "SN4", type: snell, server: s4.example.com, port: 443, psk: pw, version: 4, obfs-opts: {mode: http, host: www.example.org}}
  - {name: "SN6", type: snell, server: s6.example.com, port: 443, psk: test-psk-0001, version: 6, reuse: true}
  - {name: "SH", type: ssh, server: h.example.com, port: 22, username: u, password: pw}
  - {name: "ST", type: ss, server: st.example.com, port: 443, cipher: aes-128-gcm, password: pw, plugin: shadow-tls, plugin-opts: {host: www.example.org, password: stpw, version: 3}}
  - {name: "WG", type: wireguard, ip: 172.16.0.2, private-key: AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=, peers: [{server: w.example.com, port: 2480, public-key: AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=}]}
`))
	f.Add([]byte(`{"outbounds":[
  {"type":"shadowsocks","tag":"SS","method":"aes-128-gcm","password":"pw","detour":"st"},
  {"type":"shadowtls","tag":"st","server":"st.example.com","server_port":443,"version":3,"password":"x","tls":{"enabled":true,"server_name":"www.example.org","certificate_path":"/etc/passwd"}},
  {"type":"ssh","tag":"K","server":"k.example.com","server_port":22,"password":"pw","private_key_path":"/root/.ssh/id"},
  {"type":"snell","tag":"N","server":"n.example.com","server_port":1,"version":6,"psk":"short"}]}`))
	f.Add([]byte("vless://" + uuid + "@x.example.com:443#a\nvless://" + uuid + "@x.example.com:443#a\ntrojan://pw@y.example.com:1#direct\n"))
	f.Add([]byte(""))
	f.Add([]byte("\x00\xff\xfe"))

	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	srsFixture := srs.DomainSuffixes(f, "i.example")
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := subscription.Parse(data)
		if err != nil {
			return
		}
		var nodes []configgen.Node
		for _, n := range res.Nodes {
			nodes = append(nodes, configgen.Node{Node: n, Region: subscription.Region(n.Name)})
		}
		in := configgen.Input{Nodes: nodes, Prefs: configgen.DefaultPrefs(), Main: configgen.Exit{Kind: configgen.ExitAuto}, Fallback: configgen.Exit{Kind: configgen.ExitMain}}
		if len(nodes) > 0 {
			// Pinned exits exercise the groups with stand-ins, in the main exit, an item and an exception.
			pinned := configgen.Exit{Kind: configgen.ExitNode, Node: nodes[0].Key()}
			in.Main = pinned
			in.Items = []configgen.Item{{ID: "i", Name: "I", Exit: pinned, Domain: srsFixture, DNS: srsFixture}}
			in.Exceptions = []configgen.Exception{{ID: "e", Rule: hostRule("suffix", "e.example"), Exit: pinned}}
		}
		for round := 0; round < 12; round++ {
			out, err := configgen.Generate(in)
			if err != nil {
				return // nothing usable is an answer, not a failure
			}
			dir := t.TempDir()
			have := map[string]bool{}
			for _, r := range out.Resources {
				have[r.Name] = true
				if err := os.WriteFile(filepath.Join(dir, r.Name), r.Data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result := pol.Normalize(out.JSON, policy.Env{
				ResourceDir: dir, Resources: have, CachePath: filepath.Join(dir, "cache.db"), LogLevel: "info",
				Tun: policy.TunSettings{Addresses: []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"}, DNSMode: "disabled"},
			})
			if !result.HasErrors() {
				return
			}
			var issues []configgen.PathIssue
			for _, is := range result.Errors() {
				issues = append(issues, configgen.PathIssue{Path: is.Path, Message: is.Code})
			}
			healed, _, changed := configgen.Heal(in, out, issues)
			if !changed {
				// Only where and why: what the provider sent stays out of the message.
				t.Fatalf("the agent refused a configuration and no node explains it: %v", issues)
			}
			in = healed
		}
		t.Fatal("leaving out the nodes the agent refuses did not end")
	})
}
