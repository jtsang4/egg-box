package subscription

import (
	"strconv"
	"strings"
)

// shadowTLSSpec is the ShadowTLS plugin of a Shadowsocks server, as Clash
// plugin-opts or the plugin part of an ss:// link give it.
type shadowTLSSpec struct {
	Host        string // the TLS server name ShadowTLS borrows
	Password    string
	Version     string
	Fingerprint string
	Insecure    bool
	ALPN        []string
}

// over puts n, a Shadowsocks node, on top of ShadowTLS: the ShadowTLS outbound
// reaches the node's server, and the Shadowsocks one goes through it.
func (s shadowTLSSpec) over(n Node) (Node, error) {
	version := 2 // what Clash clients assume
	if v := strings.TrimSpace(s.Version); v != "" {
		var err error
		if version, err = strconv.Atoi(v); err != nil || version < 1 || version > 3 {
			return Node{}, skip(SkipUnsupportedFeature, "ShadowTLS version %q is not supported", v)
		}
	}
	if version > 1 && s.Password == "" {
		return Node{}, skip(SkipInvalid, "the ShadowTLS password is missing")
	}
	host := s.Host
	if host == "" {
		host = "bing.com"
	}
	t := tlsSpec{Enabled: true, ServerName: host, Insecure: s.Insecure, ALPN: s.ALPN, Fingerprint: s.Fingerprint}
	d := map[string]any{"type": "shadowtls", "server": n.Server, "server_port": n.Port, "version": version, "tls": t.object()}
	if version > 1 {
		d["password"] = s.Password
	}
	if _, plugin := n.Config["plugin"]; plugin {
		return Node{}, skip(SkipUnsupportedFeature, "ShadowTLS cannot be combined with another plugin")
	}
	n.Detour = d
	return n, nil
}

// pluginOpts reads "key=value;key=value" plugin options.
func pluginOpts(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, _ := strings.Cut(part, "=")
		if k = strings.TrimSpace(k); k != "" {
			out[strings.ToLower(k)] = strings.TrimSpace(v)
		}
	}
	return out
}
