// Package plancompile turns what the person chose (a routing plan, exceptions,
// the main exit) and what the application knows (the nodes of the active
// subscriptions, the rule files it has downloaded) into the input of
// configgen.
//
// It is the one place where the two meet: routing speaks of items, sources and
// pinned nodes; configgen of compiled rule sets and node keys. Whatever cannot
// be carried over (a node that is gone, a rule file that is not there yet) is
// reported here instead of silently dropped.
package plancompile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"

	"eggbox/internal/configgen"
	"eggbox/internal/routing"
	"eggbox/internal/rulefmt"
	"eggbox/internal/ruleurl"
	"eggbox/internal/subscription"
)

// Data gives access to the rule files the application has downloaded.
// *ruledata.Manager implements it.
type Data interface {
	// Parsed reads a stored file by its canonical address. The result may be
	// shared: it is not modified here.
	Parsed(canonical string) (*rulefmt.Parsed, error)
}

// Node is a node of an active subscription.
type Node struct {
	// Sub is the identifier of the subscription it came from.
	Sub  string
	Node subscription.Node
	// Auto is false when the subscription is kept out of the automatic choice.
	Auto bool
}

// Options are the inputs of a compilation.
type Options struct {
	State routing.State
	// Nodes are the nodes in force, one per distinct server: where two
	// subscriptions offer the same one, only the first is here.
	Nodes []Node
	Data  Data
	Prefs configgen.Prefs
}

// ItemState says what became of an item.
type ItemState int

const (
	// ItemReady: the item takes part with all its rules.
	ItemReady ItemState = iota
	// ItemOff: the person turned it off.
	ItemOff
	// ItemUnavailable: it is on but nothing of it can be used yet: its files
	// are not downloaded or hold nothing the core understands.
	ItemUnavailable
	// ItemPartial: it takes part, but some of its sources are missing or some
	// of its rules are of a kind the core cannot evaluate.
	ItemPartial
)

// ItemInfo describes one item of the plan for the interface.
type ItemInfo struct {
	State ItemState
	// Missing counts the sources (and the files they refer to) that are not
	// downloaded or cannot be read.
	Missing int
	// Unsupported counts the rules left out because the core cannot evaluate them.
	Unsupported int
	// Domain and IP count the host-name and address conditions that made it in.
	Domain, IP int
}

// ItemRules are the rules of an item as they were compiled, kept to answer
// "which item would this address go to".
type ItemRules struct {
	Domain, IP []option.HeadlessRule
}

// NoticeCode says what a notice is about.
type NoticeCode string

const (
	// NoticePinGone: a node the person pinned is no longer offered by any
	// subscription. Traffic goes to the fastest node of its region meanwhile.
	NoticePinGone NoticeCode = "pin_gone"
)

// OwnerKind says whose exit a notice is about.
type OwnerKind string

const (
	OwnerMain      OwnerKind = "main"
	OwnerFallback  OwnerKind = "fallback"
	OwnerItem      OwnerKind = "item"
	OwnerException OwnerKind = "exception"
)

// Notice is something the person should be told about a compilation.
type Notice struct {
	Code  NoticeCode
	Owner OwnerKind
	// ID is the item or exception, empty for the main exit and the fallback.
	ID string
	// Node is the identity the person pinned.
	Node routing.NodeID
}

// Result is a compiled plan.
type Result struct {
	Input   configgen.Input
	Notices []Notice
	// Items describes every row of the plan, by item id.
	Items map[string]ItemInfo
	// Rules holds the compiled items' rules, by item id.
	Rules map[string]ItemRules
	// Missing lists the canonical addresses of the files the plan needs and
	// does not have, so they can be downloaded.
	Missing []string
}

// Compiler turns plans into configuration input. It remembers the rule sets it
// compiled, so that compiling again after a small change does not compile every
// list again. It is safe for concurrent use.
type Compiler struct {
	mu    sync.Mutex
	cache map[string]compiled
}

