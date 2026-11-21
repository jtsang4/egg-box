package library

import (
	"testing"

	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

func TestSubscriptionRelayPersistAndRename(t *testing.T) {
	dir := t.TempDir()
	lib, err := Open(dir, Options{Secrets: NewMemorySecrets()})
	if err != nil {
		t.Fatal(err)
	}
	res1, err := subscription.Parse([]byte("socks5://127.0.0.1:1080#Gateway"))
	if err != nil {
		t.Fatal(err)
	}
	s1, err := lib.AddLocal("Gateways", res1)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := subscription.Parse([]byte("trojan://pw@us.example.com:443#US Home"))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := lib.AddLocal("Homes", res2)
	if err != nil {
		t.Fatal(err)
	}

	gwID := routing.NodeID{Sub: s1.ID, Name: "Gateway", Hash: res1.Nodes[0].Key()}
	// Self-subscription relay must be refused.
	if err := lib.Edit(s1.ID, func(s *Subscription) {
		r := routing.RelayNodeVal(gwID)
		s.Relay = &r
	}); err == nil {
		t.Fatal("expected error for relaying a subscription through its own node")
	}

	// Set s2's default relay to Gateway.
	if err := lib.Edit(s2.ID, func(s *Subscription) {
		r := routing.RelayNodeVal(gwID)
		s.Relay = &r
	}); err != nil {
		t.Fatal(err)
	}

	// ActiveNodes carries SubRelay.
	active, err := lib.ActiveNodes()
	if err != nil {
		t.Fatal(err)
	}
	var found *ActiveNode
	for i := range active {
		if active[i].SubID == s2.ID {
			found = &active[i]
		}
	}
	if found == nil || found.SubRelay == nil || found.SubRelay.Kind != routing.RelayNode || found.SubRelay.Node.Name != "Gateway" {
		t.Fatalf("ActiveNode SubRelay = %+v", found)
	}

	// Renaming the imported Gateway node updates s2's default relay.
	if _, err := lib.EditNode(s1.ID, res1.Nodes[0].Key(), "socks5://127.0.0.1:1081#Gateway New"); err != nil {
		t.Fatal(err)
	}
	gotS2, _ := lib.Subscription(s2.ID)
	if gotS2.Relay == nil || gotS2.Relay.Node == nil || gotS2.Relay.Node.Name != "Gateway New" {
		t.Fatalf("after EditNode rename, s2.Relay = %+v", gotS2.Relay)
	}

	// Survives reopen from disk.
	lib2, err := Open(dir, Options{Secrets: NewMemorySecrets()})
	if err != nil {
		t.Fatal(err)
	}
	gotS2, _ = lib2.Subscription(s2.ID)
	if gotS2.Relay == nil || gotS2.Relay.Node == nil || gotS2.Relay.Node.Name != "Gateway New" {
		t.Fatalf("after reopen, s2.Relay = %+v", gotS2.Relay)
	}

	// RefreshSubRelays follows provider rename by hash without bumping version when unchanged.
	v0 := lib2.Version()
	if lib2.RefreshSubRelays([]routing.LiveNode{{Sub: s1.ID, Name: "Gateway New", Hash: gotS2.Relay.Node.Hash, Region: ""}}) {
		t.Fatal("unchanged refresh should return false")
	}
	if lib2.Version() != v0 {
		t.Fatal("unchanged refresh must not bump library version")
	}
	if !lib2.RefreshSubRelays([]routing.LiveNode{{Sub: s1.ID, Name: "Gateway Renamed", Hash: gotS2.Relay.Node.Hash, Region: "HK"}}) {
		t.Fatal("renamed refresh should return true")
	}
	gotS2, _ = lib2.Subscription(s2.ID)
	if gotS2.Relay.Node.Name != "Gateway Renamed" || gotS2.Relay.Node.Region != "HK" {
		t.Fatalf("after RefreshSubRelays, s2.Relay = %+v", gotS2.Relay)
	}
}
