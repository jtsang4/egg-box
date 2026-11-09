package routing

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxExceptions bounds the exceptions list: every one becomes a rule of its
// own in the core. A long list belongs in an item of hand-written entries.
const MaxExceptions = 1000

// ErrTooManyExceptions means the list is full.
var ErrTooManyExceptions = fmt.Errorf("there can be at most %d exceptions; put a long list into a hand-written item instead", MaxExceptions)

// ErrNoException means no exception has the id.
var ErrNoException = errors.New("there is no such exception")

// AddException appends an exception. The exception gets an id.
func (s *State) AddException(x Exception) (Exception, error) {
	x.Value = strings.TrimSpace(x.Value)
	if err := x.Valid(); err != nil {
		return Exception{}, err
	}
	if len(s.Exceptions) >= MaxExceptions {
		return Exception{}, ErrTooManyExceptions
	}
	e := x.Entry().Normalize()
	x.Kind, x.Value, x.Resolver = e.Kind, e.Value, NormalizeResolver(x.Resolver)
	if x.ID == "" {
		x.ID = NewID("x")
	}
	x.Dest = x.Dest.Clone()
	s.Exceptions = append(s.Exceptions, x)
	return x, nil
}

// UpsertException sets where an entry goes: a new exception, or the
// destination of the one that already has this match (and is then switched on).
func (s *State) UpsertException(e Entry, d Dest) (Exception, error) {
	if x, ok := s.ExceptionFor(e); ok {
		if err := d.Valid(false); err != nil {
			return Exception{}, err
		}
		i := s.exception(x.ID)
		s.Exceptions[i].Dest, s.Exceptions[i].Enabled = d.Clone(), true
		return s.Exceptions[i], nil
	}
	e = e.Normalize()
	return s.AddException(Exception{Kind: e.Kind, Value: e.Value, Dest: d, Enabled: true})
}

func (s *State) exception(id string) int {
	return slices.IndexFunc(s.Exceptions, func(x Exception) bool { return x.ID == id })
}

// UpdateException replaces an exception's match and destination.
func (s *State) UpdateException(id string, x Exception) error {
	i := s.exception(id)
	if i < 0 {
		return ErrNoException
	}
	x.ID, x.Value = id, strings.TrimSpace(x.Value)
	if err := x.Valid(); err != nil {
		return err
	}
	e := x.Entry().Normalize()
	x.Kind, x.Value, x.Resolver = e.Kind, e.Value, NormalizeResolver(x.Resolver)
	x.Dest = x.Dest.Clone()
	s.Exceptions[i] = x
	return nil
}

// SetExceptionResolver changes how the names one exception matches are
// looked up: "" as usual, dnsspec.Exit, or a DNS server.
func (s *State) SetExceptionResolver(id, resolver string) error {
	i := s.exception(id)
	if i < 0 {
		return ErrNoException
	}
	x := s.Exceptions[i]
	x.Resolver = resolver
	if err := x.Valid(); err != nil {
		return err
	}
	s.Exceptions[i].Resolver = NormalizeResolver(resolver)
	return nil
}

// SetExceptionDest changes where one exception goes.
func (s *State) SetExceptionDest(id string, d Dest) error {
	i := s.exception(id)
	if i < 0 {
		return ErrNoException
	}
	if err := d.Valid(false); err != nil {
		return err
	}
	s.Exceptions[i].Dest = d.Clone()
	return nil
}

// SetExceptionEnabled switches one exception on or off.
func (s *State) SetExceptionEnabled(id string, on bool) error {
	i := s.exception(id)
	if i < 0 {
		return ErrNoException
	}
	s.Exceptions[i].Enabled = on
	return nil
}

// DeleteException removes an exception.
func (s *State) DeleteException(id string) error {
	i := s.exception(id)
	if i < 0 {
		return ErrNoException
	}
	s.Exceptions = slices.Delete(s.Exceptions, i, i+1)
	return nil
}

// MoveException shifts an exception up (-1) or down (+1).
func (s *State) MoveException(id string, delta int) error {
	i := s.exception(id)
	if i < 0 {
		return ErrNoException
	}
	j := i + delta
	if j < 0 || j >= len(s.Exceptions) {
		return nil
	}
	s.Exceptions[i], s.Exceptions[j] = s.Exceptions[j], s.Exceptions[i]
	return nil
}

// MoveExceptionBefore puts an exception right before another one ("" puts it
// last).
func (s *State) MoveExceptionBefore(id, beforeID string) error {
	i := s.exception(id)
	if i < 0 {
		return ErrNoException
	}
	if beforeID == id {
		return nil
	}
	x := s.Exceptions[i]
	s.Exceptions = slices.Delete(s.Exceptions, i, i+1)
	at := s.exception(beforeID)
	if at < 0 {
		at = len(s.Exceptions)
	}
	s.Exceptions = slices.Insert(s.Exceptions, at, x)
	return nil
}

// ExceptionIndex is the 1-based position of an exception, as people count them
// ("exception #3"); 0 when there is none with the id.
func (s State) ExceptionIndex(id string) int {
	for i, x := range s.Exceptions {
		if x.ID == id {
			return i + 1
		}
	}
	return 0
}

