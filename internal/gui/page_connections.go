package gui

import (
	"context"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/app"
	"eggbox/internal/routing"
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
			if !a.compact() {
				a.connHeader(c)
			}
			if len(rows) == 0 {
				ui.Column(c).Grow(1).Justify(ui.Center).Children(func() {
					a.emptyState(c, "search", a.t("conns.none.title"), a.t("conns.none.body"), nil)
				})
				return
			}
			a.conn.list.Key = func(i int) any { return rows[i].ID }
			jumpOnChange(&a.conn.list, &a.conn.sig, strconv.Itoa(a.conn.filter)+"|"+a.conn.query)
			ui.List(c.Key("conn-list"), &a.conn.list, len(rows), func(i int) {
				if a.compact() {
					a.phoneConnRow(c, rows[i])
					return
				}
				a.connRow(c, rows[i])
			}).Grow(1)
		})
	})
}

func (a *App) connToolbar(c *ui.Context) {
	if a.compact() {
		// The search across the screen, the filter under it.
		ui.Column(c.Key("conn-toolbar")).Gap(10).Children(func() {
			a.search(c, "conn-q", &a.conn.query, a.t("conns.search"), 0)
			w, _ := c.Size()
			sel := a.conn.filter
			if a.segmented(c, "conn-filter", &sel, []segItem{
				{a.t("conns.filter.open"), ""}, {a.t("conns.filter.closed"), ""}, {a.t("conns.filter.all"), ""},
			}, segOpts{small: true, fill: true, width: w - 2*phonePad - 8}) {
				a.conn.filter = sel
			}
		})
		return
	}
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
		if q != "" && !a.matchConn(cn, q) {
			continue
		}
		out = append(out, cn)
	}
	return out
}

// matchConn says whether a search finds a connection: in where it goes, the
// program, what decided its route, the node it leaves through, or that node's
// relay.
func (a *App) matchConn(cn app.Conn, q string) bool {
	fields := []string{cn.Host, cn.Destination, cn.Process, a.whyLabel(cn.Why), a.exitLabel(cn), cn.Protocol, cn.Network, cn.Source}
	if rl, tip, _ := a.connRelay(cn); rl != "" {
		fields = append(fields, rl, tip)
	}
	for _, s := range fields {
		if s != "" && strings.Contains(strings.ToLower(s), q) {
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
			a.futureMenu(m, cn)
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
				why := a.whyLabel(cn.Why)
				rl, tip, warn := a.connRelay(cn)
				if rl != "" || why != "" {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						if why != "" {
							a.txt(c, why, 11.5, 400, p.Text3).SingleLine().Tooltip(a.whyText(cn.Why)).Shrink(0)
						}
						if rl != "" {
							col := boolOr(warn, p.Warn, p.Text3)
							r := ui.Row(c).Gap(3).AlignItems(ui.Center).Shrink(1)
							if tip != "" {
								r.Tooltip(tip)
							}
							r.Children(func() {
								ui.Icon(c, icon(boolOr(warn, "alert", "relay"))).Size(11, 11).TextColor(col).Shrink(0)
								a.txt(c, rl, 11.5, boolOr(warn, 600, 450), col).SingleLine()
							})
						}
					})
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

// phoneConnRow is a connection on a phone: where it goes, how and from which
// application on the left, what it moved on the right.
func (a *App) phoneConnRow(c *ui.Context, cn app.Conn) {
	p := a.pl
	open := cn.Open()
	ui.Column(c).Children(func() {
		r := ui.Row(c).Padding(10, 8, 10, 14).Gap(10).AlignItems(ui.Center)
		r.PopoverMenu(func(m *ui.Menu) {
			if m.Item(a.t("conns.copy_host")).Chosen() {
				c.WriteClipboard(connName(cn))
			}
			if m.Item(a.t("conns.copy_dest")).Chosen() {
				c.WriteClipboard(cn.Destination)
			}
			a.futureMenu(m, cn)
			if open {
				m.Separator()
				if m.Item(a.t("conns.close")).Chosen() {
					a.closeConn(cn.ID)
				}
			}
		})
		r.Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
				a.txt(c, connName(cn), 13.5, 600, boolOr(open, p.Text, p.Text3)).SingleLine()
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					a.outboundChip(c, cn)
					if why := a.whyLabel(cn.Why); why != "" {
						a.txt(c, why, 11.5, 400, p.Text3).SingleLine().Shrink(1)
					}
				})
				if rl, tip, warn := a.connRelay(cn); rl != "" {
					col := boolOr(warn, p.Warn, p.Text3)
					r := ui.Row(c).Gap(3).AlignItems(ui.Center)
					if tip != "" {
						r.Tooltip(tip)
					}
					r.Children(func() {
						ui.Icon(c, icon(boolOr(warn, "alert", "relay"))).Size(11, 11).TextColor(col).Shrink(0)
						a.txt(c, rl, 11.5, boolOr(warn, 600, 450), col).SingleLine()
					})
				}
				end := a.now
				if !open {
					end = cn.Closed
				}
				line := fmtDuration(end.Sub(cn.Created))
				if cn.Process != "" {
					line = appName(cn.Process) + "  ·  " + line
				}
				a.txt(c, line, 11.5, 400, p.Text3).SingleLine()
			})
			ui.Column(c).Gap(4).AlignItems(ui.End).Shrink(0).Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon("arrow-down")).Size(11, 11).TextColor(p.BlueText)
					a.txt(c, fmtBytes(float64(cn.Down)), 12, 600, p.Text2).FontFeatures("tnum").SingleLine()
				})
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon("arrow-up")).Size(11, 11).TextColor(p.Cyan)
					a.txt(c, fmtBytes(float64(cn.Up)), 12, 600, p.Text2).FontFeatures("tnum").SingleLine()
				})
			})
			if open {
				if a.btn(c, btnSpec{kind: btnGhost, icon: "x", iconOnly: true, small: true, tip: a.t("conns.close")}).Clicked() {
					a.closeConn(cn.ID)
				}
			} else {
				ui.Box(c).Width(30).Height(1)
			}
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

