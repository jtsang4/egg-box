package routing_test

import (
	"fmt"
	"strings"
	"testing"

	"eggbox/internal/routing"
)

// "dns=…" at the end of a line, or before or after the destination, says how
// the names are looked up; it never becomes part of the value, of a regular
// expression or of a node's name.
func TestParseDNSOption(t *testing.T) {
	for _, tc := range []struct {
		line, value, target, resolver string
		disabled                      bool
	}{
		{line: "DOMAIN-SUFFIX,corp-intra.example,DIRECT,dns=10.0.0.53", value: "corp-intra.example", target: "DIRECT", resolver: "10.0.0.53"},
		{line: "DOMAIN-SUFFIX,a.example,dns=exit", value: "a.example", resolver: "exit"},
		{line: "DOMAIN-KEYWORD,portal,NODE:My, Node,dns=exit", value: "portal", target: "NODE:My, Node", resolver: "exit"},
		{line: "DOMAIN-KEYWORD,portal,NODE:My, Node,no-resolve,dns=exit", value: "portal", target: "NODE:My, Node", resolver: "exit"},
		{line: "DOMAIN-SUFFIX,a.example,DIRECT,no-resolve,dns=system", value: "a.example", target: "DIRECT", resolver: "system"},
		{line: "DOMAIN-SUFFIX,a.example,DIRECT,dns=system,no-resolve", value: "a.example", target: "DIRECT", resolver: "system"},
		{line: "DOMAIN,x.example,dns=exit,DIRECT", value: "x.example", target: "DIRECT", resolver: "exit"},
		{line: "DOMAIN,x.example,DIRECT,dns=https://dns.example/dns-query,extra", value: "x.example", target: "DIRECT", resolver: "https://dns.example/dns-query"},
		{line: `DOMAIN-REGEX,^a,b\.example$,DIRECT,dns=exit`, value: `^a,b\.example$`, target: "DIRECT", resolver: "exit"},
		{line: `DOMAIN-REGEX,^a,b\.example$,NODE:x,dns=exit`, value: `^a,b\.example$`, target: "NODE:x", resolver: "exit"},
		{line: `DOMAIN-REGEX,^a\.example$,dns=exit`, value: `^a\.example$`, resolver: "exit"},
		{line: `DOMAIN-REGEX,^a\.example$,dns=1.1.1.1,DIRECT`, value: `^a\.example$`, target: "DIRECT", resolver: "1.1.1.1"},
		{line: `DOMAIN-REGEX,^a,b\.example$,dns=1.1.1.1,NODE:x`, value: `^a,b\.example$`, target: "NODE:x", resolver: "1.1.1.1"},
		{line: `DOMAIN-REGEX,^a\.example$,DIRECT,no-resolve`, value: `^a\.example$`, target: "DIRECT"},
		{line: `DOMAIN-REGEX,"^a,dns=x$",DIRECT`, value: `^a,dns=x$`, target: "DIRECT"},
		{line: `DOMAIN-SUFFIX,a.example,"NODE:odd,dns=exit"`, value: "a.example", target: "NODE:odd,dns=exit"},
		{line: "# DOMAIN-SUFFIX,off.example,DIRECT,dns=exit", value: "off.example", target: "DIRECT", resolver: "exit", disabled: true},
		{line: "DOMAIN-SUFFIX,a.example,DIRECT,DNS=Exit", value: "a.example", target: "DIRECT", resolver: "Exit"},
	} {
		doc := routing.WriteExceptions(nil, routing.Names{})
		res := doc.Read(tc.line, routing.Names{Nodes: []routing.NamedNode{
			{ID: routing.NodeID{Sub: "s", Name: "My, Node"}}, {ID: routing.NodeID{Sub: "s", Name: "x"}}, {ID: routing.NodeID{Sub: "s", Name: "odd,dns=exit"}},
		}})
		if len(res.Problems) != 0 || len(res.List) != 1 {
			t.Errorf("%s: problems %+v list %+v", tc.line, res.Problems, res.List)
			continue
		}
		x := res.List[0]
		want := routing.NormalizeResolver(tc.resolver)
		if x.Value != tc.value || x.Resolver != want || x.Enabled == tc.disabled {
			t.Errorf("%s: got value %q resolver %q enabled %v", tc.line, x.Value, x.Resolver, x.Enabled)
		}
		pasted := routing.ParseBatch(tc.line)
		if len(pasted.Lines) == 1 && !tc.disabled && pasted.Lines[0].Target != tc.target {
			t.Errorf("%s: target %q, want %q", tc.line, pasted.Lines[0].Target, tc.target)
		}
	}
}

