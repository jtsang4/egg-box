// Package catalog is the application's built-in directory of rule data: the
// services people ask for by name ("Netflix", "AI", "ads"), where to get their
// rules, and where traffic for them should usually go.
//
// It holds names, short descriptions and addresses and nothing else. The rule
// data stays where its projects publish it, so shipping a catalog never means
// redistributing someone's rules.
//
// There are three parts, searched together:
//
//   - a curated list of services, with Chinese and English names, aliases a
//     person might type ("奈飞", "网飞"), a recommended source and alternatives
//     from other projects, a suggested destination and a suggested position;
//   - a snapshot of the names MetaCubeX publishes as geosite (every service
//     its rule sets cover), so anything it has can be found;
//   - a snapshot of the categories blackmatrix7 publishes as Clash lists.
//
// The snapshots are plain text files next to this package and are refreshed
// with the application.
package catalog

import (
	_ "embed"
	"slices"
	"sort"
	"strings"
	"sync"

	"eggbox/internal/routing"
	"eggbox/internal/ruleurl"
)

//go:embed data/geosite.txt
var geositeText string

//go:embed data/geoip.txt
var geoipText string

//go:embed data/blackmatrix7.txt
var blackmatrix7Text string

// Names of the projects the catalog points at, as shown to people.
const (
	ProjectMetaCubeX    = "MetaCubeX"
	ProjectBlackmatrix7 = "blackmatrix7"
)

const blackmatrix7Raw = "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/"

// Class is what kind of service an entry is, for grouping and searching.
type Class string

const (
	ClassBlock    Class = "block"
	ClassAI       Class = "ai"
	ClassStream   Class = "stream"
	ClassSocial   Class = "social"
	ClassChat     Class = "chat"
	ClassDev      Class = "dev"
	ClassGame     Class = "game"
	ClassCloud    Class = "cloud"
	ClassFinance  Class = "finance"
	ClassDomestic Class = "domestic"
	ClassForeign  Class = "foreign"
)

// Classes lists the classes in the order they are shown.
var Classes = []Class{
	ClassBlock, ClassAI, ClassStream, ClassSocial, ClassChat, ClassDev,
	ClassGame, ClassCloud, ClassFinance, ClassDomestic, ClassForeign,
}

var classNames = map[Class][2]string{
	ClassBlock:    {"拦截", "Blocking"},
	ClassAI:       {"AI", "AI"},
	ClassStream:   {"流媒体", "Streaming"},
	ClassSocial:   {"社交", "Social"},
	ClassChat:     {"通讯", "Messaging"},
	ClassDev:      {"开发", "Development"},
	ClassGame:     {"游戏", "Games"},
	ClassCloud:    {"云服务", "Cloud"},
	ClassFinance:  {"金融", "Finance"},
	ClassDomestic: {"国内", "China"},
	ClassForeign:  {"海外", "Overseas"},
}

var classNotes = map[Class][2]string{
	ClassBlock:    {"拦截广告、追踪和恶意网站", "Blocks ads, trackers and harmful sites"},
	ClassAI:       {"AI 对话、绘图和开发工具", "AI chat, image and coding tools"},
	ClassStream:   {"视频、音乐和直播", "Video, music and live streams"},
	ClassSocial:   {"社交网络和社区", "Social networks and communities"},
	ClassChat:     {"即时通讯和会议", "Messaging and meetings"},
	ClassDev:      {"代码托管、软件包和开发者服务", "Code hosting, packages and developer services"},
	ClassGame:     {"游戏平台和游戏服务", "Game platforms and services"},
	ClassCloud:    {"云主机、网盘和内容分发", "Hosting, file storage and content delivery"},
	ClassFinance:  {"支付、银行和加密货币", "Payments, banks and crypto"},
	ClassDomestic: {"国内能直接访问的网站和服务", "Sites and services reachable at home"},
	ClassForeign:  {"需要经过节点访问的境外服务", "Services abroad that go through a node"},
}

// Name is the class's name in a language ("zh" or "en").
func (c Class) Name(lang string) string { return pick(classNames[c], lang) }

// Note is a sentence about what the class holds.
func (c Class) Note(lang string) string { return pick(classNotes[c], lang) }

func pick(pair [2]string, lang string) string {
	if strings.HasPrefix(lang, "zh") {
		return pair[0]
	}
	return pair[1]
}

