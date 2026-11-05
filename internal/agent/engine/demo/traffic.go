package demo

import (
	"fmt"
	"net/netip"
	"slices"
	"time"

	"eggbox/internal/agentapi"
)

// A destination the invented connections go to. Hostnames are well-known
// public services and addresses come from the ranges reserved for
// documentation (RFC 5737) or for private networks, so nothing here points at
// a real machine. Where the connection goes is not decided here: the rules of
// the configuration decide, so what the interface shows is what the plan in
// force would do.
type target struct {
	host    string // "" for a connection made to a bare address
	ip      string
	port    int
	network string
	proto   string
	// heavy connections carry more of the traffic.
	heavy bool
}

var targets = []target{
	// Developer services.
	{host: "github.com", ip: "203.0.113.10", port: 443, network: "tcp", proto: "tls"},
	{host: "api.github.com", ip: "203.0.113.11", port: 443, network: "tcp", proto: "tls"},
	{host: "objects.githubusercontent.com", ip: "203.0.113.12", port: 443, network: "tcp", proto: "tls", heavy: true},
	{host: "registry.npmjs.org", ip: "203.0.113.40", port: 443, network: "tcp", proto: "tls"},
	// AI services.
	{host: "api.openai.com", ip: "203.0.113.20", port: 443, network: "tcp", proto: "tls"},
	{host: "chatgpt.com", ip: "203.0.113.21", port: 443, network: "tcp", proto: "tls"},
	{host: "api.anthropic.com", ip: "203.0.113.22", port: 443, network: "tcp", proto: "tls"},
	// Video and music.
	{host: "www.youtube.com", ip: "203.0.113.30", port: 443, network: "udp", proto: "quic", heavy: true},
	{host: "i.ytimg.com", ip: "203.0.113.31", port: 443, network: "tcp", proto: "tls"},
	{host: "rr3---sn-demo.googlevideo.com", ip: "203.0.113.32", port: 443, network: "udp", proto: "quic", heavy: true},
	{host: "www.netflix.com", ip: "203.0.113.112", port: 443, network: "tcp", proto: "tls", heavy: true},
	{host: "open.spotify.com", ip: "203.0.113.113", port: 443, network: "tcp", proto: "tls"},
	// Google.
	{host: "mtalk.google.com", ip: "203.0.113.64", port: 5228, network: "tcp", proto: ""},
	{host: "www.google.com", ip: "203.0.113.65", port: 443, network: "tcp", proto: "tls"},
	// Messaging, by name and by bare address.
	{host: "web.telegram.org", ip: "203.0.113.100", port: 443, network: "tcp", proto: "tls"},
	{host: "", ip: "203.0.113.101", port: 443, network: "tcp", proto: ""},
	// Social networks.
	{host: "www.reddit.com", ip: "203.0.113.70", port: 443, network: "tcp", proto: "tls"},
	{host: "x.com", ip: "203.0.113.71", port: 443, network: "tcp", proto: "tls"},
	{host: "www.instagram.com", ip: "203.0.113.72", port: 443, network: "tcp", proto: "tls"},
	// Elsewhere on the Internet.
	{host: "store.steampowered.com", ip: "203.0.113.50", port: 443, network: "tcp", proto: "tls"},
	{host: "cdn.discordapp.com", ip: "203.0.113.80", port: 443, network: "tcp", proto: "tls", heavy: true},
	{host: "files.small-shop.example", ip: "203.0.113.90", port: 443, network: "tcp", proto: "tls"},
	// Advertising.
	{host: "pixel.ads-tracker.example", ip: "203.0.113.200", port: 443, network: "tcp", proto: "tls"},
	// Services at home.
	{host: "www.baidu.com", ip: "198.51.100.10", port: 443, network: "tcp", proto: "tls"},
	{host: "www.bilibili.com", ip: "198.51.100.11", port: 443, network: "tcp", proto: "tls"},
	{host: "api.weibo.com", ip: "198.51.100.12", port: 443, network: "tcp", proto: "tls"},
	{host: "img.alicdn.com", ip: "198.51.100.13", port: 443, network: "tcp", proto: "tls", heavy: true},
	{host: "gateway.icloud.com", ip: "198.51.100.20", port: 443, network: "tcp", proto: "tls"},
	{host: "update.microsoft.com", ip: "198.51.100.21", port: 443, network: "tcp", proto: "tls"},
	{host: "dns.alidns.com", ip: "198.51.100.30", port: 53, network: "udp", proto: "dns"},
	// The local network.
	{host: "", ip: "192.168.1.20", port: 445, network: "tcp", proto: "", heavy: true},
	{host: "", ip: "192.168.1.30", port: 631, network: "tcp", proto: ""},
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

// newConnLocked invents a connection that has just been accepted and routes
// it by the configuration's rules. It returns nil for a connection the rules
// refuse: the core drops those before they are ever counted.
func (e *Engine) newConnLocked(now time.Time) *conn {
	return e.openLocked(now, targets[e.rng.IntN(len(targets))], programs[e.rng.IntN(len(programs))])
}

// openLocked routes and registers a connection of a program to a destination.
func (e *Engine) openLocked(now time.Time, t target, pr program) *conn {
	e.serial++
	addr, _ := netip.ParseAddr(t.ip)

	v := verdict{action: "route", outbound: "direct"}
	if e.router != nil {
		v = e.router.route(flow{
			host: t.host, addr: addr, port: uint16(t.port), network: t.network, proto: t.proto,
			process: pr.name, processPath: pr.path, mode: e.mode,
		})
	}
	name := t.host
	if name == "" {
		name = t.ip
	}
	id := e.rng.Uint32()
	if v.action == "reject" {
		e.logLocked("info", "[%d 0ms] inbound/tun[tun-in]: inbound connection to %s:%d", id, name, t.port)
		e.logLocked("info", "[%d 1ms] router: reject connection to %s:%d", id, name, t.port)
		return nil
	}

	outbound, chain := e.resolveLocked(v.outbound)
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
		Rule:          v.rule,
		Outbound:      outbound,
		OutboundType:  outType,
		Chain:         chain,
		CreatedUnixMs: now.UnixMilli(),
		Process:       &agentapi.ProcessInfo{Pid: pr.pid, Path: pr.path, User: "user"},
	}

	e.logLocked("info", "[%d 0ms] inbound/tun[tun-in]: inbound connection from %s", id, c.Source)
	e.logLocked("info", "[%d 1ms] router: found process path: %s", id, pr.path)
	e.logLocked("info", "[%d 2ms] inbound/tun[tun-in]: inbound connection to %s:%d", id, name, t.port)
	if outbound == "direct" {
		e.logLocked("info", "[%d 2ms] outbound/direct[direct]: outbound connection to %s:%d", id, name, t.port)
	} else {
		e.logLocked("info", "[%d 3ms] outbound/%s[%s]: outbound connection to %s:%d", id, outType, outbound, name, t.port)
	}
	if e.rng.IntN(40) == 0 {
		e.logLocked("warn", "[%d 5s] outbound/%s[%s]: read tcp 172.19.0.1:%d->%s:%d: i/o timeout", id, outType, outbound, 49152+e.rng.IntN(16000), t.ip, t.port)
	}

	cn := &conn{c: c, endsAt: now.Add(life), weight: weight}
	e.conns[c.Id] = cn
	return cn
}

// resolveLocked follows a rule's outbound down the groups' choices to the
// outbound that carries the traffic, and returns it with the chain of tags
// passed on the way, the carrier first.
func (e *Engine) resolveLocked(tag string) (leaf string, chain []string) {
	cur := tag
	for hop := 0; hop < 8; hop++ {
		chain = append(chain, cur)
		g := e.byTag[cur]
		if g == nil {
			break
		}
		next := e.effectiveLocked(g)
		if next == "" {
			break
		}
		cur = next
	}
	slices.Reverse(chain)
	return cur, chain
}
