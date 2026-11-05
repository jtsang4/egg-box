package policy

import "sort"

// The functions below expose the allow-lists so tests in the engine package
// can prove the policy and the compiled core agree. A type the policy allows
// but the core does not register would fail late with an unfriendly message;
// a type the core registers but the policy rejects is dead weight in the binary.

func keysOf(sets ...map[string]bool) []string {
	seen := map[string]bool{}
	for _, s := range sets {
		for k := range s {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// InboundTypes lists the inbound types the policy accepts.
func (p *Policy) InboundTypes() []string {
	sets := []map[string]bool{set("tun", "mixed")}
	if p.opts.AllowServerInbounds {
		sets = append(sets, serverInboundTypes)
		if p.opts.QUIC {
			sets = append(sets, serverInboundQUICTypes)
		}
	}
	return keysOf(sets...)
}

// OutboundTypes lists the outbound types the policy accepts.
func (p *Policy) OutboundTypes() []string {
	if p.opts.QUIC {
		return keysOf(outboundTypes, outboundQUICTypes)
	}
	return keysOf(outboundTypes)
}

// EndpointTypes lists the endpoint types the policy accepts.
func (p *Policy) EndpointTypes() []string {
	if p.opts.WireGuard {
		return keysOf(endpointTypes)
	}
	return nil
}

// DNSServerTypes lists the DNS server types the policy accepts.
func (p *Policy) DNSServerTypes() []string {
	sets := []map[string]bool{dnsServerTypes}
	if p.opts.QUIC {
		sets = append(sets, dnsQUICTypes)
	}
	if p.opts.DHCP {
		sets = append(sets, dnsDHCPTypes)
	}
	return keysOf(sets...)
}

// PathLikeKey reports whether a JSON key looks like a file or program reference.
func PathLikeKey(key string) bool { return pathKeyRe.MatchString(key) }

// KnownPathKey reports whether the policy deliberately classifies a path-like
// key (forbidden, resource reference, matcher, or contextual "path"). Keys
// that are path-like but unknown are rejected by default; the drift test
// fails when a core upgrade introduces one so it gets classified on purpose.
func KnownPathKey(key string) bool {
	return alwaysForbidden[key] || resourceKeys[key] || matcherKeys[key] || coveredBySectionDeny[key] || key == "path" || key == "output"
}
