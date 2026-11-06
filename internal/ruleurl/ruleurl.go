// Package ruleurl knows where rule files live: it turns whatever address a
// person pasted into one canonical address per file, maps mihomo's binary
// .mrs files to the sing-box files the same projects publish, names the
// well-known projects, and builds the addresses of the shared datasets
// (geosite, geoip, ASN).
//
// Everything here is string work with no network and no state, so that the
// downloader, the catalog and the plan model agree on what "the same file"
// means: one canonical address, one download, one stored copy.
package ruleurl

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// ErrAddress means the text is not an address a rule file can be fetched from.
var ErrAddress = errors.New("not a web address of a rule file")

// Info describes a canonical address.
type Info struct {
	// URL is the canonical address: the file's own home, without mirror
	// prefixes, web-page forms or encoding differences.
	URL string
	// Project is the project that publishes the file, when it is a known one.
	Project string
	// Name is a short name taken from the file name ("openai").
	Name string
	// Stripped is set when a mirror prefix was removed.
	Stripped bool
	// Converted is set when the address was rewritten to the file's own: a
	// GitHub page to the raw file, or a CDN address to the repository's.
	Converted bool
	// Swapped is set when an .mrs address was replaced by the address of its
	// sing-box counterpart.
	Swapped bool
	// MRS is set for a mihomo .mrs file with no sing-box counterpart known,
	// which cannot be read.
	MRS bool
}

const (
	rawHost = "raw.githubusercontent.com"
	metaRaw = "https://" + rawHost + "/MetaCubeX/meta-rules-dat/sing/"
)

// GeoSite is the address of a geosite category (the domains of a service).
func GeoSite(name string) string { return metaRaw + "geo/geosite/" + escapeSegment(name) + ".srs" }

// GeoIP is the address of a country or service IP set.
func GeoIP(name string) string {
	return metaRaw + "geo/geoip/" + escapeSegment(strings.ToLower(name)) + ".srs"
}

// ASN is the address of the IP ranges of an autonomous system; n may carry
// the "AS" prefix.
func ASN(n string) string {
	n = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(n)), "AS")
	return metaRaw + "asn/AS" + escapeSegment(n) + ".srs"
}

// Canonical normalizes an address. It never touches the network.
func Canonical(raw string) (Info, error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return Info{}, ErrAddress
	}
	var info Info
	s, info.Stripped = stripMirror(s)

	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return Info{}, ErrAddress
	}
	u.Fragment = ""
	host := strings.ToLower(u.Host)
	p := unescapePath(u)
	query := u.RawQuery
	if !plainPath(u, p) {
		return Info{}, ErrAddress
	}

	switch {
	case host == "github.com":
		if raw, ok := githubPage(p); ok {
			p, host, query, info.Converted = raw, rawHost, "", true
		}
	case isJSDelivr(host):
		if raw, ok := jsdelivrToRaw(p); ok {
			p, host, query, info.Converted = raw, rawHost, "", true
		}
	}
	if host == rawHost {
		p = trimRefs(p)
	}
	if np, ok := mapMRS(host, p); ok {
		p, info.Swapped = np, true
	} else if strings.HasSuffix(strings.ToLower(p), ".mrs") {
		info.MRS = true
	}

	out := u.Scheme + "://" + host + escapePath(p)
	if query != "" {
		out += "?" + query
	}
	info.URL = out
	info.Project = projectOf(host, p)
	info.Name = nameOf(p)
	return info, nil
}

// Display is an address as it may be shown or logged: no credentials, no
// query (which is where tokens live).
func Display(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "?"
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// IsGitHubRaw reports whether a canonical address is a file of a GitHub
// repository, which a CDN can serve too.
func IsGitHubRaw(canonical string) bool {
	return strings.HasPrefix(canonical, "https://"+rawHost+"/")
}

// JSDelivr returns the CDN address of a GitHub repository file, or "" when
// the address is not one.
func JSDelivr(canonical string) string {
	if !IsGitHubRaw(canonical) {
		return ""
	}
	u, err := url.Parse(canonical)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/"), "/", 4)
	if len(parts) < 4 || parts[3] == "" {
		return ""
	}
	return "https://cdn.jsdelivr.net/gh/" + parts[0] + "/" + parts[1] + "@" + parts[2] + "/" + parts[3]
}

// ValidPrefix checks a custom mirror prefix ("https://gh-proxy.com/").
func ValidPrefix(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	u, err := url.Parse(prefix)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%w: a mirror prefix is a web address such as https://mirror.example/", ErrAddress)
	}
	return nil
}

