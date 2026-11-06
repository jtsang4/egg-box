package gui

import (
	"fmt"
	"strings"
	"time"

	"eggbox/internal/app"
)

// Languages are the interface languages, in the order the settings list them.
var Languages = []struct{ Code, Name string }{
	{"en", "English"},
	{"zh", "简体中文"},
}

// translate returns the text of a key in a language. Placeholders are written
// {name} and filled from the pairs that follow the key.
func translate(lang, key string, kv ...string) string {
	cat := catalogEN
	if lang == "zh" {
		cat = catalogZH
	}
	s, ok := cat[key]
	if !ok {
		if s, ok = catalogEN[key]; !ok {
			return key
		}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		s = strings.ReplaceAll(s, "{"+kv[i]+"}", kv[i+1])
	}
	return s
}

// t returns a translated string: t("x", "name", "Ada").
func (a *App) t(key string, kv ...string) string { return translate(a.lang, key, kv...) }

// pk is t for text that depends on the platform: it looks for key.darwin,
// key.windows, and then key.other.
func (a *App) pk(key string, kv ...string) string {
	if a.has(key + "." + a.platform) {
		return a.t(key+"."+a.platform, kv...)
	}
	return a.t(key+".other", kv...)
}

// has reports whether a translation exists.
func (a *App) has(key string) bool {
	_, ok := catalogEN[key]
	return ok
}

// tn picks the singular or plural form of a count for English; Chinese has
// one form. The keys are key+".one" and key+".other".
func (a *App) tn(key string, n int, kv ...string) string {
	form := ".other"
	if a.lang != "zh" && n == 1 {
		form = ".one"
	}
	return a.t(key+form, append([]string{"n", fmtCount(int64(n))}, kv...)...)
}

// ago words a past time relative to now.
func (a *App) ago(t time.Time) string {
	if t.IsZero() {
		return a.t("time.never")
	}
	d := a.now.Sub(t)
	switch {
	case d < 45*time.Second:
		return a.t("time.just_now")
	case d < 90*time.Minute:
		return a.tn("time.minutes_ago", int(d.Minutes()+0.5))
	case d < 36*time.Hour:
		return a.tn("time.hours_ago", int(d.Hours()+0.5))
	}
	return a.tn("time.days_ago", int(d.Hours()/24+0.5))
}

// until words a future time relative to now.
func (a *App) until(t time.Time) string {
	d := t.Sub(a.now)
	switch {
	case d <= 0:
		return a.t("time.expired")
	case d < 90*time.Minute:
		return a.tn("time.in_minutes", max(1, int(d.Minutes()+0.5)))
	case d < 36*time.Hour:
		return a.tn("time.in_hours", int(d.Hours()+0.5))
	}
	return a.tn("time.in_days", int(d.Hours()/24+0.5))
}

// clock formats a time of day.
func (a *App) clock(t time.Time) string { return t.Format("15:04:05") }

// date formats a calendar date.
func (a *App) date(t time.Time) string {
	if a.lang == "zh" {
		return fmt.Sprintf("%d年%d月%d日", t.Year(), int(t.Month()), t.Day())
	}
	return t.Format("Jan 2, 2006")
}

// explain turns an error into a sentence in the user's language.
func (a *App) explain(err error) string {
	ex := app.Explain(err)
	return a.explainCode(ex.Code, ex.Detail)
}

func (a *App) explainCode(code, detail string) string {
	if k := "err." + code; a.has(k) {
		return a.t(k)
	}
	if detail != "" {
		return detail
	}
	return a.t("err.unknown")
}
