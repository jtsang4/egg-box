package routing_test

import (
	"reflect"
	"testing"

	"eggbox/internal/routing"
)

func TestParseBatchRecognisesPlainEntries(t *testing.T) {
	text := `
# a comment
example.com
+.cdn.example.net
*.wild.example.org
.dot.example.org
https://www.site.example/some/page?x=1
10.1.2.0/24
192.0.2.7
2001:db8::/32
8443
8000-9000
Slack
chrome.exe
/Applications/Foo.app/Contents/MacOS/Foo
C:\Program Files\Foo\foo.exe
`
	res := routing.ParseBatch(text)
	if len(res.Unrecognized) != 0 {
		t.Fatalf("unrecognized: %+v", res.Unrecognized)
	}
	want := []routing.Entry{
		{Kind: routing.EntryDomainSuffix, Value: "example.com"},
		{Kind: routing.EntryDomainSuffix, Value: "cdn.example.net"},
		{Kind: routing.EntryDomainSuffix, Value: "wild.example.org"},
		{Kind: routing.EntryDomainSuffix, Value: "dot.example.org"},
		{Kind: routing.EntryDomainSuffix, Value: "www.site.example"},
		{Kind: routing.EntryIPCIDR, Value: "10.1.2.0/24"},
		{Kind: routing.EntryIPCIDR, Value: "192.0.2.7/32"},
		{Kind: routing.EntryIPCIDR, Value: "2001:db8::/32"},
		{Kind: routing.EntryPort, Value: "8443"},
		{Kind: routing.EntryPort, Value: "8000:9000"},
		{Kind: routing.EntryProcessName, Value: "Slack"},
		{Kind: routing.EntryProcessName, Value: "chrome.exe"},
		{Kind: routing.EntryProcessPath, Value: "/Applications/Foo.app/Contents/MacOS/Foo"},
		{Kind: routing.EntryProcessPath, Value: `C:\Program Files\Foo\foo.exe`},
	}
	var got []routing.Entry
	for _, l := range res.Lines {
		got = append(got, l.Entry)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	for i, l := range res.Lines {
		if l.Target != "" {
			t.Errorf("line %d has the target %q, nothing was written", i, l.Target)
		}
	}
}

func TestParseBatchEntriesAndTheirTargets(t *testing.T) {
	text := `payload:
  - 'DOMAIN-SUFFIX,example.com,DIRECT'
  - "DOMAIN,www.example.org,REJECT"
DOMAIN-KEYWORD,tracker,AI Services
IP-CIDR,203.0.113.0/24,DIRECT,no-resolve
IP-CIDR6,2001:db8::/32,Proxy
DST-PORT,443,DIRECT
PROCESS-NAME,Slack,DIRECT
DOMAIN-REGEX,^ads[0-9]+\.example\.com$,REJECT
DOMAIN-SUFFIX,plain.example
`
	res := routing.ParseBatch(text)
	if len(res.Unrecognized) != 0 {
		t.Fatalf("unrecognized: %+v", res.Unrecognized)
	}
	type row struct {
		kind   routing.EntryKind
		value  string
		target string
	}
	var got []row
	for _, l := range res.Lines {
		got = append(got, row{l.Entry.Kind, l.Entry.Value, l.Target})
	}
	want := []row{
		{routing.EntryDomainSuffix, "example.com", "DIRECT"},
		{routing.EntryDomain, "www.example.org", "REJECT"},
		{routing.EntryDomainKeyword, "tracker", "AI Services"},
		{routing.EntryIPCIDR, "203.0.113.0/24", "DIRECT"},
		{routing.EntryIPCIDR, "2001:db8::/32", "Proxy"},
		{routing.EntryPort, "443", "DIRECT"},
		{routing.EntryProcessName, "Slack", "DIRECT"},
		{routing.EntryDomainRegex, `^ads[0-9]+\.example\.com$`, "REJECT"},
		{routing.EntryDomainSuffix, "plain.example", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	if got := res.Targets(); !reflect.DeepEqual(got, []string{"DIRECT", "REJECT", "AI Services", "Proxy"}) {
		t.Fatalf("targets %v", got)
	}
	if res.Counts[routing.EntryDomainSuffix] != 2 || res.Counts[routing.EntryIPCIDR] != 2 {
		t.Fatalf("counts %v", res.Counts)
	}
}

func TestParseBatchReportsWhatItCannotRead(t *testing.T) {
	text := "example.com\nGEOIP,CN,DIRECT\nMATCH,Proxy\nDOMAIN-SUFFIX\nnot a rule\n999999\nhttp://bad host/\nDOMAIN-REGEX,(unclosed,DIRECT\n"
	res := routing.ParseBatch(text)
	if len(res.Lines) != 1 {
		t.Fatalf("lines %+v", res.Lines)
	}
	lines := map[int]bool{}
	for _, b := range res.Unrecognized {
		lines[b.Line] = true
		if b.Reason == "" || b.Text == "" {
			t.Fatalf("a bad line without a reason or text: %+v", b)
		}
	}
	for _, n := range []int{2, 3, 4, 5, 6, 7, 8} {
		if !lines[n] {
			t.Errorf("line %d should have been reported: %+v", n, res.Unrecognized)
		}
	}
}

func TestResolveTarget(t *testing.T) {
	st := routing.NewState()
	rows := st.View().Rows
	if d, ok := routing.ResolveTarget("direct", rows); !ok || d.Kind != routing.DestDirect {
		t.Fatalf("DIRECT: %v %v", d, ok)
	}
	if d, ok := routing.ResolveTarget("REJECT", rows); !ok || d.Kind != routing.DestReject {
		t.Fatalf("REJECT: %v %v", d, ok)
	}
	// The built-in plan sends the Microsoft item straight out and lets AI follow
	// the main exit; names compare the way people do (no emoji, any case, in
	// either language).
	for name, want := range map[string]routing.DestKind{
		"Microsoft": routing.DestDirect, "微软": routing.DestDirect, "💬 AI 服务": routing.DestFollow, "ai services": routing.DestFollow,
	} {
		d, ok := routing.ResolveTarget(name, rows)
		if !ok || d.Kind != want {
			t.Errorf("%s resolves to %v (found %v), want %v", name, d.Kind, ok, want)
		}
	}
	if _, ok := routing.ResolveTarget("Some Group Of Theirs", rows); ok {
		t.Fatal("an unknown name must not resolve")
	}
	// An item the person redirected resolves to where it goes now.
	var microsoft string
	for _, r := range rows {
		if r.Item.Key == "microsoft" {
			microsoft = r.Item.ID
		}
	}
	if err := st.SetItemDest(microsoft, routing.ToRegion("jp")); err != nil {
		t.Fatal(err)
	}
	d, ok := routing.ResolveTarget("Microsoft", st.View().Rows)
	if !ok || d.Kind != routing.DestRegion || d.Region != "JP" {
		t.Fatalf("redirected item: %v %v", d, ok)
	}
}
