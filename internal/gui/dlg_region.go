package gui

import (
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// regionDialog sets the region of one node by hand, for the node whose name
// the application read wrongly.
type regionDialog struct {
	node  nodeEntry
	query string
	list  ui.ListState
	sig   string
}

// openRegionPicker opens the list of regions for a node.
func (a *App) openRegionPicker(n nodeEntry) { a.openDialog(&regionDialog{node: n}) }

// regionChoices lists the regions that can be set, by name.
func (a *App) regionChoices(query string, keep string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	codes := make([]string, 0, len(regionNames)+1)
	seen := false
	for code := range regionNames {
		codes = append(codes, code)
		seen = seen || code == keep
	}
	if keep != "" && !seen {
		codes = append(codes, keep)
	}
	names := map[string]string{}
	for _, code := range codes {
		names[code] = a.regionName(code)
	}
	sort.Slice(codes, func(i, j int) bool { return names[codes[i]] < names[codes[j]] })
	if q == "" {
		return codes
	}
	out := codes[:0:0]
	for _, code := range codes {
		if strings.Contains(strings.ToLower(names[code]), q) || strings.Contains(strings.ToLower(code), q) {
			out = append(out, code)
		}
	}
	return out
}

func (d *regionDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	n := d.node
	codes := a.regionChoices(d.query, n.Region)
	// "Automatic" is the first row, then the regions.
	rows := len(codes) + 1
	return dlgFrame{
		title: a.t("regiondlg.title"), subtitle: n.Label(), icon: "globe", tone: toneBlue, width: 460,
		body: func() {
			a.txt(c, a.t("regiondlg.body"), 12.5, 400, p.Text2).LineHeight(1.5)
			a.searchFirst(c, "rg-q", &d.query, a.t("regiondlg.search"), 0)
			jumpOnChange(&d.list, &d.sig, d.query)
			ui.List(c.Key("rg-list"), &d.list, rows, func(i int) {
				if i == 0 {
					d.row(a, c, "", a.t("regiondlg.auto"), a.t("regiondlg.auto.desc"), !n.Corrected)
					return
				}
				code := codes[i-1]
				d.row(a, c, code, a.regionName(code), code, n.Corrected && n.Region == code)
			}).Height(320).Gap(4).Padding(0, 0, 4, 0)
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
		},
	}
}

func (d *regionDialog) row(a *App, c *ui.Context, code, title, sub string, current bool) {
	p := a.pl
	b := ui.ButtonBase(c.Key("rg-" + code))
	b.Label(title)
	b.Height(46).Padding(0, 12).Gap(12).Radius(12).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 120 * time.Millisecond})
	b.Background(p.Field.Mix(p.PanelHi, hov).Mix(p.Blue.Alpha(0.14), boolf(current)))
	b.Border(1, p.Line.Mix(p.LineHi, hov).Mix(p.Blue.Alpha(0.6), boolf(current)))
	b.Children(func() {
		if code == "" {
			a.iconTile(c, "zap", toneBlue, 30)
		} else {
			a.regionBadge(c, code, 30)
		}
		ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
			a.txt(c, title, 13.5, 600, p.Text).SingleLine()
			a.txt(c, sub, 11.5, 400, p.Text3).SingleLine()
		})
		if current {
			ui.Icon(c, icon("check")).Size(16, 16).TextColor(p.BlueText)
		}
	})
	if b.Clicked() {
		id := d.node.ID
		a.dlgOpen = false
		a.editRouting(func(s *routing.State) error {
			s.SetNodeRegion(id, code)
			return nil
		}, nil)
	}
}
