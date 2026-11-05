package policy

import "fmt"

// references checks that the tags a configuration points at exist.
//
// The core resolves some of them when it starts (selector members, detours,
// the final outbound, rule-sets) and reports them as bare strings. The targets
// of route and DNS rules it only discovers when a connection or a query shows
// up, as "outbound not found", so a dangling rule stays invisible until
// someone's traffic breaks. Catching them here gives a located, localisable
// error when the configuration is saved.
//
// The check is deliberately limited to references whose meaning is
// unambiguous; anything the core would accept is accepted here.
func (n *normalizer) references(root map[string]any) {
	outbounds := tagSet(root["outbounds"])
	for tag := range tagSet(root["endpoints"]) {
		outbounds[tag] = true
	}

	dnsObj, _ := asObject(root["dns"])
	var dnsServers map[string]bool
	if dnsObj != nil {
		dnsServers = tagSet(dnsObj["servers"])
	}
	if len(dnsServers) == 0 {
		// Without servers the core runs one built-in resolver.
		dnsServers = map[string]bool{"local": true}
	}

	n.outboundRefs(root, outbounds, dnsServers)
	n.routeRefs(root, outbounds, dnsServers)
	n.dnsRefs(dnsObj, outbounds, dnsServers)
}

// tagSet collects the "tag" of every object in an array.
func tagSet(v any) map[string]bool {
	out := map[string]bool{}
	arr, _ := asArray(v)
	for _, it := range arr {
		if obj, ok := asObject(it); ok {
			if tag, _ := obj["tag"].(string); tag != "" {
				out[tag] = true
			}
		}
	}
	return out
}

// mustExist reports a reference to a tag that is not defined. Empty and
// non-string values are not references and are left to the core.
func (n *normalizer) mustExist(path string, v any, defined map[string]bool, kind string) {
	tag, ok := v.(string)
	if !ok || tag == "" || defined[tag] {
		return
	}
	n.errorf(path, "reference_missing", "%s %q is not defined", kind, tag)
}

// resolverTag extracts the server from a domain resolver, which is either a
// tag or an object {"server": tag, ...}.
func resolverTag(v any) any {
	if obj, ok := asObject(v); ok {
		return obj["server"]
	}
	return v
}

func (n *normalizer) outboundRefs(root map[string]any, outbounds, dnsServers map[string]bool) {
	check := func(section string) {
		arr, _ := asArray(root[section])
		for i, it := range arr {
			obj, ok := asObject(it)
			if !ok {
				continue
			}
			path := indexPath(keyPath("$", section), i)
			n.mustExist(keyPath(path, "detour"), obj["detour"], outbounds, "outbound")
			n.mustExist(keyPath(path, "domain_resolver"), resolverTag(obj["domain_resolver"]), dnsServers, "DNS server")

			typ, _ := obj["type"].(string)
			if typ != "selector" && typ != "urltest" && typ != "preferred" {
				continue
			}
			members, _ := asArray(obj["outbounds"])
			memberSet := map[string]bool{}
			for j, m := range members {
				n.mustExist(indexPath(keyPath(path, "outbounds"), j), m, outbounds, "outbound")
				if s, ok := m.(string); ok {
					memberSet[s] = true
				}
			}
			if def, _ := obj["default"].(string); def != "" && !memberSet[def] {
				n.errorf(keyPath(path, "default"), "reference_missing", "the default %q is not one of the group's outbounds", def)
			}
			if typ == "preferred" {
				n.standIns(path, obj["fallbacks"], memberSet)
			}
		}
	}
	check("outbounds")
	check("endpoints")
}

// standIns checks the stand-ins of a preferred group: each one is a member
// that stands in for another member.
func (n *normalizer) standIns(path string, v any, members map[string]bool) {
	obj, ok := asObject(v)
	if !ok {
		return
	}
	for member, target := range obj {
		fp := keyPath(keyPath(path, "fallbacks"), member)
		if !members[member] {
			n.errorf(fp, "reference_missing", "%q is not one of the group's outbounds", member)
			continue
		}
		if s, _ := target.(string); !members[s] {
			n.errorf(fp, "reference_missing", "the stand-in %v is not one of the group's outbounds", target)
		}
	}
}

func (n *normalizer) routeRefs(root map[string]any, outbounds, dnsServers map[string]bool) {
	route, ok := asObject(root["route"])
	if !ok {
		return
	}
	n.mustExist("$.route.final", route["final"], outbounds, "outbound")
	n.mustExist("$.route.default_domain_resolver", resolverTag(route["default_domain_resolver"]), dnsServers, "DNS server")

	rules, _ := asArray(route["rules"])
	for i, it := range rules {
		rule, ok := asObject(it)
		if !ok {
			continue
		}
		path := indexPath("$.route.rules", i)
		action, _ := rule["action"].(string)
		switch action {
		case "", "route":
			n.mustExist(keyPath(path, "outbound"), rule["outbound"], outbounds, "outbound")
		case "resolve":
			n.mustExist(keyPath(path, "server"), rule["server"], dnsServers, "DNS server")
		}
	}
}

func (n *normalizer) dnsRefs(dns map[string]any, outbounds, dnsServers map[string]bool) {
	if dns == nil {
		return
	}
	servers, _ := asArray(dns["servers"])
	for i, it := range servers {
		srv, ok := asObject(it)
		if !ok {
			continue
		}
		path := indexPath("$.dns.servers", i)
		n.mustExist(keyPath(path, "detour"), srv["detour"], outbounds, "outbound")
		n.mustExist(keyPath(path, "domain_resolver"), resolverTag(srv["domain_resolver"]), dnsServers, "DNS server")
	}
	n.mustExist("$.dns.final", dns["final"], dnsServers, "DNS server")

	rules, _ := asArray(dns["rules"])
	for i, it := range rules {
		rule, ok := asObject(it)
		if !ok {
			continue
		}
		// Only route and evaluate actions carry a server.
		n.mustExist(fmt.Sprintf("$.dns.rules[%d].server", i), rule["server"], dnsServers, "DNS server")
	}
}