// Prio says where an entry belongs in a plan, which is about how much of the
// traffic it claims: what blocks goes first, a service ahead of the broad
// categories that would swallow it, and the broad categories last.
type Prio int

const (
	// PrioBlock goes at the very top.
	PrioBlock Prio = iota
	// PrioService is a particular service: before the broad categories.
	PrioService
	// PrioBroad is a category of whole countries, last of the items.
	PrioBroad
)

// Alt is another place to get the rules of an entry.
type Alt struct {
	// Project is the project that publishes them ("anti-AD").
	Project string
	// Note says what is different about this one ("smaller", "Clash format").
	Note string
	// URLs are the files; their format is found out when they are read.
	URLs []string
}

// Entry is one curated service.
type Entry struct {
	ID string
	// Zh and En are the names; Aliases are other words people search with.
	Zh, En  string
	Aliases []string
	// DescZh and DescEn are a short sentence; the class's note stands in when
	// there is none.
	DescZh, DescEn string
	Icon           string
	Class          Class
	// Sites and IPs are the MetaCubeX names (geosite and geoip) of the
	// recommended source.
	Sites, IPs []string
	// Clash is the blackmatrix7 category, the alternative most services have.
	Clash string
	// Alts are further alternatives of other projects.
	Alts []Alt
	// Dest is where traffic for the entry usually goes.
	Dest routing.DestKind
	Prio Prio
}

// Name is the entry's name in a language.
func (e Entry) Name(lang string) string { return pick([2]string{e.Zh, e.En}, lang) }

// Desc is the entry's description in a language.
func (e Entry) Desc(lang string) string {
	if d := pick([2]string{e.DescZh, e.DescEn}, lang); d != "" {
		return d
	}
	return e.Class.Note(lang)
}

// Sources are the recommended rule files, as sources of an item.
func (e Entry) Sources() []routing.Source {
	var out []routing.Source
	for _, s := range e.Sites {
		out = append(out, routing.URLSource(ruleurl.GeoSite(s), "geosite: "+s, ProjectMetaCubeX))
	}
	for _, s := range e.IPs {
		out = append(out, routing.URLSource(ruleurl.GeoIP(s), "geoip: "+s, ProjectMetaCubeX))
	}
	return out
}

// Alternative is a set of rule files an entry can be made from other than the
// recommended one.
type Alternative struct {
	Project string
	Note    string
	Sources []routing.Source
}

// Alternatives lists the other places the entry's rules come from.
func (e Entry) Alternatives() []Alternative {
	var out []Alternative
	if e.Clash != "" {
		out = append(out, Alternative{
			Project: ProjectBlackmatrix7, Note: "Clash",
			Sources: []routing.Source{routing.URLSource(Blackmatrix7URL(e.Clash), e.Clash, ProjectBlackmatrix7)},
		})
	}
	for _, a := range e.Alts {
		alt := Alternative{Project: a.Project, Note: a.Note}
		for _, u := range a.URLs {
			info, err := ruleurl.Canonical(u)
			if err != nil {
				continue
			}
			alt.Sources = append(alt.Sources, routing.URLSource(info.URL, info.Name, a.Project))
		}
		if len(alt.Sources) > 0 {
			out = append(out, alt)
		}
	}
	return out
}

// Item makes an item from the entry in a language: the recommended source
// when alt is negative, otherwise the alt'th of Alternatives. The item has no
// id; adding it to a plan gives it one.
func (e Entry) Item(lang string, alt int) routing.Item {
	srcs := e.Sources()
	if alt >= 0 {
		if a := e.Alternatives(); alt < len(a) {
			srcs = a[alt].Sources
		}
	}
	it := routing.Item{
		Name: e.Name(lang), Icon: e.Icon, Sources: srcs, Enabled: true, Catalog: e.ID,
		Dest: routing.Dest{Kind: e.Dest}, Domestic: e.Dest == routing.DestDirect,
	}
	if other := e.Name(otherLang(lang)); other != "" && other != it.Name {
		it.Aliases = []string{other}
	}
	return it
}

func otherLang(lang string) string {
	if strings.HasPrefix(lang, "zh") {
		return "en"
	}
	return "zh"
}

// Blackmatrix7URL is the address of a blackmatrix7 category's Clash list.
func Blackmatrix7URL(category string) string {
	return blackmatrix7Raw + category + "/" + category + ".list"
}

// ---------------------------------------------------------------- the data

