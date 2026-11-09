package routing

import (
	"maps"
	"net/netip"
	"regexp"
	"strings"
)

// Shadow is a hand-written entry of an item that an exception already
// decides: the exceptions come before every item, so the entry's traffic goes
// where the exception says, and its names are looked up the way the exception
// says.
type Shadow struct {
	Entry Entry
	// Exception is the exception that decides it; Index is its place in the
	// list, from 1, as the interface numbers exceptions.
	Exception Exception
	Index     int
}

// Shadowed lists the hand-written entries of an item that an enabled exception
// covers, in the order of the item's sources, each with the first exception
// that covers it. Only hand-written entries are compared: what a rule file
// holds is not looked into. The exceptions are indexed once, so a long list of
// entries against a long list of exceptions stays cheap; the answer is the one
// Covers gives for every pair.
func (s State) Shadowed(it Item) []Shadow {
	type ranged struct {
		value string
		p     netip.Prefix
		at    int
	}
	exact := map[string]int{}
	suffixes := map[string]int{}
	var keywords, cidrs []ranged
	for i, x := range s.Exceptions {
		if !x.Enabled {
			continue
		}
		e := x.Entry().Normalize()
		if _, ok := exact[entryKey(e)]; !ok {
			exact[entryKey(e)] = i
		}
		switch e.Kind {
		case EntryDomainSuffix:
			if _, ok := suffixes[e.Value]; !ok {
				suffixes[e.Value] = i
			}
		case EntryDomainKeyword:
			keywords = append(keywords, ranged{value: e.Value, at: i})
		case EntryIPCIDR:
			if p, err := netip.ParsePrefix(e.Value); err == nil {
				cidrs = append(cidrs, ranged{p: p, at: i})
			}
		}
	}
	first := func(e Entry) int {
		best := -1
		take := func(i int) {
			if best < 0 || i < best {
				best = i
			}
		}
		if i, ok := exact[entryKey(e)]; ok {
			take(i)
		}
		switch e.Kind {
		case EntryDomain, EntryDomainSuffix:
			// A suffix covers the name itself and the names under it: try
			// the name and every name it is under, label by label.
			for v := e.Value; ; {
				if i, ok := suffixes[v]; ok {
					take(i)
				}
				dot := strings.IndexByte(v, '.')
				if dot < 0 {
					break
				}
				v = v[dot+1:]
			}
		}
		switch e.Kind {
		case EntryDomain, EntryDomainSuffix, EntryDomainKeyword:
			for _, k := range keywords {
				if strings.Contains(e.Value, k.value) {
					take(k.at)
					break
				}
			}
		case EntryIPCIDR:
			if p, err := netip.ParsePrefix(e.Value); err == nil {
				for _, c := range cidrs {
					if prefixCovers(c.p, p) {
						take(c.at)
						break
					}
				}
			}
		}
		return best
	}
	var out []Shadow
	for _, src := range it.Sources {
		if src.Kind != SourceInline {
			continue
		}
		for _, e := range src.Entries {
			e = e.Normalize()
			if i := first(e); i >= 0 {
				out = append(out, Shadow{Entry: e, Exception: s.Exceptions[i], Index: i + 1})
			}
		}
	}
	return out
}

// Covers reports whether everything the entry e matches is matched by the
// rule r too, as far as can be told from the two alone. Both are normalized.
func Covers(r, e Entry) bool {
	if r.Kind == e.Kind && r.Value == e.Value {
		return true
	}
	switch r.Kind {
	case EntryDomainSuffix:
		// A suffix matches the name itself and the names under it.
		if e.Kind == EntryDomain || e.Kind == EntryDomainSuffix {
			return e.Value == r.Value || strings.HasSuffix(e.Value, "."+r.Value)
		}
	case EntryDomainKeyword:
		// Every name a domain or a suffix matches holds its value.
		if e.Kind == EntryDomain || e.Kind == EntryDomainSuffix || e.Kind == EntryDomainKeyword {
			return strings.Contains(e.Value, r.Value)
		}
	case EntryIPCIDR:
		if e.Kind == EntryIPCIDR {
			outer, err1 := netip.ParsePrefix(r.Value)
			inner, err2 := netip.ParsePrefix(e.Value)
			return err1 == nil && err2 == nil && prefixCovers(outer, inner)
		}
	}
	return false
}

