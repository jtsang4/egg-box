// Package dnsspec reads DNS servers the way a person writes them: "1.1.1.1",
// "udp://1.1.1.1:5353", "tcp://...", "tls://dns.example",
// "https://dns.example/dns-query", "quic://...", "h3://..." or "system" (the
// resolver the system uses now). It also reads the resolver a rule may name
// ("dns=..." in the text of the exceptions, the resolver of an item), which is
// one of those servers or "exit": the rule's exit resolves the names itself.
//
// Both the routing model, which checks what a person wrote, and configgen,
// which writes the servers into the configuration, read servers here, so the
// two cannot disagree on what is a server.
package dnsspec

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Server is a DNS server as written.
type Server struct {
	// System is the resolver the system uses ("system", or "local").
	System bool
	// Scheme is udp, tcp, tls, https, quic or h3.
	Scheme string
	Host   string
	// Port is the port as written; DefaultPort when none was.
	Port int
	// Path is the request path of an https or h3 server, "" for the usual
	// "/dns-query".
	Path string
	// Hostname reports that Host is a name, which must itself be looked up
	// before the server can be asked anything.
	Hostname bool
}

// DefaultPort is the port a server of the scheme listens on unless told otherwise.
func DefaultPort(scheme string) int {
	return defaultPort[scheme]
}

var defaultPort = map[string]int{"udp": 53, "tcp": 53, "tls": 853, "https": 443, "quic": 853, "h3": 443}

// Parse reads a server.
func Parse(spec string) (Server, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Server{}, errors.New("empty")
	}
	if strings.EqualFold(spec, "local") || strings.EqualFold(spec, System) {
		return Server{System: true}, nil
	}
	scheme := "udp"
	rest := spec
	if i := strings.Index(spec, "://"); i > 0 {
		scheme, rest = strings.ToLower(spec[:i]), spec[i+3:]
	}
	dp, ok := defaultPort[scheme]
	if !ok {
		return Server{}, fmt.Errorf("unsupported scheme %q", scheme)
	}
	hostport, path, _ := strings.Cut(rest, "/")
	host, port := hostport, dp
	if strings.HasPrefix(hostport, "[") {
		j := strings.Index(hostport, "]")
		if j < 0 {
			return Server{}, errors.New("malformed IPv6 address")
		}
		host = hostport[1:j]
		if tail := strings.TrimPrefix(hostport[j+1:], ":"); tail != "" {
			p, err := strconv.Atoi(tail)
			if err != nil {
				return Server{}, errors.New("malformed port")
			}
			port = p
		}
	} else if h, p, found := strings.Cut(hostport, ":"); found && !strings.Contains(p, ":") {
		host = h
		n, err := strconv.Atoi(p)
		if err != nil {
			return Server{}, errors.New("malformed port")
		}
		port = n
	}
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " \t/\\@,\"") {
		return Server{}, errors.New("malformed host")
	}
	if port < 1 || port > 65535 {
		return Server{}, errors.New("port out of range")
	}
	s := Server{Scheme: scheme, Port: port}
	if scheme == "https" || scheme == "h3" {
		if path != "" && "/"+path != "/dns-query" {
			s.Path = "/" + path
		}
	} else if path != "" {
		return Server{}, errors.New("unexpected path")
	}
	// One way to write the host: a name in lower case, an address as it is
	// usually written.
	if a, err := netip.ParseAddr(host); err == nil {
		s.Host = a.String()
	} else {
		s.Host, s.Hostname = strings.ToLower(host), true
	}
	return s, nil
}

// String writes the server in one way of all those that say the same:
// "system"; the address alone for plain DNS on port 53; otherwise
// scheme://host, with the port when it is not the scheme's and the path when
// it is not the usual one. Parse reads it back to the same server.
func (s Server) String() string {
	if s.System {
		return System
	}
	if s.Scheme == "udp" && s.Port == defaultPort["udp"] {
		return s.Host
	}
	host := s.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	out := s.Scheme + "://" + host
	if s.Port != defaultPort[s.Scheme] {
		out += ":" + strconv.Itoa(s.Port)
	}
	return out + s.Path
}

// ruleHost reports whether a name can be the server a rule names: labels of
// letters, digits and hyphens with a dot between them, not all of digits (that
// is an address written wrong). In a rule a single word is a slip of the pen
// ("exti", "sytem") more often than a server, and this machine is 127.0.0.1:
// a name would have to be looked up before the server could be asked.
func ruleHost(h string) bool {
	h = strings.TrimSuffix(h, ".")
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	digits := true
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, r := range l {
			switch {
			case r >= '0' && r <= '9':
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
				digits = false
			default:
				return false
			}
		}
	}
	return !digits
}

// The resolvers a rule can name besides a server.
const (
	// Exit leaves the names to the rule's exit: they are answered with
	// addresses that stand for them, and the exit is handed the name.
	Exit = "exit"
	// System is the resolver the system uses now.
	System = "system"
)

// NormalizeResolver checks the resolver a rule names and returns it in the
// form it is kept in: "" (the default), Exit, System, or the server as
// Server.String writes it, so that two ways of writing the same server are the
// same resolver.
func NormalizeResolver(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", nil
	case strings.EqualFold(s, Exit):
		return Exit, nil
	case strings.EqualFold(s, System), strings.EqualFold(s, "local"):
		return System, nil
	}
	// A rule is a line of text with commas between its fields.
	if strings.ContainsAny(s, ",\" \t") {
		return "", errors.New("a server is written without spaces, commas or quotes")
	}
	srv, err := Parse(s)
	if err != nil {
		return "", err
	}
	if srv.Hostname && !ruleHost(srv.Host) {
		return "", fmt.Errorf("%q is neither an address nor a host name", srv.Host)
	}
	return srv.String(), nil
}
