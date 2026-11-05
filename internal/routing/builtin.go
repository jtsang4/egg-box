package routing

import (
	"strings"

	"eggbox/internal/ruleurl"
)

// BuiltinVersion changes whenever the shipped plan does. A refresh of the
// shipped plan keeps what the person chose, because choices are kept by name.
const BuiltinVersion = 1

// MetaCubeX publishes the rule data the shipped plan uses: one file per
// service, for names (geosite) and for addresses (geoip), refreshed daily. It
// also has the address ranges of services, which the sing-box project's own
// rule sets do not, and its names match what most subscription templates use,
// so a plan that came with a subscription and the shipped plan can share files.

// GeoSite and GeoIP are the canonical addresses of MetaCubeX's rule sets.
func GeoSite(name string) string { return ruleurl.GeoSite(name) }
func GeoIP(name string) string   { return ruleurl.GeoIP(name) }

// MetaCubeX is the project name shown for these sources.
const MetaCubeX = "MetaCubeX"

func geosite(name string) Source { return URLSource(GeoSite(name), "geosite: "+name, MetaCubeX) }
func geoip(name string) Source   { return URLSource(GeoIP(name), "geoip: "+name, MetaCubeX) }

// builtinNames are the names an item of the shipped plan answers to, in both
// languages and as other plans tend to call it. The first is the Chinese name
// the plan stores, the second the English one the interface shows.
var builtinNames = map[string][]string{
	"ads":       {"广告拦截", "Ad blocking", "广告", "ads", "adblock", "去广告"},
	"ai":        {"AI 服务", "AI services", "ai", "openai", "chatgpt", "人工智能"},
	"telegram":  {"Telegram", "Telegram", "电报"},
	"youtube":   {"YouTube", "YouTube", "油管"},
	"streaming": {"流媒体", "Streaming", "国际媒体", "国外媒体", "netflix"},
	"google":    {"Google", "Google", "谷歌", "谷歌服务", "google services"},
	"social":    {"社交媒体", "Social media", "社交", "social"},
	"github":    {"GitHub", "GitHub"},
	"microsoft": {"微软", "Microsoft", "微软服务", "microsoft services"},
	"apple":     {"苹果", "Apple", "苹果服务", "apple services"},
	"cn":        {"国内网站", "China sites", "国内服务", "国内", "china", "domestic", "cn"},
	"foreign":   {"国外网站", "International sites", "国外", "国际", "foreign", "global"},
	// Items an imported plan makes of rules that name no group.
	"direct": {"直连", "Direct", "direct"},
	"reject": {"拦截", "Blocked", "reject", "block"},
}

// BuiltinName returns the display name of a shipped item in a language
// ("zh" or "en"); the empty string for an unknown key.
func BuiltinName(key, lang string) string {
	n := builtinNames[key]
	if len(n) < 2 {
		return ""
	}
	if strings.HasPrefix(lang, "zh") {
		return n[0]
	}
	return n[1]
}

// Builtin returns the plan the application ships.
func Builtin() *Plan {
	item := func(key, icon string, dest Dest, on bool, srcs ...Source) Item {
		return Item{ID: "b-" + key, Key: key, Name: builtinNames[key][0], Icon: icon, Sources: srcs, Dest: dest, Enabled: on}
	}
	return &Plan{
		ID: BuiltinID, Kind: PlanBuiltin, Name: "内置推荐",
		Fallback: Follow(),
		Items: []Item{
			item("ads", "shield", Reject(), false, geosite("category-ads-all")),
			item("ai", "sparkles", Follow(), true, geosite("category-ai-!cn")),
			item("telegram", "send", Follow(), true, geosite("telegram"), geoip("telegram")),
			item("youtube", "play", Follow(), true, geosite("youtube")),
			item("streaming", "film", Follow(), true,
				geosite("netflix"), geosite("disney"), geosite("hbo"), geosite("primevideo"), geosite("spotify"), geoip("netflix")),
			item("google", "search", Follow(), true, geosite("google"), geoip("google")),
			item("social", "users", Follow(), true,
				geosite("twitter"), geosite("facebook"), geosite("instagram"), geosite("tiktok"), geoip("twitter"), geoip("facebook")),
			item("github", "code", Follow(), true, geosite("github")),
			item("microsoft", "windows", Direct(), true, geosite("microsoft")),
			item("apple", "apple", Direct(), true, geosite("apple")),
			item("cn", "home", Direct(), true, geosite("cn"), geoip("cn")),
			item("foreign", "globe", Follow(), true, geosite("geolocation-!cn")),
		},
	}
}