// prefixCovers reports whether every address of inner is in outer.
func prefixCovers(outer, inner netip.Prefix) bool {
	return outer.Addr().Is4() == inner.Addr().Is4() && outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr())
}

// privateBlocks are the address ranges the local network's direct rule
// covers: what the core counts as private (ip_is_private), as prefixes.
var privateBlocks = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("0.0.0.0/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::/128"),
}

// PrivateRange reports whether an address range lies wholly in the local
// network's address space, so that a rule naming it takes that traffic from
// the local network's direct rule. A range that only overlaps it (0.0.0.0/0)
// does not.
func PrivateRange(cidr string) bool {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		a, err := netip.ParseAddr(cidr)
		if err != nil {
			return false
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	p = p.Masked()
	for _, b := range privateBlocks {
		if prefixCovers(b, p) {
			return true
		}
	}
	return false
}

// Claims are the private ranges the entries the person wrote into an item
// name, which the local network's direct rule leaves to the rules while the
// item is in force. Neither a rule file nor the entries a provider's plan
// brought claim anything: a plan or a list cannot take the local network
// away. Normalized, without repeats, in order.
func (it Item) Claims() []string {
	var out []string
	seen := map[string]bool{}
	for _, src := range it.Sources {
		if src.Kind != SourceInline || !src.Written {
			continue
		}
		// What the provider put in this source is the provider's; the same
		// range written in another source is still the person's.
		skip := maps.Clone(seen)
		for _, c := range src.Inherited {
			skip[c] = true
		}
		for _, c := range appendClaims(nil, src.Entries, skip) {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// appendClaims adds to out the private ranges entries name that are not
// seen yet, and marks them seen.
func appendClaims(out []string, entries []Entry, seen map[string]bool) []string {
	for _, e := range entries {
		e = e.Normalize()
		if e.Kind == EntryIPCIDR && !seen[e.Value] && PrivateRange(e.Value) {
			seen[e.Value] = true
			out = append(out, e.Value)
		}
	}
	return out
}

// ExceptionFor finds the exception that has this match, the way
// UpsertException does.
func (s State) ExceptionFor(e Entry) (Exception, bool) {
	e = e.Normalize()
	for _, x := range s.Exceptions {
		if x.Kind == e.Kind && x.Value == e.Value {
			return x, true
		}
	}
	return Exception{}, false
}

// Claim is the private range an exception names, or "".
func (x Exception) Claim() string {
	if e := x.Entry().Normalize(); e.Kind == EntryIPCIDR && PrivateRange(e.Value) {
		return e.Value
	}
	return ""
}

// writtenID matches the identifiers InlineSource gives: "n" and ten hex
// digits. A provider's plan names its sources "n" and a 64-bit hash in base
// 36, which is that short and has no letter past "f" about once in ten
// million; the marking runs once, when a library of the second format is
// upgraded.
var writtenID = regexp.MustCompile(`^n[0-9a-f]{10}$`)

// Upgrade brings a state read from an older format up to date, and reports
// whether it changed anything. Older applications still read the result: what
// it adds are fields they do not know, and drop when they save. So the marks
// are put back every time, not only once: after the person went back to an
// older version and forward again, the marks it dropped return.
func (s *State) Upgrade() bool {
	changed := s.Version < FormatVersion
	if s.MarkWritten() {
		changed = true
	}
	s.Version = FormatVersion
	return changed
}

// MarkWritten marks the hand-written sources the person made before sources
// said who wrote them, so that their private ranges claim what the person
// meant them to. A source made by InlineSource is the person's; one a
// provider's plan brought is not, even in a copy of that plan. It reports
// whether it marked any; marking twice changes nothing.
func (s *State) MarkWritten() bool {
	marked := false
	mark := func(list []Source) {
		for i := range list {
			if list[i].Kind == SourceInline && !list[i].Written && writtenID.MatchString(list[i].ID) {
				list[i].Written, marked = true, true
			}
		}
	}
	for _, p := range s.Plans {
		if p == nil || p.Kind != PlanMine {
			continue
		}
		for i := range p.Items {
			mark(p.Items[i].Sources)
		}
	}
	for _, ov := range s.Overlays {
		if ov == nil {
			continue
		}
		for i := range ov.Items {
			mark(ov.Items[i].Sources)
		}
		for _, list := range ov.Extra {
			mark(list)
		}
	}
	return marked
}
