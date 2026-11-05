package ruleurl_test

import (
	"strings"
	"testing"

	"eggbox/internal/ruleurl"
)

func TestCanonical(t *testing.T) {
	const raw = "https://raw.githubusercontent.com/"
	cases := []struct {
		name, in, want, project string
		stripped, converted     bool
		mrs                     bool
	}{
		{name: "already canonical", in: raw + "MetaCubeX/meta-rules-dat/sing/geo/geosite/openai.srs",
			want: raw + "MetaCubeX/meta-rules-dat/sing/geo/geosite/openai.srs", project: "MetaCubeX"},
		{name: "mirror prefix", in: "https://gh-proxy.com/" + raw + "MetaCubeX/meta-rules-dat/sing/geo/geosite/openai.srs",
			want: raw + "MetaCubeX/meta-rules-dat/sing/geo/geosite/openai.srs", project: "MetaCubeX", stripped: true},
		{name: "mirror prefix of a page", in: "https://ghfast.top/https://github.com/o/r/blob/main/rule/x.list",
			want: raw + "o/r/main/rule/x.list", stripped: true, converted: true},
		{name: "two prefixes", in: "https://a.example/https://b.example/" + raw + "o/r/main/x.list",
			want: raw + "o/r/main/x.list", stripped: true},
		{name: "prefix with a query", in: "https://m.example/proxy?url=" + raw + "o/r/main/x.list",
			want: raw + "o/r/main/x.list", stripped: true},
		{name: "encoded address in a query", in: "https://m.example/get?u=https%3A%2F%2Fraw.githubusercontent.com%2Fo%2Fr%2Fmain%2Fx.list",
			want: raw + "o/r/main/x.list", stripped: true},
		{name: "page of a file", in: "https://github.com/blackmatrix7/ios_rule_script/blob/master/rule/Clash/OpenAI/OpenAI.list",
			want: raw + "blackmatrix7/ios_rule_script/master/rule/Clash/OpenAI/OpenAI.list", project: "blackmatrix7", converted: true},
		{name: "raw link of a page", in: "https://github.com/o/r/raw/main/a/b.txt?raw=true#L3",
			want: raw + "o/r/main/a/b.txt", converted: true},
		{name: "refs heads in a page", in: "https://github.com/o/r/blob/refs/heads/main/a.list",
			want: raw + "o/r/main/a.list", converted: true},
		{name: "refs heads in a raw link", in: raw + "o/r/refs/heads/main/a.list",
			want: raw + "o/r/main/a.list"},
		{name: "refs tags", in: raw + "o/r/refs/tags/v1/a.list", want: raw + "o/r/v1/a.list"},
		{name: "jsdelivr with a ref", in: "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/openai.srs",
			want: raw + "MetaCubeX/meta-rules-dat/sing/geo/geosite/openai.srs", project: "MetaCubeX", converted: true},
		{name: "jsdelivr mirror host", in: "https://testingcf.jsdelivr.net/gh/o/r@main/x.list", want: raw + "o/r/main/x.list", converted: true},
		{name: "jsdelivr without a ref stays", in: "https://cdn.jsdelivr.net/gh/o/r/x.list", want: "https://cdn.jsdelivr.net/gh/o/r/x.list"},
		{name: "bang spelled both ways", in: raw + "MetaCubeX/meta-rules-dat/sing/geo/geosite/geolocation-%21cn.srs",
			want: raw + "MetaCubeX/meta-rules-dat/sing/geo/geosite/geolocation-!cn.srs", project: "MetaCubeX"},
		{name: "space is encoded", in: raw + "o/r/main/a%20b.list", want: raw + "o/r/main/a%20b.list"},
		{name: "query is kept", in: "https://rules.example/list.txt?key=abc", want: "https://rules.example/list.txt?key=abc"},
		{name: "host case", in: "HTTPS://Rules.Example/List.txt", want: "https://rules.example/List.txt"},
		{name: "mrs of MetaCubeX", in: raw + "MetaCubeX/meta-rules-dat/meta/geo/geosite/openai.mrs",
			want: raw + "MetaCubeX/meta-rules-dat/sing/geo/geosite/openai.srs", project: "MetaCubeX", converted: true},
		{name: "mrs of MetaCubeX geoip", in: raw + "MetaCubeX/meta-rules-dat/meta/geo/geoip/cn.mrs",
			want: raw + "MetaCubeX/meta-rules-dat/sing/geo/geoip/cn.srs", project: "MetaCubeX", converted: true},
		{name: "mrs of MetaCubeX asn", in: raw + "MetaCubeX/meta-rules-dat/meta/asn/AS13335.mrs",
			want: raw + "MetaCubeX/meta-rules-dat/sing/asn/AS13335.srs", project: "MetaCubeX", converted: true},
		{name: "mrs of DustinWin release", in: "https://github.com/DustinWin/ruleset_geodata/releases/download/mihomo-ruleset/ai.mrs",
			want: "https://github.com/DustinWin/ruleset_geodata/releases/download/sing-box-ruleset/ai.srs", project: "DustinWin", converted: true},
		{name: "mrs of Loyalsoldier", in: raw + "Loyalsoldier/geoip/release/mrs/cn.mrs",
			want: raw + "Loyalsoldier/geoip/release/srs/cn.srs", project: "Loyalsoldier", converted: true},
		{name: "mrs of 217heidai", in: raw + "217heidai/adblockfilters/main/rules/adblockmihomolite.mrs",
			want: raw + "217heidai/adblockfilters/main/rules/adblocksingboxlite.srs", project: "217heidai", converted: true},
		{name: "mrs of AWAvenue", in: raw + "TG-Twilight/AWAvenue-Ads-Rule/main/Filters/AWAvenue-Ads-Rule-Clash-No.Privacy.mrs",
			want: raw + "TG-Twilight/AWAvenue-Ads-Rule/main/Filters/AWAvenue-Ads-Rule-Singbox-No.Privacy.json", project: "AWAvenue", converted: true},
		{name: "mrs nobody maps", in: "https://rules.example/x.mrs", want: "https://rules.example/x.mrs", mrs: true},
		{name: "mrs of a mapped host but another path", in: raw + "MetaCubeX/meta-rules-dat/meta/other/x.mrs",
			want: raw + "MetaCubeX/meta-rules-dat/meta/other/x.mrs", project: "MetaCubeX", mrs: true},
		{name: "SukkaW host", in: "https://ruleset.skk.moe/sing-box/non_ip/ai.json", want: "https://ruleset.skk.moe/sing-box/non_ip/ai.json", project: "SukkaW"},
		{name: "anti-AD host", in: "https://anti-ad.net/anti-ad-sing-box.srs", want: "https://anti-ad.net/anti-ad-sing-box.srs", project: "anti-AD"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ruleurl.Canonical(c.in)
			if err != nil {
				t.Fatalf("Canonical(%q): %v", c.in, err)
			}
			if got.URL != c.want {
				t.Errorf("URL = %q, want %q", got.URL, c.want)
			}
			if got.Project != c.project || got.Stripped != c.stripped || got.Converted != c.converted || got.MRS != c.mrs {
				t.Errorf("info = %+v, want project %q stripped %v converted %v mrs %v", got, c.project, c.stripped, c.converted, c.mrs)
			}
			again, err := ruleurl.Canonical(got.URL)
			if err != nil || again.URL != got.URL {
				t.Errorf("not stable: %q -> %q (%v)", got.URL, again.URL, err)
			}
		})
	}
}

