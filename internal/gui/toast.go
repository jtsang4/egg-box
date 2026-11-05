package gui

import (
	"sort"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/app"
)

// toast is a transient message at the corner of the window.
type toast struct {
	id     uint64
	tone   tone
	icon   string
	title  string
	body   string
	born   time.Time
	ttl    time.Duration
	action string
	do     func()
}

const maxToasts = 4

func (a *App) pushToast(t *toast) {
	a.toastSeq++
	t.id = a.toastSeq
	t.born = a.now
	if t.ttl == 0 {
		t.ttl = 4500 * time.Millisecond
	}
	a.toasts = append(a.toasts, t)
	if len(a.toasts) > maxToasts {
		a.toasts = a.toasts[len(a.toasts)-maxToasts:]
	}
	time.AfterFunc(t.ttl+80*time.Millisecond, a.invalidate)
}

// toastInfo tells something neutral.
func (a *App) toastInfo(title string) {
	a.pushToast(&toast{tone: toneBlue, icon: "info", title: title, ttl: 3200 * time.Millisecond})
}

// toastError reports a failure.
func (a *App) toastError(msg string) {
	a.pushToast(&toast{tone: toneBad, icon: "alert-circle", title: a.t("toast.failed"), body: msg, ttl: 7 * time.Second})
}

func (a *App) pruneToasts() {
	kept := a.toasts[:0]
	for _, t := range a.toasts {
		if a.now.Sub(t.born) < t.ttl {
			kept = append(kept, t)
		}
	}
	a.toasts = kept
}

// absorbNotices turns what the service reports into toasts. What happened
// before the window opened is not replayed.
func (a *App) absorbNotices() {
	ns := a.snap.Notices
	if !a.started {
		a.started = true
		if len(ns) > 0 {
			a.seenNotice = ns[len(ns)-1].ID
		}
		return
	}
	for _, n := range ns {
		if n.ID <= a.seenNotice {
			continue
		}
		a.seenNotice = n.ID
		a.pushToast(a.noticeToast(n))
	}
}

// noticeTitle words a notice.
func (a *App) noticeTitle(n app.Notice) string {
	kv := make([]string, 0, 2*len(n.Args))
	keys := make([]string, 0, len(n.Args))
	for k := range n.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kv = append(kv, k, n.Args[k])
	}
	if key := "notice." + n.Code; a.has(key) {
		return a.t(key, kv...)
	}
	if n.Detail != "" {
		return n.Detail
	}
	return n.Code
}

// noticeBody explains why, for notices that report a failure.
func (a *App) noticeBody(n app.Notice) string {
	if n.Level != app.LevelError && n.Level != app.LevelWarning {
		return ""
	}
	if r := n.Args["reason"]; r != "" {
		return a.explainCode(r, n.Detail)
	}
	return ""
}

func (a *App) noticeToast(n app.Notice) *toast {
	t := &toast{title: a.noticeTitle(n), body: a.noticeBody(n)}
	switch n.Level {
	case app.LevelSuccess:
		t.tone, t.icon = toneGood, "check-circle"
	case app.LevelWarning:
		t.tone, t.icon, t.ttl = toneWarn, "alert", 6*time.Second
	case app.LevelError:
		t.tone, t.icon, t.ttl = toneBad, "alert-circle", 7*time.Second
	default:
		t.tone, t.icon, t.ttl = toneBlue, "info", 3200*time.Millisecond
	}
	return t
}

// toastLayer draws the stack of toasts in the bottom right corner.
func (a *App) toastLayer(c *ui.Context) {
	p := a.pl
	ui.Column(c).Absolute().Right(22).Bottom(22).Gap(10).AlignItems(ui.End).Width(360).Children(func() {
		for _, t := range a.toasts {
			t := t
			col := a.toneColor(t.tone)
			e := ui.Row(c.Key(t.id)).Gap(12).Padding(13, 14, 13, 14).Radius(16).AlignItems(ui.Start).Width(360)
			e.Background(p.Raised).Border(1, p.LineHi)
			e.Shadow(0, 18, 40, -10, ui.RGBA(0, 0, 0, 0.5)).Shadow(0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.2))
			e.Transition(ui.ElementTransition{
				Enter:     &ui.Motion{Y: 18, Scale: 0.96},
				Exit:      &ui.Motion{X: 26, Scale: 0.98},
				Animation: &ui.Snappy,
			})
			e.Children(func() {
				a.iconTile(c, t.icon, t.tone, 32)
				ui.Column(c).Grow(1).Gap(3).Padding(2, 0).Children(func() {
					a.txt(c, t.title, 13.5, 650, p.Text).LineHeight(1.25)
					if t.body != "" {
						a.txt(c, t.body, 12.5, 400, p.Text2).LineHeight(1.35)
					}
					if t.action != "" {
						if a.btn(c, btnSpec{kind: btnSoft, label: t.action, small: true}).Clicked() && t.do != nil {
							t.do()
							t.born = time.Time{}
						}
					}
				})
				_ = col
				if a.btn(c, btnSpec{kind: btnGhost, icon: "x", small: true, iconOnly: true, tip: a.t("common.dismiss")}).Clicked() {
					t.born = time.Time{}
				}
			})
		}
	})
}
