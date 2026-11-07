package gui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"eggbox/internal/agentapi"
	"eggbox/internal/agentclient"
	"eggbox/internal/app"
	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/ruledata"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
)

// The interface falls back to the English text of an error when it has no
// translation of its code, so a code that is missing would not show as a raw
// key: it would show as English in a Chinese window. This test names every kind
// of failure the service can explain and asks for its text in both languages.
func TestEveryErrorCodeIsTranslated(t *testing.T) {
	errs := []error{
		routing.ErrDuplicateName, routing.ErrNoName, routing.ErrBadEntry, routing.ErrLocked, routing.ErrNotMine,
		routing.ErrTooManyExceptions, routing.ErrExceptionsChanged, routing.ErrNoItem, routing.ErrNoException, routing.ErrNoPlan, routing.ErrNoSource,
		routing.ErrDuplicateSource, routing.ErrLastSource, routing.ErrNotInline, routing.ErrNoEntries, routing.ErrTooManyEntries,
		app.ErrBadTarget, app.ErrLooksLikeSubscription, app.ErrNoNodes, app.ErrAuthorizationDeclined, app.ErrServiceSetup,
		ruleurl.ErrAddress, ruledata.ErrTooLarge, ruledata.StatusError(503), &ruledata.ParseError{Err: rulefmt.ErrUnrecognized},
		rulefmt.ErrEmpty, rulefmt.ErrUnrecognized, rulefmt.ErrMalformed, rulefmt.ErrMRS,
		subscription.ErrEmpty, subscription.ErrUnknownFormat, agentclient.ErrNotReady, library.ErrNotFound,
		context.DeadlineExceeded, context.Canceled, &app.ValidationError{}, errors.New("anything else"),
	}
	for code := range agentapi.ErrorCode_name {
		errs = append(errs, &agentclient.OpError{Code: agentapi.ErrorCode(code)})
	}
	errs = append(errs, &agentclient.OpError{Code: agentapi.ErrorCode_ERROR_CODE_ROUTE_CONFLICT, Details: map[string]string{"kind": "no_free_subnet"}},
		&agentclient.OpError{Code: agentapi.ErrorCode_ERROR_CODE_INVALID_CONFIG, RolledBack: true})
	for _, k := range []subscription.FetchErrorKind{
		subscription.FetchBadURL, subscription.FetchNetwork, subscription.FetchTimeout, subscription.FetchStatus,
		subscription.FetchTooLarge, subscription.FetchTLS, subscription.FetchDNS, subscription.FetchCanceled,
	} {
		errs = append(errs, &subscription.FetchError{Kind: k})
	}

	seen := map[string]bool{}
	for _, err := range errs {
		code := app.Explain(err).Code
		if seen[code] {
			continue
		}
		seen[code] = true
		for lang, cat := range map[string]map[string]string{"en": catalogEN, "zh": catalogZH} {
			if text, ok := cat["err."+code]; !ok || text == "" {
				t.Errorf("[%s] the failure %q (%v) has no text", lang, code, err)
			}
		}
	}
	if len(seen) < 20 {
		t.Fatalf("only %d codes were tried; has Explain changed shape?", len(seen))
	}
}

