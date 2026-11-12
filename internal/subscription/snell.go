package subscription

import (
	"strconv"
	"strings"
)

// snellSpec is a Snell server as a share link or a Clash entry describes it.
type snellSpec struct {
	Server   string
	Port     int
	PSK      string
	Version  string
	ObfsMode string // version 4 only: none, http, tls
	ObfsHost string
	Mode     string // version 6 only: default, unshaped, unsafe-raw
	Reuse    bool
}

// node builds the sing-box snell outbound. sing-box speaks the version 4 wire
// protocol (which version 5 shares, minus the QUIC proxy it leaves out) and
// version 6; older versions cannot connect.
func (s snellSpec) node(name string) (Node, error) {
	cfg := map[string]any{"type": "snell", "server": s.Server, "server_port": s.Port, "psk": s.PSK}
	v := strings.TrimSpace(s.Version)
	if v == "" {
		return Node{}, skip(SkipInvalid, "the Snell version is missing")
	}
	version, err := strconv.Atoi(v)
	if err != nil {
		return Node{}, skip(SkipInvalid, "Snell version %q is not a number", v)
	}
	obfs := strings.ToLower(strings.TrimSpace(s.ObfsMode))
	switch version {
	case 4, 5:
		cfg["version"] = 4
		switch obfs {
		case "", "none", "off":
		case "http", "tls":
			cfg["obfs_mode"] = obfs
			setIf(cfg, "obfs_host", s.ObfsHost)
		default:
			return Node{}, skip(SkipUnsupportedFeature, "Snell obfs %q is not supported", obfs)
		}
	case 6:
		cfg["version"] = 6
		if obfs != "" && obfs != "none" && obfs != "off" {
			return Node{}, skip(SkipUnsupportedFeature, "Snell v6 has no obfs")
		}
		if n := len(s.PSK); n < 12 || n > 255 {
			return Node{}, skip(SkipInvalid, "a Snell v6 PSK must be 12 to 255 bytes")
		}
		switch mode := strings.ToLower(strings.TrimSpace(s.Mode)); mode {
		case "", "default":
		case "unshaped", "unsafe-raw":
			cfg["mode"] = mode
		default:
			return Node{}, skip(SkipUnsupportedFeature, "Snell v6 mode %q is not supported", mode)
		}
	default:
		return Node{}, skip(SkipUnsupportedFeature, "Snell version %d is not supported", version)
	}
	if s.Reuse {
		cfg["reuse"] = true
	}
	return Node{Name: name, Type: "snell", Server: s.Server, Port: s.Port, Config: cfg}, nil
}

// parseSnell reads snell://psk@host:port?version=6&mode=default&reuse=true#name
// (version 4 links carry obfs= and obfs-host= instead of mode=).
func parseSnell(raw string) (Node, error) {
	l, ok := splitShare(raw)
	if !ok {
		return Node{}, skip(SkipInvalid, "not a valid link")
	}
	if err := hostPort(l); err != nil {
		return Node{}, err
	}
	psk := unescape(l.UserInfo)
	if psk == "" {
		psk = l.param("psk")
	}
	return snellSpec{
		Server:   l.Host,
		Port:     l.Port,
		PSK:      psk,
		Version:  l.param("version"),
		ObfsMode: l.param("obfs", "obfs-mode", "obfs_mode"),
		ObfsHost: l.param("obfs-host", "obfs_host"),
		Mode:     l.param("mode"),
		Reuse:    l.flag("reuse"),
	}.node(nodeName(l))
}
