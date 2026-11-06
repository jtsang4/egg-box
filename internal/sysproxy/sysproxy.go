// Package sysproxy judges the system proxy settings that sit beside the
// tunnel. Many programs (browsers, Electron applications, input methods) send
// their traffic to the system proxy before it reaches any tunnel, so a proxy
// that another program left behind - pointing at a local port nothing listens
// on any more - cuts them off while everything else works.
//
// Nothing here changes a setting: the settings belong to the person and to
// the programs that wrote them. The package reads them (through a platform
// reader), finds the ones that point at a local port, and says which of those
// lead nowhere and which lead to another program.
package sysproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind is the kind of traffic a proxy setting is for.
type Kind string

// The kinds of manual proxy settings.
const (
	KindHTTP  Kind = "http"
	KindHTTPS Kind = "https"
	KindSOCKS Kind = "socks"
)

// Setting is one enabled manual proxy of one network service.
type Setting struct {
	// Service is the network service's name as the system shows it ("Wi-Fi").
	Service string
	// Active is set for the service that carries traffic now.
	Active bool
	Kind   Kind
	Host   string
	Port   int
}

// State is what is found at the end of a local proxy setting.
type State int

const (
	// Dead: nothing listens on the port. Programs that use the system proxy
	// cannot connect at all.
	Dead State = iota + 1
	// Foreign: another program listens on the port. Programs that use the
	// system proxy go through it before they reach the tunnel.
	Foreign
)

// Issue is one local proxy address that the system settings point at and that
// is not this application's own, with every setting that points there.
type Issue struct {
	Host  string
	Port  int
	State State
	// Active is set when the service that carries traffic now points there,
	// that is, when the issue affects programs right now.
	Active   bool
	Settings []Setting
}

// Endpoint is the address as people write it ("127.0.0.1:3067").
func (i Issue) Endpoint() string { return net.JoinHostPort(i.Host, strconv.Itoa(i.Port)) }

// Services are the names of the services that point at the address, the
// active one first, each once.
func (i Issue) Services() []string {
	var out []string
	seen := map[string]bool{}
	add := func(active bool) {
		for _, s := range i.Settings {
			if s.Active == active && !seen[s.Service] {
				seen[s.Service] = true
				out = append(out, s.Service)
			}
		}
	}
	add(true)
	add(false)
	return out
}

// Key identifies the issue across checks.
func (i Issue) Key() string { return fmt.Sprintf("%d|%s", i.State, i.Endpoint()) }

// Listening says whether something accepts connections at host:port.
type Listening func(ctx context.Context, host string, port int) bool

// Analyze groups the settings that point at a local port by address and says
// what is at each. Settings that point elsewhere are left alone: a proxy on
// another machine is somebody's deliberate choice and cannot be checked from
// here. own lists the local ports this application itself listens on; a
// setting that points there is the person's choice to use it.
func Analyze(ctx context.Context, settings []Setting, own []int, listening Listening) []Issue {
	ours := map[int]bool{}
	for _, p := range own {
		ours[p] = true
	}
	byAddr := map[string]*Issue{}
	var order []string
	for _, s := range settings {
		host, ok := loopbackHost(s.Host)
		if !ok || s.Port <= 0 || s.Port > 65535 || ours[s.Port] {
			continue
		}
		key := net.JoinHostPort(host, strconv.Itoa(s.Port))
		is := byAddr[key]
		if is == nil {
			is = &Issue{Host: host, Port: s.Port}
			byAddr[key] = is
			order = append(order, key)
		}
		is.Active = is.Active || s.Active
		is.Settings = append(is.Settings, s)
	}
	out := make([]Issue, 0, len(order))
	for _, key := range order {
		is := byAddr[key]
		is.State = Dead
		if listening(ctx, is.Host, is.Port) {
			is.State = Foreign
		}
		out = append(out, *is)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Active != b.Active {
			return a.Active
		}
		if a.State != b.State {
			return a.State == Dead
		}
		return a.Port < b.Port
	})
	return out
}

// loopbackHost says whether a proxy host is this machine and returns it as it
// is best dialled and shown.
func loopbackHost(h string) (string, bool) {
	h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(h), "["), "]")
	if strings.EqualFold(h, "localhost") {
		return "localhost", true
	}
	a, err := netip.ParseAddr(h)
	if err != nil || !a.Unmap().IsLoopback() {
		return "", false
	}
	return a.Unmap().String(), true
}

// Probe is the Listening of the real machine: a connection attempt to the
// local port, which a closed port refuses at once.
func Probe(ctx context.Context, host string, port int) bool {
	hosts := []string{host}
	if host == "localhost" {
		hosts = []string{"127.0.0.1", "::1"}
	}
	d := net.Dialer{Timeout: 500 * time.Millisecond}
	for _, h := range hosts {
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(h, strconv.Itoa(port)))
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- macOS

// darwinReport is what the macOS bridge reports: the enabled services of the
// current network set, with their proxy settings as the system stores them,
// and the service that carries traffic now.
type darwinReport struct {
	Primary  string `json:"primary"`
	Services []struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		Proxies map[string]any `json:"proxies"`
	} `json:"services"`
}

// darwinKeys name, for each kind, the keys of the system's proxy dictionary.
var darwinKeys = []struct {
	kind                  Kind
	enable, host, portKey string
}{
	{KindHTTP, "HTTPEnable", "HTTPProxy", "HTTPPort"},
	{KindHTTPS, "HTTPSEnable", "HTTPSProxy", "HTTPSPort"},
	{KindSOCKS, "SOCKSEnable", "SOCKSProxy", "SOCKSPort"},
}

// ParseDarwin reads the bridge's report into the enabled manual proxies.
func ParseDarwin(data []byte) ([]Setting, error) {
	var r darwinReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("read the system proxy settings: %w", err)
	}
	var out []Setting
	for _, svc := range r.Services {
		for _, k := range darwinKeys {
			if number(svc.Proxies[k.enable]) != 1 {
				continue
			}
			host, _ := svc.Proxies[k.host].(string)
			port := number(svc.Proxies[k.portKey])
			if strings.TrimSpace(host) == "" || port <= 0 {
				continue
			}
			out = append(out, Setting{Service: svc.Name, Active: r.Primary != "" && svc.ID == r.Primary, Kind: k.kind, Host: host, Port: port})
		}
	}
	return out, nil
}

func number(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	case bool:
		if n {
			return 1
		}
	}
	return 0
}

// darwinSwitches are the networksetup subcommands that turn each kind off.
var darwinSwitches = map[Kind]string{
	KindHTTP:  "-setwebproxystate",
	KindHTTPS: "-setsecurewebproxystate",
	KindSOCKS: "-setsocksfirewallproxystate",
}

// DarwinCommands are the Terminal commands that switch off the settings of
// the issues, one per line. They switch off only: the address stays filled in,
// so the program that wrote it, or the person, can switch it on again.
func DarwinCommands(issues []Issue) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, is := range issues {
		for _, s := range is.Settings {
			line := "networksetup " + darwinSwitches[s.Kind] + " " + shellQuote(s.Service) + " off"
			if darwinSwitches[s.Kind] == "" || seen[line] {
				continue
			}
			seen[line] = true
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// shellQuote quotes a word for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
