package policy

import (
	"net/netip"
	"strconv"
)

// Fields the GUI may tune on the TUN inbound. Everything else is either owned
// by the agent (tunOwned) or rejected.
var (
	tunAllowed = set(
		"type", "tag", "mtu", "stack",
		"route_address", "route_exclude_address", "route_address_set", "route_exclude_address_set",
		"include_interface", "exclude_interface",
		"udp_timeout", "udp_mapping", "udp_filtering", "udp_nat_max", "exclude_mptcp",
	)
	tunOwned  = set("address", "auto_route", "strict_route", "dns_mode", "dns_address", "interface_name")
	tunStacks = set("system", "gvisor", "mixed")
)

const (
	defaultTunMTU = 1500
	minTunMTU     = 1280
	maxTunMTU     = 9000
	defaultTunTag = "tun-in"
)

func (n *normalizer) tun(obj map[string]any, path string) {
	for _, k := range sortedKeys(obj) {
		if !tunAllowed[k] && !tunOwned[k] {
			n.errorf(keyPath(path, k), "tun_field_not_allowed", "tun.%s is managed by the agent or unsupported on this platform", k)
		}
	}

	tag, _ := obj["tag"].(string)
	if tag == "" {
		tag = defaultTunTag
		obj["tag"] = tag
	}
	n.tunTags = append(n.tunTags, tag)

	if stack, present := obj["stack"]; present {
		if s, _ := stack.(string); !tunStacks[s] {
			n.errorf(keyPath(path, "stack"), "value_invalid", "tun.stack must be one of system, gvisor, mixed")
		}
	} else {
		obj["stack"] = "system"
	}

	if mtu, present := obj["mtu"]; present {
		num, ok := mtu.(interface{ Int64() (int64, error) })
		var v int64
		var err error
		if ok {
			v, err = num.Int64()
		}
		if !ok || err != nil || v < minTunMTU || v > maxTunMTU {
			n.errorf(keyPath(path, "mtu"), "value_invalid", "tun.mtu must be an integer between %d and %d", minTunMTU, maxTunMTU)
		}
	} else {
		obj["mtu"] = jsonNumber(strconv.Itoa(defaultTunMTU))
	}

	for _, k := range []string{"route_address", "route_exclude_address"} {
		n.checkPrefixes(obj[k], keyPath(path, k))
	}

	// Owned fields: always the platform plan, whatever the GUI sent.
	obj["address"] = stringsToAny(n.env.Tun.Addresses)
	obj["auto_route"] = true
	obj["strict_route"] = n.env.Tun.StrictRoute
	setOrDelete(obj, "dns_mode", n.env.Tun.DNSMode)
	if len(n.env.Tun.DNSAddresses) > 0 {
		obj["dns_address"] = stringsToAny(n.env.Tun.DNSAddresses)
	} else {
		delete(obj, "dns_address")
	}
	setOrDelete(obj, "interface_name", n.env.Tun.InterfaceName)
	if n.env.Tun.TakeoverInterface != "" {
		// Quarter-defaults outrank both full and split defaults used by VPNs.
		// The core owns these routes for the lifetime of its TUN.
		obj["route_address"] = stringsToAny([]string{
			"0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/2",
			"::/2", "4000::/2", "8000::/2", "c000::/2",
		})
		for _, k := range []string{"route_exclude_address", "route_address_set", "route_exclude_address_set"} {
			delete(obj, k)
		}
	}
}

func setOrDelete(obj map[string]any, key, value string) {
	if value == "" {
		delete(obj, key)
		return
	}
	obj[key] = value
}

func (n *normalizer) checkPrefixes(v any, path string) {
	switch t := v.(type) {
	case nil:
	case string:
		if _, err := netip.ParsePrefix(t); err != nil {
			n.errorf(path, "value_invalid", "%q is not a CIDR prefix", t)
		}
	case []any:
		for i, e := range t {
			n.checkPrefixes(e, indexPath(path, i))
		}
	default:
		n.errorf(path, "type_mismatch", "expected a CIDR prefix or a list of prefixes")
	}
}

// finishTun runs after every section is processed: it records the TUN in the
// result and applies the route-level settings a TUN setup needs.
func (n *normalizer) finishTun(root map[string]any) {
	if len(n.tunTags) == 0 {
		return
	}
	n.res.HasTun = true
	n.res.TunTag = n.tunTags[0]
	route, ok := asObject(root["route"])
	if !ok {
		route = map[string]any{}
		root["route"] = route
	}
	n.forceRoute(route)
}

// sandboxTun swaps the TUN for a loopback proxy that keeps the same tag, so
// route rules that reference the inbound still resolve.
func (n *normalizer) sandboxTun(obj map[string]any, path string) {
	tag, _ := obj["tag"].(string)
	if tag == "" {
		tag = defaultTunTag
	}
	for k := range obj {
		delete(obj, k)
	}
	obj["type"] = "mixed"
	obj["tag"] = tag
	obj["listen"] = "127.0.0.1"
	obj["listen_port"] = jsonNumber(strconv.Itoa(n.env.SandboxPort))
	n.res.SandboxTun = true
	n.warnf(path, "sandbox_tun_replaced", "sandbox mode: the TUN inbound runs as a loopback proxy on 127.0.0.1:%d and the host network is not touched", n.env.SandboxPort)
}
