package rulefmt

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"regexp"
	"strings"
)

var (
	srsMagic  = []byte("SRS")
	mrsMagic  = []byte("MRS")
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
	bom       = []byte{0xef, 0xbb, 0xbf}

	rePayload   = regexp.MustCompile(`(?m)^payload\s*:`)
	reClassical = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*,`)
	reHosts     = regexp.MustCompile(`^(?:0\.0\.0\.0|127\.0\.0\.1|::1?|\d{1,3}(?:\.\d{1,3}){3})\s+\S+`)
)

// sampleLines is how many content lines Detect reads before voting.
const sampleLines = 4000

// Detect names the format of a file by looking at its content. The result
// for text is the dominant line shape, so a stray comment or a few odd
// lines do not change it.
func Detect(data []byte) Format {
	data = bytes.TrimPrefix(data, bom)
	switch {
	case bytes.HasPrefix(data, srsMagic):
		return FormatSRS
	case bytes.HasPrefix(data, zstdMagic), bytes.HasPrefix(data, append(append([]byte{}, mrsMagic...), 1)):
		return FormatMRS
	}
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '{' {
		var probe struct {
			Rules json.RawMessage `json:"rules"`
		}
		if json.Unmarshal(data, &probe) == nil && len(probe.Rules) > 0 {
			return FormatSingBox
		}
		return ""
	}
	head := data
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	if rePayload.Match(head) {
		return FormatClashYAML
	}
	return detectText(data)
}

func detectText(data []byte) Format {
	var classical, adguard, hosts, cidr, dom int
	n := 0
	for len(data) > 0 && n < sampleLines {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		s := strings.TrimSpace(string(line))
		if s == "" || isComment(s) {
			continue
		}
		n++
		switch {
		case reClassical.MatchString(s):
			classical++
		case strings.HasPrefix(s, "||") || strings.HasPrefix(s, "@@") || strings.HasSuffix(s, "^") || strings.Contains(s, "^$"):
			adguard++
		case reHosts.MatchString(s) && isHostsAddr(s):
			hosts++
		case isCIDRLine(s):
			cidr++
		case isDomainLike(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(s, "+."), "*."), ".")):
			dom++
		}
	}
	best, count := Format(""), 0
	// The order is the tie-break: the more specific shape wins.
	for _, c := range []struct {
		f Format
		n int
	}{
		{FormatClassical, classical},
		{FormatAdGuard, adguard},
		{FormatHosts, hosts},
		{FormatCIDRList, cidr},
		{FormatDomainList, dom},
	} {
		if c.n > count {
			best, count = c.f, c.n
		}
	}
	return best
}

func isComment(s string) bool {
	return strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";") || strings.HasPrefix(s, "//") ||
		strings.HasPrefix(s, "!") || strings.HasPrefix(s, "[")
}

func isHostsAddr(s string) bool {
	f := strings.Fields(s)
	if len(f) < 2 {
		return false
	}
	_, err := netip.ParseAddr(f[0])
	return err == nil
}

func isCIDRLine(s string) bool {
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// isDomainLike reports whether s can be a host name: dot separated labels of
// letters, digits, hyphens and underscores (non-ASCII letters allowed).
func isDomainLike(s string) bool {
	if s == "" || len(s) > 253 || strings.ContainsAny(s, " \t,/:@?&=#!^|()[]{}\"'\\") {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, "..") {
		return false
	}
	labels := strings.Split(strings.TrimSuffix(s, "."), ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
	}
	return true
}
