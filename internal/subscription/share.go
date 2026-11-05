package subscription

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
)

// shareLink is a share link taken apart leniently. Real-world links are often
// not valid URLs: raw spaces and emoji in the name, unescaped characters in
// passwords. net/url would reject many of them, so the parts are cut by hand.
type shareLink struct {
	Scheme   string
	UserInfo string // raw, still percent-encoded
	Host     string
	Port     int
	PortSpec string // everything after the last ':' in the authority ("443", "443,5000-6000")
	Query    url.Values
	Fragment string // decoded
}

func splitShare(raw string) (*shareLink, bool) {
	raw = strings.TrimSpace(raw)
	i := strings.Index(raw, "://")
	if i <= 0 {
		return nil, false
	}
	l := &shareLink{Scheme: strings.ToLower(raw[:i])}
	rest := raw[i+3:]
	if j := strings.Index(rest, "#"); j >= 0 {
		l.Fragment = unescape(rest[j+1:])
		rest = rest[:j]
	}
	if j := strings.Index(rest, "?"); j >= 0 {
		l.Query = parseQuery(rest[j+1:])
		rest = rest[:j]
	}
	if l.Query == nil {
		l.Query = url.Values{}
	}
	if j := strings.Index(rest, "/"); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.LastIndex(rest, "@"); j >= 0 {
		l.UserInfo = rest[:j]
		rest = rest[j+1:]
	}
	host := rest
	if strings.HasPrefix(host, "[") {
		if j := strings.Index(host, "]"); j > 0 {
			l.Host = host[1:j]
			host = host[j+1:]
			host = strings.TrimPrefix(host, ":")
			l.PortSpec = host
		} else {
			return nil, false
		}
	} else if j := strings.LastIndex(host, ":"); j >= 0 {
		l.Host, l.PortSpec = host[:j], host[j+1:]
	} else {
		l.Host = host
	}
	l.Host = strings.TrimSpace(unescape(l.Host))
	if l.PortSpec != "" {
		first := l.PortSpec
		if j := strings.IndexAny(first, ",-"); j >= 0 {
			first = first[:j]
		}
		if p, err := strconv.Atoi(strings.TrimSpace(first)); err == nil {
			l.Port = p
		}
	}
	return l, true
}

// user returns the percent-decoded user name and password of the userinfo.
func (l *shareLink) user() (name, password string, hasPassword bool) {
	name, password, hasPassword = strings.Cut(l.UserInfo, ":")
	return unescape(name), unescape(password), hasPassword
}

func (l *shareLink) param(keys ...string) string {
	for _, k := range keys {
		if v := l.Query.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func (l *shareLink) flag(keys ...string) bool {
	for _, k := range keys {
		if v := strings.ToLower(l.Query.Get(k)); v == "1" || v == "true" || v == "yes" {
			return true
		}
	}
	return false
}

// parseQuery is url.ParseQuery without its strictness: a ';' inside a value
// (plugin options) or a stray escape must not drop the parameter.
func parseQuery(raw string) url.Values {
	q := url.Values{}
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		if dk, err := url.QueryUnescape(k); err == nil {
			k = dk
		}
		if dv, err := url.QueryUnescape(v); err == nil {
			v = dv
		}
		q.Add(k, v)
	}
	return q
}

// unescape decodes %XX sequences only: share links use %20 for spaces, and
// '+' appears literally in base64 and passwords.
func unescape(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	if v, err := url.PathUnescape(s); err == nil {
		return v
	}
	return s
}

// decodeBase64 accepts standard and URL-safe alphabets, with or without padding,
// and tolerates embedded line breaks.
func decodeBase64(s string) ([]byte, bool) {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return nil, false
	}
	trimmed := strings.TrimRight(s, "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(trimmed); err == nil {
			return b, true
		}
	}
	return nil, false
}

func trimBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}
