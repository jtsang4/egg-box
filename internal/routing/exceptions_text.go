package routing

import (
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The exceptions can be edited as text, one per line in the Clash syntax with
// the destination last:
//
//	DOMAIN-SUFFIX,example.com,DIRECT
//	DOMAIN,ads.example.com,REJECT
//	DOMAIN-SUFFIX,openai.com,REGION:US
//	DOMAIN-SUFFIX,abema.tv,NODE:Aurora Cloud/Tokyo 01
//	DOMAIN-SUFFIX,github.com
//	DOMAIN-SUFFIX,corp.example,DIRECT,dns=10.0.0.53
//	DOMAIN-KEYWORD,intra,NODE:Gateway,dns=exit
//	# DOMAIN-SUFFIX,old.example.com,DIRECT
//
// No destination follows the main exit, and "#" in front switches a rule off.
// "dns=" at the end says how the names a rule matches are looked up: by that
// DNS server, through the rule's exit, or with "exit" by the exit itself.
// The order of the lines is the order the exceptions are tried in.
//
// A node is written by its name, with the subscription in front when another
// subscription has a node of the same name. A line read back with the very
// destination it was written with keeps the destination it stood for, so a
// node is never looked up by name again unless the person changed what the
// line says.

// ErrExceptionsChanged means the exceptions changed while a copy of them was
// being edited, so the copy cannot simply replace them.
var ErrExceptionsChanged = errors.New("the exceptions changed while they were being edited")

// ClashType is the rule type of the Clash syntax that writes an entry kind.
func ClashType(k EntryKind) string { return clashTypeOf[k] }

var clashTypeOf = map[EntryKind]string{
	EntryDomain:        "DOMAIN",
	EntryDomainSuffix:  "DOMAIN-SUFFIX",
	EntryDomainKeyword: "DOMAIN-KEYWORD",
	EntryDomainRegex:   "DOMAIN-REGEX",
	EntryIPCIDR:        "IP-CIDR",
	EntryPort:          "DST-PORT",
	EntryProcessName:   "PROCESS-NAME",
	EntryProcessPath:   "PROCESS-PATH",
}

// EntryLine writes an entry the way a pasted list is read, with its kind.
func EntryLine(e Entry) string {
	if t := ClashType(e.Kind); t != "" {
		return t + "," + quoteField(e.Value)
	}
	return e.Value
}

// ExceptionLine writes an exception as a line of the exceptions' text, with
// the destination as FormatTarget writes it.
func ExceptionLine(x Exception, target string) string {
	s := EntryLine(x.Entry())
	if target != "" {
		s += "," + quoteTarget(target)
	}
	if x.Resolver != "" {
		s += "," + dnsOption + x.Resolver
	}
	if !x.Enabled {
		s = "# " + s
	}
	return s
}

// quoteField puts a value in quotes when it could not be read back otherwise.
func quoteField(v string) string {
	if v == "" || strings.ContainsAny(v, `,"`) || v != strings.TrimSpace(v) || v[0] == '\'' {
		return quote(v)
	}
	return v
}

// quoteTarget puts a destination in quotes when it could not be read back
// otherwise: after a regular expression, a destination that looks like a part
// of one has to be.
func quoteTarget(v string) string {
	if strings.ContainsAny(v, regexChars) || strings.EqualFold(v, "no-resolve") {
		return quote(v)
	}
	return quoteField(v)
}

func quote(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }

// NamedNode is a node as the text names it.
type NamedNode struct {
	ID NodeID
	// SubName is the subscription's name; SubAlias the short name the person
	// gave it, which is what the text writes when there is one.
	SubName  string
	SubAlias string
}

// subLabel is what the text calls the node's subscription.
func (n NamedNode) subLabel() string {
	if n.SubAlias != "" {
		return n.SubAlias
	}
	return n.SubName
}

// Names is what the text of the exceptions can name: the nodes, the regions
// and the items of the plan in force.
type Names struct {
	Nodes []NamedNode
	// Region reports whether a region code is known; nil takes any.
	Region func(code string) bool
	// Rows are the items of the plan in force: a line may name one to go
	// where it goes now.
	Rows []Row
	// Subs are what the text calls each subscription, by id, those that
	// provide no nodes now included, so that a node that is gone can be told
	// from one of the same name.
	Subs map[string]string
}

func (ns Names) live() []LiveNode {
	out := make([]LiveNode, len(ns.Nodes))
	for i, n := range ns.Nodes {
		out[i] = LiveNode(n.ID)
	}
	return out
}

// shared reports whether nodes of more than one subscription have the name.
func (ns Names) shared(name string) bool {
	sub, found := "", false
	for _, n := range ns.Nodes {
		if n.ID.Name != name {
			continue
		}
		if found && n.ID.Sub != sub {
			return true
		}
		sub, found = n.ID.Sub, true
	}
	return false
}

// FormatTarget writes a destination as the text of the exceptions does.
func FormatTarget(d Dest, ns Names) string { return ns.formatTarget(d, ns.live()) }

func (ns Names) formatTarget(d Dest, live []LiveNode) string {
	switch d.Kind {
	case DestDirect:
		return "DIRECT"
	case DestReject:
		return "REJECT"
	case DestRegion:
		return "REGION:" + d.Region
	case DestNode:
		if d.Node == nil {
			return ""
		}
		return "NODE:" + ns.nodeRef(*d.Node, live)
	}
	return ""
}

// nodeRef is how the text names a node: its name, after its subscription when
// the name alone could mean a node of another subscription.
func (ns Names) nodeRef(id NodeID, live []LiveNode) string {
	if i, m := Resolve(id, live); m != MatchNone {
		n := ns.Nodes[i]
		if ns.shared(n.ID.Name) {
			return n.subLabel() + "/" + n.ID.Name
		}
		return n.ID.Name
	}
	// The node is gone. When a node of another subscription has its name, the
	// name alone would mean that one: the subscription is written in front,
	// by its id when it is gone as well.
	for _, n := range ns.Nodes {
		if n.ID.Name == id.Name {
			// By its id when the name of the subscription is unknown, or
			// would name a node that is there.
			sub := ns.Subs[id.Sub]
			if sub == "" || len(ns.nodesCalled(sub+"/"+id.Name, false)) > 0 {
				sub = id.Sub
			}
			return sub + "/" + id.Name
		}
	}
	return id.Name
}

// ResolveExceptionTarget reads the destination a line of the exceptions'
// text names. A problem comes back with its Code, Arg and Choices set.
func ResolveExceptionTarget(target string, ns Names) (Dest, *BatchBad) {
	t := strings.TrimSpace(target)
	up := strings.ToUpper(t)
	switch {
	case t == "":
		return Follow(), nil
	case up == "DIRECT":
		return Direct(), nil
	case up == "REJECT", up == "REJECT-DROP":
		return Reject(), nil
	case strings.HasPrefix(up, "REGION:"):
		code := strings.ToUpper(strings.TrimSpace(t[len("REGION:"):]))
		if len(code) != 2 || !isLetters(code) || ns.Region != nil && !ns.Region(code) {
			return Dest{}, &BatchBad{Code: BadRegion, Arg: code, Reason: strconv.Quote(code) + " is not a known region"}
		}
		return ToRegion(code), nil
	case strings.HasPrefix(up, "NODE:"):
		// A node's name is matched as written first: it may end in a space.
		return ns.resolveNode(strings.TrimLeft(target, " \t")[len("NODE:"):])
	}
	if d, ok := ResolveTarget(t, ns.Rows); ok {
		return d, nil
	}
	return Dest{}, &BatchBad{Code: BadTarget, Arg: t, Reason: strconv.Quote(t) + " is not a destination"}
}

func (ns Names) resolveNode(ref string) (Dest, *BatchBad) {
	found := ns.nodesCalled(ref, false)
	if len(found) == 0 {
		// Spaces at either end are hard to see: without them, then.
		ref = strings.TrimSpace(ref)
		found = ns.nodesCalled(ref, true)
	}
	switch len(found) {
	case 0:
		return Dest{}, &BatchBad{Code: BadNoNode, Arg: ref, Reason: "there is no node called " + strconv.Quote(ref)}
	case 1:
		return ToNode(ns.Nodes[found[0]].ID), nil
	}
	// The ways to write each of them that name only it.
	var choices []string
	for _, i := range found {
		n := ns.Nodes[i]
		for _, sub := range []string{n.SubAlias, n.SubName} {
			if w := sub + "/" + n.ID.Name; sub != "" && len(ns.nodesCalled(w, false)) == 1 {
				choices = append(choices, "NODE:"+w)
				break
			}
		}
	}
	return Dest{}, &BatchBad{Code: BadAmbiguous, Arg: ref, Choices: choices,
		Reason: "more than one node is called " + strconv.Quote(ref)}
}

// nodesCalled finds the nodes a reference names: by name, or by subscription
// and name; with loose, names are compared without spaces at either end.
func (ns Names) nodesCalled(ref string, loose bool) []int {
	name := func(n NamedNode) string {
		if loose {
			return strings.TrimSpace(n.ID.Name)
		}
		return n.ID.Name
	}
	var found []int
	for i, n := range ns.Nodes {
		if name(n) == ref {
			found = append(found, i)
			continue
		}
		for _, sub := range []string{n.SubAlias, n.SubName} {
			if sub != "" && sub+"/"+name(n) == ref {
				found = append(found, i)
				break
			}
		}
	}
	return found
}

func isLetters(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// entryKey is what two exceptions with the same match have in common.
func entryKey(e Entry) string { return string(e.Kind) + "\x00" + e.Value }

// ExceptionsText is the exceptions written out to be edited, and what the
// text stood for when it was written, so that an edited copy can be read back
// without losing what the words alone do not say.
type ExceptionsText struct {
	// Base is the list the text was written from.
	Base []Exception
	// Text is the list written out.
	Text string
	// byKey finds an exception of Base by its match.
	byKey map[string]int
	// targets are the destinations as written, one for each of Base.
	targets []string
	// meant is what each destination as written stood for; nil when two
	// exceptions that were written the same way went to different places.
	meant map[string]*Dest
}

// WriteExceptions writes the exceptions out.
func WriteExceptions(xs []Exception, ns Names) ExceptionsText {
	t := ExceptionsText{
		Base:    cloneExceptions(xs),
		byKey:   make(map[string]int, len(xs)),
		targets: make([]string, len(xs)),
		meant:   map[string]*Dest{},
	}
	lines := make([]string, len(xs))
	live := ns.live()
	for i, x := range xs {
		target := ns.formatTarget(x.Dest, live)
		t.byKey[entryKey(x.Entry())] = i
		t.targets[i] = target
		lines[i] = ExceptionLine(x, target)
		if d, seen := t.meant[target]; !seen {
			dest := x.Dest.Clone()
			t.meant[target] = &dest
		} else if d != nil && !d.Equal(x.Dest) {
			t.meant[target] = nil
		}
	}
	if len(lines) > 0 {
		t.Text = strings.Join(lines, "\n") + "\n"
	}
	return t
}

func cloneExceptions(xs []Exception) []Exception {
	out := make([]Exception, len(xs))
	for i, x := range xs {
		x.Dest = x.Dest.Clone()
		out[i] = x
	}
	return out
}

// TargetMeaning is a destination as a line wrote it and what it stands for.
type TargetMeaning struct {
	Target string
	Dest   Dest
}

// ExceptionsDraft is what an edited text of the exceptions comes to, to be
// shown before it replaces the list.
type ExceptionsDraft struct {
	// List is the new list, in order, with the ids of the exceptions it keeps.
	List []Exception
	// Problems are the lines that cannot be used, in order. While there are
	// any, the text cannot be saved.
	Problems []BatchBad
	// Counts says how many exceptions there are of each kind.
	Counts map[EntryKind]int
	// Targets are the distinct destinations the lines wrote, in order of
	// appearance, with what each stands for.
	Targets []TargetMeaning
	// Resolvers are the distinct resolvers the lines named, in order of
	// appearance, in the form they are kept in.
	Resolvers []string
	// Added and Changed count the exceptions that are new and those whose
	// destination or switch changed; Removed are the ones that go.
	Added, Changed int
	Removed        []Exception
	// Reordered is set when exceptions that stay changed places.
	Reordered bool
	// TooMany is set when the list is longer than MaxExceptions.
	TooMany bool
}

// Changes reports whether the text says anything other than the list it was
// written from.
func (d ExceptionsDraft) Changes() bool {
	return d.Added > 0 || d.Changed > 0 || len(d.Removed) > 0 || d.Reordered
}

// Ready reports whether the text can replace the list.
func (d ExceptionsDraft) Ready() bool { return len(d.Problems) == 0 && !d.TooMany && d.Changes() }

// Read reads an edited copy of the text.
func (t ExceptionsText) Read(text string, ns Names) ExceptionsDraft {
	res := parseList(text, true)
	d := ExceptionsDraft{Counts: map[EntryKind]int{}, Problems: res.Unrecognized}
	firstLine := map[string]int{}
	shown := map[string]bool{}
	// pending are the matches of lines that cannot be used yet: the
	// exceptions they stand for are not being removed.
	pending := map[string]bool{}
	for _, l := range res.Lines {
		k := entryKey(l.Entry)
		if first, dup := firstLine[k]; dup {
			d.Problems = append(d.Problems, BatchBad{Line: l.Line, Text: l.Text, Code: BadDuplicate, Arg: strconv.Itoa(first),
				Reason: "the same as line " + strconv.Itoa(first)})
			continue
		}
		firstLine[k] = l.Line
		target := l.Target
		x := Exception{Kind: l.Entry.Kind, Value: l.Entry.Value, Enabled: !l.Disabled}
		if l.Resolver != "" {
			if bad := resolverProblem(l); bad != nil {
				d.Problems = append(d.Problems, *bad)
				pending[k] = true
				continue
			}
			x.Resolver = NormalizeResolver(l.Resolver)
		}
		i, kept := t.byKey[k]
		if kept {
			x.ID = t.Base[i].ID
		}
		switch m, known := t.meant[target]; {
		case kept && target == t.targets[i]:
			x.Dest = t.Base[i].Dest.Clone()
		case known && m != nil:
			x.Dest = m.Clone()
		default:
			dest, bad := ResolveExceptionTarget(target, ns)
			if bad != nil {
				bad.Line, bad.Text = l.Line, l.Text
				d.Problems = append(d.Problems, *bad)
				pending[k] = true
				continue
			}
			x.Dest = dest
		}
		d.List = append(d.List, x)
		d.Counts[x.Kind]++
		if target != "" && !shown[target] {
			shown[target] = true
			d.Targets = append(d.Targets, TargetMeaning{Target: target, Dest: x.Dest.Clone()})
		}
		if r := x.Resolver; r != "" && !slices.Contains(d.Resolvers, r) {
			d.Resolvers = append(d.Resolvers, r)
		}
	}
	sort.SliceStable(d.Problems, func(a, b int) bool { return d.Problems[a].Line < d.Problems[b].Line })
	d.TooMany = len(d.List) > MaxExceptions

	inList := map[string]bool{}
	var kept []string
	for _, x := range d.List {
		k := entryKey(x.Entry())
		inList[k] = true
		i, ok := t.byKey[k]
		if !ok {
			d.Added++
			continue
		}
		kept = append(kept, k)
		if b := t.Base[i]; b.Enabled != x.Enabled || !b.Dest.Equal(x.Dest) || b.Resolver != x.Resolver {
			d.Changed++
		}
	}
	var before []string
	for _, b := range t.Base {
		k := entryKey(b.Entry())
		if !inList[k] {
			if !pending[k] {
				d.Removed = append(d.Removed, b)
			}
			continue
		}
		before = append(before, k)
	}
	d.Reordered = !slices.Equal(before, kept)
	return d
}

// Rebase carries an edited copy of the text over to the list as it is now,
// after it changed while it was being edited. Lines the person left as they
// were written take the form of the exception now (and go, with exceptions
// that were removed meanwhile); exceptions added meanwhile are added at the
// end; the lines the person wrote or changed stay as they are. It returns the
// text of the list as it is now and the edited copy carried over.
func (t ExceptionsText) Rebase(text string, latest []Exception, ns Names) (ExceptionsText, string) {
	next := WriteExceptions(latest, ns)
	// The line each exception was written as, by its match, and what that
	// exception is called now, by its id: one changed elsewhere keeps its place.
	written := make(map[string]string, len(t.Base))
	idOf := make(map[string]string, len(t.Base))
	was := make(map[string]bool, len(t.Base))
	for i, b := range t.Base {
		k := entryKey(b.Entry())
		written[k], idOf[k], was[b.ID] = ExceptionLine(b, t.targets[i]), b.ID, true
	}
	now := make(map[string]string, len(next.Base))
	nowKey := make(map[string]string, len(next.Base))
	for i, x := range next.Base {
		now[x.ID], nowKey[x.ID] = ExceptionLine(x, next.targets[i]), entryKey(x.Entry())
	}
	// present are the matches the carried-over text has.
	present := map[string]bool{}
	var out []string
	for _, raw := range listLines(text) {
		if res := parseList(raw, true); len(res.Lines) == 1 {
			k := entryKey(res.Lines[0].Entry)
			if old, ok := written[k]; ok && strings.TrimSpace(raw) == old {
				if line, ok := now[idOf[k]]; ok {
					out = append(out, line)
					present[nowKey[idOf[k]]] = true
				}
				continue
			}
			present[k] = true
		}
		out = append(out, raw)
	}
	var added []string
	for _, x := range next.Base {
		if !was[x.ID] && !present[entryKey(x.Entry())] {
			added = append(added, now[x.ID])
		}
	}
	if len(added) > 0 {
		end := len(out)
		for end > 0 && strings.TrimSpace(out[end-1]) == "" {
			end--
		}
		out = append(out[:end], append(added, out[end:]...)...)
		if end == len(out)-len(added) {
			out = append(out, "")
		}
	}
	return next, strings.Join(out, "\n")
}

// StripTargets takes the destination off the given lines of a text, so they
// follow the main exit; the other lines stay as they are.
func StripTargets(text string, lines map[int]bool) string {
	all := listLines(text)
	for i, raw := range all {
		if !lines[i+1] {
			continue
		}
		res := parseList(raw, true)
		if len(res.Lines) != 1 {
			continue
		}
		l := res.Lines[0]
		all[i] = ExceptionLine(Exception{Kind: l.Entry.Kind, Value: l.Entry.Value, Enabled: !l.Disabled, Resolver: l.Resolver}, "")
	}
	return strings.Join(all, "\n")
}

// ReplaceExceptions replaces the exceptions with list, which was made from a
// copy of base. When the exceptions are no longer base, it changes nothing
// and says ErrExceptionsChanged. Exceptions of list without an id get one.
func (s *State) ReplaceExceptions(base, list []Exception) error {
	if !sameExceptions(s.Exceptions, base) {
		return ErrExceptionsChanged
	}
	if len(list) > MaxExceptions {
		return ErrTooManyExceptions
	}
	out := make([]Exception, 0, len(list))
	seen := map[string]bool{}
	for _, x := range list {
		x.Value = strings.TrimSpace(x.Value)
		if err := x.Valid(); err != nil {
			return err
		}
		x.Resolver = NormalizeResolver(x.Resolver)
		e := x.Entry().Normalize()
		x.Kind, x.Value = e.Kind, e.Value
		if k := entryKey(e); seen[k] {
			return badEntryf("%s appears twice", e.Value)
		} else {
			seen[k] = true
		}
		if x.ID == "" {
			x.ID = NewID("x")
		}
		x.Dest = x.Dest.Clone()
		out = append(out, x)
	}
	s.Exceptions = out
	return nil
}

func sameExceptions(a, b []Exception) bool {
	return slices.EqualFunc(a, b, func(x, y Exception) bool {
		return x.ID == y.ID && x.Kind == y.Kind && x.Value == y.Value && x.Enabled == y.Enabled && x.Dest.Equal(y.Dest) &&
			x.Resolver == y.Resolver
	})
}

// resolverProblem says why the resolver a line named cannot be used, or nil.
func resolverProblem(l BatchLine) *BatchBad {
	if !l.Entry.Kind.IsHost() {
		return &BatchBad{Line: l.Line, Text: l.Text, Code: BadResolverHost, Arg: ClashType(l.Entry.Kind),
			Reason: ErrResolverNotHost.Error()}
	}
	if err := ValidResolver(l.Resolver); err != nil {
		return &BatchBad{Line: l.Line, Text: l.Text, Code: BadResolver, Arg: strings.TrimSpace(l.Resolver), Reason: err.Error()}
	}
	return nil
}
