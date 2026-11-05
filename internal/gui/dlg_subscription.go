package gui

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/library"
	"github.com/jtsang4/sing-box-ui/internal/subscription"
)

// subIntervals are the refresh intervals the dialog offers, in hours; 0 is
// manual updates only.
var subIntervals = []int{6, 12, 24, 48, 0}

// subDialog adds a subscription, or edits one.
type subDialog struct {
	edit     *library.Subscription // nil when adding
	name     string
	link     string
	ua       string
	include  string
	exclude  string
	interval int
	advanced bool
	busy     bool
	err      string
	origLink string
}

func (a *App) openAddSubscription() {
	d := &subDialog{interval: indexOfInterval(library.DefaultIntervalHours)}
	// A copied link is the most likely thing to add.
	a.openDialog(d)
}

func (a *App) openEditSubscription(s library.Subscription) {
	d := &subDialog{edit: &s, name: s.Name, ua: s.UserAgent, include: s.Include, exclude: s.Exclude,
		interval: indexOfInterval(s.IntervalHours), advanced: s.UserAgent != "" || s.Include != "" || s.Exclude != ""}
	if s.Kind == library.KindRemote {
		if link, err := a.be.Link(s.ID); err == nil {
			d.link, d.origLink = link, link
		}
	}
	a.openDialog(d)
}

func indexOfInterval(h int) int {
	for i, v := range subIntervals {
		if v == h {
			return i
		}
	}
	return 2
}

func (d *subDialog) remote() bool { return d.edit == nil || d.edit.Kind == library.KindRemote }

func (d *subDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	adding := d.edit == nil
	title, sub, action := a.t("subdlg.add.title"), a.t("subdlg.add.sub"), a.t("subs.add")
	if !adding {
		title, sub, action = a.t("subdlg.edit.title"), d.edit.Name, a.t("common.save")
	}
	return dlgFrame{
		title: title, subtitle: sub, icon: "rss", tone: toneBlue, width: 540,
		body: func() {
			if d.remote() {
				a.field(c, a.t("subdlg.link"), "", func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						in := a.input(c, "sd-link", &d.link, "https://")
						in.Grow(1)
						if adding && d.link == "" {
							in.AutoFocus()
						}
						if in.Submitted() {
							d.submit(a)
						}
						if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("subdlg.paste"), icon: "copy"}).Clicked() {
							d.link = strings.TrimSpace(c.ReadClipboard())
							d.err = ""
						}
					})
				})
			}
			a.field(c, a.t("subdlg.name"), "", func() {
				in := a.input(c, "sd-name", &d.name, a.t("subdlg.name.placeholder"))
				if !d.remote() {
					in.AutoFocus()
				}
				if in.Submitted() {
					d.submit(a)
				}
			})
			if d.remote() {
				a.field(c, a.t("subdlg.interval"), a.t("subdlg.interval.hint"), func() {
					items := make([]segItem, len(subIntervals))
					for i, h := range subIntervals {
						if h == 0 {
							items[i] = segItem{label: a.t("subs.manual_short")}
						} else {
							items[i] = segItem{label: a.t("subdlg.hours", "h", strconv.Itoa(h))}
						}
					}
					a.segmented(c, "sd-interval", &d.interval, items, segOpts{small: true})
				})
			}
			// Advanced options fold away. The button sits at the start of its
			// own row: alone in the column it would be stretched across it.
			ui.Row(c).Children(func() {
				adv := a.btn(c, btnSpec{kind: btnGhost, small: true, label: a.t("subdlg.advanced"), icon: boolOr(d.advanced, "chevron-down", "chevron-right")})
				if adv.Clicked() {
					d.advanced = !d.advanced
				}
			})
			if d.advanced {
				ui.Column(c).Gap(14).Padding(0, 0, 0, 2).Children(func() {
					if d.remote() {
						a.field(c, a.t("subdlg.ua"), a.t("subdlg.ua.hint"), func() { a.input(c, "sd-ua", &d.ua, "clash.meta") })
					}
					a.field(c, a.t("subdlg.include"), a.t("subdlg.filter.hint"), func() { a.input(c, "sd-inc", &d.include, "HK|SG|Japan") })
					a.field(c, a.t("subdlg.exclude"), "", func() { a.input(c, "sd-exc", &d.exclude, a.t("subdlg.exclude.placeholder")) })
				})
			}
			if d.err != "" {
				a.errorNote(c, d.err)
			}
			if d.busy {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					a.spinner(c, 16, p.BlueText)
					a.txt(c, a.t("subdlg.fetching"), 13, 500, p.Text2)
				})
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			ready := !d.busy && (!d.remote() || strings.TrimSpace(d.link) != "")
			if a.btn(c, btnSpec{kind: btnPrimary, label: action, busy: d.busy, disabled: !ready}).Clicked() {
				d.submit(a)
			}
		},
	}
}

// errorNote shows a problem inside a dialog.
func (a *App) errorNote(c *ui.Context, msg string) {
	p := a.pl
	ui.Row(c).Padding(10, 12).Gap(10).Radius(12).Background(p.Bad.Alpha(0.1)).Border(1, p.Bad.Alpha(0.24)).Children(func() {
		ui.Icon(c, icon("alert-circle")).Size(16, 16).TextColor(p.Bad)
		a.txt(c, msg, 12.5, 450, p.Text).Grow(1).Basis(0).LineHeight(1.45)
	})
}

