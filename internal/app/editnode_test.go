package app_test

import (
	"errors"
	"testing"

	"eggbox/internal/app"
	"eggbox/internal/library"
	"eggbox/internal/routing"
)

// An imported node is edited as its share link: the import keeps its place,
// the link is kept, and a node renamed by its link keeps every choice made of
// it.
func TestEditImportedNode(t *testing.T) {
	h := newHarness(t, nil)
	sub, err := h.svc.ImportNodes(h.ctx(), "Gateway", "socks5://127.0.0.1:1080#Gateway\n")
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := h.lib.Nodes(sub.ID)
	if len(nodes) != 1 || nodes[0].Link != "socks5://127.0.0.1:1080#Gateway" {
		t.Fatalf("imported %+v", nodes)
	}
	gw := routing.NodeID{Sub: sub.ID, Name: "Gateway", Hash: nodes[0].Key()}
	h.edit(func(st *routing.State) error {
		st.SetNodeAuto(gw, false)
		_, err := st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "corp.example", Dest: routing.ToNode(gw), Enabled: true})
		return err
	})

	// A new port, the same name: nothing else changes.
	stored, err := h.svc.EditImportedNode(h.ctx(), sub.ID, nodes[0].Key(), "  socks5://127.0.0.1:1081#Gateway  ")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Port != 1081 || stored.Link != "socks5://127.0.0.1:1081#Gateway" {
		t.Fatalf("stored %+v", stored)
	}
	if subs := h.lib.Subscriptions(); len(subs) != 1 || subs[0].ID != sub.ID {
		t.Fatalf("subscriptions %+v", subs)
	}

	// A new name: the exception and the preference follow it.
	stored, err = h.svc.EditImportedNode(h.ctx(), sub.ID, stored.Key(), "socks5://127.0.0.1:1081#Backup%20gateway")
	if err != nil {
		t.Fatal(err)
	}
	st := h.lib.Routing()
	if d := st.Exceptions[0].Dest; d.Node == nil || d.Node.Name != "Backup gateway" || d.Node.Sub != sub.ID {
		t.Fatalf("the exception goes to %+v", d.Node)
	}
	if !st.NodePref(routing.NodeID{Sub: sub.ID, Name: "Backup gateway"}).NoAuto {
		t.Fatalf("the preference was lost: %+v", st.Nodes)
	}

	// What cannot be used changes nothing.
	for _, tc := range []struct {
		link string
		want error
	}{
		{"", library.ErrBadLink},
		{"gopher://x", library.ErrBadLink},
		{"socks5://127.0.0.1:1#A\nsocks5://127.0.0.1:2#B", library.ErrBadLink},
		{"https://sub.example/subscription", library.ErrBadLink},
	} {
		if _, err := h.svc.EditImportedNode(h.ctx(), sub.ID, stored.Key(), tc.link); !errors.Is(err, tc.want) {
			t.Errorf("%q: %v", tc.link, err)
		}
	}
	if _, err := h.svc.EditImportedNode(h.ctx(), sub.ID, "nope", "socks5://127.0.0.1:1#A"); !errors.Is(err, library.ErrNoNode) {
		t.Errorf("a node that is not there: %v", err)
	}
	if got, _ := h.lib.Nodes(sub.ID); len(got) != 1 || got[0].Port != 1081 || got[0].Name != "Backup gateway" {
		t.Fatalf("a refused edit changed the import: %+v", got)
	}
	if ex := app.Explain(library.ErrBadLink); ex.Code != "node_bad_link" {
		t.Errorf("explain = %+v", ex)
	}
	if ex := app.Explain(library.ErrNodeTaken); ex.Code != "node_taken" {
		t.Errorf("explain = %+v", ex)
	}
}
