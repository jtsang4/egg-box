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
	f.Add([]byte("vless://" + uuid + "@x.example.com:443#a\nvless://" + uuid + "@x.example.com:443#a\ntrojan://pw@y.example.com:1#direct\n"))
	f.Add([]byte(""))
	f.Add([]byte("\x00\xff\xfe"))

	pol := policy.New(singbox.PolicyOptions(singbox.RegistryOptions{}))
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := subscription.Parse(data)
		if err != nil {
			return
		}
		in := configgen.Input{Nodes: res.Nodes, Prefs: configgen.DefaultPrefs()}
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
