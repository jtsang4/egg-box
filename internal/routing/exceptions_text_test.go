package routing_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"eggbox/internal/routing"
)

// twoTokyos are the nodes of two subscriptions that both have a "Tokyo 01",
// and a few that are only in one place.
func twoTokyos() routing.Names {
	node := func(sub, subName, alias, name, hash string) routing.NamedNode {
		return routing.NamedNode{ID: routing.NodeID{Sub: sub, Name: name, Hash: hash}, SubName: subName, SubAlias: alias}
	}
	return routing.Names{
		Nodes: []routing.NamedNode{
			node("sub-a", "Aurora Cloud", "", "Tokyo 01", "h1"),
			node("sub-a", "Aurora Cloud", "", "Seoul 01", "h2"),
			node("sub-a", "Aurora Cloud", "", "HK, IPLC [x2]", "h3"),
			node("sub-n", "Nimbus Net", "Nim", "Tokyo 01", "h4"),
			node("sub-n", "Nimbus Net", "Nim", "London 02", "h5"),
		},
		Region: func(code string) bool { return code == "US" || code == "JP" || code == "SG" },
	}
}

func idOf(ns routing.Names, sub, name string) routing.NodeID {
	for _, n := range ns.Nodes {
		if n.ID.Sub == sub && n.ID.Name == name {
			return n.ID
		}
	}
	panic("no node " + sub + "/" + name)
}

