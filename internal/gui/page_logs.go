package gui

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
)

// logsState is what the log page remembers.
type logsState struct {
	query     string
	level     int // 0 all, 1 info and above, 2 warnings and errors, 3 errors
	list      ui.ListState
	clearedAt time.Time
	paused    bool
	sig       string
}

func (a *App) initLogs() { a.logs.list.FollowEnd = true }

// levelRank orders the log levels.
func levelRank(l string) int {
	switch strings.ToLower(l) {
	case "trace":
		return 0
	case "debug":
		return 1
	case "info":
		return 2
	case "warn", "warning":
		return 3
	case "error":
		return 4
	}
	return 5 // fatal, panic
}

var levelFloor = []int{0, 2, 3, 4}

// logsPage is the core's log, live.
func (a *App) logsPage(c *ui.Context) {
	lines := a.visibleLogs()
	a.fixedFrame(c, a.t("nav.logs"), a.logsSubtitle(len(lines)), func() {
		a.logActions(c, lines)
	}, func() {
		a.logToolbar(c)
		a.card(c).Grow(1).Padding(0).Gap(0).Clip().Children(func() {
			if len(lines) == 0 {
				ui.Column(c).Grow(1).Justify(ui.Center).Children(func() {
					a.emptyState(c, "terminal", a.t("logs.empty.title"), a.t("logs.empty.body"), nil)
				})
				return
			}
			a.logs.list.FollowEnd = !a.logs.paused
			jumpOnChange(&a.logs.list, &a.logs.sig, strconv.Itoa(a.logs.level)+"|"+a.logs.query)
			ui.List(c.Key("log-list"), &a.logs.list, len(lines), func(i int) {
				a.logRow(c, lines[i])
			}).Grow(1).Padding(8, 0, 8, 0)
		})
	})
}

func (a *App) logsSubtitle(n int) string {
	if !a.snap.Phase.Online() && len(a.snap.Logs) == 0 {
		return a.t("logs.sub.offline")
	}
	return a.tn("logs.sub", n)
}

// visibleLogs applies the level, the search and what was cleared.
func (a *App) visibleLogs() []app.LogLine {
	q := strings.ToLower(strings.TrimSpace(a.logs.query))
	floor := levelFloor[a.logs.level]
	out := make([]app.LogLine, 0, len(a.snap.Logs))
	for _, l := range a.snap.Logs {
		if !a.logs.clearedAt.IsZero() && !l.Time.After(a.logs.clearedAt) {
			continue
		}
		if levelRank(l.Level) < floor {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(l.Message), q) && !strings.Contains(strings.ToLower(l.Source), q) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func (a *App) logToolbar(c *ui.Context) {
	ui.Row(c.Key("log-toolbar")).Gap(12).AlignItems(ui.Center).Children(func() {
		a.search(c, "log-q", &a.logs.query, a.t("logs.search"), 0)
		sel := a.logs.level
		if a.segmented(c, "log-level", &sel, []segItem{
			{a.t("logs.level.all"), ""}, {a.t("logs.level.info"), ""}, {a.t("logs.level.warn"), ""}, {a.t("logs.level.error"), ""},
		}, segOpts{small: true}) {
			a.logs.level = sel
		}
	})
}

func (a *App) logActions(c *ui.Context, lines []app.LogLine) {
	pauseIcon, pauseTip := "pause", a.t("logs.pause")
	if a.logs.paused {
		pauseIcon, pauseTip = "play", a.t("logs.follow")
	}
	if a.btn(c, btnSpec{kind: btnSecondary, icon: pauseIcon, iconOnly: true, tip: pauseTip}).Clicked() {
		a.logs.paused = !a.logs.paused
		if !a.logs.paused {
			a.logs.list.ScrollToEnd()
		}
	}
	if a.btn(c, btnSpec{kind: btnSecondary, icon: "copy", iconOnly: true, tip: a.t("logs.copy"), disabled: len(lines) == 0}).Clicked() {
		c.WriteClipboard(a.logText(lines))
		a.toastInfo(a.t("logs.copied"))
	}
	if a.opt.SaveFile != nil {
		if a.btn(c, btnSpec{kind: btnSecondary, icon: "download", iconOnly: true, tip: a.t("logs.export"), disabled: len(lines) == 0}).Clicked() {
			text := a.logText(lines)
			name := "egg-box-" + a.now.Format("20060102-150405") + ".log"
			if path, err := a.opt.SaveFile(name, []byte(text)); err != nil {
				a.fail(err)
			} else if path != "" {
				a.toastInfo(a.t("logs.exported"))
			}
		}
	}
	if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("common.clear"), icon: "trash", disabled: len(lines) == 0}).Clicked() {
		if n := len(a.snap.Logs); n > 0 {
			a.logs.clearedAt = a.snap.Logs[n-1].Time
		}
	}
}

// logText is the visible lines as plain text.
func (a *App) logText(lines []app.LogLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Time.Format("15:04:05.000"))
		b.WriteByte(' ')
		b.WriteString(strings.ToUpper(l.Level))
		b.WriteByte(' ')
		if l.Source != "" {
			b.WriteString(l.Source)
			b.WriteString(": ")
		}
		b.WriteString(l.Message)
		b.WriteByte('\n')
	}
	return b.String()
}

func (a *App) logRow(c *ui.Context, l app.LogLine) {
	p := a.pl
	col, bg := p.Text3, p.Field
	switch levelRank(l.Level) {
	case 2:
		col, bg = p.BlueText, p.Blue.Alpha(0.14)
	case 3:
		col, bg = p.Warn, p.Warn.Alpha(0.14)
	case 4, 5:
		col, bg = p.Bad, p.Bad.Alpha(0.14)
	}
	ui.Row(c).Padding(3, 20, 3, 20).Gap(12).AlignItems(ui.Start).Children(func() {
		a.txt(c, l.Time.Format("15:04:05"), 11.5, 400, p.Text3).Font("monospace").FontFeatures("tnum").Width(58).SingleLine()
		ui.Box(c).Width(44).Padding(1, 0).Radius(5).Background(bg).Center().Children(func() {
			a.txt(c, strings.ToUpper(shortLevel(l.Level)), 10, 700, col).LetterSpacing(0.4).SingleLine()
		})
		ui.Column(c).Grow(1).Basis(0).Children(func() {
			ui.Text(c, l.Message).FontSize(12).TextColor(p.Text).Font("monospace").LineHeight(1.5).Selectable()
			if l.Source != "" {
				a.txt(c, l.Source, 11, 400, p.Text3).Font("monospace").SingleLine()
			}
		})
	})
}

func shortLevel(l string) string {
	switch strings.ToLower(l) {
	case "warning":
		return "warn"
	case "trace", "debug", "info", "warn", "error":
		return strings.ToLower(l)
	}
	return "fatal"
}
