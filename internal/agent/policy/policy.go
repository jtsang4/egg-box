// Package policy is the gatekeeper between the unprivileged GUI and the
// privileged core. The GUI hands the agent a sing-box configuration; this
// package decides what of it may run as SYSTEM/root and rewrites the parts the
// agent must own.
//
// Rules, in the order they matter:
//
//  1. Only an allow-list of sections, inbound/outbound/endpoint/DNS types.
//  2. No access to arbitrary files: path-like fields are rejected unless they
//     are "res:<name>" references to files the GUI handed over (rewritten to the
//     protected resource directory) or known-harmless URL paths.
//  3. No new network surface: listeners must be loopback, no management APIs,
//     no services, no system-proxy toggling, no external programs.
//  4. The TUN inbound is owned by the agent: addressing, routing and DNS fields
//     are overwritten from the platform plan.
//
// The input is parsed once into a generic tree (duplicate keys rejected), the
// tree is checked and rewritten, and that same tree is serialised for the core,
// so there is no parser disagreement to exploit.
package policy

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"eggbox/internal/agent/store"
)

// Severity of an Issue.
type Severity int

const (
	SeverityError Severity = iota + 1
	SeverityWarning
)

func (s Severity) String() string {
	if s == SeverityWarning {
		return "warning"
	}
	return "error"
}

// Issue is one finding, located by a JSON path such as "$.inbounds[0].listen".
type Issue struct {
	Severity Severity
	Path     string
	// Code is stable and machine readable so the GUI can localise the message.
	Code    string
	Message string
}

func (i Issue) String() string {
	return fmt.Sprintf("%s %s [%s]: %s", i.Severity, i.Path, i.Code, i.Message)
}

// Options describes what the running binary supports.
type Options struct {
	// AllowServerInbounds permits socks/http/shadowsocks/... listeners (still loopback only).
	// It exists for tests and the demo backend; production leaves it false.
	AllowServerInbounds bool
	// WireGuard, QUIC, DHCP and UTLS mirror the build tags compiled into the
	// core. UTLS covers uTLS client fingerprints and REALITY, which needs them.
	WireGuard bool
	QUIC      bool
	DHCP      bool
	UTLS      bool
}

// Policy is stateless and safe for concurrent use.
type Policy struct{ opts Options }

// New returns a policy for the given build capabilities.
func New(opts Options) *Policy { return &Policy{opts: opts} }

// TunSettings are the platform-owned values written into the TUN inbound.
type TunSettings struct {
	Addresses     []string // CIDR strings, e.g. "172.19.0.1/30"
	DNSAddresses  []string // optional; sing-box derives them from Addresses when empty
	InterfaceName string   // "" lets the OS choose (macOS utunN)
	StrictRoute   bool
	DNSMode       string // "disabled" | "native" | "hijack"
	// TakeoverInterface binds the core to a physical uplink and enables
	// more-specific default routes after explicit user confirmation.
	TakeoverInterface string
	// TakeoverRoutes become the TUN's routes during a takeover; they cover
	// everything and outrank the other tunnel's broad routes. Quarters of
	// both families are used when empty.
	TakeoverRoutes []string
}

// Env carries everything the policy needs from the agent.
type Env struct {
	// ResourceDir is the absolute directory "res:<name>" references resolve into.
	ResourceDir string
	// Resources lists the names the bundle provides.
	Resources map[string]bool
	// CachePath is the absolute path of the core's cache database.
	CachePath string
	// LogLevel, when set, replaces log.level (the agent setting is authoritative).
	LogLevel string
	// Tun is applied when the configuration contains a TUN inbound.
	Tun TunSettings
	// SandboxPort, when non-zero, replaces the TUN inbound with a loopback
	// "mixed" proxy on this port. The development sandbox uses it to run the
	// real core without touching the host's interfaces, routes or DNS.
	SandboxPort int
}

// Result is the outcome of Normalize.
type Result struct {
	// JSON is the configuration to run. It is empty when there are errors.
	JSON   []byte
	HasTun bool
	TunTag string
	// SandboxTun is true when a TUN inbound was replaced by a loopback proxy.
	SandboxTun bool
	Issues     []Issue
}

// HasErrors reports whether any issue is an error.
func (r *Result) HasErrors() bool {
	for _, i := range r.Issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors returns the error-level issues.
func (r *Result) Errors() []Issue {
	var out []Issue
	for _, i := range r.Issues {
		if i.Severity == SeverityError {
			out = append(out, i)
		}
	}
	return out
}

// Normalize checks raw against the policy and returns the configuration to run.
func (p *Policy) Normalize(raw []byte, env Env) *Result {
	r := &Result{}
	if len(raw) == 0 {
		r.add(SeverityError, "$", "config_empty", "the configuration is empty")
		return r
	}
	if len(raw) > store.MaxConfigBytes {
		r.add(SeverityError, "$", "config_too_large", "the configuration exceeds %d bytes", store.MaxConfigBytes)
		return r
	}
	root, err := decodeStrict(raw)
	if err != nil {
		r.add(SeverityError, "$", "json_invalid", "%v", err)
		return r
	}

	n := &normalizer{p: p, env: env, res: r}
	n.top(root)
	if r.HasErrors() {
		return r
	}
	out, err := marshalCanonical(root)
	if err != nil {
		r.add(SeverityError, "$", "json_encode_failed", "%v", err)
		return r
	}
	r.JSON = out
	return r
}

func (r *Result) add(sev Severity, path, code, format string, args ...any) {
	r.Issues = append(r.Issues, Issue{Severity: sev, Path: path, Code: code, Message: fmt.Sprintf(format, args...)})
}

type normalizer struct {
	p       *Policy
	env     Env
	res     *Result
	tunTags []string
}

func (n *normalizer) errorf(path, code, format string, args ...any) {
	n.res.add(SeverityError, path, code, format, args...)
}

func (n *normalizer) warnf(path, code, format string, args ...any) {
	n.res.add(SeverityWarning, path, code, format, args...)
}

// resourcePath resolves "res:<name>" to the protected resource directory.
func (n *normalizer) resourcePath(ref, path string) (string, bool) {
	name := strings.TrimPrefix(ref, store.ResourceRef)
	if !n.env.Resources[name] {
		n.errorf(path, "resource_missing", "%q is not part of the submitted resources", ref)
		return "", false
	}
	return filepath.Join(n.env.ResourceDir, name), true
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asArray(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	case string:
		return t == ""
	}
	return false
}

func indexPath(parent string, i int) string { return fmt.Sprintf("%s[%d]", parent, i) }
func keyPath(parent, key string) string     { return parent + "." + key }
