package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agentapi"
)

const diagConfigJSON = `{
  "log": {"level": "info"},
  "inbounds": [{"type": "tun", "tag": "tun-in"}],
  "outbounds": [
    {"type": "vless", "tag": "hk", "server": "hk1.example.com", "server_port": 443, "uuid": "b8f8c3f4-1111-4222-8333-abcdefabcdef"},
    {"type": "shadowsocks", "tag": "us", "server": "203.0.113.9", "server_port": 8388, "password": "hunter2-very-secret"},
    {"type": "direct", "tag": "direct"}
  ],
  "route": {"final": "hk", "rules": [{"action": "sniff"}, {"domain_suffix": ["example.com"], "outbound": "direct"}]},
  "dns": {"servers": [{"tag": "local", "type": "udp", "server": "223.5.5.5"}], "rules": []}
}`

func TestDiagnosticsReportIsUsefulAndRedacted(t *testing.T) {
	h := newHarness(t)
	h.mustApply(diagConfigJSON, 0)
	h.mustConnect()
	h.eng.Log("info", "dial tcp 203.0.113.9:443 via hk1.example.com failed for b8f8c3f4-1111-4222-8333-abcdefabcdef")
	h.eng.Log("warn", "token=abcdef0123456789 rejected, home /Users/jtsang/Library")
	settle()

	rep, err := h.c.GetDiagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rep.SuggestedFilename, "sing-box-ui-diagnostics-") || !strings.HasSuffix(rep.SuggestedFilename, ".json") || rep.GeneratedUnixMs == 0 {
		t.Fatalf("report = %q at %d", rep.SuggestedFilename, rep.GeneratedUnixMs)
	}
	body := string(rep.Json)

	for _, secret := range []string{"203.0.113.9", "hk1.example.com", "b8f8c3f4", "hunter2", "abcdef0123456789", "/Users/jtsang", "223.5.5.5"} {
		if strings.Contains(body, secret) {
			t.Errorf("%q leaked into the report:\n%s", secret, body)
		}
	}

	var doc struct {
		Schema int `json:"schema"`
		Agent  struct {
			Version  string `json:"version"`
			Protocol int    `json:"protocol"`
			Driver   string `json:"driver"`
		} `json:"agent"`
		Status struct {
			Connection string `json:"connection"`
		} `json:"status"`
		Revisions struct {
			Current  uint64 `json:"current"`
			LastGood uint64 `json:"last_good"`
		} `json:"revisions"`
		Config struct {
			Inbounds   []struct{ Type string } `json:"inbounds"`
			Outbounds  map[string]int          `json:"outbounds_by_type"`
			RouteRules int                     `json:"route_rules"`
			DNSServers int                     `json:"dns_servers"`
		} `json:"config"`
		Ledger []struct {
			Kind     string `json:"kind"`
			Resource string `json:"resource"`
		} `json:"ledger"`
		Network struct {
			Online     bool `json:"online"`
			Interfaces []struct {
				Name  string   `json:"name"`
				Addrs []string `json:"addrs"`
			} `json:"interfaces"`
			RouteCount int `json:"route_count"`
		} `json:"network"`
		Logs []struct {
			Level   string `json:"level"`
			Source  string `json:"source"`
			Message string `json:"message"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(rep.Json, &doc); err != nil {
		t.Fatalf("the report must be valid JSON: %v\n%s", err, body)
	}
	if doc.Schema != 1 || doc.Agent.Protocol != 1 || doc.Agent.Driver != "sim" {
		t.Errorf("agent = %+v schema %d", doc.Agent, doc.Schema)
	}
	if doc.Status.Connection != "CONNECTION_STATE_CONNECTED" || doc.Revisions.Current != 1 {
		t.Errorf("status %+v revisions %+v", doc.Status, doc.Revisions)
	}
	if len(doc.Config.Inbounds) != 1 || doc.Config.Inbounds[0].Type != "tun" || doc.Config.Outbounds["vless"] != 1 ||
		doc.Config.Outbounds["direct"] != 1 || doc.Config.RouteRules != 2 || doc.Config.DNSServers != 1 {
		t.Errorf("config shape = %+v", doc.Config)
	}
	if len(doc.Ledger) != 2 {
		t.Errorf("ledger = %+v", doc.Ledger)
	}
	if strings.Contains(body, `"token"`) {
		t.Errorf("ledger tokens must never be part of a report:\n%s", body)
	}
	if !doc.Network.Online || len(doc.Network.Interfaces) == 0 || doc.Network.RouteCount == 0 || !strings.Contains(body, "192.168.1.20/24") {
		t.Errorf("the network section must keep private addressing: %+v", doc.Network)
	}

	var sawHost, sawIP, sawPath bool
	for _, l := range doc.Logs {
		sawHost = sawHost || strings.Contains(l.Message, "host-1")
		sawIP = sawIP || strings.Contains(l.Message, "ip-1")
		sawPath = sawPath || strings.Contains(l.Message, "/Users/user/Library")
	}
	if !sawHost || !sawIP || !sawPath {
		t.Errorf("log lines should be present with placeholders (host %v ip %v path %v):\n%s", sawHost, sawIP, sawPath, body)
	}
}

func TestDiagnosticsWorkWithoutAnyConfigurationAndWhileDisconnected(t *testing.T) {
	h := newHarness(t)
	rep, err := h.c.GetDiagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(rep.Json, &doc); err != nil {
		t.Fatal(err)
	}
	if _, has := doc["config"]; has {
		t.Fatal("there is no configuration to describe yet")
	}
	if doc["ledger"] == nil || doc["logs"] == nil {
		t.Fatalf("lists must be present (empty), not null: %s", rep.Json)
	}
}

func TestDiagnosticsDoNotWaitForABusyAgent(t *testing.T) {
	h := newHarness(t)
	h.mustApply(tunConfig, 0)
	release := h.eng.BlockStart()
	defer release()
	go h.connect()
	h.waitConn(agentapi.ConnectionState_CONNECTION_STATE_CONNECTING)

	done := make(chan struct{})
	go func() {
		_, _ = h.c.GetDiagnostics(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the report must not queue behind a long-running operation")
	}
}
