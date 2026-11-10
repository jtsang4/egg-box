package library_test

import (
	"context"
	"errors"
	"testing"

	"eggbox/internal/library"
	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

func importLinks(t *testing.T, l *library.Library, text string) library.Subscription {
	t.Helper()
	res, err := subscription.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := l.AddLocal("Gateway", res)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// A node of an import is replaced in place: the import and its other nodes
// stay, the new node keeps the link it was read from, and so does the file.
func TestEditNode(t *testing.T) {
	dir := t.TempDir()
	l := openWith(t, dir, library.NewMemorySecrets())
	sub := importLinks(t, l, "socks5://127.0.0.1:1080#Gateway\ntrojan://pw@a.example:443#Other\n")
	nodes, _ := l.Nodes(sub.ID)
	if nodes[0].Link != "socks5://127.0.0.1:1080#Gateway" {
		t.Fatalf("the import did not keep the link: %q", nodes[0].Link)
	}

	stored, err := l.EditNode(sub.ID, nodes[0].Key(), " socks5://127.0.0.1:1081#Gateway ")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Port != 1081 || stored.Name != "Gateway" || stored.Link != "socks5://127.0.0.1:1081#Gateway" {
		t.Fatalf("stored %+v", stored)
	}
	after := openWith(t, dir, library.NewMemorySecrets())
	got, _ := after.Nodes(sub.ID)
	if len(got) != 2 || got[0].Port != 1081 || got[0].Link != stored.Link || got[1].Name != "Other" {
		t.Fatalf("after reopening: %+v", got)
	}

	// What cannot be stored leaves the import as it was.
	for _, tc := range []struct {
		name, id, key, link string
		want                error
	}{
		{"the same server as another node", sub.ID, got[0].Key(), "trojan://pw@a.example:443#Elsewhere", library.ErrNodeTaken},
		{"the name of another node", sub.ID, got[0].Key(), "socks5://127.0.0.1:1080#other", library.ErrNodeTaken},
		{"not a link", sub.ID, got[0].Key(), "gopher://x", library.ErrBadLink},
		{"two links", sub.ID, got[0].Key(), "socks5://127.0.0.1:1#A\nsocks5://127.0.0.1:2#B", library.ErrBadLink},
		{"nothing", sub.ID, got[0].Key(), "  ", library.ErrBadLink},
		{"a node that is not there", sub.ID, "nope", "socks5://127.0.0.1:1#A", library.ErrNoNode},
		{"no such import", "nope", got[0].Key(), "socks5://127.0.0.1:1#A", library.ErrNotFound},
	} {
		if _, err := after.EditNode(tc.id, tc.key, tc.link); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if again, _ := after.Nodes(sub.ID); again[0].Port != 1081 || again[1].Name != "Other" {
		t.Fatalf("a refused edit changed the import: %+v", again)
	}
}

// An import may have renamed a node to tell it from another of the same
// name; editing anything but the name keeps that name.
func TestEditNodeKeepsANameTheImportGave(t *testing.T) {
	l := openWith(t, t.TempDir(), library.NewMemorySecrets())
	sub := importLinks(t, l, "socks5://127.0.0.1:1#HK\nsocks5://127.0.0.1:2#HK\n")
	nodes, _ := l.Nodes(sub.ID)
	if nodes[1].Name != "HK 2" {
		t.Fatalf("names %q %q", nodes[0].Name, nodes[1].Name)
	}
	stored, err := l.EditNode(sub.ID, nodes[1].Key(), "socks5://127.0.0.1:3#HK")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "HK 2" || stored.Port != 3 {
		t.Fatalf("stored %+v", stored)
	}
	// A name the person writes is theirs.
	if stored, err = l.EditNode(sub.ID, stored.Key(), "socks5://127.0.0.1:3#Backup"); err != nil || stored.Name != "Backup" {
		t.Fatalf("stored %+v, %v", stored, err)
	}
}

// A new name takes every choice made of the node with it, in the same change,
// the region the person set for it included.
func TestEditNodeRenamesTheChoicesMadeOfIt(t *testing.T) {
	l := openWith(t, t.TempDir(), library.NewMemorySecrets())
	sub := importLinks(t, l, "socks5://127.0.0.1:1#Gateway\n")
	nodes, _ := l.Nodes(sub.ID)
	gw := routing.NodeID{Sub: sub.ID, Name: "Gateway", Hash: nodes[0].Key()}
	if err := l.UpdateRouting(func(st *routing.State) error {
		st.SetNodeRegion(gw, "JP")
		_, err := st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "corp.example", Dest: routing.ToNode(gw), Enabled: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.EditNode(sub.ID, nodes[0].Key(), "socks5://127.0.0.1:1#Backup"); err != nil {
		t.Fatal(err)
	}
	st := l.Routing()
	d := st.Exceptions[0].Dest.Node
	if d == nil || d.Name != "Backup" || d.Region != "JP" {
		t.Fatalf("the exception goes to %+v", d)
	}
	if p := st.NodePref(routing.NodeID{Sub: sub.ID, Name: "Backup"}); p.Region != "JP" {
		t.Fatalf("prefs %+v", st.Nodes)
	}
}

// A subscription fetched from a link cannot have its nodes edited (the next
// refresh would undo it), and does not keep the links its nodes came as.
func TestSubscriptionNodesAreNotEditedAndKeepNoLinks(t *testing.T) {
	p := newProvider(t, linkList("HK 01", "JP 01"))
	l := open(t, t.TempDir(), nil)
	sub, err := l.AddRemote("", p.link(), library.RemoteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Refresh(context.Background(), sub.ID, library.RefreshOptions{Client: p.srv.Client()}); err != nil {
		t.Fatal(err)
	}
	nodes, _ := l.Nodes(sub.ID)
	if len(nodes) != 2 {
		t.Fatalf("nodes %+v", nodes)
	}
	for _, n := range nodes {
		if n.Link != "" {
			t.Fatalf("a subscription's node keeps its link: %q", n.Name)
		}
	}
	if _, err := l.EditNode(sub.ID, nodes[0].Key(), "socks5://127.0.0.1:1080#X"); !errors.Is(err, library.ErrNotImported) {
		t.Fatalf("err = %v", err)
	}
}
