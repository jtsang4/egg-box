package routing_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"eggbox/internal/routing"
)

func TestCovers(t *testing.T) {
	e := func(k routing.EntryKind, v string) routing.Entry { return routing.Entry{Kind: k, Value: v}.Normalize() }
	for _, tc := range []struct {
		r, x routing.Entry
		want bool
	}{
		{e(routing.EntryDomainSuffix, "corp-intra.example"), e(routing.EntryDomainSuffix, "corp-intra.example"), true},
		{e(routing.EntryDomainSuffix, "corp-intra.example"), e(routing.EntryDomain, "a.corp-intra.example"), true},
		{e(routing.EntryDomainSuffix, "corp-intra.example"), e(routing.EntryDomainSuffix, "x.corp-intra.example"), true},
		{e(routing.EntryDomainSuffix, "corp-intra.example"), e(routing.EntryDomainSuffix, "notcorp-intra.example"), false},
		{e(routing.EntryDomainSuffix, "a.corp-intra.example"), e(routing.EntryDomainSuffix, "corp-intra.example"), false},
		{e(routing.EntryDomainKeyword, "portal"), e(routing.EntryDomainSuffix, "api.portalapp.example"), true},
		{e(routing.EntryDomainKeyword, "portal"), e(routing.EntryDomainKeyword, "portalapp"), true},
		{e(routing.EntryDomainKeyword, "portalapp"), e(routing.EntryDomainKeyword, "portal"), false},
		{e(routing.EntryDomainKeyword, "portal"), e(routing.EntryDomainRegex, "portal"), false},
		{e(routing.EntryDomain, "a.example"), e(routing.EntryDomainSuffix, "a.example"), false},
		{e(routing.EntryIPCIDR, "10.0.0.0/8"), e(routing.EntryIPCIDR, "10.99.0.0/24"), true},
		{e(routing.EntryIPCIDR, "10.99.0.0/24"), e(routing.EntryIPCIDR, "10.0.0.0/8"), false},
		{e(routing.EntryIPCIDR, "10.0.0.0/8"), e(routing.EntryIPCIDR, "10.1.2.3"), true},
		{e(routing.EntryIPCIDR, "::/0"), e(routing.EntryIPCIDR, "10.0.0.0/8"), false},
		{e(routing.EntryPort, "443"), e(routing.EntryPort, "443"), true},
		{e(routing.EntryProcessName, "curl"), e(routing.EntryProcessName, "Curl"), false},
	} {
		if got := routing.Covers(tc.r, tc.x); got != tc.want {
			t.Errorf("Covers(%v, %v) = %v", tc.r, tc.x, got)
		}
	}
}