func TestCanonicalRefusesWhatIsNotAnAddress(t *testing.T) {
	for _, in := range []string{
		"", "   ", "raw.githubusercontent.com/o/r/main/x", "ftp://rules.example/x", "file:///etc/hosts",
		"https://", "https://user:pw@rules.example/x", "https://rules.example/a b", "just words", "javascript:alert(1)",
		"https://rules.example/a/../b", "https://rules.example/a/%2e%2e/b", "https://rules.example/a%2Fb", "https://rules.example/a\\b",
	} {
		if got, err := ruleurl.Canonical(in); err == nil {
			t.Errorf("Canonical(%q) = %q, want an error", in, got.URL)
		}
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{
		"https://raw.githubusercontent.com/o/r/main/rule/Clash/OpenAI/OpenAI.list":               "OpenAI",
		"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/geosite/openai.srs": "openai",
		"https://rules.example/lists/some.name.txt":                                              "some.name",
	} {
		got, _ := ruleurl.Canonical(in)
		if got.Name != want {
			t.Errorf("name of %q = %q, want %q", in, got.Name, want)
		}
	}
}

func TestDatasetAddresses(t *testing.T) {
	const base = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/"
	for got, want := range map[string]string{
		ruleurl.GeoSite("openai"):          base + "geo/geosite/openai.srs",
		ruleurl.GeoSite("geolocation-!cn"): base + "geo/geosite/geolocation-!cn.srs",
		ruleurl.GeoIP("CN"):                base + "geo/geoip/cn.srs",
		ruleurl.ASN("13335"):               base + "asn/AS13335.srs",
		ruleurl.ASN("as13335"):             base + "asn/AS13335.srs",
		ruleurl.ASN(" AS13335 "):           base + "asn/AS13335.srs",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		info, err := ruleurl.Canonical(got)
		if err != nil || info.URL != got {
			t.Errorf("a dataset address must already be canonical: %q -> %q (%v)", got, info.URL, err)
		}
	}
	// A hostile name cannot leave the dataset's directory, and the address
	// it makes is not accepted back.
	for hostile, plain := range map[string]string{
		ruleurl.GeoSite("../../x"): ruleurl.GeoSite("x"),
		ruleurl.GeoIP("a/b"):       ruleurl.GeoIP("x"),
		ruleurl.ASN("1/../2"):      ruleurl.ASN("1"),
	} {
		if strings.Contains(hostile, "/../") || strings.Count(hostile, "/") != strings.Count(plain, "/") {
			t.Errorf("path traversal survived: %q", hostile)
		}
		if _, err := ruleurl.Canonical(hostile); err == nil {
			t.Errorf("%q must not be accepted as a file address", hostile)
		}
	}
}

func TestJSDelivr(t *testing.T) {
	got := ruleurl.JSDelivr("https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/geosite/geolocation-!cn.srs")
	if want := "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/geolocation-!cn.srs"; got != want {
		t.Errorf("JSDelivr = %q, want %q", got, want)
	}
	for _, in := range []string{
		"https://anti-ad.net/anti-ad-sing-box.srs",
		"https://github.com/DustinWin/ruleset_geodata/releases/download/sing-box-ruleset/ai.srs",
		"https://raw.githubusercontent.com/o/r/main",
		"https://raw.githubusercontent.com/o/r",
	} {
		if got := ruleurl.JSDelivr(in); got != "" {
			t.Errorf("JSDelivr(%q) = %q, want nothing: a CDN cannot serve it", in, got)
		}
	}
	// The CDN's address maps back to the file it serves.
	back, err := ruleurl.Canonical(ruleurl.JSDelivr("https://raw.githubusercontent.com/o/r/main/a/b.json"))
	if err != nil || back.URL != "https://raw.githubusercontent.com/o/r/main/a/b.json" {
		t.Errorf("round trip = %q (%v)", back.URL, err)
	}
}

func TestPrefixes(t *testing.T) {
	const file = "https://raw.githubusercontent.com/o/r/main/x.list"
	for _, prefix := range []string{"https://gh-proxy.com/", "https://gh-proxy.com", "http://mirror.example/path/", "https://m.example/proxy?url="} {
		if err := ruleurl.ValidPrefix(prefix); err != nil {
			t.Errorf("ValidPrefix(%q) = %v", prefix, err)
		}
		through := ruleurl.WithPrefix(prefix, file)
		if !strings.HasSuffix(through, file) || through == file {
			t.Errorf("WithPrefix(%q) = %q", prefix, through)
		}
		back, err := ruleurl.Canonical(through)
		if err != nil || back.URL != file || !back.Stripped {
			t.Errorf("a prefixed address must map back to the file: %q -> %q (%v)", through, back.URL, err)
		}
	}
	for _, bad := range []string{"mirror.example", "ftp://m.example/", "https://", "https://u:p@m.example/", "https://m.example/#x"} {
		if ruleurl.ValidPrefix(bad) == nil {
			t.Errorf("ValidPrefix(%q) accepted", bad)
		}
		if got := ruleurl.WithPrefix(bad, file); got != "" {
			t.Errorf("WithPrefix(%q) = %q, want nothing", bad, got)
		}
	}
	if ruleurl.ValidPrefix("  ") != nil || ruleurl.WithPrefix("", file) != "" {
		t.Error("an empty prefix means none")
	}
}

func TestDisplayHidesWhatIsPrivate(t *testing.T) {
	got := ruleurl.Display("https://rules.example/path/list.txt?token=secret-value&x=1")
	if got != "https://rules.example/path/list.txt" {
		t.Errorf("Display = %q", got)
	}
	if ruleurl.Display("not a url") != "?" {
		t.Error("an unreadable address shows as ?")
	}
}

func FuzzCanonical(f *testing.F) {
	for _, s := range []string{
		"https://gh-proxy.com/https://github.com/o/r/blob/main/x.list",
		"https://cdn.jsdelivr.net/gh/o/r@main/x.srs",
		"https://raw.githubusercontent.com/o/r/refs/heads/main/%21x.mrs",
		"https://a/https://b/https%3A%2F%2Fc/d",
		"http://x/%zz", "https://[::1]/x", "https://x/\x00",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		info, err := ruleurl.Canonical(in)
		if err != nil {
			return
		}
		again, err := ruleurl.Canonical(info.URL)
		if err != nil {
			t.Fatalf("canonical form is not an address: %q -> %q: %v", in, info.URL, err)
		}
		if again.URL != info.URL {
			t.Fatalf("not stable: %q -> %q -> %q", in, info.URL, again.URL)
		}
		_ = ruleurl.Display(info.URL)
		_ = ruleurl.JSDelivr(info.URL)
	})
}
