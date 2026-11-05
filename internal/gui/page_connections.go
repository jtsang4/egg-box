package gui

import (
	"context"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
)

// connsState is what the connections page remembers.
type connsState struct {
	query  string
	filter int // 0 open, 1 closed, 2 all
	list   ui.ListState
	sig    string
}

func (a *App) initConns() {}

// connectionsPage shows what is flowing through the proxy, live.
func (a *App) connectionsPage(c *ui.Context) {
	online := a.snap.Phase.Online()
	sub := a.t("conns.sub.offline")
	if online {
		sub = a.tn("conns.sub", a.snap.Traffic.Connections, "rate", fmtRate(a.snap.Traffic.Down+a.snap.Traffic.Up))
	}
	a.fixedFrame(c, a.t("nav.connections"), sub, func() {
		if online && a.snap.Traffic.Connections > 0 {
			if a.btn(c, btnSpec{kind: btnDanger, label: a.t("conns.close_all"), icon: "x-circle"}).Clicked() {
				a.confirm(a.t("conns.close_all.title"), a.t("conns.close_all.body"), a.t("conns.close_all"), true, func() {
					a.job("close-all", func(ctx context.Context) error { return a.be.CloseAllConnections(ctx) }, func(err error) { a.fail(err) })
				})
			}
		}
	}, func() {
		if !online {
			ui.Column(c).Grow(1).Justify(ui.Center).Children(func() {
				a.emptyState(c, "activity", a.t("conns.offline.title"), a.t("conns.offline.body"), func() {
					if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("hero.connect"), icon: "power", busy: a.snap.Phase.Transitional()}).Clicked() {
						a.toggleConnection()
					}
				})
			})
			return
		}
		a.connToolbar(c)
		rows := a.visibleConns()
		a.card(c).Grow(1).Padding(0).Gap(0).Clip().Children(func() {
			a.connHeader(c)
			if len(rows) == 0 {
				ui.Column(c).Grow(1).Justify(ui.Center).Children(func() {
					a.emptyState(c, "search", a.t("conns.none.title"), a.t("conns.none.body"), nil)
				})
				return
			}
			a.conn.list.Key = func(i int) any { return rows[i].ID }
			jumpOnChange(&a.conn.list, &a.conn.sig, strconv.Itoa(a.conn.filter)+"|"+a.conn.query)
			ui.List(c.Key("conn-list"), &a.conn.list, len(rows), func(i int) {
				a.connRow(c, rows[i])
			}).Grow(1)
		})
	})
}

func (a *App) connToolbar(c *ui.Context) {
	ui.Row(c.Key("conn-toolbar")).Gap(12).AlignItems(ui.Center).Children(func() {
		a.search(c, "conn-q", &a.conn.query, a.t("conns.search"), 0)
		sel := a.conn.filter
		if a.segmented(c, "conn-filter", &sel, []segItem{
			{a.t("conns.filter.open"), ""}, {a.t("conns.filter.closed"), ""}, {a.t("conns.filter.all"), ""},
		}, segOpts{small: true}) {
			a.conn.filter = sel
		}
	})
}

// visibleConns applies the filter and the search to the tracked connections.
func (a *App) visibleConns() []app.Conn {
	q := strings.ToLower(strings.TrimSpace(a.conn.query))
	out := make([]app.Conn, 0, len(a.snap.Conns))
	for _, cn := range a.snap.Conns {
		switch a.conn.filter {
		case 0:
			if !cn.Open() {
				continue
			}
		case 1:
			if cn.Open() {
				continue
			}
		}
		if q != "" && !matchConn(cn, q) {
			continue
		}
		out = append(out, cn)
	}
	return out
}

