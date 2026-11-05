package subscription

import (
	"encoding/base64"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Usage is a provider's quota report (the Subscription-Userinfo header).
type Usage struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	// Total is the quota in bytes; 0 means unknown or unlimited.
	Total int64 `json:"total"`
	// Expire is when the plan ends; the zero time means unknown or never.
	Expire time.Time `json:"expire,omitempty"`
}

// Used is the traffic consumed so far.
func (u Usage) Used() int64 { return u.Upload + u.Download }

// Remaining is the traffic left, never negative; ok is false without a quota.
func (u Usage) Remaining() (n int64, ok bool) {
	if u.Total <= 0 {
		return 0, false
	}
	if r := u.Total - u.Used(); r > 0 {
		return r, true
	}
	return 0, true
}

// ParseUserinfo reads "upload=1; download=2; total=3; expire=1700000000".
func ParseUserinfo(h string) (Usage, bool) {
	var u Usage
	found := false
	for _, part := range strings.FieldsFunc(h, func(r rune) bool { return r == ';' || r == ',' }) {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || n < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "upload":
			u.Upload, found = int64(n), true
		case "download":
			u.Download, found = int64(n), true
		case "total":
			u.Total, found = int64(n), true
		case "expire":
			if n > 0 {
				u.Expire, found = time.Unix(int64(n), 0).UTC(), true
			}
		}
	}
	return u, found
}

// headerInfo extracts the metadata providers send alongside the file.
type headerInfo struct {
	Usage    *Usage
	Interval time.Duration
	Title    string
	HomePage string
}

func readHeaders(h http.Header) headerInfo {
	var info headerInfo
	if u, ok := ParseUserinfo(h.Get("Subscription-Userinfo")); ok {
		info.Usage = &u
	}
	if v := strings.TrimSpace(h.Get("Profile-Update-Interval")); v != "" {
		if hours, err := strconv.ParseFloat(v, 64); err == nil && hours > 0 && hours < 24*365 {
			info.Interval = time.Duration(hours * float64(time.Hour))
		}
	}
	info.HomePage = strings.TrimSpace(h.Get("Profile-Web-Page-Url"))
	if t := strings.TrimSpace(h.Get("Profile-Title")); t != "" {
		if enc, ok := strings.CutPrefix(t, "base64:"); ok {
			if b, err := base64.StdEncoding.DecodeString(enc); err == nil && utf8.Valid(b) {
				t = string(b)
			}
		}
		info.Title = cleanName(t)
	}
	if info.Title == "" {
		if _, params, err := mime.ParseMediaType(h.Get("Content-Disposition")); err == nil {
			if f := params["filename"]; f != "" {
				f = strings.TrimSuffix(f, path.Ext(f))
				info.Title = cleanName(f)
			}
		}
	}
	return info
}
