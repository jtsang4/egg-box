package gui

import (
	"testing"

	"eggbox/internal/routing"
)

// withImportNotes marks two items of the plan Aurora Cloud brought the way its
// import does: Streaming went through a group that picks among regions, and
// Games had its rules apart.
func withImportNotes(be *fixtureBackend, _ *Options) {
	be.arrange(func(st *routing.State) {
		for _, p := range st.Plans {
			if p.ID != routing.SubPlanID("sub-aurora") {
				continue
			}
			p.Items[0].Notes = []string{routing.NoteMultiRegionAuto}
			p.Items[1].Notes = []string{routing.NoteReordered}
		}
	})
}

// What making an item from a provider's plan changed where the person could
// not tell is said on the item's page: the rules the plan had apart, and the
// choice of region it could not carry over. The second explains why the item
// follows the main exit, so it is gone once the person chose another way.
func TestItemDialogSaysWhatTheImportDidToTheItem(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: withImportNotes})
	h.click("Recommended")
	h.pick("Aurora Cloud’s plan")
	const reordered = "In the original plan, other items’ rules came between this item’s rules. Here they are together, so a few of them may be checked earlier or later than before."
	const multi = "The original plan picked the fastest node among several regions for this item. That is not a single destination, so it follows the main exit here."

	h.click("Local sites")
	h.mustHave("Goes to")
	h.mustNotHave(reordered, multi)
	h.click("Done")

	h.click("Games")
	h.mustHave("Goes to", reordered)
	h.mustNotHave(multi)
	h.click("Done")

	h.click("Streaming")
	h.mustHave("Goes to", multi)
	h.mustNotHave(reordered)
	h.chooseInDialog("Follow main exit", "Direct")
	h.mustNotHave(multi)
	if got := h.row("aur-1").Item.Dest.Kind; got != routing.DestDirect {
		t.Fatalf("Streaming goes %s", got)
	}
	// The note belongs to the plan's item: another choice does not remove it,
	// and going back to following the main exit brings it back.
	h.chooseInDialog("Direct", "Follow main exit")
	h.mustHave(multi)
}

func TestItemDialogSaysWhatTheImportDidToTheItemInChinese(t *testing.T) {
	h := routingHarness(t, harnessOpt{setup: withImportNotes, lang: "zh"})
	h.click("内置推荐")
	// The entries of the menu start with a quotation mark, which no key types:
	// the plan Aurora Cloud brought is the second, and the first Down only
	// reaches the first.
	h.pick("“#2")
	h.click("Games")
	h.mustHave("原方案里，其他分流项的规则夹在这一项的规则中间。这里把它们放在了一起，个别规则的先后顺序可能与原方案略有不同。")
	h.click("完成")
	h.click("Streaming")
	h.mustHave("原方案对这一项用的是跨多个地区的自动选择，没有单一的去向，所以这里改为跟随主出口。")
}