func matchConn(cn app.Conn, q string) bool {
	for _, s := range []string{cn.Host, cn.Destination, cn.Process, cn.Rule, cn.Outbound, cn.Protocol, cn.Network, cn.Source} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// The columns of the table: a fixed width, or a share of what is left.
const (
	colTraffic = 132
	colAge     = 68
	colAction  = 40
)

// cell makes the n-th column of a row or of the header.
func connCell(c *ui.Context, n int) ui.Element {
	e := ui.Column(c).Gap(3)
	switch n {
	case 0:
		return e.Grow(2.5).Basis(0)
	case 1:
		return e.Grow(1.9).Basis(0)
	case 2:
		return e.Grow(1.6).Basis(0)
	case 3:
		return e.Width(colTraffic)
	case 4:
		return e.Width(colAge)
	}
	return e.Width(colAction)
}

func (a *App) connHeader(c *ui.Context) {
	titles := []string{"conns.col.host", "conns.col.route", "conns.col.process", "conns.col.traffic", "conns.col.age", ""}
	ui.Row(c).Padding(0, 14, 0, 20).Height(40).Gap(14).AlignItems(ui.Center).Background(a.pl.Field.Alpha(0.6)).Children(func() {
		for i, k := range titles {
			cell := connCell(c, i).Justify(ui.Center)
			cell.Children(func() {
				if k != "" {
					a.eyebrow(c, a.t(k))
				}
			})
		}
	})
	a.hr(c)
}

func (a *App) connRow(c *ui.Context, cn app.Conn) {
	p := a.pl
	open := cn.Open()
	row := ui.Column(c)
	row.Children(func() {
		r := ui.Row(c).Height(58).Padding(0, 14, 0, 20).Gap(14).AlignItems(ui.Center)
		hov := r.AnimateTo("hov", boolf(r.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
		r.Background(p.PanelHi.Alpha(0.8 * hov))
		r.PopoverMenu(func(m *ui.Menu) {
			if m.Item(a.t("conns.copy_host")).Chosen() {
				c.WriteClipboard(connName(cn))
			}
			if m.Item(a.t("conns.copy_dest")).Chosen() {
				c.WriteClipboard(cn.Destination)
			}
			if open {
				m.Separator()
				if m.Item(a.t("conns.close")).Chosen() {
					a.closeConn(cn.ID)
				}
			}
		})
		r.Children(func() {
			connCell(c, 0).Justify(ui.Center).Children(func() {
				a.txt(c, connName(cn), 13.5, 600, boolOr(open, p.Text, p.Text3)).SingleLine()
				a.txt(c, connDetail(cn), 11.5, 400, p.Text3).SingleLine()
			})
			connCell(c, 1).Justify(ui.Center).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					a.outboundChip(c, cn)
				})
				if why := a.matchedRule(cn.Rule); why != "" {
					a.txt(c, why, 11.5, 400, p.Text3).SingleLine().Tooltip(cn.Rule)
				}
			})
			connCell(c, 2).Justify(ui.Center).Children(func() {
				if cn.Process != "" {
					a.txt(c, appName(cn.Process), 12.5, 500, p.Text2).SingleLine().Tooltip(cn.Process)
				} else {
					a.txt(c, "—", 12.5, 400, p.Text3)
				}
			})
			connCell(c, 3).Justify(ui.Center).Children(func() {
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon("arrow-down")).Size(11, 11).TextColor(p.BlueText)
					a.txt(c, fmtBytes(float64(cn.Down)), 12, 600, p.Text2).FontFeatures("tnum").SingleLine()
					if open && cn.DownRate > 0 {
						a.txt(c, fmtRate(cn.DownRate), 11, 500, p.BlueText).FontFeatures("tnum").SingleLine()
					}
				})
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon("arrow-up")).Size(11, 11).TextColor(p.Cyan)
					a.txt(c, fmtBytes(float64(cn.Up)), 12, 600, p.Text2).FontFeatures("tnum").SingleLine()
					if open && cn.UpRate > 0 {
						a.txt(c, fmtRate(cn.UpRate), 11, 500, p.Cyan).FontFeatures("tnum").SingleLine()
					}
				})
			})
			connCell(c, 4).Justify(ui.Center).Children(func() {
				end := a.now
				if !open {
					end = cn.Closed
				}
				a.txt(c, fmtDuration(end.Sub(cn.Created)), 12, 500, p.Text3).FontFeatures("tnum").SingleLine()
			})
			connCell(c, 5).Justify(ui.Center).AlignItems(ui.End).Children(func() {
				if open {
					if a.btn(c, btnSpec{kind: btnGhost, icon: "x", iconOnly: true, small: true, tip: a.t("conns.close")}).Clicked() {
						a.closeConn(cn.ID)
					}
				}
			})
		})
		a.hr(c)
	})
}