// New returns a compiler.
func New() *Compiler { return &Compiler{cache: map[string]compiled{}} }

type compiled struct {
	domain, ip, dns []byte
	process         bool
	rules           ItemRules
	info            ItemInfo
	// files are the parsed lists the key names by address; holding them keeps
	// the address from being reused for another list while this entry lives.
	files []*rulefmt.Parsed
}

// maxCache bounds the remembered rule sets.
const maxCache = 512

// Compile builds the configuration input of the plan in force.
func (c *Compiler) Compile(o Options) (*Result, error) {
	view := o.State.View()
	res := &Result{Items: map[string]ItemInfo{}, Rules: map[string]ItemRules{}}

	nodes := c.nodes(o)
	in := configgen.Input{Prefs: o.Prefs, Nodes: nodes.configNodes}
	regions := nodes.regions()

	in.Main = nodes.exit(o.State.Main, true, regions, Notice{Owner: OwnerMain}, res)
	in.Fallback = nodes.exit(view.Fallback, false, regions, Notice{Owner: OwnerFallback}, res)

	for _, x := range o.State.Exceptions {
		if !x.Enabled {
			continue
		}
		rule, err := EntryRule(x.Entry())
		if err != nil {
			continue
		}
		exit := nodes.exit(x.Dest, false, regions, Notice{Owner: OwnerException, ID: x.ID}, res)
		in.Exceptions = append(in.Exceptions, configgen.Exception{ID: x.ID, Rule: rule, Exit: exit})
	}

	missing := map[string]bool{}
	for _, row := range view.Rows {
		it := row.Item
		if !it.Enabled {
			res.Items[it.ID] = ItemInfo{State: ItemOff}
			continue
		}
		cd := c.item(it, o.Data, missing)
		res.Items[it.ID] = cd.info
		if cd.info.State == ItemUnavailable {
			continue
		}
		res.Rules[it.ID] = cd.rules
		exit := nodes.exit(it.Dest, false, regions, Notice{Owner: OwnerItem, ID: it.ID}, res)
		in.Items = append(in.Items, configgen.Item{
			ID: it.ID, Name: it.Name, Exit: exit,
			Domain: cd.domain, IP: cd.ip, DNS: cd.dns, Process: cd.process, Domestic: it.Domestic,
		})
	}
	res.Missing = sortedKeys(missing)
	res.Input = in
	return res, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ------------------------------------------------------------------- nodes

type nodeSet struct {
	configNodes []configgen.Node
	live        []routing.LiveNode
	keys        []string // configgen key of each live node
}

// nodes prepares the nodes: regions as the person corrected them, the ones
// they keep out of the automatic choice, one entry per server.
func (c *Compiler) nodes(o Options) *nodeSet {
	ns := &nodeSet{}
	seen := map[string]bool{}
	for _, n := range o.Nodes {
		key := n.Node.Key()
		if seen[key] {
			continue
		}
		seen[key] = true
		id := routing.NodeID{Sub: n.Sub, Name: n.Node.Name}
		pref := o.State.NodePref(id)
		region := subscription.Region(n.Node.Name)
		if pref.Region != "" {
			region = pref.Region
		}
		ns.configNodes = append(ns.configNodes, configgen.Node{
			Node: n.Node, Region: strings.ToUpper(region), NoAuto: pref.NoAuto || !n.Auto,
		})
		ns.live = append(ns.live, routing.LiveNode{Sub: n.Sub, Name: n.Node.Name, Hash: key, Region: strings.ToUpper(region)})
		ns.keys = append(ns.keys, key)
	}
	return ns
}

// regions are the regions with at least one node in the automatic choice, which
// is where a region group exists.
func (ns *nodeSet) regions() map[string]bool {
	out := map[string]bool{}
	for _, n := range ns.configNodes {
		if !n.NoAuto && len(n.Region) == 2 {
			out[n.Region] = true
		}
	}
	return out
}

// exit turns a destination into an exit. A pinned node is looked up among the
// live ones; one that is gone sends traffic to the fastest node of its region
// (or follows the main exit) and says so.
func (ns *nodeSet) exit(d routing.Dest, main bool, regions map[string]bool, n Notice, res *Result) configgen.Exit {
	switch d.Kind {
	case routing.DestDirect:
		if main {
			return configgen.Exit{Kind: configgen.ExitAuto}
		}
		return configgen.Exit{Kind: configgen.ExitDirect}
	case routing.DestReject:
		if main {
			return configgen.Exit{Kind: configgen.ExitAuto}
		}
		return configgen.Exit{Kind: configgen.ExitReject}
	case routing.DestAuto:
		return configgen.Exit{Kind: configgen.ExitAuto}
	case routing.DestRegion:
		return configgen.Exit{Kind: configgen.ExitRegion, Region: strings.ToUpper(d.Region)}
	case routing.DestNode:
		if d.Node != nil {
			if i, match := routing.Resolve(*d.Node, ns.live); match != routing.MatchNone {
				return configgen.Exit{Kind: configgen.ExitNode, Node: ns.keys[i], NoStandIn: d.NoFallback}
			}
			n.Code, n.Node = NoticePinGone, *d.Node
			res.Notices = append(res.Notices, n)
			if r := strings.ToUpper(d.Node.Region); r != "" && regions[r] {
				return configgen.Exit{Kind: configgen.ExitRegion, Region: r}
			}
		}
	}
	// Follow, or whatever could not be honoured.
	if main {
		return configgen.Exit{Kind: configgen.ExitAuto}
	}
	return configgen.Exit{Kind: configgen.ExitMain}
}

// ------------------------------------------------------------------- items

// item compiles the rules of one item.
func (c *Compiler) item(it routing.Item, data Data, missing map[string]bool) compiled {
	var sets [][]option.HeadlessRule
	var files []*rulefmt.Parsed
	var key strings.Builder
	info := ItemInfo{}
	visited := map[string]bool{}

	var addFile func(url string, depth int)
	addFile = func(url string, depth int) {
		if visited[url] {
			return
		}
		visited[url] = true
		var parsed *rulefmt.Parsed
		var err error
		if data != nil {
			parsed, err = data.Parsed(url)
		}
		if data == nil || err != nil || parsed == nil {
			info.Missing++
			missing[url] = true
			fmt.Fprintf(&key, "m:%s;", url)
			return
		}
		fmt.Fprintf(&key, "p:%p;", parsed)
		files = append(files, parsed)
		sets = append(sets, parsed.Rules)
		info.Unsupported += parsed.Stats.UnsupportedTotal()
		if depth > 0 {
			return
		}
		for _, r := range parsed.Refs {
			if u := refURL(r); u != "" {
				addFile(u, depth+1)
			}
		}
	}
	for _, src := range it.Sources {
		switch src.Kind {
		case routing.SourceURL:
			addFile(src.URL, 0)
		case routing.SourceInline:
			var rules []option.HeadlessRule
			for _, e := range src.Entries {
				if r, err := EntryRule(e); err == nil {
					rules = append(rules, r)
					fmt.Fprintf(&key, "e:%s=%s;", e.Kind, e.Value)
				}
			}
			sets = append(sets, rules)
		}
	}

	cacheKey := key.String()
	c.mu.Lock()
	if cd, ok := c.cache[cacheKey]; ok && cacheKey != "" {
		c.mu.Unlock()
		return cd
	}
	c.mu.Unlock()

	dom, ip := rulefmt.Split(sets...)
	cd := compiled{rules: ItemRules{Domain: dom, IP: ip}, files: files}
	cd.info = info
	cd.info.Domain, cd.info.IP = rulefmt.Count(dom).Domain, rulefmt.Count(ip).IP
	cd.process = rulefmt.Inspect(dom).Process
	var err error
	if cd.domain, err = rulefmt.Compile(dom); err != nil {
		cd.domain, cd.info.State = nil, ItemUnavailable
	}
	if cd.ip, err = rulefmt.Compile(ip); err != nil {
		cd.ip, cd.info.State = nil, ItemUnavailable
	}
	if host := rulefmt.HostRules(dom); len(host) > 0 {
		if len(host) == len(dom) {
			cd.dns = cd.domain
		} else if cd.dns, err = rulefmt.Compile(host); err != nil {
			cd.dns = nil
		}
	}
	switch {
	case len(cd.domain) == 0 && len(cd.ip) == 0:
		cd.info.State = ItemUnavailable
	case cd.info.State == ItemReady && (cd.info.Missing > 0 || cd.info.Unsupported > 0):
		cd.info.State = ItemPartial
	}
	if cacheKey != "" {
		c.mu.Lock()
		if len(c.cache) >= maxCache {
			c.cache = map[string]compiled{}
		}
		c.cache[cacheKey] = cd
		c.mu.Unlock()
	}
	return cd
}

// SourceRules returns the rules one source contributes to its item: its own,
// and one level of the files it refers to, the way the item is compiled. It is
// what "which source matched" is answered from. Files that cannot be read are
// listed in missing.
func SourceRules(src routing.Source, data Data) (rules []option.HeadlessRule, missing []string) {
	switch src.Kind {
	case routing.SourceInline:
		for _, e := range src.Entries {
			if r, err := EntryRule(e); err == nil {
				rules = append(rules, r)
			}
		}
	case routing.SourceURL:
		seen := map[string]bool{}
		var add func(url string, depth int)
		add = func(url string, depth int) {
			if seen[url] {
				return
			}
			seen[url] = true
			var parsed *rulefmt.Parsed
			var err error
			if data != nil {
				parsed, err = data.Parsed(url)
			}
			if data == nil || err != nil || parsed == nil {
				missing = append(missing, url)
				return
			}
			rules = append(rules, parsed.Rules...)
			if depth > 0 {
				return
			}
			for _, r := range parsed.Refs {
				if u := refURL(r); u != "" {
					add(u, depth+1)
				}
			}
		}
		add(src.URL, 0)
	}
	return rules, missing
}

func refURL(r rulefmt.Ref) string {
	switch r.Kind {
	case rulefmt.RefGeoSite:
		return ruleurl.GeoSite(r.Name)
	case rulefmt.RefGeoIP:
		return ruleurl.GeoIP(r.Name)
	case rulefmt.RefASN:
		return ruleurl.ASN(r.Name)
	}
	return ""
}

// -------------------------------------------------------------------- rules

// EntryRule turns a hand-written entry into the rule the core evaluates.
func EntryRule(e routing.Entry) (option.HeadlessRule, error) {
	if err := e.Valid(); err != nil {
		return option.HeadlessRule{}, err
	}
	e = e.Normalize()
	d := option.DefaultHeadlessRule{}
	switch e.Kind {
	case routing.EntryDomain:
		d.Domain = []string{e.Value}
	case routing.EntryDomainSuffix:
		d.DomainSuffix = []string{e.Value}
	case routing.EntryDomainKeyword:
		d.DomainKeyword = []string{e.Value}
	case routing.EntryDomainRegex:
		d.DomainRegex = []string{e.Value}
	case routing.EntryIPCIDR:
		d.IPCIDR = []string{e.Value}
	case routing.EntryPort:
		if strings.Contains(e.Value, ":") {
			d.PortRange = []string{e.Value}
		} else {
			n, err := strconv.ParseUint(e.Value, 10, 16)
			if err != nil {
				return option.HeadlessRule{}, err
			}
			d.Port = []uint16{uint16(n)}
		}
	case routing.EntryProcessName:
		d.ProcessName = []string{e.Value}
	case routing.EntryProcessPath:
		d.ProcessPath = []string{e.Value}
	default:
		return option.HeadlessRule{}, fmt.Errorf("unknown entry type %q", e.Kind)
	}
	return option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: d}, nil
}