// A notice whose code has no text shows its raw code; this test finds the codes
// in the sources that raise notices and asks for their texts.
func TestEveryNoticeCodeIsTranslated(t *testing.T) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\.notice\(\s*Level\w+,\s*"([a-z_]+)"`),
		regexp.MustCompile(`\.report\(\s*"([a-z_]+)"`),
		regexp.MustCompile(`tellOnce\(.*?Level\w+,\s*"([a-z_]+)"`),
		regexp.MustCompile(`addNoticeLocked\(\s*"([a-z_]+)"`),
		regexp.MustCompile(`AddNotice\(\s*"([a-z_]+)"`),
	}
	codes := map[string]string{}
	for _, dir := range []string{"../app", "../library", "../plancompile"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, re := range patterns {
				for _, m := range re.FindAllStringSubmatch(string(src), -1) {
					codes[m[1]] = f
				}
			}
		}
	}
	if len(codes) < 20 {
		t.Fatalf("found only %d notice codes (%v); have the notices moved?", len(codes), codes)
	}
	for code, where := range codes {
		for lang, cat := range map[string]map[string]string{"en": catalogEN, "zh": catalogZH} {
			text, ok := cat["notice."+code]
			if _, counts := noticeCounts[code]; counts {
				one, other := cat["notice."+code+".one"], cat["notice."+code+".other"]
				text, ok = one+other, one != "" && other != ""
			}
			if !ok || text == "" {
				t.Errorf("[%s] the notice %q (raised in %s) has no text", lang, code, where)
			}
		}
	}
}

// Every picture an item can wear has a name, for those who do not see it.
func TestEveryItemIconHasAName(t *testing.T) {
	for _, name := range iconChoices {
		for lang, cat := range map[string]map[string]string{"en": catalogEN, "zh": catalogZH} {
			if text, ok := cat["icon."+name]; !ok || text == "" {
				t.Errorf("[%s] the icon %q has no name", lang, name)
			}
		}
	}
}

// Every kind of entry a person can write has its name, hint and example.
func TestEveryEntryKindIsDescribed(t *testing.T) {
	if len(routing.EntryKinds) < 8 {
		t.Fatalf("only %d entry kinds", len(routing.EntryKinds))
	}
	for _, k := range routing.EntryKinds {
		for _, stem := range []string{"entry.", "entry.hint.", "entry.example."} {
			for lang, cat := range map[string]map[string]string{"en": catalogEN, "zh": catalogZH} {
				if text, ok := cat[stem+string(k)]; !ok || text == "" {
					t.Errorf("[%s] the entry kind %q has no %q text", lang, k, stem)
				}
			}
		}
	}
}

// A notice that counts says "1 node" and "2 nodes", in English; Chinese has
// one form.
func TestNoticesAgreeWithTheirCount(t *testing.T) {
	en := newHarness(t, harnessOpt{}).app
	zh := newHarness(t, harnessOpt{lang: "zh"}).app
	cases := []struct {
		code string
		args map[string]string
		en   string
		zh   string
	}{
		{"subscription_added", map[string]string{"name": "A", "nodes": "1"}, "“A” added with 1 node", "已添加“A”，共 1 个节点"},
		{"subscription_added", map[string]string{"name": "A", "nodes": "9"}, "“A” added with 9 nodes", "已添加“A”，共 9 个节点"},
		{"subscriptions_updated", map[string]string{"count": "1"}, "1 subscription updated", "已更新 1 个订阅"},
		{"subscriptions_updated", map[string]string{"count": "3"}, "3 subscriptions updated", "已更新 3 个订阅"},
		{"items_waiting", map[string]string{"count": "1"}, "1 routing item waits for rule files and is skipped for now", "有 1 个分流项还在等规则文件，暂时不生效"},
		{"items_waiting", map[string]string{"count": "4"}, "4 routing items wait for rule files and are skipped for now", "有 4 个分流项还在等规则文件，暂时不生效"},
		{"nodes_dropped", map[string]string{"count": "0"}, "0 nodes were left out", "有 0 个节点被排除"},
		{"connected", nil, "Connected", "已连接"},
	}
	for _, c := range cases {
		n := app.Notice{Code: c.code, Args: c.args}
		if got := en.noticeTitle(n); got != c.en {
			t.Errorf("en %s %v: %q, want %q", c.code, c.args, got, c.en)
		}
		if got := zh.noticeTitle(n); got != c.zh {
			t.Errorf("zh %s %v: %q, want %q", c.code, c.args, got, c.zh)
		}
	}
}

// A node somebody chose that no subscription carries any more is reported
// with what it was chosen for, in the words of the window: a shipped item by
// its name in the interface language, whatever language it was stored in.
func TestAChosenNodeThatWentAwayIsNamedForWhatItServed(t *testing.T) {
	en := newHarness(t, harnessOpt{}).app
	zh := newHarness(t, harnessOpt{lang: "zh"}).app
	cases := []struct {
		code string
		args map[string]string
		en   string
		zh   string
	}{
		{"pin_gone", map[string]string{"node": "Tokyo 01", "for": "AI 服务", "item": "b-ai"},
			"“Tokyo 01”, chosen for “AI services”, is no longer in any subscription", "为“AI 服务”指定的节点“Tokyo 01”已不在任何订阅里"},
		{"pin_gone", map[string]string{"node": "Tokyo 01", "for": "Work", "item": "x-nothing-like-it"},
			"“Tokyo 01”, chosen for “Work”, is no longer in any subscription", "为“Work”指定的节点“Tokyo 01”已不在任何订阅里"},
		{"pin_gone_main", map[string]string{"node": "Tokyo 01"},
			"“Tokyo 01”, your main exit, is no longer in any subscription", "你指定的主出口“Tokyo 01”已不在任何订阅里"},
		{"pin_gone_fallback", map[string]string{"node": "Tokyo 01"},
			"“Tokyo 01”, chosen for everything else, is no longer in any subscription", "你为“其他所有流量”指定的节点“Tokyo 01”已不在任何订阅里"},
	}
	for _, c := range cases {
		n := app.Notice{Code: c.code, Args: c.args, Level: app.LevelWarning}
		if got := en.noticeTitle(n); got != c.en {
			t.Errorf("en %s %v: %q, want %q", c.code, c.args, got, c.en)
		}
		if got := zh.noticeTitle(n); got != c.zh {
			t.Errorf("zh %s %v: %q, want %q", c.code, c.args, got, c.zh)
		}
	}
	// What was passed in is left as it was.
	n := app.Notice{Code: "pin_gone", Args: map[string]string{"node": "N", "for": "AI 服务", "item": "b-ai"}}
	en.noticeTitle(n)
	if n.Args["for"] != "AI 服务" {
		t.Errorf("the notice was rewritten in place: %v", n.Args)
	}
}
