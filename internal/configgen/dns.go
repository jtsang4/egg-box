package configgen

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// dnsServer is a DNS upstream parsed from a user-facing spec.
type dnsServer struct {
	obj      map[string]any // sing-box server object without its tag
	hostname bool           // the server is named by a host name, so it needs a bootstrap resolver
}

// dnsServerFor parses "1.1.1.1", "udp://1.1.1.1:5353", "tcp://...", "tls://dns.example",
// "https://dns.example/dns-query", "quic://...", "h3://...", "local".
func dnsServerFor(spec string) (dnsServer, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return dnsServer{}, errors.New("empty")
	}
	if strings.EqualFold(spec, "local") || strings.EqualFold(spec, "system") {
		return dnsServer{obj: map[string]any{"type": "local"}}, nil
	}
	scheme := "udp"
	rest := spec
	if i := strings.Index(spec, "://"); i > 0 {
		scheme, rest = strings.ToLower(spec[:i]), spec[i+3:]
	}
	defaultPort := map[string]int{"udp": 53, "tcp": 53, "tls": 853, "https": 443, "quic": 853, "h3": 443}
	dp, ok := defaultPort[scheme]
	if !ok {
		return dnsServer{}, fmt.Errorf("unsupported scheme %q", scheme)
	}
	hostport, path, _ := strings.Cut(rest, "/")
	host, port := hostport, dp
	if strings.HasPrefix(hostport, "[") {
		j := strings.Index(hostport, "]")
		if j < 0 {
			return dnsServer{}, errors.New("malformed IPv6 address")
		}
		host = hostport[1:j]
		if tail := strings.TrimPrefix(hostport[j+1:], ":"); tail != "" {
			p, err := strconv.Atoi(tail)
			if err != nil {
				return dnsServer{}, errors.New("malformed port")
			}
			port = p
		}
	} else if h, p, found := strings.Cut(hostport, ":"); found && !strings.Contains(p, ":") {
		host = h
		n, err := strconv.Atoi(p)
		if err != nil {
			return dnsServer{}, errors.New("malformed port")
		}
		port = n
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " \t/\\@") {
		return dnsServer{}, errors.New("malformed host")
	}
	if port < 1 || port > 65535 {
		return dnsServer{}, errors.New("port out of range")
	}
	obj := map[string]any{"type": scheme, "server": host}
	if port != dp {
		obj["server_port"] = port
	}
	if scheme == "https" || scheme == "h3" {
		if path != "" && "/"+path != "/dns-query" {
			obj["path"] = "/" + path
		}
	} else if path != "" {
		return dnsServer{}, errors.New("unexpected path")
	}
	_, err := netip.ParseAddr(host)
	return dnsServer{obj: obj, hostname: err != nil}, nil
}

// needsQUIC reports whether the server type is only available in builds with QUIC.
func (d dnsServer) needsQUIC() bool {
	t, _ := d.obj["type"].(string)
	return t == "quic" || t == "h3"
}

func compileRegex(s string) (*regexp.Regexp, error) { return regexp.Compile(s) }

func validCIDR(s string) bool {
	if strings.Contains(s, "/") {
		_, err := netip.ParsePrefix(s)
		return err == nil
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

func validPort(s string) bool {
	lo, hi, ranged := strings.Cut(s, ":")
	if !ranged {
		lo, hi, ranged = strings.Cut(s, "-")
	}
	a, err := strconv.Atoi(lo)
	if err != nil || a < 1 || a > 65535 {
		return false
	}
	if !ranged {
		return true
	}
	b, err := strconv.Atoi(hi)
	return err == nil && b >= a && b <= 65535
}
