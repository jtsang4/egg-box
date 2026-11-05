package policy

import (
	"fmt"
	"net/netip"
	"strings"
)

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

// Allow-lists. They mirror what internal/agent/engine/singbox registers; a
// type missing from either place cannot run.
var (
	outboundTypes     = set("direct", "block", "selector", "urltest", "socks", "http", "shadowsocks", "snell", "vmess", "vless", "trojan", "shadowtls", "anytls")
	outboundQUICTypes = set("hysteria", "hysteria2", "tuic")
	endpointTypes     = set("wireguard")
	dnsServerTypes    = set("udp", "tcp", "tls", "https", "hosts", "local", "fakeip")
	dnsQUICTypes      = set("quic", "h3")
	dnsDHCPTypes      = set("dhcp")
	// listener inbounds that open a local port; only enabled for tests/demo.
	serverInboundTypes     = set("direct", "socks", "http", "shadowsocks")
	serverInboundQUICTypes = set("hysteria", "hysteria2", "tuic")

	logKeys  = set("disabled", "level", "timestamp", "output")
	logLevel = set("trace", "debug", "info", "warn", "warning", "error", "fatal", "panic")
)

// top walks the root object in a fixed order so findings are deterministic and
// later sections can rely on what earlier ones learned (the TUN tag).
func (n *normalizer) top(root map[string]any) {
	order := []string{"$schema", "log", "dns", "ntp", "certificate", "http_clients", "endpoints", "inbounds", "outbounds", "route", "experimental"}
	handled := map[string]bool{}
	for _, key := range order {
		handled[key] = true
		val, present := root[key]
		if !present {
			continue
		}
		path := keyPath("$", key)
		switch key {
		case "$schema":
			delete(root, key)
		case "log":
			n.log(root, val, path)
		case "dns":
			n.dns(val, path)
		case "endpoints":
			n.endpoints(val, path)
		case "inbounds":
			n.inbounds(val, path)
		case "outbounds":
			n.outbounds(val, path)
		case "route":
			n.route(val, path)
		case "experimental":
			n.experimental(root, val, path)
		default: // ntp, certificate, http_clients
			n.walk(val, path, "")
		}
	}
	for _, key := range sortedKeys(root) {
		if handled[key] {
			continue
		}
		switch key {
		case "certificate_providers", "network_namespaces", "services":
			if !isEmpty(root[key]) {
				n.errorf(keyPath("$", key), "section_not_allowed", "%q is not available in this client", key)
			}
			delete(root, key)
		default:
			n.errorf(keyPath("$", key), "section_unknown", "unknown top-level section %q", key)
		}
	}

	// The agent always runs the core with a cache file it owns, so selected
	// nodes and the mode survive restarts. A missing log section gets the agent level.
	if _, ok := root["log"]; !ok {
		n.log(root, nil, "$.log")
	}
	if _, ok := root["experimental"]; !ok {
		n.experimental(root, nil, "$.experimental")
	}
	n.finishTun(root)
}

func (n *normalizer) log(root map[string]any, val any, path string) {
	obj, ok := asObject(val)
	if val != nil && !ok {
		n.errorf(path, "type_mismatch", "log must be an object")
		return
	}
	if obj == nil {
		obj = map[string]any{}
	}
	for _, k := range sortedKeys(obj) {
		if !logKeys[k] {
			n.errorf(keyPath(path, k), "field_not_allowed", "log.%s is not supported", k)
		}
	}
	if out, ok := obj["output"].(string); ok && out != "" {
		n.warnf(keyPath(path, "output"), "log_output_ignored", "log output is managed by the agent")
	}
	// The core writes to the agent's log hub, never to a file chosen by the GUI.
	delete(obj, "output")
	if lvl, ok := obj["level"].(string); ok && !logLevel[lvl] {
		n.errorf(keyPath(path, "level"), "value_invalid", "unknown log level %q", lvl)
	}
	if n.env.LogLevel != "" {
		obj["level"] = n.env.LogLevel
	}
	root["log"] = obj
}

