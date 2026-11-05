// Package redact removes what must not leave the machine from text that is
// meant to be shared: diagnostics reports, copied logs, bug reports.
//
// Server addresses, host names, credentials and account names are replaced by
// placeholders. A placeholder is stable within one Redactor, so a reader can
// still tell that "ip-1" in two log lines is the same server, which is usually
// all that is needed to understand a problem. Addresses that identify nothing
// (loopback, private, link-local, the tunnel's own subnet) are kept because
// route conflicts cannot be diagnosed without them.
package redact

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Placeholder is what a sensitive value is replaced with when no stable label
// is needed.
const Placeholder = "[redacted]"

var (
	reURLUserinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s@"']+@`)
	reQuerySecret = regexp.MustCompile(`(?i)([?&;](?:token|access_token|auth|key|api_key|apikey|password|passwd|pass|secret|sig|signature|uuid|psk)=)[^&\s"'#]+`)
	reKeyValue    = regexp.MustCompile(`(?i)\b(token|access_token|password|passwd|secret|api_key|apikey|psk|auth_str)(\s*"?\s*[=:]\s*)"?[^\s"',;&]+"?`)
	reBearer      = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]{8,}`)
	reUserPath    = regexp.MustCompile(`(?i)(/Users/|/home/|[A-Z]:\\{1,2}Users\\{1,2})[^/\\\s"']+`)
	reUUID        = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reIPv6        = regexp.MustCompile(`(?i)[0-9a-f:]*:[0-9a-f:]*:[0-9a-f:.]*(?:%[a-z0-9_.\-]+)?`)
	reIPv4        = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reHost        = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}\b`)
	reLongToken   = regexp.MustCompile(`\b[A-Za-z0-9_\-]{32,}\b`)
)

// fileSuffixes are "top-level domains" that are really file extensions; a name
// ending in one of them is not a host name.
var fileSuffixes = map[string]bool{
	"json": true, "db": true, "log": true, "srs": true, "txt": true, "yaml": true, "yml": true,
	"dll": true, "exe": true, "dylib": true, "plist": true, "go": true, "sock": true, "pipe": true,
	"pid": true, "tmp": true, "bak": true, "conf": true, "cfg": true, "toml": true, "md": true,
	"pkg": true, "dmg": true, "msi": true, "so": true, "bin": true, "sh": true, "ps1": true,
	"pb": true, "proto": true, "js": true, "ts": true, "css": true, "html": true, "png": true,
	"svg": true, "ico": true, "lock": true, "ini": true, "mmdb": true, "csv": true, "xml": true,
	"app": true, "swift": true, "h": true, "c": true, "mod": true, "sum": true, "zip": true, "gz": true,
	"nsi": true, "nsh": true,
}

// sensitiveKeys are JSON keys whose string values are always replaced.
var sensitiveKeys = map[string]bool{
	"password": true, "passwd": true, "pass": true, "secret": true, "token": true, "access_token": true,
	"auth": true, "auth_str": true, "authorization": true, "private_key": true, "pre_shared_key": true,
	"psk": true, "uuid": true, "username": true, "user": true, "key": true, "public_key": true,
	"short_id": true, "certificate": true, "obfs_password": true, "cookie": true, "api_key": true,
	"user_id": true, "secret_key": true,
}

// Redactor is safe for concurrent use. Create one per report.
type Redactor struct {
	mu   sync.Mutex
	keep []string

	ips, hosts, ids, secrets map[string]string
}

// New returns a Redactor. Host-like names starting with one of keep (for
// example the application's own identifiers) are left alone.
func New(keep ...string) *Redactor {
	r := &Redactor{
		ips: map[string]string{}, hosts: map[string]string{},
		ids: map[string]string{}, secrets: map[string]string{},
	}
	for _, k := range keep {
		if k != "" {
			r.keep = append(r.keep, strings.ToLower(k))
		}
	}
	return r
}

func (r *Redactor) label(m map[string]string, prefix, value string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToLower(value)
	if l, ok := m[key]; ok {
		return l
	}
	l := prefix + "-" + strconv.Itoa(len(m)+1)
	m[key] = l
	return l
}

// String redacts free text.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}
	s = reURLUserinfo.ReplaceAllString(s, "${1}"+Placeholder+"@")
	s = reQuerySecret.ReplaceAllString(s, "${1}"+Placeholder)
	s = reKeyValue.ReplaceAllString(s, "${1}${2}"+Placeholder)
	s = reBearer.ReplaceAllString(s, "${1} "+Placeholder)
	s = reUserPath.ReplaceAllStringFunc(s, func(m string) string {
		i := strings.LastIndexAny(m, `/\`)
		return m[:i+1] + "user"
	})
	s = reUUID.ReplaceAllStringFunc(s, func(m string) string { return r.label(r.ids, "id", m) })
	s = reIPv6.ReplaceAllStringFunc(s, r.ipv6)
	s = reIPv4.ReplaceAllStringFunc(s, r.ipv4)
	s = reHost.ReplaceAllStringFunc(s, r.host)
	s = reLongToken.ReplaceAllStringFunc(s, r.longToken)
	return s
}

func (r *Redactor) ipv4(m string) string {
	addr, err := netip.ParseAddr(m)
	if err != nil || harmless(addr) {
		return m
	}
	return r.label(r.ips, "ip", addr.String())
}

func (r *Redactor) ipv6(m string) string {
	// The candidate pattern is loose on purpose (it also matches clock times
	// and MAC addresses); only something that parses as an address is replaced.
	cand := m
	if i := strings.IndexByte(m, '%'); i >= 0 {
		cand = m[:i]
	}
	addr, err := netip.ParseAddr(cand)
	if err != nil || !addr.Is6() || harmless(addr) {
		return m
	}
	return r.label(r.ips, "ip", addr.String())
}

// harmless addresses identify no one.
func harmless(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return true
	}
	if a.Is4() {
		// Carrier-grade NAT space, used by overlay networks such as Tailscale.
		cgnat := netip.MustParsePrefix("100.64.0.0/10")
		return cgnat.Contains(a)
	}
	return false
}

func (r *Redactor) host(m string) string {
	lower := strings.ToLower(m)
	i := strings.LastIndexByte(lower, '.')
	if fileSuffixes[lower[i+1:]] || lower == "localhost" {
		return m
	}
	for _, k := range r.keep {
		if strings.HasPrefix(lower, k) {
			return m
		}
	}
	return r.label(r.hosts, "host", lower)
}

func (r *Redactor) longToken(m string) string {
	var digit, letter bool
	for _, c := range m {
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			letter = true
		}
	}
	if !digit || !letter {
		return m
	}
	return r.label(r.secrets, "secret", m)
}

// JSON redacts a JSON document: the values of credential-like keys are
// replaced outright and every other string goes through String. The result is
// indented. Numbers, booleans and the structure are untouched.
func (r *Redactor) JSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(r.walk("", v), "", "  ")
}

func (r *Redactor) walk(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = r.walk(k, val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, el := range t {
			out[i] = r.walk(key, el)
		}
		return out
	case string:
		if t == "" {
			return t
		}
		if sensitiveKeys[strings.ReplaceAll(strings.ToLower(key), "-", "_")] {
			return Placeholder
		}
		return r.String(t)
	default:
		return v
	}
}
