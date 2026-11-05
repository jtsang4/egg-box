package redact

import (
	"strings"
	"sync"
	"testing"
)

func TestPublicAddressesAreReplacedConsistently(t *testing.T) {
	r := New()
	got := r.String("connect to 203.0.113.9:443, retry 203.0.113.9, fallback 198.51.100.7")
	want := "connect to ip-1:443, retry ip-1, fallback ip-2"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// A second report starts numbering again: placeholders carry no information.
	if got := New().String("198.51.100.7"); got != "ip-1" {
		t.Fatalf("got %q", got)
	}
}

func TestHarmlessAddressesAreKept(t *testing.T) {
	r := New()
	for _, in := range []string{
		"tun 172.19.0.1/30 dns 172.19.0.2",
		"lan 192.168.1.20 gw 192.168.1.1 and 10.0.0.2",
		"loopback 127.0.0.1:7890 and ::1",
		"link-local 169.254.10.1 fe80::1%en0",
		"ula fd12:3456:789a::1",
		"tailscale 100.101.102.103",
		"any 0.0.0.0:53 and ::",
	} {
		if got := r.String(in); got != in {
			t.Errorf("%q was changed to %q", in, got)
		}
	}
}

func TestIPv6PublicAddresses(t *testing.T) {
	r := New()
	got := r.String("dial [2001:db8::1]:443 failed, then 2001:db8:0:0:0:0:0:1 and 2606:4700::1111")
	if strings.Contains(got, "2001:db8") || strings.Contains(got, "2606:4700") {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(got, "[ip-1]:443") || !strings.Contains(got, "then ip-1 and ip-2") {
		t.Fatalf("equal addresses must share a placeholder regardless of spelling: %q", got)
	}
}

func TestClockTimesAndMACsAreNotAddresses(t *testing.T) {
	r := New()
	for _, in := range []string{"at 12:34:56.789 something", "mac aa:bb:cc:dd:ee:ff", "ratio 1:2:3"} {
		if got := r.String(in); got != in {
			t.Errorf("%q was changed to %q", in, got)
		}
	}
}

func TestHostNames(t *testing.T) {
	r := New()
	got := r.String("dial tcp: lookup hk1.example.net: no such host; also HK1.Example.NET and cdn.example.org")
	want := "dial tcp: lookup host-1: no such host; also host-1 and host-2"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFileNamesAndLocalhostAreNotHostNames(t *testing.T) {
	r := New()
	in := "open /var/lib/agent/cache.db and config.json, rules cn.srs, localhost, sing-box.exe, ServiceBridge.dylib"
	if got := r.String(in); got != in {
		t.Fatalf("got %q", got)
	}
}

func TestKeptIdentifiersSurvive(t *testing.T) {
	r := New("org.eggbox.app")
	in := "service org.eggbox.app.agent registered, pipe org.eggbox.app.agent"
	if got := r.String(in); got != in {
		t.Fatalf("got %q", got)
	}
	if got := r.String("other.vendor.thing"); got != "host-1" {
		t.Fatalf("got %q", got)
	}
}

func TestCredentials(t *testing.T) {
	cases := map[string]string{
		"proxy http://alice:s3cret@proxy.example.com:8080/x":     "proxy http://" + Placeholder + "@host-1:8080/x",
		"GET https://sub.example.com/c?token=abcdef123456&x=1":   "GET https://host-1/c?token=" + Placeholder + "&x=1",
		"Authorization: Bearer abcdefghijklmnop.qrstuvwxyz":      "Authorization: Bearer " + Placeholder,
		"vless id b8f8c3f4-1111-4222-8333-abcdefabcdef accepted": "vless id id-1 accepted",
	}
	for in, want := range cases {
		if got := New().String(in); got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestKeyValuePairsInFreeText(t *testing.T) {
	cases := map[string]string{
		"token=abc123 rejected":                `token=` + Placeholder + ` rejected`,
		"login password: hunter2, retrying":    `login password: ` + Placeholder + `, retrying`,
		`body {"password":"hunter2","a":1}`:    `body {"password":` + Placeholder + `,"a":1}`,
		"tokens: 5 secretary=bob Password = x": "tokens: 5 secretary=bob Password = " + Placeholder,
	}
	for in, want := range cases {
		if got := New().String(in); got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestLongTokensNeedDigitsAndLetters(t *testing.T) {
	r := New()
	secret := "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8"
	if got := r.String("psk=" + secret + " end"); strings.Contains(got, secret) {
		t.Fatalf("got %q", got)
	}
	plain := "supercalifragilisticexpialidocious-and-more-words"
	if got := r.String(plain); got != plain {
		t.Fatalf("plain words were changed: %q", got)
	}
}

func TestAccountNamesInPaths(t *testing.T) {
	r := New()
	cases := map[string]string{
		"/Users/alex/Library/Application Support/x":     "/Users/user/Library/Application Support/x",
		`C:\Users\Alex\AppData\Local\x`:                 `C:\Users\user\AppData\Local\x`,
		`"C:\\Users\\Alex\\AppData"`:                    `"C:\\Users\\user\\AppData"`,
		"/home/alice/.config and /home/bob/.cache":      "/home/user/.config and /home/user/.cache",
		"/var/root/Library is not a user path, kept ok": "/var/root/Library is not a user path, kept ok",
	}
	for in, want := range cases {
		if got := r.String(in); got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestJSONRedactsCredentialKeysAndKeepsStructure(t *testing.T) {
	in := `{
	  "log": {"level": "info"},
	  "outbounds": [
	    {"type": "vless", "tag": "hk", "server": "hk.example.com", "server_port": 443,
	     "uuid": "b8f8c3f4-1111-4222-8333-abcdefabcdef",
	     "tls": {"enabled": true, "server_name": "cdn.example.com", "reality": {"public_key": "AAAA", "short_id": "ab12"}}},
	    {"type": "shadowsocks", "tag": "us", "server": "203.0.113.9", "server_port": 8388, "password": "hunter2"},
	    {"type": "direct", "tag": "direct"}
	  ],
	  "route": {"final": "direct", "rule_set": [{"tag": "cn", "path": "/Users/alex/x/cn.srs"}]}
	}`
	out, err := New().JSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, leak := range []string{"hk.example.com", "cdn.example.com", "203.0.113.9", "hunter2", "b8f8c3f4", "AAAA", "ab12", "alex"} {
		if strings.Contains(s, leak) {
			t.Errorf("%q leaked:\n%s", leak, s)
		}
	}
	for _, keep := range []string{`"type": "vless"`, `"server_port": 443`, `"enabled": true`, `"final": "direct"`, `"tag": "hk"`, "/Users/user/x/cn.srs"} {
		if !strings.Contains(s, keep) {
			t.Errorf("%q was lost:\n%s", keep, s)
		}
	}
	// The same host in two places shares one placeholder.
	if strings.Count(s, "host-1") != 1 || !strings.Contains(s, "host-2") || !strings.Contains(s, "ip-1") {
		t.Errorf("placeholders:\n%s", s)
	}
}

func TestJSONRejectsInvalidInput(t *testing.T) {
	if _, err := New().JSON([]byte("{not json")); err == nil {
		t.Fatal("invalid JSON must be an error, not silently passed through")
	}
}

func TestRedactorIsSafeForConcurrentUse(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = r.String("a.example.com 203.0.113.9 b8f8c3f4-1111-4222-8333-abcdefabcdef")
			}
		}()
	}
	wg.Wait()
	if got := r.String("203.0.113.9"); got != "ip-1" {
		t.Fatalf("got %q", got)
	}
}