func TestShadowed(t *testing.T) {
	st := routing.NewState()
	st.Exceptions = []routing.Exception{
		{ID: "x1", Kind: routing.EntryDomainKeyword, Value: "portal", Dest: routing.Direct(), Enabled: false},
		{ID: "x2", Kind: routing.EntryDomainSuffix, Value: "corp.example", Dest: routing.Direct(), Enabled: true},
		{ID: "x3", Kind: routing.EntryDomainKeyword, Value: "corp", Dest: routing.Follow(), Enabled: true},
	}
	it := routing.Item{Name: "Own", Sources: []routing.Source{
		routing.URLSource("https://example.com/list.txt", "", ""),
		routing.InlineSource([]routing.Entry{
			{Kind: routing.EntryDomainKeyword, Value: "portal"},        // only a switched-off exception
			{Kind: routing.EntryDomainSuffix, Value: "a.corp.example"}, // x2 first
			{Kind: routing.EntryDomainKeyword, Value: "mycorp"},        // x3
			{Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8"},
		}),
	}}
	got := st.Shadowed(it)
	if len(got) != 2 || got[0].Exception.ID != "x2" || got[0].Index != 2 || got[1].Exception.ID != "x3" || got[1].Entry.Value != "mycorp" {
		t.Fatalf("shadowed = %+v", got)
	}
}

func TestPrivateRangeAndClaims(t *testing.T) {
	for cidr, want := range map[string]bool{
		"10.0.0.0/8": true, "10.99.0.0/24": true, "10.1.2.3": true, "192.168.1.0/24": true, "172.20.0.0/16": true,
		"127.0.0.1": true, "fd00::/8": true, "fd12:3456::/32": true,
		"0.0.0.0/0": false, "8.0.0.0/7": false, "172.0.0.0/8": false, "100.64.0.0/10": false, "2001:db8:cd50::/48": false, "nope": false,
	} {
		if got := routing.PrivateRange(cidr); got != want {
			t.Errorf("PrivateRange(%s) = %v", cidr, got)
		}
	}
	own := routing.Item{Name: "Own", Sources: []routing.Source{
		routing.URLSource("https://example.com/private.txt", "", ""),
		routing.InlineSource([]routing.Entry{
			{Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8"}, {Kind: routing.EntryIPCIDR, Value: "8.8.8.8"},
			{Kind: routing.EntryDomainSuffix, Value: "corp.example"}, {Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8"},
			{Kind: routing.EntryIPCIDR, Value: "192.168.5.1"},
		}),
	}}
	if got := strings.Join(own.Claims(), " "); got != "10.0.0.0/8 192.168.5.1/32" {
		t.Fatalf("item claims = %s", got)
	}
	// The entries a provider's plan brought claim nothing, until the person
	// edits them.
	provider := routing.Item{ID: "i1", Name: "LAN", Dest: routing.Direct(), Enabled: true, Sources: []routing.Source{
		{ID: "n1", Kind: routing.SourceInline, Entries: []routing.Entry{{Kind: routing.EntryIPCIDR, Value: "192.168.0.0/16"}}},
	}}
	if got := provider.Claims(); len(got) != 0 {
		t.Fatalf("a provider's entries claim %v", got)
	}
	st := routing.NewState()
	if _, err := st.SaveAsMine("Mine"); err != nil {
		t.Fatal(err)
	}
	added, err := st.AddItem(provider, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEntries(added.ID, "n1", []routing.Entry{{Kind: routing.EntryIPCIDR, Value: "192.168.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	// Edited, the provider's own range stays the provider's: only what the
	// person adds claims.
	if row, _, _ := st.View().Row(added.ID); len(row.Item.Claims()) != 0 {
		t.Fatalf("the provider's range claims %v once the list was edited", row.Item.Claims())
	}
	if err := st.SetEntries(added.ID, "n1", []routing.Entry{{Kind: routing.EntryIPCIDR, Value: "192.168.0.0/16"}, {Kind: routing.EntryIPCIDR, Value: "10.8.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := st.View().Row(added.ID); strings.Join(row.Item.Claims(), " ") != "10.8.0.0/16" {
		t.Fatalf("after adding a range: claims %v", row.Item.Claims())
	}
	// Taken out, then written in again: now it is the person's.
	if err := st.SetEntries(added.ID, "n1", []routing.Entry{{Kind: routing.EntryIPCIDR, Value: "10.8.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEntries(added.ID, "n1", []routing.Entry{{Kind: routing.EntryIPCIDR, Value: "10.8.0.0/16"}, {Kind: routing.EntryIPCIDR, Value: "192.168.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := st.View().Row(added.ID); strings.Join(row.Item.Claims(), " ") != "10.8.0.0/16 192.168.0.0/16" {
		t.Fatalf("after writing it in again: claims %v", row.Item.Claims())
	}
	for x, want := range map[routing.Exception]string{
		{Kind: routing.EntryIPCIDR, Value: "10.99.0.0/24"}: "10.99.0.0/24",
		{Kind: routing.EntryIPCIDR, Value: "0.0.0.0/0"}:    "",
		{Kind: routing.EntryDomain, Value: "10.example"}:   "",
	} {
		if got := x.Claim(); got != want {
			t.Errorf("claim of %s = %q, want %q", x.Value, got, want)
		}
	}
}

// Sources the person wrote before sources said so are told from a provider's
// by their identifiers, in plans of the person's own and in what they laid
// over other plans; a provider's plan itself is left alone.
func TestMarkWritten(t *testing.T) {
	mine := routing.InlineSource([]routing.Entry{{Kind: routing.EntryIPCIDR, Value: "10.0.0.0/8"}})
	mine.Written = false // as stored before
	imported := routing.Source{ID: "n1x2y3z4w5v6u7", Kind: routing.SourceInline, Entries: []routing.Entry{{Kind: routing.EntryIPCIDR, Value: "192.168.0.0/16"}}}
	st := routing.NewState()
	st.Plans = []*routing.Plan{
		{ID: "p1", Kind: routing.PlanMine, Name: "Mine", Items: []routing.Item{{ID: "i1", Sources: []routing.Source{mine, imported}}}},
		{ID: "sub:s", Kind: routing.PlanSubscription, Name: "Sub", Items: []routing.Item{{ID: "i2", Sources: []routing.Source{mine}}}},
	}
	extra := mine
	extra.ID = "n0123456789"
	st.Overlays = map[string]*routing.Overlay{"builtin": {
		Items: []routing.Item{{ID: "i3", Sources: []routing.Source{mine, imported}}},
		Extra: map[string][]routing.Source{"ai": {extra}},
	}}
	for round := 0; round < 2; round++ {
		st.MarkWritten()
		got := []bool{
			st.Plans[0].Items[0].Sources[0].Written, st.Plans[0].Items[0].Sources[1].Written,
			st.Plans[1].Items[0].Sources[0].Written,
			st.Overlays["builtin"].Items[0].Sources[0].Written, st.Overlays["builtin"].Items[0].Sources[1].Written,
			st.Overlays["builtin"].Extra["ai"][0].Written,
		}
		if want := []bool{true, false, false, true, false, true}; !slices.Equal(got, want) {
			t.Fatalf("round %d: written = %v, want %v", round, got, want)
		}
	}
	// Upgrading a state of the first format marks them, once.
	old := routing.State{Version: 1, Plans: []*routing.Plan{{ID: "p1", Kind: routing.PlanMine, Items: []routing.Item{{ID: "i1", Sources: []routing.Source{mine}}}}}}
	if !old.Upgrade() || !old.Plans[0].Items[0].Sources[0].Written || old.Version != routing.FormatVersion {
		t.Fatalf("upgrade: %+v", old)
	}
	if old.Upgrade() {
		t.Fatal("a second upgrade changed something")
	}
	// An older version saved it and dropped the marks: they come back.
	old.Plans[0].Items[0].Sources[0].Written = false
	if !old.Upgrade() || !old.Plans[0].Items[0].Sources[0].Written {
		t.Fatal("the marks an older version dropped did not come back")
	}
}

// A range the provider put in one source does not stop the person's own
// source of the same item from claiming it, whichever comes first.
func TestInheritedStaysInItsSource(t *testing.T) {
	provider := routing.Source{ID: "n1", Kind: routing.SourceInline, Written: true, Inherited: []string{"192.168.0.0/16"},
		Entries: []routing.Entry{{Kind: routing.EntryIPCIDR, Value: "192.168.0.0/16"}}}
	mine := routing.InlineSource([]routing.Entry{{Kind: routing.EntryIPCIDR, Value: "192.168.0.0/16"}})
	for _, order := range [][]routing.Source{{provider, mine}, {mine, provider}} {
		if got := (routing.Item{Sources: order}).Claims(); len(got) != 1 || got[0] != "192.168.0.0/16" {
			t.Fatalf("claims = %v", got)
		}
	}
	if got := (routing.Item{Sources: []routing.Source{provider}}).Claims(); len(got) != 0 {
		t.Fatalf("the provider's range alone claims %v", got)
	}
}

// The indexed answer is the one Covers gives for every pair, first exception
// first.
func TestShadowedAgreesWithCovers(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	pick := func(xs []string) string { return xs[rng.IntN(len(xs))] }
	names := []string{"a.example", "b.a.example", "example", "corp.example", "x.corp.example", "portal.com", "api.portalapp.example", "portal", "corp"}
	cidrs := []string{"10.0.0.0/8", "10.1.0.0/16", "10.1.2.3/32", "192.168.0.0/16", "192.168.1.0/24", "fd00::/8", "fd00:1::/32"}
	kinds := []routing.EntryKind{routing.EntryDomain, routing.EntryDomainSuffix, routing.EntryDomainKeyword, routing.EntryIPCIDR, routing.EntryPort}
	entry := func() routing.Entry {
		k := kinds[rng.IntN(len(kinds))]
		switch k {
		case routing.EntryIPCIDR:
			return routing.Entry{Kind: k, Value: pick(cidrs)}.Normalize()
		case routing.EntryPort:
			return routing.Entry{Kind: k, Value: pick([]string{"22", "443"})}
		}
		return routing.Entry{Kind: k, Value: pick(names)}.Normalize()
	}
	for round := 0; round < 300; round++ {
		st := routing.NewState()
		for i := 0; i < 1+rng.IntN(8); i++ {
			e := entry()
			st.Exceptions = append(st.Exceptions, routing.Exception{ID: fmt.Sprint("x", i), Kind: e.Kind, Value: e.Value, Dest: routing.Direct(), Enabled: rng.IntN(4) > 0})
		}
		var entries []routing.Entry
		for i := 0; i < 12; i++ {
			entries = append(entries, entry())
		}
		got := st.Shadowed(routing.Item{Sources: []routing.Source{routing.InlineSource(entries)}})
		var want []routing.Shadow
		for _, e := range entries {
			for i, x := range st.Exceptions {
				if x.Enabled && routing.Covers(x.Entry().Normalize(), e) {
					want = append(want, routing.Shadow{Entry: e, Exception: x, Index: i + 1})
					break
				}
			}
		}
		if len(got) != len(want) {
			t.Fatalf("round %d: got %d, want %d\n%+v\n%+v", round, len(got), len(want), got, want)
		}
		for i := range got {
			if got[i].Entry != want[i].Entry || got[i].Index != want[i].Index {
				t.Fatalf("round %d entry %d: got %+v, want %+v", round, i, got[i], want[i])
			}
		}
	}
}
