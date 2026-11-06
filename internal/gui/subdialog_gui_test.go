package gui

import (
	"testing"

	"eggbox/internal/library"
)

// sub is a subscription of the fixture as the library keeps it now.
func (h *harness) sub(id string) library.Subscription {
	h.t.Helper()
	for _, s := range h.be.Subscriptions() {
		if s.ID == id {
			return s
		}
	}
	h.t.Fatalf("the library has no subscription %q", id)
	return library.Subscription{}
}

// The two settings of a subscription that the node choices depend on: a short
// name to tell its nodes apart from the ones of another subscription, and
// whether its nodes take part in the automatic choice.
func TestSubscriptionDialogSetsTheShortNameAndTheAutomaticChoice(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 1100})
	h.click("Subscriptions")
	h.settle()
	before := h.sub("sub-aurora")
	if before.Alias != "" || before.NoAuto {
		t.Fatalf("the fixture starts with alias %q and NoAuto %v", before.Alias, before.NoAuto)
	}

	h.app.openEditSubscription(before)
	h.settle()
	h.mustHave("Edit subscription", "Short name", "Use in the automatic choice")
	h.tabTo("Optional") // the short name
	h.typeText("Aur")
	h.click("Use in the automatic choice")
	h.submitDialog("Save")

	h.mustNotHave("Edit subscription")
	after := h.sub("sub-aurora")
	if after.Alias != "Aur" || !after.NoAuto {
		t.Fatalf("saved alias %q and NoAuto %v, want %q and true", after.Alias, after.NoAuto, "Aur")
	}

	// The dialog shows what was saved, and putting it back works the same way.
	h.app.openEditSubscription(after)
	h.settle()
	h.mustHave("Aur")
	h.click("Use in the automatic choice")
	h.submitDialog("Save")
	if again := h.sub("sub-aurora"); again.NoAuto || again.Alias != "Aur" {
		t.Fatalf("after switching it back: alias %q and NoAuto %v", again.Alias, again.NoAuto)
	}
}

// A subscription is added with them as well, in the same dialog.
func TestSubscriptionDialogAddsWithAShortNameAndOutOfTheAutomaticChoice(t *testing.T) {
	h := newHarness(t, harnessOpt{h: 1100})
	h.click("Subscriptions")
	h.click("Add subscription")
	h.settle()
	h.typeText("https://sub.example.invalid/abc?token=demo")
	h.tabTo("Optional") // the short name
	h.typeText("Zed")
	h.click("Use in the automatic choice")
	h.submitDialog("Add subscription")

	if !h.be.took("add-sub") {
		t.Fatalf("calls: %v", h.be.called())
	}
	h.mustNotHave("Add a subscription")
	added := h.sub("sub-new")
	if added.Alias != "Zed" || !added.NoAuto {
		t.Fatalf("the new subscription has alias %q and NoAuto %v, want %q and true", added.Alias, added.NoAuto, "Zed")
	}
}
