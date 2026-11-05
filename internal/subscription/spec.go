package subscription

import (
	"fmt"
	"strconv"
	"strings"
)

// tlsSpec collects the TLS settings the different formats spell differently
// and renders them as sing-box's "tls" object.
type tlsSpec struct {
	Enabled     bool
	ServerName  string
	Insecure    bool
	ALPN        []string
	Fingerprint string
	Reality     bool
	PublicKey   string
	ShortID     string
	DisableSNI  bool
}

func (t tlsSpec) object() map[string]any {
	if !t.Enabled {
		return nil
	}
	o := map[string]any{"enabled": true}
	if t.ServerName != "" {
		o["server_name"] = t.ServerName
	}
	if t.Insecure {
		o["insecure"] = true
	}
	if len(t.ALPN) > 0 {
		o["alpn"] = anySlice(t.ALPN)
	}
	if t.DisableSNI {
		o["disable_sni"] = true
	}
	fp := fingerprint(t.Fingerprint)
	if t.Reality {
		if fp == "" {
			fp = "chrome" // REALITY needs uTLS
		}
		o["reality"] = map[string]any{"enabled": true, "public_key": t.PublicKey, "short_id": t.ShortID}
	}
	if fp != "" {
		o["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	return o
}

// utlsFingerprints are the client fingerprints sing-box can imitate.
var utlsFingerprints = map[string]bool{
	"chrome": true, "firefox": true, "edge": true, "safari": true, "360": true,
	"qq": true, "ios": true, "android": true, "random": true, "randomized": true,
}

func fingerprint(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if utlsFingerprints[s] {
		return s
	}
	return ""
}

// transportSpec describes a V2Ray-style transport; Kind uses the V2Ray names.
type transportSpec struct {
	Kind        string // tcp, ws, grpc, h2, http, httpupgrade
	Path        string
	Host        string // comma separated for h2/http
	ServiceName string
	Headers     map[string]string
	Method      string
	EarlyData   int
	EarlyHeader string
}

// object returns sing-box's "transport" object; nil means plain TCP.
func (t transportSpec) object() (map[string]any, error) {
	kind := strings.ToLower(strings.TrimSpace(t.Kind))
	switch kind {
	case "", "tcp", "raw":
		return nil, nil
	case "ws", "websocket":
		o := map[string]any{"type": "ws"}
		path, ed := splitEarlyData(t.Path)
		if t.EarlyData == 0 {
			t.EarlyData = ed
		}
		if path != "" {
			o["path"] = path
		}
		headers := map[string]any{}
		for k, v := range t.Headers {
			headers[k] = v
		}
		if t.Host != "" {
			headers["Host"] = t.Host
		}
		if len(headers) > 0 {
			o["headers"] = headers
		}
		if t.EarlyData > 0 {
			o["max_early_data"] = t.EarlyData
			header := t.EarlyHeader
			if header == "" {
				header = "Sec-WebSocket-Protocol"
			}
			o["early_data_header_name"] = header
		}
		return o, nil
	case "grpc", "gun":
		o := map[string]any{"type": "grpc"}
		if t.ServiceName != "" {
			o["service_name"] = t.ServiceName
		}
		return o, nil
	case "h2", "http":
		o := map[string]any{"type": "http"}
		if hosts := splitList(t.Host); len(hosts) > 0 {
			o["host"] = anySlice(hosts)
		}
		if t.Path != "" {
			o["path"] = t.Path
		}
		if t.Method != "" {
			o["method"] = t.Method
		}
		return o, nil
	case "httpupgrade":
		o := map[string]any{"type": "httpupgrade"}
		if t.Host != "" {
			o["host"] = t.Host
		}
		if t.Path != "" {
			o["path"] = t.Path
		}
		return o, nil
	default:
		return nil, fmt.Errorf("transport %q is not supported", kind)
	}
}

// splitEarlyData turns Xray's "/path?ed=2048" into ("/path", 2048).
func splitEarlyData(path string) (string, int) {
	i := strings.Index(path, "?")
	if i < 0 {
		return path, 0
	}
	base, query := path[:i], path[i+1:]
	ed := 0
	var rest []string
	for _, kv := range strings.Split(query, "&") {
		if v, ok := strings.CutPrefix(kv, "ed="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				ed = n
				continue
			}
		}
		if kv != "" {
			rest = append(rest, kv)
		}
	}
	if ed == 0 {
		return path, 0
	}
	if len(rest) > 0 {
		base += "?" + strings.Join(rest, "&")
	}
	return base, ed
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func anySlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// mbps reads a bandwidth such as "100", "100 Mbps", "50mbps", "1 Gbps".
func mbps(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "gbps"):
		mult, s = 1000, strings.TrimSpace(strings.TrimSuffix(s, "gbps"))
	case strings.HasSuffix(s, "mbps"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "mbps"))
	case strings.HasSuffix(s, "kbps"):
		mult, s = 0.001, strings.TrimSpace(strings.TrimSuffix(s, "kbps"))
	case strings.HasSuffix(s, "bps"):
		mult, s = 0.000001, strings.TrimSpace(strings.TrimSuffix(s, "bps"))
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	v := int(f*mult + 0.5)
	if v < 1 {
		v = 1
	}
	return v
}

// portRanges converts "20000-30000,40000" into sing-box's ["20000:30000","40000:40000"].
func portRanges(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ReplaceAll(p, "-", ":"))
		if p == "" {
			continue
		}
		lo, hi, ranged := strings.Cut(p, ":")
		a, err1 := strconv.Atoi(lo)
		if err1 != nil || a < 1 || a > 65535 {
			continue
		}
		b := a
		if ranged {
			var err2 error
			if b, err2 = strconv.Atoi(hi); err2 != nil || b < a || b > 65535 {
				continue
			}
		}
		out = append(out, fmt.Sprintf("%d:%d", a, b))
	}
	return out
}
