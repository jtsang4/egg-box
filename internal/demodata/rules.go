package demodata

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"

	"eggbox/internal/rulefmt"
)

// ruleSet is what an invented rule set holds: domain suffixes and address
// ranges. The names are public services, the addresses come from the ranges
// reserved for documentation (RFC 5737), and the invented traffic of the
// demonstration core (internal/agent/engine/demo) goes to exactly these.
type ruleSet struct {
	suffixes []string
	cidrs    []string
}

// geosite and geoip are the files of the shared dataset the built-in plan
// uses, by name.
var geosite = map[string]ruleSet{
	"category-ads-all": {suffixes: []string{"ads-tracker.example", "adnetwork.example"}},
	"category-ai-!cn":  {suffixes: []string{"openai.com", "chatgpt.com", "anthropic.com", "claude.ai"}},
	"telegram":         {suffixes: []string{"telegram.org", "t.me", "telegram.me"}},
	"youtube":          {suffixes: []string{"youtube.com", "ytimg.com", "googlevideo.com", "youtu.be"}},
	"netflix":          {suffixes: []string{"netflix.com", "nflxvideo.net"}},
	"disney":           {suffixes: []string{"disneyplus.com", "disney.com"}},
	"hbo":              {suffixes: []string{"max.com", "hbo.com"}},
	"primevideo":       {suffixes: []string{"primevideo.com"}},
	"spotify":          {suffixes: []string{"spotify.com", "scdn.co"}},
	"google":           {suffixes: []string{"google.com", "gstatic.com", "googleapis.com"}},
	"twitter":          {suffixes: []string{"twitter.com", "x.com"}},
	"facebook":         {suffixes: []string{"facebook.com", "fbcdn.net"}},
	"instagram":        {suffixes: []string{"instagram.com"}},
	"tiktok":           {suffixes: []string{"tiktok.com"}},
	"github":           {suffixes: []string{"github.com", "githubusercontent.com", "githubassets.com"}},
	"microsoft":        {suffixes: []string{"microsoft.com", "windowsupdate.com", "live.com"}},
	"apple":            {suffixes: []string{"apple.com", "icloud.com", "mzstatic.com"}},
	"cn":               {suffixes: []string{"baidu.com", "bilibili.com", "weibo.com", "alicdn.com", "qq.com", "taobao.com"}},
	"geolocation-!cn":  {suffixes: []string{"reddit.com", "npmjs.org", "steampowered.com", "discordapp.com", "wikipedia.org"}},
}

var geoip = map[string]ruleSet{
	"telegram": {cidrs: []string{"203.0.113.96/28"}},
	"netflix":  {cidrs: []string{"203.0.113.112/28"}},
	"google":   {cidrs: []string{"203.0.113.64/27"}},
	"twitter":  {cidrs: []string{"203.0.113.72/29"}},
	"facebook": {cidrs: []string{"203.0.113.80/29"}},
	"cn":       {cidrs: []string{"198.51.100.0/24"}},
}

// auroraSets are the rule sets Aurora Cloud's own routing plan refers to.
var auroraSets = map[string]ruleSet{
	"ads":             {suffixes: []string{"ads-tracker.example"}},
	"ai":              {suffixes: []string{"openai.com", "chatgpt.com", "anthropic.com", "claude.ai"}},
	"telegram-domain": {suffixes: []string{"telegram.org", "t.me"}},
	"telegram-ip":     {cidrs: []string{"203.0.113.96/28"}},
	"streaming":       {suffixes: []string{"netflix.com", "spotify.com", "hbo.com", "disneyplus.com", "primevideo.com"}},
	"games":           {suffixes: []string{"steampowered.com", "discordapp.com", "epicgames.com"}},
	"cn-domain":       {suffixes: []string{"baidu.com", "bilibili.com", "weibo.com", "alicdn.com", "qq.com", "taobao.com"}},
	"cn-ip":           {cidrs: []string{"198.51.100.0/24"}},
}

// srsOf writes a rule set as the binary file the projects publish.
func srsOf(s ruleSet) []byte {
	var rules []option.HeadlessRule
	if len(s.suffixes) > 0 {
		rules = append(rules, option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{DomainSuffix: s.suffixes}})
	}
	if len(s.cidrs) > 0 {
		rules = append(rules, option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: option.DefaultHeadlessRule{IPCIDR: s.cidrs}})
	}
	data, err := rulefmt.Compile(rules)
	if err != nil {
		// Fixed inputs: this is a programming error, not a runtime condition.
		panic(fmt.Sprintf("demodata: build rule set: %v", err))
	}
	return data
}

var nonLabel = regexp.MustCompile(`[^a-z0-9]+`)

// nameOf is the service a file name stands for ("Netflix.list" -> "netflix").
func nameOf(file string) string {
	base := strings.TrimSuffix(file, path.Ext(file))
	n := strings.Trim(nonLabel.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if n == "" {
		return "service"
	}
	return n
}

// genericSet is the rule set of a file the invented world has no particular
// content for: one domain named after the file, and one address.
func genericSet(file string) ruleSet {
	return ruleSet{suffixes: []string{nameOf(file) + ".example"}, cidrs: []string{"203.0.113.250/32"}}
}

// ruleFile answers a request to the rules host. The path is where the file
// lives at the project the mirror stands in for, so any file a person pastes
// from the well-known projects has something to download; what is not a rule
// file is not found.
func ruleFile(p string) (body []byte, contentType string, ok bool) {
	file := path.Base(p)
	ext := strings.ToLower(path.Ext(file))
	name := strings.TrimSuffix(file, path.Ext(file))
	const binary = "application/octet-stream"
	switch {
	case strings.HasPrefix(p, "/aurora/") && ext == ".srs":
		s, found := auroraSets[name]
		if !found {
			return nil, "", false
		}
		return srsOf(s), binary, true
	case strings.Contains(p, "/geo/geosite/") && ext == ".srs":
		s, found := geosite[name]
		if !found {
			s = genericSet(file)
		}
		return srsOf(ruleSet{suffixes: s.suffixes}), binary, true
	case strings.Contains(p, "/geo/geoip/") && ext == ".srs":
		s, found := geoip[name]
		if !found {
			s = ruleSet{cidrs: genericSet(file).cidrs}
		}
		return srsOf(ruleSet{cidrs: s.cidrs}), binary, true
	case ext == ".srs":
		return srsOf(genericSet(file)), binary, true
	case ext == ".list" || ext == ".txt":
		s := genericSet(file)
		var b strings.Builder
		fmt.Fprintf(&b, "# %s (invented)\nDOMAIN-SUFFIX,%s\nDOMAIN-KEYWORD,%s-cdn\n", name, s.suffixes[0], nameOf(file))
		for _, c := range s.cidrs {
			fmt.Fprintf(&b, "IP-CIDR,%s,no-resolve\n", c)
		}
		return []byte(b.String()), "text/plain; charset=utf-8", true
	case ext == ".json":
		s := genericSet(file)
		body := fmt.Sprintf(`{"version":3,"rules":[{"domain_suffix":[%q],"ip_cidr":[%q]}]}`, s.suffixes[0], s.cidrs[0])
		return []byte(body), "application/json", true
	}
	return nil, "", false
}
