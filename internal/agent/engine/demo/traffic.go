package demo

import (
	"fmt"
	"time"

	"github.com/jtsang4/sing-box-ui/internal/agentapi"
)

// A destination the invented connections go to. Hostnames are well-known
// public services and addresses come from the ranges reserved for
// documentation (RFC 5737), so nothing here points at a real machine.
type target struct {
	host    string
	ip      string
	port    int
	network string
	proto   string
	// direct is whether the destination is reached without the proxy in Rule
	// mode, with the rule set that says so.
	direct  bool
	ruleSet string
	// heavy connections carry more of the traffic.
	heavy bool
}

var targets = []target{
	{host: "github.com", ip: "203.0.113.10", port: 443, network: "tcp", proto: "tls"},
	{host: "api.github.com", ip: "203.0.113.11", port: 443, network: "tcp", proto: "tls"},
	{host: "objects.githubusercontent.com", ip: "203.0.113.12", port: 443, network: "tcp", proto: "tls", heavy: true},
	{host: "api.openai.com", ip: "203.0.113.20", port: 443, network: "tcp", proto: "tls"},
	{host: "www.youtube.com", ip: "203.0.113.30", port: 443, network: "udp", proto: "quic", heavy: true},
	{host: "i.ytimg.com", ip: "203.0.113.31", port: 443, network: "tcp", proto: "tls"},
	{host: "rr3---sn-demo.googlevideo.com", ip: "203.0.113.32", port: 443, network: "udp", proto: "quic", heavy: true},
	{host: "registry.npmjs.org", ip: "203.0.113.40", port: 443, network: "tcp", proto: "tls"},
	{host: "store.steampowered.com", ip: "203.0.113.50", port: 443, network: "tcp", proto: "tls"},
	{host: "mtalk.google.com", ip: "203.0.113.60", port: 5228, network: "tcp", proto: ""},
	{host: "www.reddit.com", ip: "203.0.113.70", port: 443, network: "tcp", proto: "tls"},
	{host: "cdn.discordapp.com", ip: "203.0.113.80", port: 443, network: "tcp", proto: "tls", heavy: true},
	{host: "www.baidu.com", ip: "198.51.100.10", port: 443, network: "tcp", proto: "tls", direct: true, ruleSet: "geosite-cn"},
	{host: "www.bilibili.com", ip: "198.51.100.11", port: 443, network: "tcp", proto: "tls", direct: true, ruleSet: "geosite-cn"},
	{host: "api.weibo.com", ip: "198.51.100.12", port: 443, network: "tcp", proto: "tls", direct: true, ruleSet: "geosite-cn"},
	{host: "img.alicdn.com", ip: "198.51.100.13", port: 443, network: "tcp", proto: "tls", direct: true, ruleSet: "geosite-cn", heavy: true},
	{host: "gateway.icloud.com", ip: "198.51.100.20", port: 443, network: "tcp", proto: "tls", direct: true, ruleSet: "geoip-cn"},
	{host: "dns.alidns.com", ip: "198.51.100.30", port: 53, network: "udp", proto: "dns", direct: true},
}

type program struct {
	name string
	path string
	pid  uint32
}

var programs = []program{
	{"Google Chrome Helper", "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper", 4821},
	{"Safari", "/Applications/Safari.app/Contents/MacOS/Safari", 5107},
	{"Code Helper", "/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper", 6312},
	{"node", "/usr/local/bin/node", 7740},
	{"curl", "/usr/bin/curl", 8216},
	{"Slack", "/Applications/Slack.app/Contents/MacOS/Slack", 4377},
	{"Spotify", "/Applications/Spotify.app/Contents/MacOS/Spotify", 4590},
	{"Telegram", "/Applications/Telegram.app/Contents/MacOS/Telegram", 5733},
}

// newConnLocked invents a connection that has just been accepted.
func (e *Engine) newConnLocked(now time.Time) *conn {
	t := targets[e.rng.IntN(len(targets))]
	pr := programs[e.rng.IntN(len(programs))]
	e.serial++

	direct := t.direct
	switch e.mode {
	case "Global":
		direct = false
	case "Direct":
		direct = true
	}
	rule := "final => route(Proxy)"
	switch {
	case t.proto == "dns":
		rule = "protocol=dns => hijack-dns"
	case e.mode == "Global":
		rule = "clash_mode=Global => route(Proxy)"
	case e.mode == "Direct":
		rule = "clash_mode=Direct => route(direct)"
	case direct && e.rules && t.ruleSet != "":
		rule = fmt.Sprintf("rule_set=%s => route(direct)", t.ruleSet)
	case direct:
		rule = "final => route(direct)"
	}

	outbound, chain := "direct", []string{"direct"}
	if !direct {
		outbound = "Proxy"
		chain = []string{"Proxy"}
		if g := e.byTag["Proxy"]; g != nil && g.selected != "" {
			leaf := g.selected
			chain = []string{leaf, "Proxy"}
			if sub := e.byTag[leaf]; sub != nil && sub.selected != "" {
				chain = []string{sub.selected, leaf, "Proxy"}
				leaf = sub.selected
			}
			outbound = leaf
		}
	}
	outType := "direct"
	if n := e.nodes[outbound]; n != nil {
		outType = n.typ
	}

	life := time.Duration(3+e.rng.IntN(55)) * time.Second
	if t.network == "udp" {
		life = time.Duration(8+e.rng.IntN(90)) * time.Second
	}
	if t.proto == "dns" {
		life = time.Duration(1+e.rng.IntN(2)) * time.Second
	}
	weight := 0.2 + e.rng.Float64()
	if t.heavy {
		weight *= 6
	}
	if t.proto == "dns" {
		weight = 0.01
	}

	c := &agentapi.Connection{
		Id:      fmt.Sprintf("c%06d", e.serial),
		Inbound: "tun-in", Network: t.network,
		Source:        fmt.Sprintf("172.19.0.1:%d", 49152+e.rng.IntN(16000)),
		Destination:   fmt.Sprintf("%s:%d", t.ip, t.port),
		Domain:        t.host,
		Protocol:      t.proto,
		Rule:          rule,
		Outbound:      outbound,
		OutboundType:  outType,
		Chain:         chain,
		CreatedUnixMs: now.UnixMilli(),
		Process:       &agentapi.ProcessInfo{Pid: pr.pid, Path: pr.path, User: "user"},
	}

	id := e.rng.Uint32()
	e.logLocked("info", "[%d 0ms] inbound/tun[tun-in]: inbound connection from %s", id, c.Source)
	e.logLocked("info", "[%d 1ms] router: found process path: %s", id, pr.path)
	e.logLocked("info", "[%d 2ms] inbound/tun[tun-in]: inbound connection to %s:%d", id, t.host, t.port)
	if direct {
		e.logLocked("info", "[%d 2ms] outbound/direct[direct]: outbound connection to %s:%d", id, t.host, t.port)
	} else {
		e.logLocked("info", "[%d 3ms] outbound/%s[%s]: outbound connection to %s:%d", id, outType, outbound, t.host, t.port)
	}
	if e.rng.IntN(40) == 0 {
		e.logLocked("warn", "[%d 5s] outbound/%s[%s]: read tcp 172.19.0.1:%d->%s:%d: i/o timeout", id, outType, outbound, 49152+e.rng.IntN(16000), t.ip, t.port)
	}

	cn := &conn{c: c, endsAt: now.Add(life), weight: weight}
	e.conns[c.Id] = cn
	return cn
}
