package routing

import "testing"

func TestResolveRelayPrecedence(t *testing.T) {
	hk := NodeID{Sub: "s1", Name: "HK 01", Hash: "h1", Region: "HK"}
	us := NodeID{Sub: "s2", Name: "US Home", Hash: "h2", Region: "US"}
	subRelay := RelayRegionVal("SG")
	provRelay := RelayNodeVal(hk)

	st := NewState()
	// 1. No override, no sub default, no provider -> direct
	eff := ResolveRelay(us, st, nil, nil)
	if eff.Active() || eff.Source != RelaySourceNone || eff.Relay.Kind != RelayDirect {
		t.Fatalf("plain = %+v", eff)
	}
	// 2. Provider relay applies when no sub or node override
	eff = ResolveRelay(us, st, nil, &provRelay)
	if !eff.Active() || eff.Source != RelaySourceProvider || eff.Relay.Kind != RelayNode || !eff.Relay.Node.Same(hk) {
		t.Fatalf("provider = %+v", eff)
	}
	// 3. Subscription default beats provider relay
	eff = ResolveRelay(us, st, &subRelay, &provRelay)
	if !eff.Active() || eff.Source != RelaySourceSub || eff.Relay.Kind != RelayRegion || eff.Relay.Region != "SG" {
		t.Fatalf("sub = %+v", eff)
	}
	// 4. Per-node Direct override beats both subscription default and provider relay
	dir := RelayDirectVal()
	if err := st.SetNodeRelay(us, &dir); err != nil {
		t.Fatal(err)
	}
	eff = ResolveRelay(us, st, &subRelay, &provRelay)
	if eff.Active() || eff.Source != RelaySourceNode || eff.Relay.Kind != RelayDirect {
		t.Fatalf("node direct override = %+v", eff)
	}
	// 5. Clearing per-node override falls back to subscription default
	if err := st.SetNodeRelay(us, nil); err != nil {
		t.Fatal(err)
	}
	eff = ResolveRelay(us, st, &subRelay, &provRelay)
	if eff.Source != RelaySourceSub || eff.Relay.Region != "SG" {
		t.Fatalf("back to sub = %+v", eff)
	}
	// 6. Self-relay is refused
	self := RelayNodeVal(us)
	if err := st.SetNodeRelay(us, &self); err == nil {
		t.Error("expected error for self relay")
	}
}

func TestRelayRenameAndRefreshIdentities(t *testing.T) {
	hk := NodeID{Sub: "s1", Name: "HK 01", Hash: "hhk", Region: "HK"}
	us := NodeID{Sub: "s2", Name: "US Home", Hash: "hus", Region: "US"}
	st := NewState()
	r := RelayNodeVal(hk)
	if err := st.SetNodeRelay(us, &r); err != nil {
		t.Fatal(err)
	}

	// Rename the relay node: US Home's relay follows it.
	hkRenamed := NodeID{Sub: "s1", Name: "HK Main", Hash: "hhk2", Region: "HK"}
	if !st.RenameNode("s1", "HK 01", hkRenamed) {
		t.Fatal("RenameNode returned false")
	}
	got := st.NodePref(us).Relay
	if got == nil || got.Node == nil || got.Node.Name != "HK Main" || got.Node.Hash != "hhk2" {
		t.Fatalf("after rename relay = %+v", got)
	}

	// RefreshIdentities follows a provider rename via hash.
	live := []LiveNode{
		{Sub: "s1", Name: "HK Fresh", Hash: "hhk2", Region: "HK"},
		{Sub: "s2", Name: "US Home", Hash: "hus", Region: "US"},
	}
	if !st.RefreshIdentities(live) {
		t.Fatal("RefreshIdentities returned false")
	}
	got = st.NodePref(us).Relay
	if got == nil || got.Node == nil || got.Node.Name != "HK Fresh" {
		t.Fatalf("after refresh relay = %+v", got)
	}

	// Clone must deep copy Relay.
	cp := st.Clone()
	cp.Nodes[us.Key()].Relay.Node.Name = "Mutated"
	if st.NodePref(us).Relay.Node.Name != "HK Fresh" {
		t.Fatal("Clone did not deep copy NodePref.Relay")
	}
}

func TestRelayDependents(t *testing.T) {
	hk1 := NodeID{Sub: "s1", Name: "HK 01", Hash: "h1", Region: "HK"}
	hk2 := NodeID{Sub: "s1", Name: "HK 02", Hash: "h2", Region: "HK"}
	us1 := NodeID{Sub: "s2", Name: "US Home", Hash: "h3", Region: "US"}
	jp1 := NodeID{Sub: "s3", Name: "JP Home", Hash: "h4", Region: "JP"}

	st := NewState()
	r1 := RelayNodeVal(hk1)
	_ = st.SetNodeRelay(us1, &r1)
	sub3Relay := RelayNodeVal(hk2)

	nodes := []RelayNodeInfo{
		{ID: hk1, Name: "HK 01", SubID: "s1"},
		{ID: hk2, Name: "HK 02", SubID: "s1"},
		{ID: us1, Name: "US Home", SubID: "s2"},
		{ID: jp1, Name: "JP Home", SubID: "s3", SubRelay: &sub3Relay},
	}
	subNames := map[string]string{"s1": "Airport", "s2": "Mine", "s3": "Home Sub"}
	subRelays := map[string]*Relay{"s3": &sub3Relay}

	deps := RelayDependentsOfNode(hk1, nodes, subNames, subRelays, st)
	if len(deps) != 1 || deps[0].IsSub || deps[0].Name != "US Home" {
		t.Fatalf("deps of hk1 = %+v", deps)
	}
	deps2 := RelayDependentsOfNode(hk2, nodes, subNames, subRelays, st)
	if len(deps2) != 1 || !deps2[0].IsSub || deps2[0].Name != "Home Sub" {
		t.Fatalf("deps of hk2 = %+v", deps2)
	}
	subDeps := RelayDependentsOfSub("s1", nodes, subNames, subRelays, st)
	if len(subDeps) != 2 {
		t.Fatalf("deps of s1 = %+v, want 2", subDeps)
	}
	// If hk1 and hk2 both have explicit Direct overrides, s1 has no dependents affected by default relay.
	dir := RelayDirectVal()
	_ = st.SetNodeRelay(hk1, &dir)
	_ = st.SetNodeRelay(hk2, &dir)
	if got := RelayDependentsOfSub("s1", nodes, subNames, subRelays, st); len(got) != 0 {
		t.Fatalf("with direct overrides, deps of s1 = %+v", got)
	}
}