func (n *normalizer) experimental(root map[string]any, val any, path string) {
	obj, ok := asObject(val)
	if val != nil && !ok {
		n.errorf(path, "type_mismatch", "experimental must be an object")
		return
	}
	if obj == nil {
		obj = map[string]any{}
	}
	for _, k := range sortedKeys(obj) {
		switch k {
		case "cache_file":
		case "clash_api", "v2ray_api", "debug":
			n.errorf(keyPath(path, k), "section_not_allowed", "experimental.%s would open a management surface and is not allowed", k)
		default:
			n.errorf(keyPath(path, k), "section_unknown", "unknown experimental option %q", k)
		}
	}
	cache, _ := asObject(obj["cache_file"])
	if cache == nil {
		cache = map[string]any{}
	}
	cachePath := keyPath(path, "cache_file")
	for _, k := range sortedKeys(cache) {
		switch k {
		case "enabled", "path", "cache_id", "store_fakeip", "store_rdrc", "rdrc_timeout", "store_dns":
		default:
			n.errorf(keyPath(cachePath, k), "field_not_allowed", "cache_file.%s is not supported", k)
		}
	}
	// Location and activation belong to the agent.
	cache["enabled"] = true
	if n.env.CachePath != "" {
		cache["path"] = n.env.CachePath
	} else {
		delete(cache, "path")
	}
	obj["cache_file"] = cache
	root["experimental"] = obj
}

func (n *normalizer) dns(val any, path string) {
	obj, ok := asObject(val)
	if !ok {
		n.errorf(path, "type_mismatch", "dns must be an object")
		return
	}
	servers, _ := asArray(obj["servers"])
	for i, it := range servers {
		sp := indexPath(keyPath(path, "servers"), i)
		srv, ok := asObject(it)
		if !ok {
			n.errorf(sp, "type_mismatch", "a DNS server must be an object")
			continue
		}
		typ, _ := srv["type"].(string)
		if !n.dnsTypeAllowed(typ) {
			n.errorf(keyPath(sp, "type"), "type_not_allowed", "DNS server type %q is not available", typ)
			continue
		}
	}
	n.walk(obj, path, "dns")
}

func (n *normalizer) dnsTypeAllowed(t string) bool {
	return dnsServerTypes[t] || (n.p.opts.QUIC && dnsQUICTypes[t]) || (n.p.opts.DHCP && dnsDHCPTypes[t])
}

func (n *normalizer) endpoints(val any, path string) {
	arr, ok := asArray(val)
	if !ok {
		n.errorf(path, "type_mismatch", "endpoints must be an array")
		return
	}
	for i, it := range arr {
		ep := indexPath(path, i)
		obj, ok := asObject(it)
		if !ok {
			n.errorf(ep, "type_mismatch", "an endpoint must be an object")
			continue
		}
		typ, _ := obj["type"].(string)
		if !endpointTypes[typ] || (typ == "wireguard" && !n.p.opts.WireGuard) {
			n.errorf(keyPath(ep, "type"), "type_not_allowed", "endpoint type %q is not available", typ)
			continue
		}
		if sys, _ := obj["system"].(bool); sys {
			n.errorf(keyPath(ep, "system"), "field_not_allowed", "a system WireGuard interface would bypass the agent's network ownership")
		}
		n.walk(obj, ep, "endpoint:"+typ)
	}
}

func (n *normalizer) outbounds(val any, path string) {
	arr, ok := asArray(val)
	if !ok {
		n.errorf(path, "type_mismatch", "outbounds must be an array")
		return
	}
	for i, it := range arr {
		op := indexPath(path, i)
		obj, ok := asObject(it)
		if !ok {
			n.errorf(op, "type_mismatch", "an outbound must be an object")
			continue
		}
		typ, _ := obj["type"].(string)
		if !outboundTypes[typ] && !(n.p.opts.QUIC && outboundQUICTypes[typ]) {
			n.errorf(keyPath(op, "type"), "type_not_allowed", "outbound type %q is not available", typ)
			continue
		}
		n.walk(obj, op, "outbound:"+typ)
	}
}

func (n *normalizer) inbounds(val any, path string) {
	arr, ok := asArray(val)
	if !ok {
		n.errorf(path, "type_mismatch", "inbounds must be an array")
		return
	}
	tunCount := 0
	for i, it := range arr {
		ip := indexPath(path, i)
		obj, ok := asObject(it)
		if !ok {
			n.errorf(ip, "type_mismatch", "an inbound must be an object")
			continue
		}
		typ, _ := obj["type"].(string)
		switch {
		case typ == "tun":
			tunCount++
			if tunCount > 1 {
				n.errorf(ip, "tun_multiple", "only one TUN inbound can be active")
				continue
			}
			n.tun(obj, ip)
		case typ == "mixed" || n.serverInboundAllowed(typ):
			n.listener(obj, ip)
		default:
			n.errorf(keyPath(ip, "type"), "type_not_allowed", "inbound type %q is not available", typ)
			continue
		}
		n.walk(obj, ip, "inbound:"+typ)
	}
}

