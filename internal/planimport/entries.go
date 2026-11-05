package planimport

import (
	"regexp"
	"strings"

	"eggbox/internal/routing"
	"eggbox/internal/ruleurl"
)

// entriesOf turns values of one kind into hand-written entries, leaving out
// the ones that are not valid.
func (d *doc) entriesOf(kind routing.EntryKind, values ...string) []routing.Entry {
	var out []routing.Entry
	for _, v := range values {
		e := routing.Entry{Kind: kind, Value: strings.TrimSpace(v)}.Normalize()
		if e.Valid() != nil {
			d.skip("invalid_" + string(kind))
			continue
		}
		out = append(out, e)
	}
	return out
}

// portEntries reads port conditions: "443", "8000-9000", "8000:9000".
func (d *doc) portEntries(values ...string) []routing.Entry {
	var out []routing.Entry
	for _, v := range values {
		for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == '/' || r == ',' || r == ' ' }) {
			out = append(out, d.entriesOf(routing.EntryPort, part)...)
		}
	}
	return out
}

func inlineSource(entries []routing.Entry) (routing.Source, bool) {
	if len(entries) == 0 {
		return routing.Source{}, false
	}
	return routing.Source{Kind: routing.SourceInline, Entries: entries}, true
}

// fileSource makes the source for a rule file at an address the provider
// wrote, and tells whether it is the list of private networks.
func (d *doc) fileSource(raw, name string) (src routing.Source, lan, ok bool) {
	info, err := ruleurl.Canonical(raw)
	if err != nil {
		d.skip("bad_address")
		return routing.Source{}, false, false
	}
	if isPrivateList(info.URL, name) {
		return routing.Source{}, true, true
	}
	return routing.URLSource(info.URL, name, info.Project), false, true
}

var privateTag = regexp.MustCompile(`(?i)^(geoip[-_:]?)?(private|lan|local)([-_]?ip)?$`)

// isPrivateList recognises the data set of private networks, which the
// application's own "local network goes straight out" switch covers.
func isPrivateList(url, name string) bool {
	u := strings.ToLower(url)
	if strings.HasSuffix(u, "/geoip/private.srs") || strings.HasSuffix(u, "/private.srs") || strings.HasSuffix(u, "/geoip/private.json") {
		return true
	}
	return privateTag.MatchString(strings.TrimSpace(name))
}

// geoSource is the data set a GEOSITE or GEOIP reference names.
func (d *doc) geoSource(kind, name string) (src routing.Source, lan, ok bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return routing.Source{}, false, false
	}
	switch kind {
	case "geosite":
		return routing.URLSource(routing.GeoSite(strings.ToLower(name)), "geosite: "+name, routing.MetaCubeX), false, true
	case "geoip":
		if privateTag.MatchString(name) {
			return routing.Source{}, true, true
		}
		return routing.URLSource(routing.GeoIP(strings.ToLower(name)), "geoip: "+name, routing.MetaCubeX), false, true
	}
	return routing.Source{}, false, false
}

// glob turns a wildcard domain ("*.example.com") into the regular expression
// that means the same.
func glob(v string) string {
	var sb strings.Builder
	sb.WriteByte('^')
	for _, r := range strings.ToLower(v) {
		switch r {
		case '*':
			sb.WriteString(".*")
		case '?':
			sb.WriteString(".")
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteByte('$')
	return sb.String()
}