// WithPrefix puts a custom mirror prefix in front of an address.
func WithPrefix(prefix, canonical string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || ValidPrefix(prefix) != nil {
		return ""
	}
	if !strings.HasSuffix(prefix, "/") && !strings.Contains(prefix, "?") {
		prefix += "/"
	}
	return prefix + canonical
}

// ---------------------------------------------------------------- stripping

var embedded = regexp.MustCompile(`(?i)https?(?:://|%3A%2F%2F)`)

// stripMirror removes any mirror prefix: the address of the real file is the
// last one written inside the text.
func stripMirror(s string) (string, bool) {
	stripped := false
	for i := 0; i < 4; i++ {
		start := strings.Index(s, "://")
		if start < 0 {
			break
		}
		rest := s[start+3:]
		loc := embedded.FindStringIndex(rest)
		if loc == nil {
			break
		}
		inner := rest[loc[0]:]
		if l := strings.ToLower(inner); strings.HasPrefix(l, "http%3a") || strings.HasPrefix(l, "https%3a") {
			if dec, err := url.QueryUnescape(inner); err == nil {
				inner = dec
			}
		}
		s, stripped = inner, true
	}
	return s, stripped
}

// ------------------------------------------------------------ GitHub shapes

// githubPage turns the path of a github.com page for a file into the path of
// the raw file: /o/r/blob/ref/path and /o/r/raw/ref/path.
func githubPage(p string) (string, bool) {
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 5)
	if len(parts) < 5 || (parts[2] != "blob" && parts[2] != "raw") {
		return "", false
	}
	return "/" + parts[0] + "/" + parts[1] + "/" + parts[3] + "/" + parts[4], true
}

// trimRefs turns /o/r/refs/heads/b/path (and refs/tags) into /o/r/b/path:
// both spellings name the same file.
func trimRefs(p string) string {
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 6)
	if len(parts) < 6 || parts[2] != "refs" || (parts[3] != "heads" && parts[3] != "tags") {
		return p
	}
	return "/" + parts[0] + "/" + parts[1] + "/" + parts[4] + "/" + parts[5]
}

func isJSDelivr(host string) bool {
	return host == "cdn.jsdelivr.net" || strings.HasSuffix(host, ".jsdelivr.net")
}

// jsdelivrToRaw turns /gh/o/r@ref/path into /o/r/ref/path. Without "@ref" the
// CDN picks a version itself, so there is nothing certain to rewrite to.
func jsdelivrToRaw(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, "/gh/")
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		return "", false
	}
	repo, ref, ok := strings.Cut(parts[1], "@")
	if !ok || repo == "" || ref == "" {
		return "", false
	}
	return "/" + parts[0] + "/" + repo + "/" + ref + "/" + parts[2], true
}

// ------------------------------------------------------------- .mrs mapping

type mrsRule struct {
	host string
	re   *regexp.Regexp
	to   string
}

// mrsTable maps mihomo's binary files to the sing-box files the same
// projects publish next to them. Keep it small and exact: a wrong guess
// would silently load another list.
var mrsTable = []mrsRule{
	// MetaCubeX: the "meta" branch holds Clash data, the "sing" branch the same names for sing-box.
	{rawHost, regexp.MustCompile(`^/MetaCubeX/meta-rules-dat/meta/((?:geo|geo-lite)/(?:geosite|geoip)|asn)/([^/]+)\.mrs$`), "/MetaCubeX/meta-rules-dat/sing/$1/$2.srs"},
	// Loyalsoldier/geoip: release/mrs next to release/srs.
	{rawHost, regexp.MustCompile(`^/Loyalsoldier/geoip/release/mrs/([^/]+)\.mrs$`), "/Loyalsoldier/geoip/release/srs/$1.srs"},
	// DustinWin: two release tags, one per client.
	{"github.com", regexp.MustCompile(`^/DustinWin/ruleset_geodata/releases/download/mihomo-ruleset/([^/]+)\.mrs$`), "/DustinWin/ruleset_geodata/releases/download/sing-box-ruleset/$1.srs"},
	{rawHost, regexp.MustCompile(`^/DustinWin/ruleset_geodata/mihomo-ruleset/([^/]+)\.mrs$`), "/DustinWin/ruleset_geodata/sing-box-ruleset/$1.srs"},
	// 217heidai/adblockfilters.
	{rawHost, regexp.MustCompile(`^/217heidai/adblockfilters/([^/]+)/rules/adblockmihomo(lite)?\.mrs$`), "/217heidai/adblockfilters/$1/rules/adblocksingbox$2.srs"},
	// AWAvenue: only the JSON is published for sing-box.
	{rawHost, regexp.MustCompile(`^/TG-Twilight/AWAvenue-Ads-Rule/([^/]+)/Filters/AWAvenue-Ads-Rule-Clash((?:-[A-Za-z.]+)?)\.mrs$`), "/TG-Twilight/AWAvenue-Ads-Rule/$1/Filters/AWAvenue-Ads-Rule-Singbox$2.json"},
}