// ------------------------------------------------------------------ nodes

// LiveNode is a node as the subscriptions have it now.
type LiveNode struct {
	Sub    string
	Name   string
	Hash   string
	Region string
}

// ID is the identity of the live node.
func (n LiveNode) ID() NodeID {
	return NodeID(n)
}

// Match says how a node a person pinned was found again.
type Match int

const (
	// MatchNone: the node is gone.
	MatchNone Match = iota
	// MatchName: found by subscription and name.
	MatchName
	// MatchHash: found by its settings under another name.
	MatchHash
	// MatchSettings: found by its settings in another subscription.
	MatchSettings
)

// Resolve finds a node among the live ones: by subscription and name first,
// then by the hash of its settings (the provider renamed it), then by the hash
// in any subscription (the node moved, or a copy of the subscription was
// kept).
func Resolve(id NodeID, live []LiveNode) (int, Match) {
	if id.IsZero() {
		return -1, MatchNone
	}
	for i, n := range live {
		if n.Sub == id.Sub && n.Name == id.Name {
			return i, MatchName
		}
	}
	if id.Hash == "" {
		return -1, MatchNone
	}
	for i, n := range live {
		if n.Sub == id.Sub && n.Hash == id.Hash {
			return i, MatchHash
		}
	}
	for i, n := range live {
		if n.Hash == id.Hash {
			return i, MatchSettings
		}
	}
	return -1, MatchNone
}

// visitDests calls fn for every destination the state holds, so identities can
// be brought up to date in one place.
func (s *State) visitDests(fn func(*Dest)) {
	fn(&s.Main)
	for _, p := range s.Plans {
		fn(&p.Fallback)
		for i := range p.Items {
			fn(&p.Items[i].Dest)
		}
	}
	for _, ov := range s.Overlays {
		for k, d := range ov.Dest {
			fn(&d)
			ov.Dest[k] = d
		}
		for i := range ov.Items {
			fn(&ov.Items[i].Dest)
		}
		if ov.Fallback != nil {
			fn(ov.Fallback)
		}
	}
	for i := range s.Exceptions {
		fn(&s.Exceptions[i].Dest)
	}
}

// RefreshIdentities brings every pinned node's identity up to date with the
// live nodes: a node found under a new name (or with new settings or region)
// is followed; one that is gone keeps its identity, so the person is told
// about it and can choose again. It reports whether anything changed.
func (s *State) RefreshIdentities(live []LiveNode) bool {
	changed := false
	s.visitDests(func(d *Dest) {
		if d.Kind != DestNode || d.Node == nil {
			return
		}
		i, how := Resolve(*d.Node, live)
		if how == MatchNone {
			return
		}
		n := live[i].ID()
		if how == MatchSettings {
			// The same server in another subscription is the same node for the
			// purpose of finding it, but the pin stays where the person put it.
			n.Sub, n.Name = d.Node.Sub, d.Node.Name
		}
		if *d.Node != n {
			*d.Node = n
			changed = true
		}
	})
	for i, r := range s.Recent {
		if j, how := Resolve(r, live); how == MatchName || how == MatchHash {
			if n := live[j].ID(); n != r {
				s.Recent[i] = n
				changed = true
			}
		}
	}
	if len(s.Nodes) > 0 {
		next := make(map[string]NodePref, len(s.Nodes))
		for key, pref := range s.Nodes {
			sub, name, _ := strings.Cut(key, "\x00")
			i, how := Resolve(NodeID{Sub: sub, Name: name, Hash: pref.Hash}, live)
			if how == MatchName || how == MatchHash {
				n := live[i]
				if pref.Hash != n.Hash {
					pref.Hash = n.Hash
					changed = true
				}
				if k := n.ID().Key(); k != key {
					key = k
					changed = true
				}
			}
			next[key] = pref
		}
		s.Nodes = next
	}
	return changed
}

// NodePref returns the choices made about a node.
func (s State) NodePref(id NodeID) NodePref { return s.Nodes[id.Key()] }

func (s *State) editNode(id NodeID, fn func(*NodePref)) {
	if s.Nodes == nil {
		s.Nodes = map[string]NodePref{}
	}
	p := s.Nodes[id.Key()]
	if p.Hash == "" {
		p.Hash = id.Hash
	}
	fn(&p)
	if p.Region == "" && !p.NoAuto {
		delete(s.Nodes, id.Key())
		return
	}
	s.Nodes[id.Key()] = p
}

// SetNodeRegion corrects the region of a node ("" goes back to the recognised one).
func (s *State) SetNodeRegion(id NodeID, region string) {
	s.editNode(id, func(p *NodePref) { p.Region = strings.ToUpper(strings.TrimSpace(region)) })
}

// SetNodeAuto includes a node in the automatic groups or leaves it out.
func (s *State) SetNodeAuto(id NodeID, auto bool) {
	s.editNode(id, func(p *NodePref) { p.NoAuto = !auto })
}
