package gui

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/configgen"
)

// ruleKinds are the kinds of custom rule the dialog offers.
var ruleKinds = []configgen.RuleKind{
	configgen.KindDomainSuffix, configgen.KindDomain, configgen.KindDomainKeyword, configgen.KindDomainRegex,
	configgen.KindIPCIDR, configgen.KindProcessName, configgen.KindPort,
}

// ruleTargets are what a rule can do with what it matches.
var ruleTargets = []string{configgen.TargetProxy, configgen.TargetDirect, configgen.TargetReject}

// ruleDialog adds or edits a custom rule.
type ruleDialog struct {
	index int // -1 when adding
	kind  int
	value string
	tgt   int
	err   string
}

func (a *App) openRule(index int, r configgen.CustomRule) {
	d := &ruleDialog{index: index, value: r.Value}
	for i, k := range ruleKinds {
		if k == r.Kind {
			d.kind = i
		}
	}
	for i, t := range ruleTargets {
		if t == r.Target {
			d.tgt = i
		}
	}
	a.openDialog(d)
}

func (d *ruleDialog) frame(a *App, c *ui.Context) dlgFrame {
	adding := d.index < 0
	title, action := a.t("ruledlg.add.title"), a.t("rules.add")
	if !adding {
		title, action = a.t("ruledlg.edit.title"), a.t("common.save")
	}
	kind := ruleKinds[d.kind]
	return dlgFrame{
		title: title, subtitle: a.t("ruledlg.sub"), icon: "route", tone: toneBlue, width: 500,
		body: func() {
			a.field(c, a.t("ruledlg.kind"), "", func() {
				names := make([]string, len(ruleKinds))
				for i, k := range ruleKinds {
					names[i] = a.t("rulekind." + string(k))
				}
				a.dropdown(c, "rd-kind", names, d.kind, func(i int) { d.kind, d.err = i, "" })
			})
			a.field(c, a.t("ruledlg.value"), a.t("ruledlg.hint."+string(kind)), func() {
				in := a.input(c, "rd-value", &d.value, a.t("ruledlg.example."+string(kind)))
				in.Font("monospace")
				if adding {
					in.AutoFocus()
				}
				if in.Changed() {
					d.err = ""
				}
				if in.Submitted() {
					d.submit(a)
				}
			})
			a.field(c, a.t("ruledlg.target"), a.t("ruledlg.target.hint"), func() {
				items := []segItem{
					{a.t("target.proxy"), "shield-check"}, {a.t("target.direct"), "arrow-right"}, {a.t("target.reject"), "shield-off"},
				}
				a.segmented(c, "rd-target", &d.tgt, items, segOpts{small: true})
			})
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: action, disabled: strings.TrimSpace(d.value) == ""}).Clicked() {
				d.submit(a)
			}
		},
	}
}

func (d *ruleDialog) submit(a *App) {
	r := configgen.CustomRule{Kind: ruleKinds[d.kind], Value: strings.TrimSpace(d.value), Target: ruleTargets[d.tgt]}
	if err := r.Valid(); err != nil {
		d.err = err.Error()
		return
	}
	rules := append([]configgen.CustomRule(nil), a.be.Rules()...)
	if d.index >= 0 && d.index < len(rules) {
		rules[d.index] = r
	} else {
		rules = append(rules, r)
	}
	a.dlgOpen = false
	a.job("rules", func(ctx context.Context) error { return a.be.SaveRules(ctx, rules) }, func(err error) { a.fail(err) })
}

// dropdown is a select drawn in the application's style: a field that opens
// a menu of choices.
func (a *App) dropdown(c *ui.Context, key string, items []string, cur int, pick func(int)) {
	p := a.pl
	if cur < 0 || cur >= len(items) {
		cur = 0
	}
	b := ui.ButtonBase(c.Key(key))
	b.Label(items[cur])
	b.Height(38).Padding(0, 12).Gap(8).Radius(radiusControl).Border(1, p.Line).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	hov := b.AnimateTo("hov", boolf(b.Hovered()), ui.Animation{Duration: 130 * time.Millisecond})
	b.Background(p.Field.Mix(p.PanelHi, hov))
	b.BorderColor(p.Line.Mix(p.LineHi, hov))
	b.Children(func() {
		a.txt(c, items[cur], 13.5, 500, p.Text).SingleLine().Grow(1)
		ui.Icon(c, icon("chevron-down")).Size(15, 15).TextColor(p.Text3)
	})
	b.PopoverMenuButton(func(m *ui.Menu) {
		for i, it := range items {
			if m.Item(it).Radio(i == cur).Chosen() {
				pick(i)
			}
		}
	})
}