// exitName is the outbound a connection leaves through: the last link of its
// chain, as the core names it.
func exitName(cn app.Conn) string {
	if n := len(cn.Chain); n > 0 {
		return cn.Chain[0]
	}
	return cn.Outbound
}

// exitLabel is where a connection leaves, as a person reads it: "Direct", a
// node (with its subscription when another one has a node of the same name).
func (a *App) exitLabel(cn app.Conn) string {
	out := exitName(cn)
	switch strings.ToLower(out) {
	case "direct":
		return a.t("dest.direct")
	case "block", "reject":
		return a.t("dest.reject")
	case "":
		return ""
	}
	if n, ok := a.nodeByTag(out); ok {
		return n.Label()
	}
	return out
}

// nodeByTag finds the node the core knows under a tag.
func (a *App) nodeByTag(tag string) (nodeEntry, bool) {
	nodes := a.liveNodes()
	if a.tagMap == nil || a.tagSnap != a.snap || a.tagVer != a.nodesVer {
		a.tagMap = make(map[string]int, len(nodes))
		for i, n := range nodes {
			if n.Tag != "" {
				a.tagMap[n.Tag] = i
			}
		}
		a.tagSnap, a.tagVer = a.snap, a.nodesVer
	}
	if i, ok := a.tagMap[tag]; ok && i < len(nodes) {
		return nodes[i], true
	}
	return nodeEntry{}, false
}

// connRelay describes the first-hop relay of a connection's exit node, or ""
// when the connection leaves directly or through a plain node.
func (a *App) connRelay(cn app.Conn) (label, tip string, warn bool) {
	out := exitName(cn)
	switch strings.ToLower(out) {
	case "", "direct", "block", "reject":
		return "", "", false
	}
	n, ok := a.nodeByTag(out)
	if !ok || !n.Relay.Active() {
		return "", "", false
	}
	if ri, ok := a.snap.Relays[n.Key]; ok {
		if ri.Unavailable {
			return a.t("relay.unavailable"), a.t("relay.unavailable.tip"), true
		}
		if ri.Node != nil {
			tip = a.t("relay.chain", "relay", ri.Node.Label(), "node", n.Label())
			if ri.StandIn && ri.Wanted != nil {
				want := ri.Wanted.Name
				if wn, m := a.nodeFor(*ri.Wanted); m != routing.MatchNone {
					want = wn.Label()
				}
				tip = a.t("relay.chain.stand_in", "relay", ri.Node.Label(), "node", n.Label(), "want", want)
			}
			return a.t("relay.via", "relay", truncate(ri.Node.Label(), 18)), tip, ri.StandIn
		}
	}
	if a.isRelayUnavailable(n) {
		return a.t("relay.unavailable"), a.t("relay.unavailable.tip"), true
	}
	short := a.relayShortLabel(n.Relay.Relay)
	return a.t("relay.via", "relay", truncate(short, 18)), a.t("relay.chain", "relay", short, "node", n.Label()), false
}

// outboundChip names where a connection leaves.
func (a *App) outboundChip(c *ui.Context, cn app.Conn) {
	out := exitName(cn)
	switch strings.ToLower(out) {
	case "direct":
		a.chip(c, a.t("dest.direct"), toneNeutral, "arrow-right")
	case "block", "reject":
		a.chip(c, a.t("dest.reject"), toneBad, "shield-off")
	case "":
		a.chip(c, "—", toneNeutral, "")
	default:
		a.chip(c, truncate(a.exitLabel(cn), 26), toneBlue, "shield-check")
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

// whyLabel says in a few words why a connection went where it did: the name
// of the item or exception, never the core's own rule text.
func (a *App) whyLabel(w app.Why) string {
	switch w.Kind {
	case app.WhyItem:
		if row, _, ok := a.rv().Row(w.ID); ok {
			return a.itemName(row.Item)
		}
		return w.Name
	case app.WhyException:
		return a.t("why.short.exception", "n", strconv.Itoa(w.Index))
	case app.WhyFallback:
		return a.t("why.short.fallback")
	case app.WhyLAN:
		return a.t("why.short.lan")
	case app.WhyMode:
		return a.t("why.short.mode")
	}
	return ""
}

// futureMenu is the part of a connection's menu that decides where such
// connections go from now on: one choice for each way the connection can be
// described (its name, its domain, its address, its program).
func (a *App) futureMenu(m *ui.Menu, cn app.Conn) {
	cands := app.ConnCandidates(cn)
	if len(cands) == 0 {
		return
	}
	m.Separator()
	for _, cand := range cands {
		m.Submenu(a.t("conns.future", "what", a.scopeLabel(cand)), func(sm *ui.Menu) {
			if sm.Item(a.t("dest.direct")).Chosen() {
				a.exceptionFromConn(cn, cand, routing.Direct())
			}
			if sm.Item(a.t("dest.follow.menu", "main", a.destLabel(a.rt().Main))).Chosen() {
				a.exceptionFromConn(cn, cand, routing.Follow())
			}
			if sm.Item(a.t("dest.reject")).Chosen() {
				a.exceptionFromConn(cn, cand, routing.Reject())
			}
			sm.Separator()
			if sm.Item(a.t("conns.future.else")).Chosen() {
				a.openExceptionFor(cand.Entry, routing.Follow())
			}
		})
	}
}
