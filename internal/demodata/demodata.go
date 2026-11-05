// Package demodata is the invented world of the demonstration mode: two
// subscription providers and a handful of imported nodes that do not exist,
// served from memory through an http.RoundTripper, plus the rule files the
// presets ask for.
//
// Everything here is made up. Names, hosts (example.net, example.invalid),
// identifiers and keys are placeholders; no real provider, server or account
// is described, and no byte of it comes from a real subscription.
package demodata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"eggbox/internal/configgen"
	"eggbox/internal/library"
	"eggbox/internal/ruledata"
)

// Hosts the invented world answers for.
const (
	SubscriptionHost = "sub.example.invalid"
	RulesHost        = "rules.example.invalid"
)

// Links of the invented providers. The token is a placeholder.
const (
	AuroraLink = "https://" + SubscriptionHost + "/aurora?token=demo-token-aurora"
	NimbusLink = "https://" + SubscriptionHost + "/nimbus?token=demo-token-nimbus"
)

type spec struct {
	name string
	kind string
}

var aurora = []spec{
	{"Hong Kong 01", "vless-reality"}, {"Hong Kong 02", "hysteria2"}, {"Hong Kong 03", "trojan-ws"},
	{"Tokyo 01", "vless-reality"}, {"Tokyo 02", "hysteria2"}, {"Tokyo 03", "vmess-ws"},
	{"Osaka 01", "trojan-ws"},
	{"Singapore 01", "vless-reality"}, {"Singapore 02", "tuic"},
	{"US Los Angeles 01", "hysteria2"}, {"US San Jose 01", "vless-ws"},
	{"Seoul 01", "vless-reality"}, {"Taipei 01", "trojan-ws"}, {"Taipei 02", "shadowsocks"},
}

var nimbus = []spec{
	{"Frankfurt 01", "vless-reality"}, {"London 01", "trojan-ws"}, {"Amsterdam 01", "hysteria2"},
	{"Sydney 01", "vmess-ws"}, {"Toronto 01", "vless-ws"}, {"Mumbai 01", "shadowsocks"},
}

var mine = []spec{
	{"Office Relay", "shadowsocks"}, {"Home Lab", "trojan-ws"},
}

// zeroKey is 32 zero bytes in the encoding of a Reality public key: a valid
// shape, a worthless key.
const zeroKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func uuidFor(provider string, i int) string {
	return fmt.Sprintf("00000000-%04x-4000-8000-%012x", len(provider)*257, i+1)
}

// outbound writes one sing-box outbound for a spec.
func outbound(provider, domain string, i int, s spec) map[string]any {
	host := fmt.Sprintf("node%02d.%s.example.net", i+1, domain)
	o := map[string]any{"tag": s.name, "server": host, "server_port": 443}
	tls := map[string]any{"enabled": true, "server_name": host}
	switch s.kind {
	case "vless-reality":
		o["type"], o["uuid"], o["flow"] = "vless", uuidFor(provider, i), "xtls-rprx-vision"
		tls["server_name"] = "www.example.com"
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		tls["reality"] = map[string]any{"enabled": true, "public_key": zeroKey, "short_id": fmt.Sprintf("%08x", 0xa11ce000+i)}
		o["tls"] = tls
	case "vless-ws":
		o["type"], o["uuid"] = "vless", uuidFor(provider, i)
		o["tls"] = tls
		o["transport"] = map[string]any{"type": "ws", "path": "/stream", "headers": map[string]any{"Host": host}}
	case "vmess-ws":
		o["type"], o["uuid"], o["security"] = "vmess", uuidFor(provider, i), "auto"
		o["tls"] = tls
		o["transport"] = map[string]any{"type": "ws", "path": "/vm", "headers": map[string]any{"Host": host}}
	case "trojan-ws":
		o["type"], o["password"] = "trojan", "demo-password-"+strconv.Itoa(i)
		o["tls"] = tls
		o["transport"] = map[string]any{"type": "ws", "path": "/tr", "headers": map[string]any{"Host": host}}
	case "hysteria2":
		o["type"], o["password"] = "hysteria2", "demo-password-"+strconv.Itoa(i)
		o["tls"] = tls
	case "tuic":
		o["type"], o["uuid"], o["password"], o["congestion_control"] = "tuic", uuidFor(provider, i), "demo-password", "bbr"
		o["tls"] = tls
	case "shadowsocks":
		o["type"], o["method"], o["password"] = "shadowsocks", "aes-128-gcm", "demo-password-"+strconv.Itoa(i)
		o["server_port"] = 8388 + i
	}
	return o
}

