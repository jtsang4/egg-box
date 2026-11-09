package configgen

import (
	"eggbox/internal/dnsspec"
)

// dnsServer is a DNS upstream parsed from a user-facing spec.
type dnsServer struct {
	obj      map[string]any // sing-box server object without its tag
	hostname bool           // the server is named by a host name, so it needs a bootstrap resolver
	system   bool           // the system's resolver, which is asked on this machine only
}

// dnsServerFor turns a server as dnsspec reads it into a sing-box server.
func dnsServerFor(spec string) (dnsServer, error) {
	s, err := dnsspec.Parse(spec)
	if err != nil {
		return dnsServer{}, err
	}
	if s.System {
		return dnsServer{obj: map[string]any{"type": "local"}, system: true}, nil
	}
	obj := map[string]any{"type": s.Scheme, "server": s.Host}
	if s.Port != dnsspec.DefaultPort(s.Scheme) {
		obj["server_port"] = s.Port
	}
	if s.Path != "" {
		obj["path"] = s.Path
	}
	return dnsServer{obj: obj, hostname: s.Hostname}, nil
}
