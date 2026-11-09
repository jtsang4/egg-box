package dnsspec_test

import (
	"testing"

	"eggbox/internal/dnsspec"
)

func TestParse(t *testing.T) {
	for spec, want := range map[string]dnsspec.Server{
		"10.0.0.53":                      {Scheme: "udp", Host: "10.0.0.53", Port: 53},
		" udp://10.0.0.53:5353 ":         {Scheme: "udp", Host: "10.0.0.53", Port: 5353},
		"TCP://10.0.0.53":                {Scheme: "tcp", Host: "10.0.0.53", Port: 53},
		"tls://dns.example":              {Scheme: "tls", Host: "dns.example", Port: 853, Hostname: true},
		"tls://DNS.example":              {Scheme: "tls", Host: "dns.example", Port: 853, Hostname: true},
		"https://dns.example/dns-query":  {Scheme: "https", Host: "dns.example", Port: 443, Hostname: true},
		"https://dns.example/custom":     {Scheme: "https", Host: "dns.example", Port: 443, Path: "/custom", Hostname: true},
		"https://[2606:4700::1111]:8443": {Scheme: "https", Host: "2606:4700::1111", Port: 8443},
		"h3://dns.example":               {Scheme: "h3", Host: "dns.example", Port: 443, Hostname: true},
		"system":                         {System: true},
		"Local":                          {System: true},
	} {
		got, err := dnsspec.Parse(spec)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", spec, got, err, want)
		}
	}
	for _, bad := range []string{"", "gopher://x", "1.1.1.1:0", "1.1.1.1:99999", "udp://x/path", "a,b", `"x"`, "a b", "udp://@x", "[::1", "x:y",
		"tls://dns.exa mple"} {
		if _, err := dnsspec.Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

func TestNormalizeResolver(t *testing.T) {
	for in, want := range map[string]string{
		"":                                "",
		"  ":                              "",
		"EXIT":                            "exit",
		"System":                          "system",
		"local":                           "system",
		" 10.0.0.53 ":                     "10.0.0.53",
		"tls://dns.example":               "tls://dns.example",
		"tls://DNS.Example":               "tls://dns.example",
		"2001:DB8::1":                     "2001:db8::1",
		"udp://10.0.0.53:53":              "10.0.0.53",
		"UDP://10.0.0.53:5353":            "udp://10.0.0.53:5353",
		"TLS://dns.example":               "tls://dns.example",
		"tls://dns.example:853":           "tls://dns.example",
		"https://dns.example/dns-query":   "https://dns.example",
		"https://dns.example:8443/q":      "https://dns.example:8443/q",
		"2001:db8::53":                    "2001:db8::53",
		"tcp://[2001:db8::53]":            "tcp://[2001:db8::53]",
		"https://[2606:4700::1111]:8443/": "https://[2606:4700::1111]:8443",
	} {
		if got, err := dnsspec.NormalizeResolver(in); err != nil || got != want {
			t.Errorf("NormalizeResolver(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := dnsspec.NormalizeResolver("exit,now"); err == nil {
		t.Error("a resolver with a comma was accepted")
	}
}

// What String writes, Parse reads back to the same server.
func TestStringRoundTrip(t *testing.T) {
	for _, spec := range []string{"10.0.0.53", "udp://1.1.1.1:5353", "tcp://8.8.8.8", "tls://dot.pub", "https://dns.example/custom",
		"https://[2606:4700::1111]:8443", "h3://dns.example", "quic://dns.example:784", "2001:db8::53", "system"} {
		s, err := dnsspec.Parse(spec)
		if err != nil {
			t.Fatal(err)
		}
		back, err := dnsspec.Parse(s.String())
		if err != nil || back != s {
			t.Errorf("%s -> %s -> %+v, %v", spec, s.String(), back, err)
		}
	}
}

// The settings take any host a server can have; a rule's resolver is held to
// host names that look like one, so that a slip of the pen is caught.
func TestRuleHosts(t *testing.T) {
	for _, ok := range []string{"localhost", "https://localhost:8443/dns-query", "tls://dnsserver", "router"} {
		if _, err := dnsspec.Parse(ok); err != nil {
			t.Errorf("Parse(%q): %v", ok, err)
		}
	}
	for _, ok := range []string{"127.0.0.1", "tls://[::1]:853", "tls://dns.corp.example", "10.0.0.53"} {
		if _, err := dnsspec.NormalizeResolver(ok); err != nil {
			t.Errorf("NormalizeResolver(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"exti", "sytem", "udp://exit", "udp://local", "10.0.0.256", "tls://dns..example", "tls://-dns.example", "tls://dns.ex!ample", "tls://router", "localhost"} {
		if _, err := dnsspec.NormalizeResolver(bad); err == nil {
			t.Errorf("NormalizeResolver(%q) accepted", bad)
		}
	}
}

// The settings keep taking a DoH address with whatever its path holds; only a
// rule, written between commas, cannot hold one.
func TestSettingsServersKeepTheirPaths(t *testing.T) {
	if _, err := dnsspec.Parse("https://dns.example/dns-query?a=1,2"); err != nil {
		t.Fatal(err)
	}
	if _, err := dnsspec.NormalizeResolver("https://dns.example/q?a=1,2"); err == nil {
		t.Fatal("a rule's resolver with a comma was accepted")
	}
}
