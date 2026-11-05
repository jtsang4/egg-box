package subscription

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// dialFields are outbound options that tie a node to the provider's own
// machine or configuration. The agent owns routing and interface binding, so
// they are dropped.
var dialFields = []string{
	"domain_resolver", "bind_interface", "inet4_bind_address", "inet6_bind_address",
	"routing_mark", "reuse_addr", "netns", "network_strategy", "network_type",
	"fallback_network_type", "fallback_delay",
}

// structuralTypes make up a provider's groups, not its servers.
var structuralTypes = map[string]bool{"selector": true, "urltest": true, "preferred": true, "direct": true, "block": true, "dns": true}

// parseSingBox extracts the servers of a sing-box configuration (or of a bare
// array of outbounds). Everything around them is ignored.
func parseSingBox(data []byte) ([]Node, []Skipped, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, errors.New("the file is not valid JSON")
	}
	var outbounds, endpoints []any
	switch t := v.(type) {
	case map[string]any:
		outbounds, _ = t["outbounds"].([]any)
		endpoints, _ = t["endpoints"].([]any)
		if outbounds == nil && endpoints == nil {
			return nil, nil, errors.New("the configuration has no outbounds")
		}
	case []any:
		outbounds = t
	default:
		return nil, nil, errors.New("the file is not a sing-box configuration")
	}

	var nodes []Node
	var skipped []Skipped
	handle := func(raw any, endpoint bool) {
		obj, ok := raw.(map[string]any)
		if !ok {
			return
		}
		typ, _ := obj["type"].(string)
		name, _ := obj["tag"].(string)
		if !endpoint && structuralTypes[typ] {
			return
		}
		n, err := singBoxNode(obj, endpoint)
		if err == nil {
			err = n.Validate()
			if err != nil {
				err = skip(SkipInvalid, "%v", err)
			}
		}
		if err != nil {
			s := Skipped{Name: name, Type: typ, Code: SkipInvalid, Detail: err.Error()}
			var se *skipError
			if errors.As(err, &se) {
				s.Code, s.Detail = se.code, se.detail
			}
			skipped = append(skipped, s)
			return
		}
		nodes = append(nodes, n)
	}
	for _, o := range outbounds {
		handle(o, false)
	}
	for _, o := range endpoints {
		handle(o, true)
	}
	return nodes, skipped, nil
}

func singBoxNode(obj map[string]any, endpoint bool) (Node, error) {
	typ, _ := obj["type"].(string)
	name, _ := obj["tag"].(string)
	if !supportedTypes[typ] {
		return Node{}, skip(SkipUnsupportedProtocol, "%s is not supported", typ)
	}
	if endpoint != (typ == "wireguard") {
		return Node{}, skip(SkipInvalid, "%s is in the wrong section", typ)
	}
	cfg, _ := normalizeNumbers(obj).(map[string]any)
	delete(cfg, "tag")
	if d, ok := cfg["detour"].(string); ok {
		if d != "" && d != "direct" {
			return Node{}, skip(SkipUnsupportedFeature, "proxy chaining (detour) is not supported")
		}
		delete(cfg, "detour")
	}
	for _, k := range dialFields {
		delete(cfg, k)
	}
	if sys, _ := cfg["system"].(bool); sys {
		return Node{}, skip(SkipUnsupportedFeature, "system WireGuard interfaces are not supported")
	}

	n := Node{Name: name, Type: typ, Endpoint: endpoint, Config: cfg}
	switch {
	case typ == "wireguard":
		peers, _ := cfg["peers"].([]any)
		if len(peers) == 0 {
			return Node{}, skip(SkipInvalid, "peers are missing")
		}
		if p, ok := peers[0].(map[string]any); ok {
			n.Server, _ = p["address"].(string)
			n.Port = intOf(p["port"])
		}
	default:
		n.Server, _ = cfg["server"].(string)
		n.Port = intOf(cfg["server_port"])
		if n.Port == 0 {
			// hysteria2 / hysteria may give only a port range list.
			if ports, ok := cfg["server_ports"].([]any); ok && len(ports) > 0 {
				first, _ := ports[0].(string)
				n.Port = intOf(strings.SplitN(first, ":", 2)[0])
			}
		}
	}
	return n, nil
}

// normalizeNumbers turns json.Number into int or float64 so a node looks the
// same whether it was parsed here or loaded back from a file.
func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = normalizeNumbers(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeNumbers(e)
		}
		return out
	}
	return v
}

func intOf(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	case string:
		var n int
		fmt.Sscanf(strings.TrimSpace(t), "%d", &n)
		return n
	}
	return 0
}