var (
	once        sync.Once
	byID        map[string]Entry
	geositeList []string
	geoipList   []string
	b7List      []string
	// covered are the snapshot names the curated entries already use, which
	// the search does not list a second time.
	coveredSite map[string]bool
	coveredB7   map[string]bool
)

func load() {
	once.Do(func() {
		byID = make(map[string]Entry, len(entries))
		coveredSite = map[string]bool{}
		coveredB7 = map[string]bool{}
		for _, e := range entries {
			byID[e.ID] = e
			for _, s := range e.Sites {
				coveredSite[s] = true
			}
			if e.Clash != "" {
				coveredB7[e.Clash] = true
			}
		}
		geositeList = lines(geositeText)
		geoipList = lines(geoipText)
		b7List = lines(blackmatrix7Text)
	})
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Entries returns the curated entries in the order they are shown: by class,
// then by name.
func Entries() []Entry {
	load()
	out := slices.Clone(entries)
	rank := map[Class]int{}
	for i, c := range Classes {
		rank[c] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Class] < rank[out[j].Class] })
	return out
}

// Get returns the entry with an identifier.
func Get(id string) (Entry, bool) {
	load()
	e, ok := byID[id]
	return e, ok
}

// GeoSiteNames returns every geosite name MetaCubeX publishes (as of the
// snapshot).
func GeoSiteNames() []string { load(); return slices.Clone(geositeList) }

// GeoIPNames returns every geoip name MetaCubeX publishes.
func GeoIPNames() []string { load(); return slices.Clone(geoipList) }

// Blackmatrix7Names returns every category blackmatrix7 publishes.
func Blackmatrix7Names() []string { load(); return slices.Clone(b7List) }

// --------------------------------------------------------------- searching

// OtherHit is a category of another project.
type OtherHit struct {
	Project string
	Name    string
	Source  routing.Source
}

// GeoSiteHit is a name MetaCubeX publishes.
type GeoSiteHit struct {
	Name   string
	Source routing.Source
}

// Results are what a search found, in the order they are shown.
type Results struct {
	Curated []Entry
	GeoSite []GeoSiteHit
	Other   []OtherHit
	// MoreGeoSite and MoreOther count what the limit cut off.
	MoreGeoSite, MoreOther int
}

// Search looks a query up in the curated entries (names in both languages,
// aliases, class names), in the geosite names and in the other projects'
// categories. An empty query lists every curated entry and nothing else. Words
// are matched anywhere, all of them. limit caps each of the two long lists.
func Search(query, lang string, limit int) Results {
	load()
	if limit <= 0 {
		limit = 30
	}
	words := strings.Fields(strings.ToLower(query))
	var res Results
	if len(words) == 0 {
		res.Curated = Entries()
		return res
	}

	type scored struct {
		e     Entry
		score int
	}
	var hits []scored
	for _, e := range entries {
		if s, ok := score(e, words); ok {
			hits = append(hits, scored{e, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score < hits[j].score })
	for _, h := range hits {
		res.Curated = append(res.Curated, h.e)
	}

	whole := strings.ToLower(strings.Join(words, " "))
	res.GeoSite, res.MoreGeoSite = searchNames(geositeList, words, whole, coveredSite, limit, func(n string) GeoSiteHit {
		return GeoSiteHit{Name: n, Source: routing.URLSource(ruleurl.GeoSite(n), "geosite: "+n, ProjectMetaCubeX)}
	})
	var other []OtherHit
	other, res.MoreOther = searchNames(b7List, words, whole, coveredB7, limit, func(n string) OtherHit {
		return OtherHit{Project: ProjectBlackmatrix7, Name: n, Source: routing.URLSource(Blackmatrix7URL(n), n, ProjectBlackmatrix7)}
	})
	res.Other = other
	return res
}

// score ranks an entry for a query (lower is better) and says whether it
// matches: every word must appear in the entry's names, aliases, id or class.
func score(e Entry, words []string) (int, bool) {
	fields := []string{e.ID, e.Zh, e.En}
	fields = append(fields, e.Aliases...)
	best := 100
	for _, w := range words {
		found := false
		for i, f := range fields {
			f = strings.ToLower(f)
			var s int
			switch {
			case f == w:
				s = 0
			case strings.HasPrefix(f, w):
				s = 1
			case strings.Contains(f, w):
				s = 2
			default:
				continue
			}
			if i >= 3 {
				s++ // an alias is a little less certain than a name
			}
			best = min(best, s)
			found = true
		}
		if !found {
			// The class names count too: "游戏" finds every game.
			names := strings.ToLower(classNames[e.Class][0] + " " + classNames[e.Class][1])
			if !strings.Contains(names, w) {
				return 0, false
			}
			best = min(best, 5)
		}
	}
	if best == 100 {
		best = 5
	}
	return best, true
}

// searchNames filters a snapshot of names by a query: names the curated
// entries already show are left out; exact and prefix matches come first, then
// shorter names.
func searchNames[T any](all []string, words []string, whole string, covered map[string]bool, limit int, mk func(string) T) ([]T, int) {
	type scored struct {
		name  string
		score int
	}
	var hits []scored
	for _, n := range all {
		l := strings.ToLower(n)
		ok := true
		for _, w := range words {
			if !strings.Contains(l, w) {
				ok = false
				break
			}
		}
		if !ok || covered[n] {
			continue
		}
		s := 2
		switch {
		case l == whole:
			s = 0
		case strings.HasPrefix(l, whole):
			s = 1
		}
		hits = append(hits, scored{n, s})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score < hits[j].score
		}
		return len(hits[i].name) < len(hits[j].name)
	})
	more := 0
	if len(hits) > limit {
		more = len(hits) - limit
		hits = hits[:limit]
	}
	out := make([]T, len(hits))
	for i, h := range hits {
		out[i] = mk(h.name)
	}
	return out, more
}