func (d *subDialog) submit(a *App) {
	if d.busy {
		return
	}
	link := strings.TrimSpace(d.link)
	if d.remote() {
		if err := subscription.ValidateURL(link); err != nil {
			d.err = a.explain(err)
			return
		}
	}
	for _, f := range []string{d.include, d.exclude} {
		if err := library.ValidateFilter(f); err != nil {
			d.err = a.t("subdlg.filter.invalid")
			return
		}
	}
	d.busy, d.err = true, ""
	hours := subIntervals[d.interval]
	name, ua, inc, exc := strings.TrimSpace(d.name), strings.TrimSpace(d.ua), d.include, d.exclude
	var run func(ctx context.Context) error
	if d.edit == nil {
		run = func(ctx context.Context) error {
			sub, _, err := a.be.AddSubscription(ctx, name, link, library.RemoteOptions{UserAgent: ua, Include: inc, Exclude: exc})
			if err != nil {
				return err
			}
			if hours != library.DefaultIntervalHours {
				return a.be.EditSubscription(ctx, sub.ID, func(s *library.Subscription) { s.IntervalHours = hours })
			}
			return nil
		}
	} else {
		id, remote := d.edit.ID, d.edit.Kind == library.KindRemote
		changed := remote && link != d.origLink
		run = func(ctx context.Context) error {
			if changed {
				if err := a.be.SetLink(id, link); err != nil {
					return err
				}
			}
			err := a.be.EditSubscription(ctx, id, func(s *library.Subscription) {
				s.Name, s.Include, s.Exclude = name, inc, exc
				if remote {
					s.UserAgent, s.IntervalHours = ua, hours
				}
			})
			if err == nil && changed {
				_, err = a.be.RefreshSubscription(ctx, id)
			}
			return err
		}
	}
	a.job("sub-dialog", run, func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		if cur, ok := a.dlg.(*subDialog); ok && cur == d {
			a.dlgOpen = false
		}
	})
}

// ---------------------------------------------------------------- import

// importDialog adds nodes from pasted text: share links, a Clash profile or a
// sing-box configuration.
type importDialog struct {
	name string
	text string
	busy bool
	err  string

	parsedFor string
	parsed    *subscription.Result
	parseErr  string
}

func (a *App) openImport() { a.openDialog(&importDialog{}) }

// preview parses what was pasted, once per change.
func (d *importDialog) preview(a *App) {
	if d.text == d.parsedFor {
		return
	}
	d.parsedFor = d.text
	d.parsed, d.parseErr = nil, ""
	if strings.TrimSpace(d.text) == "" {
		return
	}
	res, err := subscription.Parse([]byte(d.text))
	if err != nil {
		d.parseErr = a.explain(err)
		return
	}
	d.parsed = res
}

func (d *importDialog) frame(a *App, c *ui.Context) dlgFrame {
	d.preview(a)
	return dlgFrame{
		title: a.t("import.title"), subtitle: a.t("import.sub"), icon: "download", tone: toneBlue, width: 580,
		body: func() {
			a.field(c, a.t("import.content"), "", func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					a.caption(c, a.t("import.formats")).Grow(1)
					if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("subdlg.paste"), icon: "copy"}).Clicked() {
						d.text = c.ReadClipboard()
						d.err = ""
					}
				})
				a.area(c, "imp-text", &d.text, "vless://…\ntrojan://…\nss://…", 5, 9).AutoFocus()
			})
			switch {
			case d.parsed != nil:
				a.importSummary(c, d.parsed)
			case d.parseErr != "":
				a.errorNote(c, d.parseErr)
			}
			a.field(c, a.t("subdlg.name"), "", func() {
				a.input(c, "imp-name", &d.name, a.t("import.name.placeholder"))
			})
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			ready := d.parsed != nil && !d.busy
			label := a.t("import.action")
			if d.parsed != nil {
				label = a.tn("import.action.n", len(d.parsed.Nodes))
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: label, busy: d.busy, disabled: !ready}).Clicked() {
				d.submit(a)
			}
		},
	}
}

// importSummary tells what a paste contains.
func (a *App) importSummary(c *ui.Context, res *subscription.Result) {
	p := a.pl
	counts := map[string]int{}
	for _, n := range res.Nodes {
		counts[n.Protocol()]++
	}
	names := make([]string, 0, len(counts))
	for k := range counts {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	ui.Column(c).Gap(10).Padding(12, 14).Radius(12).Background(p.Good.Alpha(0.09)).Border(1, p.Good.Alpha(0.22)).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon("check-circle")).Size(16, 16).TextColor(p.Good)
			a.txt(c, a.tn("import.found", len(res.Nodes)), 13, 650, p.Text)
			if n := len(res.Skipped); n > 0 {
				a.caption(c, a.tn("import.skipped", n))
			}
		})
		ui.Row(c).Gap(6).Wrap().Children(func() {
			for i, k := range names {
				if i >= 8 {
					break
				}
				a.chip(c, k+" · "+strconv.Itoa(counts[k]), toneNeutral, "")
			}
		})
	})
}

func (d *importDialog) submit(a *App) {
	if d.busy || d.parsed == nil {
		return
	}
	d.busy, d.err = true, ""
	name, text := strings.TrimSpace(d.name), d.text
	a.job("import", func(ctx context.Context) error {
		_, err := a.be.ImportNodes(ctx, name, text)
		return err
	}, func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		if cur, ok := a.dlg.(*importDialog); ok && cur == d {
			a.dlgOpen = false
		}
	})
}
