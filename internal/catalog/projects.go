package catalog

// Project is a community project whose rule files the application reads.
type Project struct {
	Name string
	// Role is what the project is mostly used for: "base" (country and service
	// data), "service" (one list per service) or "ads".
	Role string
	// Format is how its files are written: "srs" and "json" are sing-box's,
	// "clash" is Clash and Surge text, "adblock" is an Adblock filter list.
	Format string
	// Example is the address of one file the project publishes: what the
	// application is expected to be able to add from it.
	Example string
}

// Projects are the projects the application knows how to read, one example file
// each. The live test (opt-in) adds each of them, so a project changing where
// it publishes shows up as a failing check instead of a bad experience.
func Projects() []Project {
	return []Project{
		{"MetaCubeX", "base", "srs", "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/geosite/netflix.srs"},
		{"SagerNet", "base", "srs", "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-netflix.srs"},
		{"SagerNet", "base", "srs", "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs"},
		{"Loyalsoldier", "base", "srs", "https://raw.githubusercontent.com/Loyalsoldier/geoip/release/srs/cn.srs"},
		{"DustinWin", "base", "srs", "https://github.com/DustinWin/ruleset_geodata/releases/download/sing-box-ruleset/ai.srs"},
		{"blackmatrix7", "service", "clash", "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/Netflix/Netflix.list"},
		{"Loyalsoldier", "service", "clash", "https://raw.githubusercontent.com/Loyalsoldier/clash-rules/release/proxy.txt"},
		{"ACL4SSR", "service", "clash", "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ChinaDomain.list"},
		{"SukkaW", "service", "json", "https://ruleset.skk.moe/sing-box/non_ip/ai.json"},
		{"VPSDance", "service", "json", "https://raw.githubusercontent.com/VPSDance/ai-proxy-rules/main/rules/sing-box/all.json"},
		{"anti-AD", "ads", "srs", "https://anti-ad.net/anti-ad-sing-box.srs"},
		{"217heidai", "ads", "srs", "https://raw.githubusercontent.com/217heidai/adblockfilters/main/rules/adblocksingbox.srs"},
		{"AWAvenue", "ads", "json", "https://raw.githubusercontent.com/TG-Twilight/AWAvenue-Ads-Rule/main/Filters/AWAvenue-Ads-Rule-Singbox.json"},
		{"hagezi", "ads", "adblock", "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/adblock/pro.txt"},
	}
}
