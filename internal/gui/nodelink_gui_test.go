package gui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// node finds a node of the fixture by name.
func (h *harness) nodeNamed(name string) nodeEntry {
	h.t.Helper()
	for _, n := range h.app.liveNodes() {
		if n.Name == name {
			return n
		}
	}
	h.t.Fatalf("no node %q", name)
	return nodeEntry{}
}

// linkDialog is the node link dialog that is open.
func (h *harness) linkDialog() *nodeLinkDialog {
	h.t.Helper()
	d, ok := h.app.dlg.(*nodeLinkDialog)
	if !ok || !h.app.dlgOpen {
		h.t.Fatal("the node link dialog is not open")
	}
	return d
}

// replaceLink puts text in place of the link in the dialog, which has the
// keyboard when it opens.
func (h *harness) replaceLink(text string) {
	h.t.Helper()
	h.ui.Key(ui.Cmd, ui.KeyA)
	h.typeText(text)
	h.settle()
}

func TestImportedNodeIsEditedAsItsLink(t *testing.T) {
	h := nodesHarness(t, nil)
	h.rightClick("Osaka 01")
	h.mustNotHave("Edit link…") // a subscription's node is the provider's
	h.closeMenu()

	h.rightClick("Sydney 01")
	h.mustHave("Edit link…")
	h.pick("Edit")
	h.mustHave("Edit node", "Sydney 01 · My nodes", "SOCKS · 127.0.0.1:1080  ·  Sydney 01")
	if got := h.linkDialog().link; got != "socks5://127.0.0.1:1080#Sydney%2001" {
		t.Fatalf("the dialog shows %q", got)
	}

	h.replaceLink("socks5://127.0.0.1:1081#Sydney%2001")
	h.mustHave("SOCKS · 127.0.0.1:1081  ·  Sydney 01")
	h.submitDialog("Save")
	if !h.be.took("edit-node sub-local") {
		t.Fatalf("nothing was saved: %v", h.be.called())
	}
	h.mustNotHave("Edit node")
	h.mustHave("Node updated")
	if n := h.nodeNamed("Sydney 01"); n.Port != 1081 {
		t.Fatalf("node = %+v", n)
	}
}

// A new name in the link takes every choice made of the node with it.
func TestRenamingAnImportedNodeKeepsItsChoices(t *testing.T) {
	h := nodesHarness(t, func(be *fixtureBackend, _ *Options) {
		be.arrange(func(st *routing.State) {
			for _, n := range be.nodes {
				if n.Node.Name == "Sydney 01" {
					if _, err := st.AddException(routing.Exception{Kind: routing.EntryDomainSuffix, Value: "corp.example", Dest: routing.ToNode(n.ID()), Enabled: true}); err != nil {
						panic(err)
					}
				}
			}
		})
	})
	h.app.openImportedNode("sub-local", "My nodes", h.nodeNamed("Sydney 01").Key)
	h.settle()
	h.replaceLink("socks5://127.0.0.1:1080#Gateway")
	h.submitDialog("Save")
	x, ok := h.exception("corp.example")
	if !ok || x.Dest.Node == nil || x.Dest.Node.Name != "Gateway" {
		t.Fatalf("the exception goes to %+v", x.Dest.Node)
	}
}

// A node imported without its link says so; what is not one link cannot be saved.
func TestImportedNodeWithoutItsLink(t *testing.T) {
	h := nodesHarness(t, nil)
	h.app.openImportedNode("sub-local", "My nodes", h.nodeNamed("Amsterdam 01").Key)
	h.settle()
	h.mustHave("This node was imported without keeping its link (by an earlier version, or from a configuration file). Paste a new link to replace it.")
	h.replaceLink("gopher://nope")
	h.mustHave("That is not a node link the app can read. Give just one, such as socks5://127.0.0.1:1080#Name.")
	if h.linkDialog().read != nil {
		t.Fatal("an unreadable link can be saved")
	}
	h.replaceLink("socks5://127.0.0.1:1#A\nsocks5://127.0.0.1:2#B")
	if h.linkDialog().read != nil {
		t.Fatal("two links can be saved")
	}
	h.replaceLink("trojan://pw@new.example:443#Amsterdam%2001")
	h.submitDialog("Save")
	if n := h.nodeNamed("Amsterdam 01"); n.Server != "new.example" {
		t.Fatalf("node = %+v", n)
	}
}

// The imported card's menu leads to its nodes; a subscription's does not.
func TestImportCardMenuEditsItsNodes(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 1200})
	h.click(h.app.t("nav.subscriptions"))
	h.settle()
	h.clickOnLine("Aurora Cloud", "More")
	h.mustNotHave("Edit node…")
	h.closeMenu()
	h.clickOnLine("My nodes", "More")
	h.mustHave("Edit node…")
	h.pick("Edit#1", "Amsterdam")
	h.mustHave("Edit node", "Amsterdam 01 · My nodes")
}