func document(provider, domain string, specs []spec) []byte {
	outs := make([]any, 0, len(specs))
	for i, s := range specs {
		outs = append(outs, outbound(provider, domain, i, s))
	}
	raw, _ := json.MarshalIndent(map[string]any{"outbounds": outs}, "", "  ")
	return raw
}

// NodesText is a list of nodes to paste into the import dialog, as one would
// paste what a friend sent.
func NodesText() string { return string(document("mine", "mine", mine)) }

// Provider describes an invented provider.
type Provider struct {
	Name string
	Link string
}

// Providers are the subscriptions the demonstration adds.
func Providers() []Provider {
	return []Provider{{"Aurora Cloud", AuroraLink}, {"Nimbus Net", NimbusLink}}
}

// Rules are custom rules that show what the rule list looks like.
func Rules() []configgen.CustomRule {
	return []configgen.CustomRule{
		{Kind: "domain_suffix", Value: "intranet.example", Target: "direct"},
		{Kind: "domain_keyword", Value: "ads-tracker", Target: "reject"},
		{Kind: "process_name", Value: "Slack", Target: "Proxy"},
	}
}

// Mirrors makes the rule data come from the invented world.
func Mirrors(string) []ruledata.Mirror {
	return []ruledata.Mirror{{Name: "demo", URL: func(repo, file string) string {
		return "https://" + RulesHost + "/" + repo + "/" + file
	}}}
}

// Client returns an HTTP client that answers for the invented hosts and
// refuses everything else, so a demonstration cannot reach the network by
// accident.
func Client(now func() time.Time) *http.Client {
	if now == nil {
		now = time.Now
	}
	return &http.Client{Transport: roundTripper{now: now}, Timeout: 30 * time.Second}
}

type roundTripper struct{ now func() time.Time }

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	respond := func(status int, header http.Header, body []byte) (*http.Response, error) {
		if header == nil {
			header = http.Header{}
		}
		return &http.Response{
			Status: strconv.Itoa(status) + " " + http.StatusText(status), StatusCode: status,
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: header, Body: io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)), Request: req,
		}, nil
	}
	switch req.URL.Hostname() {
	case SubscriptionHost:
		h := http.Header{}
		h.Set("Content-Type", "application/json; charset=utf-8")
		h.Set("Profile-Update-Interval", "24")
		switch req.URL.Path {
		case "/aurora":
			h.Set("Subscription-Userinfo", fmt.Sprintf("upload=6200000000; download=32400000000; total=200000000000; expire=%d", rt.now().Add(23*24*time.Hour).Unix()))
			h.Set("Profile-Title", "Aurora Cloud")
			return respond(http.StatusOK, h, document("aurora", "aurora", aurora))
		case "/nimbus":
			h.Set("Subscription-Userinfo", fmt.Sprintf("upload=20000000000; download=101000000000; total=150000000000; expire=%d", rt.now().Add(4*24*time.Hour).Unix()))
			h.Set("Profile-Title", "Nimbus Net")
			return respond(http.StatusOK, h, document("nimbus", "nimbus", nimbus))
		}
		return respond(http.StatusNotFound, nil, []byte("not found"))
	case RulesHost:
		// A rule set is a file of the binary format whose first bytes are
		// "SRS"; the demonstration core never reads the rest.
		body := append([]byte("SRS\x03"), bytes.Repeat([]byte("demo-rule-set-"), 64)...)
		h := http.Header{}
		h.Set("Content-Type", "application/octet-stream")
		return respond(http.StatusOK, h, body)
	}
	return nil, fmt.Errorf("the demonstration has no network: %s is not reachable", req.URL.Host)
}

// Seeder is what Seed needs from the application service.
type Seeder interface {
	AddSubscription(ctx context.Context, name, link string, opts library.RemoteOptions) (library.Subscription, *library.RefreshResult, error)
	ImportNodes(ctx context.Context, name, text string) (library.Subscription, error)
	SaveRules(ctx context.Context, rules []configgen.CustomRule) error
}

// Seed fills a new library with the invented providers, some imported nodes
// and a few rules, through the same operations the interface uses.
func Seed(ctx context.Context, s Seeder) error {
	for _, p := range Providers() {
		if _, _, err := s.AddSubscription(ctx, p.Name, p.Link, library.RemoteOptions{}); err != nil {
			return fmt.Errorf("demonstration: add %s: %w", p.Name, err)
		}
	}
	if _, err := s.ImportNodes(ctx, "My nodes", NodesText()); err != nil {
		return fmt.Errorf("demonstration: import: %w", err)
	}
	return s.SaveRules(ctx, Rules())
}