func mapMRS(host, p string) (string, bool) {
	if !strings.HasSuffix(strings.ToLower(p), ".mrs") {
		return "", false
	}
	for _, r := range mrsTable {
		if r.host != host {
			continue
		}
		if r.re.MatchString(p) {
			return r.re.ReplaceAllString(p, r.to), true
		}
	}
	return "", false
}

// ----------------------------------------------------------------- projects

var projects = []struct{ host, prefix, name string }{
	{rawHost, "/metacubex/meta-rules-dat/", "MetaCubeX"},
	{rawHost, "/sagernet/sing-geosite/", "SagerNet"},
	{rawHost, "/sagernet/sing-geoip/", "SagerNet"},
	{rawHost, "/loyalsoldier/", "Loyalsoldier"},
	{rawHost, "/dustinwin/ruleset_geodata/", "DustinWin"},
	{"github.com", "/dustinwin/ruleset_geodata/", "DustinWin"},
	{rawHost, "/blackmatrix7/ios_rule_script/", "blackmatrix7"},
	{rawHost, "/acl4ssr/acl4ssr/", "ACL4SSR"},
	{rawHost, "/sukkaw/surge/", "SukkaW"},
	{"ruleset.skk.moe", "/", "SukkaW"},
	{rawHost, "/vpsdance/ai-proxy-rules/", "VPSDance"},
	{rawHost, "/privacy-protection-tools/anti-ad/", "anti-AD"},
	{"anti-ad.net", "/", "anti-AD"},
	{rawHost, "/217heidai/adblockfilters/", "217heidai"},
	{rawHost, "/tg-twilight/awavenue-ads-rule/", "AWAvenue"},
	{rawHost, "/hagezi/dns-blocklists/", "hagezi"},
}

func projectOf(host, p string) string {
	lower := strings.ToLower(p)
	for _, pr := range projects {
		if pr.host == host && strings.HasPrefix(lower, pr.prefix) {
			return pr.name
		}
	}
	return ""
}

func nameOf(p string) string {
	base := path.Base(p)
	if base == "." || base == "/" {
		return ""
	}
	if ext := path.Ext(base); ext != "" && len(ext) <= 6 {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

// ----------------------------------------------------------------- escaping

// unescapePath is the decoded path of u; an undecodable path stays as written.
func unescapePath(u *url.URL) string {
	if u.Path != "" || u.RawPath == "" {
		if u.Path == "" {
			return "/"
		}
		return u.Path
	}
	if dec, err := url.PathUnescape(u.RawPath); err == nil {
		return dec
	}
	return u.RawPath
}

// plainPath refuses paths that climb out of their directory or hide a slash
// in an escape: a rule file's address has no use for either, and servers
// disagree about what they mean.
func plainPath(u *url.URL, decoded string) bool {
	if strings.Contains(decoded, "\\") || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return false
	}
	for _, seg := range strings.Split(decoded, "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

// escapeSegment is escapePath for one path segment: a slash is escaped too,
// so a name cannot reach another directory.
func escapeSegment(s string) string {
	return strings.ReplaceAll(escapePath(s), "/", "%2F")
}

// escapePath writes a decoded path in one fixed way, so that "!" and "%21"
// are the same file. Characters RFC 3986 allows in a path stay as they are.
func escapePath(p string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case strings.IndexByte("-._~!$&'()*+,;=:@/", c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