// someExceptions are exceptions of every kind and destination, with the ids a
// state gives them.
func someExceptions(t *testing.T, ns routing.Names) []routing.Exception {
	t.Helper()
	st := routing.NewState()
	pinned := routing.ToNode(idOf(ns, "sub-a", "Seoul 01"))
	pinned.NoFallback = true
	for _, x := range []routing.Exception{
		{Kind: routing.EntryDomainSuffix, Value: "intranet.example", Dest: routing.Direct(), Enabled: true},
		{Kind: routing.EntryDomainKeyword, Value: "ads-tracker", Dest: routing.Reject(), Enabled: true},
		{Kind: routing.EntryDomain, Value: "www.example.org", Dest: routing.Follow(), Enabled: true},
		{Kind: routing.EntryDomainSuffix, Value: "openai.com", Dest: routing.ToRegion("US"), Enabled: true},
		{Kind: routing.EntryDomainSuffix, Value: "abema.tv", Dest: routing.ToNode(idOf(ns, "sub-a", "Tokyo 01")), Enabled: true},
		{Kind: routing.EntryDomainSuffix, Value: "tver.jp", Dest: routing.ToNode(idOf(ns, "sub-n", "Tokyo 01")), Enabled: true},
		{Kind: routing.EntryDomainSuffix, Value: "naver.com", Dest: pinned, Enabled: true},
		{Kind: routing.EntryDomainSuffix, Value: "hk.example", Dest: routing.ToNode(idOf(ns, "sub-a", "HK, IPLC [x2]")), Enabled: true},
		{Kind: routing.EntryDomainRegex, Value: `^a{1,3}\.example$`, Dest: routing.Direct(), Enabled: true},
		{Kind: routing.EntryDomainRegex, Value: `^b[0-9]+\.example$`, Dest: routing.ToNode(idOf(ns, "sub-a", "HK, IPLC [x2]")), Enabled: true},
		{Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8", Dest: routing.Direct(), Enabled: false},
		{Kind: routing.EntryPort, Value: "8000-9000", Dest: routing.Reject(), Enabled: true},
		{Kind: routing.EntryProcessName, Value: "Slack", Dest: routing.Follow(), Enabled: true},
		{Kind: routing.EntryProcessPath, Value: `/Applications/Foo, Inc.app/Contents/MacOS/"Foo"`, Dest: routing.Direct(), Enabled: true},
	} {
		if _, err := st.AddException(x); err != nil {
			t.Fatal(err)
		}
	}
	return st.Exceptions
}

func TestExceptionsTextIsWrittenTheWayPeopleReadIt(t *testing.T) {
	ns := twoTokyos()
	doc := routing.WriteExceptions(someExceptions(t, ns), ns)
	want := `DOMAIN-SUFFIX,intranet.example,DIRECT
DOMAIN-KEYWORD,ads-tracker,REJECT
DOMAIN,www.example.org
DOMAIN-SUFFIX,openai.com,REGION:US
DOMAIN-SUFFIX,abema.tv,NODE:Aurora Cloud/Tokyo 01
DOMAIN-SUFFIX,tver.jp,NODE:Nim/Tokyo 01
DOMAIN-SUFFIX,naver.com,NODE:Seoul 01
DOMAIN-SUFFIX,hk.example,"NODE:HK, IPLC [x2]"
DOMAIN-REGEX,"^a{1,3}\.example$",DIRECT
DOMAIN-REGEX,^b[0-9]+\.example$,"NODE:HK, IPLC [x2]"
# IP-CIDR,10.0.0.0/8,DIRECT
DST-PORT,8000:9000,REJECT
PROCESS-NAME,Slack
PROCESS-PATH,"/Applications/Foo, Inc.app/Contents/MacOS/""Foo""",DIRECT
`
	if doc.Text != want {
		t.Fatalf("text:\n%s\nwant:\n%s", doc.Text, want)
	}
}

func TestExceptionsTextReadsBackToTheSameList(t *testing.T) {
	ns := twoTokyos()
	xs := someExceptions(t, ns)
	doc := routing.WriteExceptions(xs, ns)
	d := doc.Read(doc.Text, ns)
	if len(d.Problems) != 0 {
		t.Fatalf("problems: %+v", d.Problems)
	}
	if d.Changes() || d.Ready() {
		t.Fatalf("the text as written changes something: %+v", d)
	}
	if !reflect.DeepEqual(d.List, xs) {
		t.Fatalf("list:\n%+v\nwant:\n%+v", d.List, xs)
	}
}

// Every line as written means what it meant even when the nodes it names are
// read again from scratch: what is written is enough to find them.
func TestExceptionsTextNamesEveryNodeUnambiguously(t *testing.T) {
	ns := twoTokyos()
	xs := someExceptions(t, ns)
	text := routing.WriteExceptions(xs, ns).Text
	fresh := routing.WriteExceptions(nil, ns)
	d := fresh.Read(text, ns)
	if len(d.Problems) != 0 {
		t.Fatalf("problems: %+v", d.Problems)
	}
	for i, x := range d.List {
		want := xs[i].Dest
		want.NoFallback = false // a choice the text does not write
		if !x.Dest.Equal(want) {
			t.Errorf("%s goes to %v, want %v", x.Value, x.Dest, want)
		}
	}
}

func TestExceptionsTextKeepsWhatAnUntouchedDestinationStoodFor(t *testing.T) {
	ns := twoTokyos()
	xs := someExceptions(t, ns)
	doc := routing.WriteExceptions(xs, ns)

	// Another subscription gets a "Seoul 01" too: the name alone would now be
	// ambiguous, but the line was not touched.
	more := ns
	more.Nodes = append(append([]routing.NamedNode(nil), ns.Nodes...),
		routing.NamedNode{ID: routing.NodeID{Sub: "sub-x", Name: "Seoul 01"}, SubName: "Extra"})
	d := doc.Read(doc.Text, more)
	if len(d.Problems) != 0 || d.Changes() {
		t.Fatalf("an untouched text came to %+v", d)
	}
	naver := d.List[6]
	if naver.Value != "naver.com" || naver.Dest.Node == nil || naver.Dest.Node.Sub != "sub-a" || !naver.Dest.NoFallback {
		t.Fatalf("naver.com = %+v", naver)
	}

	// A new line written with a destination copied from another line goes
	// where that line goes.
	d = doc.Read(doc.Text+"DOMAIN-SUFFIX,daum.net,NODE:Seoul 01\nDOMAIN-SUFFIX,nhk.jp,NODE:Nim/Tokyo 01\n", more)
	if len(d.Problems) != 0 || d.Added != 2 {
		t.Fatalf("copied destinations came to %+v", d)
	}
	daum, nhk := d.List[len(d.List)-2], d.List[len(d.List)-1]
	if daum.Dest.Node == nil || daum.Dest.Node.Sub != "sub-a" || nhk.Dest.Node == nil || nhk.Dest.Node.Sub != "sub-n" || daum.ID != "" {
		t.Fatalf("daum.net = %+v, nhk.jp = %+v", daum, nhk)
	}
}

func TestExceptionsTextKeepsANodeThatIsGone(t *testing.T) {
	ns := twoTokyos()
	gone := routing.Exception{ID: "x1", Kind: routing.EntryDomainSuffix, Value: "old.example", Enabled: true,
		Dest: routing.ToNode(routing.NodeID{Sub: "sub-a", Name: "Paris 09"})}
	twin := routing.Exception{ID: "x2", Kind: routing.EntryDomainSuffix, Value: "twin.example", Enabled: true,
		Dest: routing.ToNode(routing.NodeID{Sub: "sub-gone", Name: "London 02"})}
	// Nimbus Net has a London 02 that is there: the one that is gone is
	// written with its subscription, by its id when nothing else is known.
	doc := routing.WriteExceptions([]routing.Exception{gone, twin}, ns)
	if want := "DOMAIN-SUFFIX,old.example,NODE:Paris 09\nDOMAIN-SUFFIX,twin.example,NODE:sub-gone/London 02\n"; doc.Text != want {
		t.Fatalf("text %q", doc.Text)
	}
	named := ns
	named.Subs = map[string]string{"sub-gone": "Old Sub"}
	if got := routing.WriteExceptions([]routing.Exception{twin}, named).Text; got != "DOMAIN-SUFFIX,twin.example,NODE:Old Sub/London 02\n" {
		t.Fatalf("text %q", got)
	}
	// A line written anew with the bare name means the node that is there.
	d0 := doc.Read(doc.Text+"DOMAIN-SUFFIX,new.example,NODE:London 02\n", ns)
	if len(d0.Problems) != 0 || d0.List[2].Dest.Node.Sub != "sub-n" {
		t.Fatalf("a bare name came to %+v", d0)
	}
	d := doc.Read(doc.Text, ns)
	if len(d.Problems) != 0 || d.Changes() || d.List[0].Dest.Node.Name != "Paris 09" || d.List[1].Dest.Node.Sub != "sub-gone" {
		t.Fatalf("a node that is gone came to %+v", d)
	}
	// A new line written the same way goes to the same node, gone or not.
	d = doc.Read(doc.Text+"DOMAIN-SUFFIX,new.example,NODE:Paris 09 \n", ns)
	if len(d.Problems) != 0 || d.Added != 1 || d.List[2].Dest.Node.Name != "Paris 09" {
		t.Fatalf("a copied destination came to %+v", d)
	}
	// In a text that never named it, the name is looked up: Paris 09 is nowhere.
	d = routing.WriteExceptions(nil, ns).Read("DOMAIN-SUFFIX,new.example,NODE:Paris 09\n", ns)
	if len(d.List) != 0 || len(d.Problems) != 1 || d.Problems[0].Code != routing.BadNoNode {
		t.Fatalf("a node that is nowhere came to %+v", d)
	}
}

func TestExceptionsTextSaysWhatIsWrongWithALine(t *testing.T) {
	ns := twoTokyos()
	ns.Rows = routing.NewState().View().Rows
	doc := routing.WriteExceptions(nil, ns)
	text := strings.Join([]string{
		"DOMAIN-SUFFIX,a.example,NODE:Tokyo 01",              // 1 ambiguous
		"DOMAIN-SUFFIX,b.example,NODE:Nimbus Net/Tokyo 01",   // 2 fine, by the subscription's own name
		"DOMAIN-SUFFIX,c.example,NODE:Nowhere",               // 3 no such node
		"DOMAIN-SUFFIX,d.example,REGION:ZZ",                  // 4 unknown region
		"DOMAIN-SUFFIX,e.example,Proxy",                      // 5 unknown destination
		"GEOIP,CN,DIRECT",                                    // 6 unsupported type
		"DOMAIN-SUFFIX,b.example,DIRECT",                     // 7 the same as line 2
		"# just a note, not a rule",                          // a comment
		"# DOMAIN-SUFFIX,off.example,REGION:jp",              // 9 switched off
		"DOMAIN-SUFFIX,f.example,Microsoft",                  // 10 the name of an item
		"DOMAIN-SUFFIX,g.example,NODE:Aurora Cloud/Tokyo 01", // 11 fine
	}, "\n")
	d := doc.Read(text, ns)
	type bad struct {
		line int
		code string
		arg  string
	}
	var got []bad
	for _, p := range d.Problems {
		got = append(got, bad{p.Line, p.Code, p.Arg})
	}
	want := []bad{
		{1, routing.BadAmbiguous, "Tokyo 01"},
		{3, routing.BadNoNode, "Nowhere"},
		{4, routing.BadRegion, "ZZ"},
		{5, routing.BadTarget, "Proxy"},
		{6, routing.BadType, "GEOIP"},
		{7, routing.BadDuplicate, "2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("problems\n%+v\nwant\n%+v", got, want)
	}
	if c := d.Problems[0].Choices; !reflect.DeepEqual(c, []string{"NODE:Aurora Cloud/Tokyo 01", "NODE:Nim/Tokyo 01"}) {
		t.Fatalf("choices %v", c)
	}
	if d.Problems[0].Text != "DOMAIN-SUFFIX,a.example,NODE:Tokyo 01" {
		t.Fatalf("text of a problem %q", d.Problems[0].Text)
	}
	if d.Ready() {
		t.Fatal("a text with problems is ready to save")
	}
	if len(d.List) != 4 {
		t.Fatalf("list %+v", d.List)
	}
	b, off, ms, g := d.List[0], d.List[1], d.List[2], d.List[3]
	if b.Dest.Node == nil || b.Dest.Node.Sub != "sub-n" {
		t.Errorf("b.example = %+v", b)
	}
	if off.Enabled || off.Dest.Kind != routing.DestRegion || off.Dest.Region != "JP" {
		t.Errorf("off.example = %+v", off)
	}
	if ms.Dest.Kind != routing.DestDirect {
		t.Errorf("an item's name: %+v", ms)
	}
	if g.Dest.Node == nil || g.Dest.Node.Sub != "sub-a" {
		t.Errorf("g.example = %+v", g)
	}

	// The destinations no one answers to can be taken off in one go.
	fixed := routing.StripTargets(text, map[int]bool{5: true, 9: true})
	lines := strings.Split(fixed, "\n")
	if lines[4] != "DOMAIN-SUFFIX,e.example" || lines[8] != "# DOMAIN-SUFFIX,off.example" || lines[0] != "DOMAIN-SUFFIX,a.example,NODE:Tokyo 01" {
		t.Fatalf("stripped:\n%s", fixed)
	}
}

func TestExceptionsTextCountsWhatChanged(t *testing.T) {
	ns := twoTokyos()
	xs := someExceptions(t, ns)
	doc := routing.WriteExceptions(xs, ns)
	lines := strings.Split(strings.TrimSuffix(doc.Text, "\n"), "\n")

	// Swap the first two, drop the third, switch the disabled one on, send
	// Slack direct and add one.
	edited := append([]string{lines[1], lines[0]}, lines[3:]...)
	for i, l := range edited {
		switch {
		case strings.HasPrefix(l, "# IP-CIDR"):
			edited[i] = strings.TrimPrefix(l, "# ")
		case l == "PROCESS-NAME,Slack":
			edited[i] = l + ",DIRECT"
		}
	}
	edited = append(edited, "new.example")
	d := doc.Read(strings.Join(edited, "\n"), ns)
	if len(d.Problems) != 0 {
		t.Fatalf("problems %+v", d.Problems)
	}
	if d.Added != 1 || d.Changed != 2 || len(d.Removed) != 1 || d.Removed[0].Value != "www.example.org" || !d.Reordered || !d.Ready() {
		t.Fatalf("draft: added %d changed %d removed %+v reordered %v", d.Added, d.Changed, d.Removed, d.Reordered)
	}
	// What stays keeps its id; what is new has none yet.
	byValue := map[string]routing.Exception{}
	for _, x := range d.List {
		byValue[x.Value] = x
	}
	if byValue["ads-tracker"].ID != xs[1].ID || byValue["new.example"].ID != "" || !byValue["10.0.0.0/8"].Enabled {
		t.Fatalf("list %+v", d.List)
	}

	// Emptied, the text removes everything.
	d = doc.Read("\n# nothing\n", ns)
	if len(d.Removed) != len(xs) || len(d.List) != 0 || !d.Ready() {
		t.Fatalf("an empty text: %+v", d)
	}
}

func TestReplaceExceptions(t *testing.T) {
	ns := twoTokyos()
	st := routing.NewState()
	st.Exceptions = someExceptions(t, ns)
	doc := routing.WriteExceptions(st.Exceptions, ns)
	d := doc.Read("DOMAIN-SUFFIX,intranet.example,REJECT\nDOMAIN-SUFFIX,new.example,DIRECT\n", ns)
	if len(d.Problems) != 0 {
		t.Fatalf("problems %+v", d.Problems)
	}

	// Something else changed the list in the meantime: nothing is replaced.
	moved := st.Clone()
	if _, err := moved.UpsertException(routing.Entry{Kind: routing.EntryDomain, Value: "meanwhile.example"}, routing.Direct()); err != nil {
		t.Fatal(err)
	}
	if err := moved.ReplaceExceptions(doc.Base, d.List); !errors.Is(err, routing.ErrExceptionsChanged) {
		t.Fatalf("replacing a list that changed: %v", err)
	}
	if len(moved.Exceptions) != len(doc.Base)+1 {
		t.Fatal("a list that changed was replaced")
	}

	firstID := st.Exceptions[0].ID
	if err := st.ReplaceExceptions(doc.Base, d.List); err != nil {
		t.Fatal(err)
	}
	if len(st.Exceptions) != 2 || st.Exceptions[0].ID != firstID || st.Exceptions[0].Dest.Kind != routing.DestReject ||
		st.Exceptions[1].ID == "" || st.Exceptions[1].Value != "new.example" || !st.Exceptions[1].Enabled {
		t.Fatalf("exceptions %+v", st.Exceptions)
	}

	// Too many, or the same twice, is refused.
	base := st.Exceptions
	many := make([]routing.Exception, routing.MaxExceptions+1)
	for i := range many {
		many[i] = routing.Exception{Kind: routing.EntryPort, Value: "1", Dest: routing.Direct(), Enabled: true}
	}
	if err := st.ReplaceExceptions(base, many); !errors.Is(err, routing.ErrTooManyExceptions) {
		t.Fatalf("too many: %v", err)
	}
	if err := st.ReplaceExceptions(base, many[:2]); !errors.Is(err, routing.ErrBadEntry) {
		t.Fatalf("twice: %v", err)
	}
	if len(st.Exceptions) != 2 {
		t.Fatal("a refused list changed the exceptions")
	}
}

func TestParseBatchReadsQuotedFields(t *testing.T) {
	text := `PROCESS-PATH,"/Apps/A, B.app/a",DIRECT
DOMAIN-SUFFIX, "x.example" , "NODE:HK, 01"
DOMAIN-REGEX,^c,d$,"NODE:[1]"
DOMAIN-REGEX,"^e,f$"
# DOMAIN-SUFFIX,commented.example,DIRECT
DOMAIN-SUFFIX,"unclosed.example,DIRECT
`
	res := routing.ParseBatch(text)
	type row struct {
		value, target string
		off           bool
	}
	var got []row
	for _, l := range res.Lines {
		got = append(got, row{l.Entry.Value, l.Target, l.Disabled})
	}
	want := []row{
		{"/Apps/A, B.app/a", "DIRECT", false},
		{"x.example", "NODE:HK, 01", false},
		{"^c,d$", "NODE:[1]", false},
		{"^e,f$", "", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%+v\nwant\n%+v", got, want)
	}
	// Outside the exceptions editor a commented rule is a comment; a quote
	// that does not close leaves a value that is not a domain.
	if len(res.Unrecognized) != 1 || res.Unrecognized[0].Line != 6 || res.Unrecognized[0].Code != routing.BadSyntax {
		t.Fatalf("unrecognized %+v", res.Unrecognized)
	}
}

func TestEntryLineReadsBack(t *testing.T) {
	for _, e := range []routing.Entry{
		{Kind: routing.EntryProcessPath, Value: `C:\Program Files\A, B\a.exe`},
		{Kind: routing.EntryDomainRegex, Value: `^x{2,}$`},
		{Kind: routing.EntryDomainSuffix, Value: "example.com"},
	} {
		res := routing.ParseBatch(routing.EntryLine(e))
		if len(res.Lines) != 1 || res.Lines[0].Entry != e || res.Lines[0].Target != "" {
			t.Errorf("%q read back as %+v", routing.EntryLine(e), res)
		}
	}
}

func TestExceptionsTextReportsARuleSwitchedOffThatIsWrong(t *testing.T) {
	ns := twoTokyos()
	xs := []routing.Exception{{ID: "x1", Kind: routing.EntryDomainRegex, Value: "^a$", Dest: routing.Direct(), Enabled: true}}
	doc := routing.WriteExceptions(xs, ns)
	d := doc.Read("# DOMAIN-REGEX,^(a$,DIRECT\n# a comment, with a comma\n# DOMAIN-SUFFIX\n"+
		"# DOMAIN, IP-CIDR and GEOIP rules go below\n# PROCESS-NAME, the apps below\n# DOMAIN, NODE: rules below\n", ns)
	if len(d.Problems) != 1 || d.Problems[0].Line != 1 || d.Ready() || len(d.List) != 0 {
		t.Fatalf("a broken rule switched off came to %+v", d)
	}
}

func TestExceptionsTextReadsANodeNameWrittenByHand(t *testing.T) {
	ns := twoTokyos()
	ns.Nodes = append(ns.Nodes,
		routing.NamedNode{ID: routing.NodeID{Sub: "sub-a", Name: "HK (IPLC) | 01"}, SubName: "Aurora Cloud"},
		routing.NamedNode{ID: routing.NodeID{Sub: "sub-a", Name: "Trailing "}, SubName: "Aurora Cloud"})
	doc := routing.WriteExceptions(nil, ns)
	d := doc.Read("DOMAIN-REGEX,^a\\.com$,NODE:HK (IPLC) | 01\n"+
		"DOMAIN-REGEX,^b{1,2}$,region:jp\n"+
		"DOMAIN-SUFFIX,c.example,NODE:HK, IPLC [x2]\n"+
		"DOMAIN-SUFFIX,d.example,NODE:Trailing\n", ns)
	if len(d.Problems) != 0 || len(d.List) != 4 {
		t.Fatalf("hand-written node names came to %+v", d)
	}
	if a := d.List[0]; a.Value != `^a\.com$` || a.Dest.Node == nil || a.Dest.Node.Name != "HK (IPLC) | 01" {
		t.Errorf("regex line = %+v", a)
	}
	if b := d.List[1]; b.Value != "^b{1,2}$" || b.Dest.Region != "JP" {
		t.Errorf("regex line with a region = %+v", b)
	}
	if c := d.List[2]; c.Dest.Node == nil || c.Dest.Node.Name != "HK, IPLC [x2]" {
		t.Errorf("a name with a comma = %+v", c)
	}
	if e := d.List[3]; e.Dest.Node == nil || e.Dest.Node.Name != "Trailing " {
		t.Errorf("a name that ends in a space = %+v", e)
	}
	// Written out, a name that ends in a space reads back untouched.
	doc = routing.WriteExceptions(d.List, ns)
	if back := doc.Read(doc.Text, ns); len(back.Problems) != 0 || back.Changes() {
		t.Fatalf("read back: %+v\n%s", back, doc.Text)
	}
}

func TestExceptionsTextSuggestsOnlyWaysThatNameOneNode(t *testing.T) {
	ns := routing.Names{Nodes: []routing.NamedNode{
		{ID: routing.NodeID{Sub: "s1", Name: "Work/Tokyo"}, SubName: "S1"},
		{ID: routing.NodeID{Sub: "s2", Name: "Tokyo"}, SubName: "Work"},
		{ID: routing.NodeID{Sub: "s3", Name: "Tokyo"}, SubName: "Other"},
	}}
	_, bad := routing.ResolveExceptionTarget("NODE:Work/Tokyo", ns)
	if bad == nil || bad.Code != routing.BadAmbiguous || !reflect.DeepEqual(bad.Choices, []string{"NODE:S1/Work/Tokyo"}) {
		t.Fatalf("problem %+v", bad)
	}
}

func TestExceptionsTextDoesNotRemoveWhatALineWithAProblemStandsFor(t *testing.T) {
	ns := twoTokyos()
	xs := someExceptions(t, ns)
	doc := routing.WriteExceptions(xs, ns)
	text := strings.Replace(doc.Text, "DOMAIN-SUFFIX,intranet.example,DIRECT", "DOMAIN-SUFFIX,intranet.example,FOO", 1)
	d := doc.Read(text, ns)
	if len(d.Problems) != 1 || len(d.Removed) != 0 {
		t.Fatalf("problems %+v, removed %+v", d.Problems, d.Removed)
	}
}

func TestExceptionsTextIsCarriedOverToAListThatChanged(t *testing.T) {
	ns := twoTokyos()
	st := routing.NewState()
	for _, v := range []string{"a.example", "b.example", "c.example"} {
		if _, err := st.UpsertException(routing.Entry{Kind: routing.EntryDomainSuffix, Value: v}, routing.Direct()); err != nil {
			t.Fatal(err)
		}
	}
	doc := routing.WriteExceptions(st.Exceptions, ns)
	// The person sends b somewhere else, removes c and adds d.
	edited := "DOMAIN-SUFFIX,a.example,DIRECT\nDOMAIN-SUFFIX,b.example,REJECT\nDOMAIN-SUFFIX,d.example\n"
	// Meanwhile a is blocked, c goes direct... (c, which the person removed),
	// e is added and b is switched off.
	now := st.Clone()
	now.Exceptions[0].Dest = routing.Reject()
	now.Exceptions[1].Enabled = false
	if _, err := now.UpsertException(routing.Entry{Kind: routing.EntryDomainSuffix, Value: "e.example"}, routing.Direct()); err != nil {
		t.Fatal(err)
	}
	next, text := doc.Rebase(edited, now.Exceptions, ns)
	want := "DOMAIN-SUFFIX,a.example,REJECT\nDOMAIN-SUFFIX,b.example,REJECT\nDOMAIN-SUFFIX,d.example\nDOMAIN-SUFFIX,e.example,DIRECT\n"
	if text != want {
		t.Fatalf("carried over:\n%s\nwant:\n%s", text, want)
	}
	d := next.Read(text, ns)
	// What remains is what the person did: b changed (and back on), c removed, d added.
	if len(d.Problems) != 0 || d.Added != 1 || d.Changed != 1 || len(d.Removed) != 1 || d.Removed[0].Value != "c.example" {
		t.Fatalf("draft after carrying over: %+v", d)
	}
	if err := now.ReplaceExceptions(next.Base, d.List); err != nil {
		t.Fatal(err)
	}
}

func TestExceptionsTextNodeTargetsAndNoResolve(t *testing.T) {
	ns := twoTokyos()
	d := routing.WriteExceptions(nil, ns).Read("IP-CIDR,1.1.1.1/32,NODE:Seoul 01,no-resolve\n"+
		"DOMAIN-REGEX,^x$,NODE:Seoul 01,NO-RESOLVE\n"+
		"DOMAIN-REGEX,^ı$,NODE:Seoul 01\n", ns)
	if len(d.Problems) != 0 || len(d.List) != 3 {
		t.Fatalf("draft %+v", d)
	}
	for _, x := range d.List {
		if x.Dest.Node == nil || x.Dest.Node.Name != "Seoul 01" {
			t.Errorf("%s goes to %v", x.Value, x.Dest)
		}
	}
	if d.List[2].Value != "^ı$" {
		t.Errorf("value %q", d.List[2].Value)
	}
}

func TestExceptionsTextWithSubscriptionsOfTheSameName(t *testing.T) {
	ns := routing.Names{Nodes: []routing.NamedNode{
		{ID: routing.NodeID{Sub: "s1", Name: "Tokyo"}, SubName: "W"},
		{ID: routing.NodeID{Sub: "s2", Name: "Tokyo"}, SubName: "W"},
	}, Subs: map[string]string{"s1": "W", "s2": "W", "s3": "W"}}
	_, bad := routing.ResolveExceptionTarget("NODE:Tokyo", ns)
	if bad == nil || bad.Code != routing.BadAmbiguous || len(bad.Choices) != 0 {
		t.Fatalf("problem %+v", bad)
	}
	// A node of a third subscription called W, that is gone, is written by
	// the subscription's id.
	gone := routing.Exception{Kind: routing.EntryDomain, Value: "a.example", Enabled: true, Dest: routing.ToNode(routing.NodeID{Sub: "s3", Name: "Tokyo"})}
	if got := routing.WriteExceptions([]routing.Exception{gone}, ns).Text; got != "DOMAIN,a.example,NODE:s3/Tokyo\n" {
		t.Fatalf("text %q", got)
	}
}

func TestExceptionsTextCarriedOverKeepsTheOrderOfOneChangedElsewhere(t *testing.T) {
	ns := twoTokyos()
	st := routing.NewState()
	for _, v := range []string{"a.example", "m.example", "z.example"} {
		if _, err := st.UpsertException(routing.Entry{Kind: routing.EntryDomainSuffix, Value: v}, routing.Direct()); err != nil {
			t.Fatal(err)
		}
	}
	doc := routing.WriteExceptions(st.Exceptions, ns)
	edited := strings.Replace(doc.Text, "DOMAIN-SUFFIX,z.example,DIRECT", "DOMAIN-SUFFIX,z.example,REJECT", 1)
	now := st.Clone()
	if err := now.UpdateException(now.Exceptions[0].ID, routing.Exception{Kind: routing.EntryDomainSuffix, Value: "b.example", Dest: routing.Direct(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	next, text := doc.Rebase(edited, now.Exceptions, ns)
	if want := "DOMAIN-SUFFIX,b.example,DIRECT\nDOMAIN-SUFFIX,m.example,DIRECT\nDOMAIN-SUFFIX,z.example,REJECT\n"; text != want {
		t.Fatalf("carried over %q", text)
	}
	if d := next.Read(text, ns); d.Reordered || d.Changed != 1 || d.Added != 0 || len(d.Removed) != 0 {
		t.Fatalf("draft %+v", d)
	}
}

func TestExceptionsTextRoundThreeCases(t *testing.T) {
	ns := twoTokyos()
	hk := idOf(ns, "sub-a", "HK, IPLC [x2]")
	xs := []routing.Exception{{ID: "x1", Kind: routing.EntryDomain, Value: "a.com", Dest: routing.ToNode(hk), Enabled: true}}
	doc := routing.WriteExceptions(xs, ns)

	// Switched off by hand, with the node's name written without quotes.
	d := doc.Read("# DOMAIN,a.com,NODE:HK, IPLC [x2]\n", ns)
	if len(d.Problems) != 0 || len(d.Removed) != 0 || len(d.List) != 1 || d.List[0].Enabled || d.List[0].ID != "x1" {
		t.Fatalf("switched off by hand: %+v", d)
	}
	// no-resolve after a space.
	d = doc.Read("DOMAIN,c.com,NODE:Seoul 01, no-resolve\n", ns)
	if len(d.Problems) != 0 || d.List[0].Dest.Node == nil || d.List[0].Dest.Node.Name != "Seoul 01" {
		t.Fatalf("no-resolve after a space: %+v", d)
	}
	// A gone node whose subscription's name and its own would name a node
	// that is there is written by the subscription's id.
	named := ns
	named.Nodes = append(append([]routing.NamedNode(nil), ns.Nodes...), routing.NamedNode{ID: routing.NodeID{Sub: "sub-n", Name: "Work/Tokyo 01"}, SubName: "Nimbus Net"})
	named.Subs = map[string]string{"sub-w": "Work"}
	gone := routing.Exception{Kind: routing.EntryDomain, Value: "g.com", Enabled: true, Dest: routing.ToNode(routing.NodeID{Sub: "sub-w", Name: "Tokyo 01"})}
	if got := routing.WriteExceptions([]routing.Exception{gone}, named).Text; got != "DOMAIN,g.com,NODE:sub-w/Tokyo 01\n" {
		t.Fatalf("text %q", got)
	}
}

func TestExceptionsTextCarriedOverKeepsOneRemovedAndAddedAgainElsewhere(t *testing.T) {
	ns := twoTokyos()
	base := []routing.Exception{
		{ID: "A", Kind: routing.EntryDomain, Value: "a.com", Dest: routing.Direct(), Enabled: true},
		{ID: "M", Kind: routing.EntryDomain, Value: "m.com", Dest: routing.Direct(), Enabled: true},
	}
	doc := routing.WriteExceptions(base, ns)
	latest := []routing.Exception{base[1], {ID: "fresh", Kind: routing.EntryDomain, Value: "a.com", Dest: routing.Reject(), Enabled: true}}
	next, text := doc.Rebase(doc.Text, latest, ns)
	if want := "DOMAIN,m.com,DIRECT\nDOMAIN,a.com,REJECT\n"; text != want {
		t.Fatalf("carried over %q", text)
	}
	if d := next.Read(text, ns); d.Changes() || len(d.Problems) != 0 {
		t.Fatalf("draft %+v", d)
	}
}