// What cannot be used is shown, and blocks saving, without the exception it
// would have changed counting as removed.
func TestDNSOptionProblems(t *testing.T) {
	base := []routing.Exception{
		{ID: "x1", Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8", Dest: routing.Direct(), Enabled: true},
		{ID: "x2", Kind: routing.EntryDomain, Value: "a.example", Dest: routing.Direct(), Enabled: true},
	}
	doc := routing.WriteExceptions(base, routing.Names{})
	for _, tc := range []struct {
		text, code, arg string
		syntax          bool
	}{
		{text: "IP-CIDR,10.0.0.0/8,DIRECT,dns=exit\nDOMAIN,a.example,DIRECT", code: routing.BadResolverHost, arg: "IP-CIDR"},
		{text: "IP-CIDR,10.0.0.0/8,DIRECT\nDOMAIN,a.example,DIRECT,dns=gopher://x", code: routing.BadResolver, arg: "gopher://x"},
		{text: "IP-CIDR,10.0.0.0/8,DIRECT\nDOMAIN,a.example,DIRECT,dns=1.1.1.1:99999", code: routing.BadResolver, arg: "1.1.1.1:99999"},
		{text: "IP-CIDR,10.0.0.0/8,DIRECT\nDOMAIN,a.example,DIRECT,dns=", code: routing.BadResolver},
		{text: "IP-CIDR,10.0.0.0/8,DIRECT\nDOMAIN,a.example,DIRECT,dns=exit,dns=system", code: routing.BadResolverTwice},
	} {
		d := doc.Read(tc.text, routing.Names{})
		if len(d.Problems) != 1 || d.Problems[0].Code != tc.code || tc.arg != "" && d.Problems[0].Arg != tc.arg {
			t.Errorf("%q: problems %+v", tc.text, d.Problems)
			continue
		}
		if d.Ready() {
			t.Errorf("%q: a text with problems cannot be saved", tc.text)
		}
		if len(d.Removed) != 0 && tc.code != routing.BadResolver && tc.code != routing.BadResolverTwice {
			t.Errorf("%q: the exception of a line with a problem is not removed: %+v", tc.text, d.Removed)
		}
	}
}

// A changed resolver is a change of the exception, which keeps its identity;
// the text written out says it and reads back unchanged.
func TestDNSOptionRoundTripAndIdentity(t *testing.T) {
	base := []routing.Exception{
		{ID: "x1", Kind: routing.EntryDomainSuffix, Value: "corp-intra.example", Dest: routing.Direct(), Enabled: true, Resolver: "10.0.0.53"},
		{ID: "x2", Kind: routing.EntryDomainKeyword, Value: "portal", Dest: routing.Follow(), Enabled: false, Resolver: "exit"},
		{ID: "x3", Kind: routing.EntryIPCIDR, Value: "10.99.0.0/24", Dest: routing.Direct(), Enabled: true},
	}
	doc := routing.WriteExceptions(base, routing.Names{})
	wantText := "DOMAIN-SUFFIX,corp-intra.example,DIRECT,dns=10.0.0.53\n# DOMAIN-KEYWORD,portal,dns=exit\nIP-CIDR,10.99.0.0/24,DIRECT\n"
	if doc.Text != wantText {
		t.Fatalf("text:\n%s\nwant:\n%s", doc.Text, wantText)
	}
	if d := doc.Read(doc.Text, routing.Names{}); d.Changes() || len(d.Problems) != 0 {
		t.Fatalf("an untouched text changed something: %+v", d)
	}
	edited := strings.Replace(doc.Text, "dns=10.0.0.53", "dns=SYSTEM", 1)
	d := doc.Read(edited, routing.Names{})
	if d.Changed != 1 || d.Added != 0 || len(d.Removed) != 0 || !d.Ready() {
		t.Fatalf("draft = %+v", d)
	}
	if d.List[0].ID != "x1" || d.List[0].Resolver != "system" {
		t.Fatalf("the exception keeps its id and gets the kept form: %+v", d.List[0])
	}
	if strings.Join(d.Resolvers, " ") != "system exit" {
		t.Fatalf("resolvers = %v", d.Resolvers)
	}
	st := routing.NewState()
	st.Exceptions = base
	if err := st.ReplaceExceptions(base, d.List); err != nil {
		t.Fatal(err)
	}
	if st.Exceptions[0].Resolver != "system" {
		t.Fatalf("saved = %+v", st.Exceptions[0])
	}
	// Removing the option is a change too.
	d = doc.Read(strings.Replace(doc.Text, ",dns=10.0.0.53", "", 1), routing.Names{})
	if d.Changed != 1 || d.List[0].Resolver != "" {
		t.Fatalf("removing dns= = %+v", d)
	}
	// Letting a line follow the main exit keeps how its names are looked up.
	if got := routing.StripTargets(doc.Text, map[int]bool{1: true}); !strings.HasPrefix(got, "DOMAIN-SUFFIX,corp-intra.example,dns=10.0.0.53\n") {
		t.Fatalf("strip targets: %q", got)
	}
	// Saving refuses what the editor would have shown as a problem.
	bad := []routing.Exception{{Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8", Dest: routing.Direct(), Enabled: true, Resolver: "exit"}}
	if err := st.ReplaceExceptions(st.Exceptions, bad); err == nil {
		t.Fatal("an address rule cannot carry a resolver")
	}
}

// Node names that look like options are written so that they read back to
// the same node, with and without a resolver after them.
func TestDNSOptionWithAwkwardNodeNames(t *testing.T) {
	names := []string{"a,dns=exit", "dns=x", "x,no-resolve", "plain", "NODE:inner", "y, dns=10.0.0.1"}
	var ns routing.Names
	for _, n := range names {
		ns.Nodes = append(ns.Nodes, routing.NamedNode{ID: routing.NodeID{Sub: "s", Name: n}, SubName: "Sub"})
	}
	var xs []routing.Exception
	for i, n := range ns.Nodes {
		for j, r := range []string{"", "exit", "10.0.0.53", "tls://dns.example"} {
			kind := routing.EntryDomainSuffix
			value := fmt.Sprintf("d%d-%d.example", i, j)
			if j%2 == 1 {
				kind, value = routing.EntryDomainRegex, fmt.Sprintf(`^r%d,%d\.example$`, i, j)
			}
			xs = append(xs, routing.Exception{ID: fmt.Sprintf("x%d-%d", i, j), Kind: kind, Value: value, Dest: routing.ToNode(n.ID), Enabled: true, Resolver: r})
		}
	}
	doc := routing.WriteExceptions(xs, ns)
	fresh := routing.WriteExceptions(nil, ns).Read(doc.Text, ns)
	if len(fresh.Problems) != 0 || len(fresh.List) != len(xs) {
		t.Fatalf("problems %+v\n%s", fresh.Problems, doc.Text)
	}
	for i, x := range fresh.List {
		if !x.Dest.Equal(xs[i].Dest) || x.Resolver != xs[i].Resolver || x.Value != xs[i].Value {
			t.Errorf("line %d: got %+v resolver %q, want %+v resolver %q", i+1, x.Dest.Node, x.Resolver, xs[i].Dest.Node, xs[i].Resolver)
		}
	}
}

// In a list of an item's entries a line cannot say how its names are looked
// up: the item says it once, for all of them.
func TestDNSOptionIsNotAnEntrysToSay(t *testing.T) {
	res := routing.ParseBatch("DOMAIN-SUFFIX,corp.example,DIRECT,dns=10.0.0.53\nDOMAIN-SUFFIX,other.example\nDOMAIN,x.example,dns=\n")
	if len(res.Lines) != 1 || len(res.Unrecognized) != 2 || res.Unrecognized[0].Code != routing.BadResolverHere || res.Unrecognized[0].Line != 1 ||
		res.Unrecognized[1].Code != routing.BadResolverHere {
		t.Fatalf("lines %+v, unrecognized %+v", res.Lines, res.Unrecognized)
	}
}