// -------------------------------------------------------------- positioning

// broadKeys are the names of the shipped items that claim whole countries.
var broadKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range []string{"cn", "foreign"} {
		for _, n := range (routing.Item{Key: k}).Keys() {
			m[n] = true
		}
	}
	return m
}()

// broad reports whether an item claims whole countries (so something more
// particular belongs before it).
func broad(it routing.Item) bool {
	if it.Key == "cn" || it.Key == "foreign" {
		return true
	}
	if e, ok := Get(it.Catalog); ok && e.Prio == PrioBroad {
		return true
	}
	for _, k := range it.Keys() {
		if broadKeys[k] {
			return true
		}
	}
	return false
}

// InsertBefore says where an entry belongs among the items of a plan: the
// id of the item it goes before, or "" for the end. What blocks goes before
// everything that does not; a service goes before the first item that claims
// whole countries; a broad category goes last. Whatever the class, an entry
// goes before an item whose rules take in all of its own (Netflix before a
// streaming item that has Netflix among others), or that item would answer
// first and the new one would never be asked.
func InsertBefore(items []routing.Item, e Entry) string {
	at := len(items)
	switch e.Prio {
	case PrioBlock:
		for i, it := range items {
			if it.Dest.Kind != routing.DestReject {
				at = i
				break
			}
		}
	case PrioService:
		for i, it := range items {
			if broad(it) {
				at = i
				break
			}
		}
	}
	want := urlSet(e.Sources())
	for i, it := range items[:at] {
		if have := urlSet(it.Sources); len(have) > len(want) && contains(have, want) {
			at = i
			break
		}
	}
	if at == len(items) {
		return ""
	}
	return items[at].ID
}

// Covers reports the item of a plan that already is the entry: one made from
// it, or one with the same rule files, so the interface can say "already
// there" instead of adding it twice. An item that has the entry's files among
// others (a streaming item that has Netflix's) is not the entry: a person may
// want Netflix on its own, to send it somewhere else.
func Covers(items []routing.Item, e Entry) (routing.Item, bool) {
	want := urlSet(e.Sources())
	if len(want) == 0 {
		return routing.Item{}, false
	}
	for _, it := range items {
		if it.Catalog == e.ID {
			return it, true
		}
		have := urlSet(it.Sources)
		if len(have) == len(want) && contains(have, want) {
			return it, true
		}
	}
	return routing.Item{}, false
}

// urlSet is the addresses of the rule files among sources; the lines a person
// wrote into an item do not make it another item.
func urlSet(srcs []routing.Source) map[string]bool {
	out := map[string]bool{}
	for _, s := range srcs {
		if s.URL != "" {
			out[s.URL] = true
		}
	}
	return out
}

func contains(have, want map[string]bool) bool {
	if len(want) == 0 {
		return false
	}
	for u := range want {
		if !have[u] {
			return false
		}
	}
	return true
}
