package routing_test

import (
	"fmt"
	"strings"
	"testing"

	"eggbox/internal/routing"
)

// Names of subscriptions and nodes with every character the text gives a
// meaning to are written so that they read back to the same nodes, and the
// text that is written finds them again by name.
func TestExceptionsTextWithAwkwardNames(t *testing.T) {
	subs := []string{"Aurora Cloud", "A/B", "机场, 主力", `Q"uote`, "#1 Sub", "🚀 飞", "x NODE:y", "Sub/"}
	names := []string{"Tokyo 01", "HK, IPLC [x2]", `a"b`, "#1", "/lead", "trail/", "日本，东京", `C:\x`, "🇯🇵 东京 01", "a\u200bb",
		"x,no-resolve", "no-resolve", "NODE:inner", "REGION:JP", "'q'", `"dq"`, "DIRECT", "a/b/c", "x ,y", "(.*)|^$"}
	var ns routing.Names
	ns.Subs = map[string]string{}
	for i, s := range subs {
		id := fmt.Sprintf("s%d", i)
		ns.Subs[id] = s
		for _, n := range names {
			ns.Nodes = append(ns.Nodes, routing.NamedNode{ID: routing.NodeID{Sub: id, Name: n}, SubName: s})
		}
	}
	// One node of each name only in one place, too.
	ns.Nodes = append(ns.Nodes, routing.NamedNode{ID: routing.NodeID{Sub: "solo", Name: "only, here [1]"}, SubName: "Solo"})
	var xs []routing.Exception
	i := 0
	for _, n := range ns.Nodes {
		for _, kind := range []routing.EntryKind{routing.EntryDomainSuffix, routing.EntryDomainRegex, routing.EntryProcessPath} {
			v := fmt.Sprintf("d%d.example", i)
			switch kind {
			case routing.EntryDomainRegex:
				v = fmt.Sprintf(`^r%d{1,2}\.ex,ample$`, i)
			case routing.EntryProcessPath:
				v = fmt.Sprintf(`/Apps/A, "B" %d.app`, i)
			}
			xs = append(xs, routing.Exception{ID: fmt.Sprint("x", i), Kind: kind, Value: v, Dest: routing.ToNode(n.ID), Enabled: i%2 == 0})
			i++
		}
	}
	if len(xs) > routing.MaxExceptions {
		xs = xs[:routing.MaxExceptions]
	}
	doc := routing.WriteExceptions(xs, ns)
	// 1. Untouched text reads back to the same list.
	d := doc.Read(doc.Text, ns)
	if len(d.Problems) != 0 || d.Changes() {
		t.Fatalf("round trip: %d problems, e.g. %+v; changes %v", len(d.Problems), d.Problems[:min(3, len(d.Problems))], d.Changes())
	}
	// 2. The same text typed into an editor that never wrote it finds the same nodes.
	fresh := routing.WriteExceptions(nil, ns).Read(doc.Text, ns)
	bad := 0
	for _, p := range fresh.Problems {
		if bad < 8 {
			t.Logf("fresh problem: line %d %q: %s %v", p.Line, p.Text, p.Reason, p.Choices)
		}
		bad++
	}
	wrong := 0
	for _, x := range fresh.List {
		for _, want := range xs {
			if want.Kind == x.Kind && want.Value == x.Value && !want.Dest.Equal(x.Dest) {
				wrong++
				if wrong < 5 {
					t.Logf("WRONG node: %s → %v, want %v", x.Value, x.Dest, want.Dest)
				}
			}
		}
	}
	if bad > 0 || wrong > 0 {
		t.Fatalf("read anew: %d problems, %d wrong nodes", bad, wrong)
	}
}

// The same names typed by hand, without quotes, find their nodes.
func TestExceptionsTextWithAwkwardNamesTypedByHand(t *testing.T) {
	subs := []string{"Aurora Cloud", "A/B", "机场, 主力", `Q"uote`, "#1 Sub", "🚀 飞", "x NODE:y"}
	names := []string{"Tokyo 01", "HK, IPLC [x2]", `a"b`, "#1", "/lead", "日本，东京", `C:\x`, "🇯🇵 东京 01",
		"no-resolve", "NODE:inner", "REGION:JP", "'q'", `"dq"`, "DIRECT", "a/b/c", "x ,y", "(.*)|^$"}
	var ns routing.Names
	for i, s := range subs {
		for _, n := range names {
			ns.Nodes = append(ns.Nodes, routing.NamedNode{ID: routing.NodeID{Sub: fmt.Sprint("s", i), Name: n}, SubName: s})
		}
	}
	doc := routing.WriteExceptions(nil, ns)
	var b strings.Builder
	type want struct{ sub, name string }
	var wants []want
	for i, s := range subs {
		for _, n := range names {
			k := len(wants)
			fmt.Fprintf(&b, "DOMAIN-SUFFIX,h%d.example,NODE:%s/%s\n", k, s, n)
			wants = append(wants, want{fmt.Sprint("s", i), n})
			fmt.Fprintf(&b, "DOMAIN-REGEX,^g%d$,NODE:%s/%s\n", k, s, n)
			wants = append(wants, want{fmt.Sprint("s", i), n})
		}
	}
	d := doc.Read(b.String(), ns)
	byLine := map[int]routing.BatchBad{}
	for _, p := range d.Problems {
		byLine[p.Line] = p
	}
	li := 0
	ok, problems, wrong := 0, 0, 0
	for line := 1; line <= len(wants); line++ {
		w := wants[line-1]
		if p, bad := byLine[line]; bad {
			problems++
			if problems <= 10 {
				t.Logf("problem line %d %q: %s", line, p.Text, p.Reason)
			}
			continue
		}
		x := d.List[li]
		li++
		if x.Dest.Node == nil || x.Dest.Node.Sub != w.sub || x.Dest.Node.Name != w.name {
			wrong++
			t.Logf("WRONG %s → %v want %s/%s", x.Value, x.Dest, w.sub, w.name)
			continue
		}
		ok++
	}
	if problems > 0 || wrong > 0 || ok != len(wants) {
		t.Fatalf("typed by hand: %d found, %d problems, %d wrong", ok, problems, wrong)
	}
}