func (a *App) closeConn(id string) {
	a.job("close-"+id, func(ctx context.Context) error { return a.be.CloseConnection(ctx, id) }, func(err error) { a.fail(err) })
}

// connName is the best name for where a connection goes.
func connName(cn app.Conn) string {
	if cn.Host != "" {
		return cn.Host
	}
	return cn.Destination
}

// connDetail is the second line of a connection's first column.
func connDetail(cn app.Conn) string {
	parts := []string{strings.ToUpper(cn.Network)}
	if cn.Protocol != "" {
		parts = append(parts, strings.ToUpper(cn.Protocol))
	}
	if cn.Host != "" && cn.Destination != "" {
		parts = append(parts, cn.Destination)
	}
	return strings.Join(parts, "  ·  ")
}

// outboundChip names where a connection leaves: the last link of its chain.
func (a *App) outboundChip(c *ui.Context, cn app.Conn) {
	out := cn.Outbound
	if n := len(cn.Chain); n > 0 {
		out = cn.Chain[0]
	}
	switch strings.ToLower(out) {
	case "direct":
		a.chip(c, a.t("target.direct"), toneNeutral, "arrow-right")
	case "block", "reject":
		a.chip(c, a.t("target.reject"), toneBad, "shield-off")
	case "":
		a.chip(c, "—", toneNeutral, "")
	default:
		a.chip(c, truncate(out, 22), toneBlue, "shield-check")
	}
}

// appName is what a person calls the program behind a connection: the bundle
// of a macOS application, the executable of anything else, never a path.
func appName(process string) string {
	p := strings.ReplaceAll(process, "\\", "/")
	// The outermost bundle: a browser's helpers all count as the browser.
	if i := strings.Index(p, ".app/"); i > 0 {
		return path.Base(p[:i])
	}
	name := path.Base(strings.TrimSuffix(p, "/"))
	if strings.EqualFold(path.Ext(name), ".exe") {
		name = name[:len(name)-4]
	}
	if name == "." || name == "/" || name == "" {
		return process
	}
	return name
}

var ruleKind = regexp.MustCompile(`^[a-z_]+$`)

// matchedRule says in a few words why a connection went where it did, from
// the core's own description of the rule that decided it, which looks like
// "rule_set=[geosite-cn] => route(direct)" or "final".
func (a *App) matchedRule(rule string) string {
	left, _, _ := strings.Cut(rule, " => ")
	left = strings.TrimSpace(left)
	if left == "" {
		return ""
	}
	if left == "final" {
		return a.t("conns.rule.final")
	}
	kind, rest, ok := strings.Cut(left, "=")
	if !ok || !ruleKind.MatchString(kind) {
		return a.t("conns.rule.generic") // an inverted or composite rule
	}
	var value string
	if strings.HasPrefix(rest, "[") {
		inner, _, _ := strings.Cut(rest[1:], "]")
		value = strings.Join(strings.Fields(inner), ", ")
	} else {
		value, _, _ = strings.Cut(rest, " ")
	}
	label := a.t("conns.rule.generic")
	if k := "conns.rule." + kind; a.has(k) {
		label = a.t(k)
	}
	if kind == "rule_set" {
		value = a.ruleSetLabel(value)
	}
	if value == "" {
		return label
	}
	return label + "  ·  " + value
}

// ruleSetLabel names the rule sets this application ships; others keep the
// name their owner gave them.
func (a *App) ruleSetLabel(tags string) string {
	parts := strings.Split(tags, ", ")
	for i, t := range parts {
		if k := "ruleset." + t; a.has(k) {
			parts[i] = a.t(k)
		}
	}
	return strings.Join(parts, ", ")
}
