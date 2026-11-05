package library

import (
	"encoding/json"
	"strconv"
	"strings"

	"eggbox/internal/routing"
	"eggbox/internal/subscription"
)

// legacyRuleUpdateHours was the default interval of rule data checks before
// plans existed. A library that still has it never chose an interval, so it
// takes the new default.
const legacyRuleUpdateHours = 72

// migrate brings the data read from a library file of an older format up to
// date. Each step starts from the file's contents, never from a half-migrated
// state, so a migration that was interrupted before the file was saved simply
// runs again, and a file that is already current is not touched.
func (l *Library) migrate(raw []byte, from int) error {
	if from < 2 {
		return l.migrateV1(raw)
	}
	return nil
}

// migrateV1 replaces the three routing presets, the ad switch, the list of
// custom rules and the single selected node by a routing state: the shipped
// plan with the person's choices laid over it, the rules as exceptions and the
// selection as the main exit.
func (l *Library) migrateV1(raw []byte) error {
	var old struct {
		Prefs struct {
			Preset   string `json:"preset"`
			BlockAds bool   `json:"blockAds"`
		} `json:"prefs"`
		Rules []struct {
			Kind   routing.EntryKind `json:"kind"`
			Value  string            `json:"value"`
			Target string            `json:"target"`
		} `json:"rules"`
		Settings struct {
			SelectedNode    string `json:"selectedNode"`
			RuleUpdateHours *int   `json:"ruleUpdateHours"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		return err
	}
	st := routing.NewState()

	// The presets are now choices inside the shipped plan.
	switch old.Prefs.Preset {
	case "proxy-all":
		if err := st.SetItemDest("b-cn", routing.Follow()); err != nil {
			return err
		}
	case "foreign-only":
		if err := st.SetFallback(routing.Direct()); err != nil {
			return err
		}
	}
	if old.Prefs.BlockAds {
		if err := st.SetItemEnabled("b-ads", true); err != nil {
			return err
		}
	}

	// Custom rules keep their order and become exceptions.
	dropped := 0
	for _, r := range old.Rules {
		dest := routing.Follow()
		switch strings.ToLower(r.Target) {
		case "direct":
			dest = routing.Direct()
		case "reject":
			dest = routing.Reject()
		}
		x := routing.Exception{Kind: r.Kind, Value: strings.TrimSpace(r.Value), Dest: dest, Enabled: true}
		x.Value = x.Entry().Normalize().Value
		if x.Valid() != nil {
			dropped++
			continue
		}
		if _, err := st.AddException(x); err != nil {
			dropped++
		}
	}
	if dropped > 0 {
		l.addNoticeLocked("migrate_rules_dropped", map[string]string{"count": strconv.Itoa(dropped)})
	}

	// The selected node becomes the main exit.
	switch sel := old.Settings.SelectedNode; {
	case sel == "":
		// Automatic is the new state's default.
	case strings.HasPrefix(sel, "region-"):
		if code := strings.ToUpper(strings.TrimPrefix(sel, "region-")); len(code) == 2 {
			_ = st.SetMain(routing.ToRegion(code))
		}
	default:
		if id, ok := l.nodeByHash(sel); ok {
			_ = st.SetMain(routing.ToNode(id))
		} else {
			l.addNoticeLocked("migrate_node_lost", nil)
		}
	}

	l.data.Routing = st
	if old.Settings.RuleUpdateHours != nil && *old.Settings.RuleUpdateHours == legacyRuleUpdateHours {
		l.data.Settings.RuleUpdateHours = DefaultRuleUpdateHours
	}
	return nil
}

// nodeByHash finds the node whose settings have the given hash, enabled
// subscriptions first.
func (l *Library) nodeByHash(hash string) (routing.NodeID, bool) {
	for _, enabled := range []bool{true, false} {
		for _, s := range l.data.Subscriptions {
			if s.Enabled != enabled {
				continue
			}
			nodes, err := l.nodesLocked(s.ID)
			if err != nil {
				continue
			}
			for _, n := range nodes {
				if n.Key() == hash {
					return routing.NodeID{Sub: s.ID, Name: n.Name, Hash: hash, Region: subscription.Region(n.Name)}, true
				}
			}
		}
	}
	return routing.NodeID{}, false
}
