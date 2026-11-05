package gui

import "testing"

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{
		"/Applications/Safari.app/Contents/MacOS/Safari": "Safari",
		"/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper": "Google Chrome",
		"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper":                                                        "Visual Studio Code",
		"/usr/bin/curl":                    "curl",
		`C:\Program Files\Slack\slack.exe`: "slack",
		`C:\Windows\System32\SVCHOST.EXE`:  "SVCHOST",
		"node":                             "node",
		"/":                                "/",
		"":                                 "",
	} {
		if got := appName(in); got != want {
			t.Errorf("appName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchedRule(t *testing.T) {
	en, zh := &App{lang: "en"}, &App{lang: "zh"}
	for in, want := range map[string]string{
		"":                                     "",
		"final":                                "Default route",
		"final => route(Proxy)":                "Default route",
		"rule_set=geosite-cn => route(direct)": "Rule set  ·  Mainland China sites",
		"rule_set=[geosite-cn geoip-cn] => route(direct)":            "Rule set  ·  Mainland China sites, Mainland China addresses",
		"rule_set=my-own-list => route(Proxy)":                       "Rule set  ·  my-own-list",
		"domain_suffix=intranet.example => route(direct)":            "Domain suffix  ·  intranet.example",
		"domain_keyword=[ads tracker] => reject":                     "Keyword  ·  ads, tracker",
		"protocol=dns => hijack-dns":                                 "Protocol  ·  dns",
		"clash_mode=Global => route(Proxy)":                          "Mode  ·  Global",
		"process_name=Slack domain_suffix=slack.com => route(Proxy)": "App  ·  Slack",
		"!(domain=example.com) => reject":                            "Rule",
		"unheard_of=1 => reject":                                     "Rule  ·  1",
	} {
		if got := en.matchedRule(in); got != want {
			t.Errorf("matchedRule(%q) = %q, want %q", in, got, want)
		}
	}
	if got := zh.matchedRule("final"); got != "默认路由" {
		t.Errorf("zh final = %q", got)
	}
	if got := zh.matchedRule("rule_set=geosite-cn => route(direct)"); got != "规则集  ·  中国大陆网站" {
		t.Errorf("zh rule set = %q", got)
	}
}