func (n *normalizer) serverInboundAllowed(t string) bool {
	if !n.p.opts.AllowServerInbounds {
		return false
	}
	return serverInboundTypes[t] || (n.p.opts.QUIC && serverInboundQUICTypes[t])
}

// listener restricts local listeners to loopback: the agent never opens a port
// reachable from the network.
func (n *normalizer) listener(obj map[string]any, path string) {
	if sys, _ := obj["set_system_proxy"].(bool); sys {
		n.errorf(keyPath(path, "set_system_proxy"), "system_proxy_not_allowed", "changing the system proxy is not supported")
	}
	listen, present := obj["listen"]
	if !present || isEmpty(listen) {
		obj["listen"] = "127.0.0.1"
		return
	}
	s, ok := listen.(string)
	if !ok {
		n.errorf(keyPath(path, "listen"), "type_mismatch", "listen must be an address string")
		return
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || !addr.IsLoopback() {
		n.errorf(keyPath(path, "listen"), "listen_not_loopback", "listeners must bind to a loopback address, got %q", s)
	}
}

func (n *normalizer) route(val any, path string) {
	obj, ok := asObject(val)
	if !ok {
		n.errorf(path, "type_mismatch", "route must be an object")
		return
	}
	for _, k := range []string{"geoip", "geosite"} {
		if _, present := obj[k]; present {
			n.errorf(keyPath(path, k), "section_not_allowed", "route.%s databases were removed; use rule sets", k)
		}
	}
	if sets, ok := asArray(obj["rule_set"]); ok {
		n.ruleSets(sets, keyPath(path, "rule_set"))
	}
	n.walk(obj, path, "route")
}

func (n *normalizer) ruleSets(sets []any, path string) {
	for i, it := range sets {
		sp := indexPath(path, i)
		obj, ok := asObject(it)
		if !ok {
			n.errorf(sp, "type_mismatch", "a rule set must be an object")
			continue
		}
		typ, _ := obj["type"].(string)
		switch typ {
		case "", "inline":
		case "local":
			ref, _ := obj["path"].(string)
			if !strings.HasPrefix(ref, "res:") {
				n.errorf(keyPath(sp, "path"), "path_not_allowed", "local rule sets must reference a submitted resource as \"res:<name>\"")
				continue
			}
			if abs, ok := n.resourcePath(ref, keyPath(sp, "path")); ok {
				obj["path"] = abs
			}
		case "remote":
			n.errorf(sp, "rule_set_remote_not_allowed", "remote rule sets are downloaded by the client and submitted as local resources")
		default:
			n.errorf(keyPath(sp, "type"), "type_not_allowed", "rule set type %q is not available", typ)
		}
	}
}

// forceRoute applies the settings the agent insists on for any TUN setup.
func (n *normalizer) forceRoute(route map[string]any) {
	if len(n.tunTags) == 0 {
		return
	}
	// Proxy-node connections and bootstrap DNS must leave through the physical
	// interface, never loop back into the TUN.
	route["auto_detect_interface"] = true
	n.ensureDNSHijack(route)
}

// ensureDNSHijack makes sure DNS arriving through the TUN is answered by the
// core even when the platform driver (macOS) points the resolver at it
// instead of the core configuring interface DNS itself.
func (n *normalizer) ensureDNSHijack(route map[string]any) {
	rules, _ := asArray(route["rules"])
	for _, it := range rules {
		rule, ok := asObject(it)
		if !ok {
			continue
		}
		if action, _ := rule["action"].(string); action != "hijack-dns" {
			continue
		}
		if coversPort53(rule["port"]) && onlyKeys(rule, "action", "port", "inbound", "network") {
			return
		}
	}
	hijack := map[string]any{
		"inbound": stringsToAny(n.tunTags),
		"port":    []any{jsonNumber("53")},
		"action":  "hijack-dns",
	}
	route["rules"] = append([]any{hijack}, rules...)
}

func coversPort53(v any) bool {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if coversPort53(e) {
				return true
			}
		}
	default:
		return fmt.Sprint(t) == "53"
	}
	return false
}

func onlyKeys(m map[string]any, keys ...string) bool {
	allowed := set(keys...)
	for k := range m {
		if !allowed[k] {
			return false
		}
	}
	return true
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}
