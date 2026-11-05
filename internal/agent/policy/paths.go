package policy

import (
	"encoding/json"
	"regexp"
	"strings"

	"eggbox/internal/agent/store"
)

func jsonNumber(s string) json.Number { return json.Number(s) }

var (
	// pathKeyRe finds fields that look like they reference something on disk or
	// an external program. Deny-by-default: an unknown match is rejected, so a
	// future sing-box release adding "ca_file" cannot silently become a file read
	// primitive. A test scans the compiled option types to catch drift early.
	pathKeyRe = regexp.MustCompile(`(?i)(^|_)(path|paths|file|files|dir|dirs|directory|socket|output|executable|command|program|script|binary)(_|$)`)

	// matcherKeys are routing matchers: they compare strings and never open the file.
	matcherKeys = set("process_path", "process_path_regex", "process_name", "package_name")

	// resourceKeys may hold a file when it is one the GUI submitted as "res:<name>".
	resourceKeys = set("certificate_path", "client_certificate_path", "client_key_path", "key_path", "config_path")

	// alwaysForbidden are never acceptable, whatever their value.
	alwaysForbidden = set(
		"protect_path",      // connects to an arbitrary unix socket
		"dhcp_lease_files",  // reads arbitrary files
		"netns", "pid_file", // Linux namespaces
		"data_directory",             // ACME storage
		"directory",                  // static file server root
		"certificate_directory_path", // reads a directory of certificates
		"external_ui",                // serves a directory over HTTP
		"initial_path",               // remote rule set seed file (remote rule sets are denied)
	)

	// coveredBySectionDeny are path-like fields that only exist inside sections
	// the policy rejects wholesale (the deprecated clash_api.cache_file).
	coveredBySectionDeny = set("cache_file")

	// benignPathKinds are positions where "path" is a URL path, not a file, or
	// where a dedicated handler has already validated and rewritten it.
	// "rule-set:local" relies on normalizer.ruleSets running before the walk.
	benignPathKinds = set(
		"transport:http", "transport:ws", "transport:httpupgrade",
		"outbound:http", "dns-server:https", "dns-server:h3",
		"rule-set:local",
	)
)

// walk checks every key of the subtree below v. kind describes where in the
// configuration the value sits, which decides what "path" means.
func (n *normalizer) walk(v any, path, kind string) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			cp := keyPath(path, k)
			n.checkKey(t, k, cp, kind)
			n.walk(t[k], cp, childKind(kind, k, t[k]))
		}
	case []any:
		for i, e := range t {
			n.walk(e, indexPath(path, i), elementKind(kind, e))
		}
	}
}

func childKind(parent, key string, child any) string {
	switch {
	case key == "transport":
		if o, ok := child.(map[string]any); ok {
			t, _ := o["type"].(string)
			return "transport:" + t
		}
	case parent == "dns" && key == "servers":
		return "dns-servers"
	case parent == "route" && key == "rule_set":
		return "rule-sets"
	}
	return ""
}

func elementKind(kind string, e any) string {
	o, ok := e.(map[string]any)
	if !ok {
		return kind
	}
	t, _ := o["type"].(string)
	switch kind {
	case "dns-servers":
		return "dns-server:" + t
	case "rule-sets":
		return "rule-set:" + t
	}
	return kind
}

// checkKey classifies one key/value pair. It may rewrite the value in place
// (resource references) or record an issue.
func (n *normalizer) checkKey(obj map[string]any, key, path, kind string) {
	val := obj[key]
	if alwaysForbidden[key] {
		if !isEmpty(val) && val != false {
			n.errorf(path, "path_not_allowed", "%q would give the core access to local files or sockets", key)
		}
		return
	}
	if matcherKeys[key] || !pathKeyRe.MatchString(key) || !holdsString(val) {
		return
	}
	switch {
	case resourceKeys[key]:
		n.rewriteResource(obj, key, val, path)
	case key == "path" && benignPathKinds[kind]:
		// A URL path (WebSocket, HTTP transport, DoH, HTTP proxy).
	default:
		if !isEmpty(val) {
			n.errorf(path, "path_not_allowed", "%q is not allowed here; local files can only be referenced as \"res:<name>\" in supported fields", key)
		}
	}
}

func holdsString(v any) bool {
	switch t := v.(type) {
	case string:
		return true
	case []any:
		for _, e := range t {
			if _, ok := e.(string); ok {
				return true
			}
		}
	}
	return false
}

func (n *normalizer) rewriteResource(obj map[string]any, key string, val any, path string) {
	switch t := val.(type) {
	case string:
		if t == "" {
			return
		}
		if !strings.HasPrefix(t, store.ResourceRef) {
			n.errorf(path, "path_not_allowed", "%q must reference a submitted resource as \"res:<name>\"", key)
			return
		}
		if abs, ok := n.resourcePath(t, path); ok {
			obj[key] = abs
		}
	case []any:
		for i, e := range t {
			s, ok := e.(string)
			if !ok || s == "" {
				continue
			}
			ep := indexPath(path, i)
			if !strings.HasPrefix(s, store.ResourceRef) {
				n.errorf(ep, "path_not_allowed", "%q must reference a submitted resource as \"res:<name>\"", key)
				continue
			}
			if abs, ok := n.resourcePath(s, ep); ok {
				t[i] = abs
			}
		}
	}
}
