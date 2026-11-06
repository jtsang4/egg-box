package gui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// What a plan dialog does.
const (
	// planSaveAs keeps the plan in force, a followed one, as the person's own.
	planSaveAs = iota
	// planRename renames the plan of the person's own in force.
	planRename
	// planNew makes a plan of the person's own, empty or a copy of any plan.
	planNew
)

// planDialog names a plan of the person's own, and for a new one says what it
// starts from.
type planDialog struct {
	mode int
	name string
	// empty starts a new plan with no items; otherwise it copies from.
	empty bool
	from  string
	err   string
	busy  bool
}

// openPlanName opens the dialog that saves the plan in force as one of the
// person's own (rename=false) or renames the plan of their own in force.
func (a *App) openPlanName(rename bool) {
	d := &planDialog{mode: planSaveAs, name: a.t("plan.default_name")}
	if rename {
		d.mode, d.name = planRename, a.rv().Plan.Name
	}
	a.openDialog(d)
}

// openNewPlan opens the dialog that makes a plan of the person's own: an empty
// one, or (with from set) a copy of that plan.
func (a *App) openNewPlan(from string) {
	d := &planDialog{mode: planNew, name: a.t("plan.default_name"), empty: from == "", from: from}
	if from == "" {
		d.from = a.rt().ActiveID()
	}
	a.openDialog(d)
}

func (d *planDialog) submit(a *App) {
	name := strings.TrimSpace(d.name)
	if name == "" || d.busy {
		return
	}
	d.busy, d.err = true, ""
	done := func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		a.dlgOpen = false
	}
	switch d.mode {
	case planRename:
		id := a.rv().Plan.ID
		a.editRouting(func(s *routing.State) error { return s.RenamePlan(id, name) }, done)
	case planNew:
		from := d.from
		if d.empty {
			from = ""
		}
		a.editRouting(func(s *routing.State) error {
			_, err := s.NewPlan(name, from)
			return err
		}, done)
	default:
		a.editRouting(func(s *routing.State) error {
			_, err := s.SaveAsMine(name)
			return err
		}, done)
	}
}

func (d *planDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	title, body, action := a.t("plan.save_as.title"), a.t("plan.save_as.body"), a.t("plan.save_as")
	switch d.mode {
	case planRename:
		title, body, action = a.t("plan.rename.title"), a.t("plan.rename.body"), a.t("common.save")
	case planNew:
		title, body, action = a.t("plan.new.title"), a.t("plan.new.body"), a.t("plan.new.create")
	}
	return dlgFrame{
		title: title, subtitle: body, icon: "layers", tone: toneBlue, width: 480,
		body: func() {
			a.field(c, a.t("plan.name"), "", func() {
				in := a.input(c, "pd-name", &d.name, a.t("plan.name.placeholder"))
				in.AutoFocus()
				if in.Submitted() {
					d.submit(a)
				}
			})
			if d.mode == planNew {
				a.field(c, a.t("plan.new.from"), "", func() {
					sel := boolOr(d.empty, 1, 0)
					if a.segmented(c, "pd-start", &sel, []segItem{
						{a.t("plan.new.copy"), "copy"}, {a.t("plan.new.empty"), "file-text"},
					}, segOpts{small: true}) {
						d.empty = sel == 1
					}
				})
				if d.empty {
					a.txt(c, a.t("plan.new.empty.hint"), 12.5, 400, p.Text2).LineHeight(1.5)
				} else {
					plans := a.allPlans()
					names := make([]string, len(plans))
					cur := 0
					for i, pl := range plans {
						names[i] = a.planName(pl)
						if pl.ID == d.from {
							cur = i
						}
					}
					a.field(c, a.t("plan.new.copy_of"), "", func() {
						a.dropdown(c, "pd-from", names, cur, func(i int) { d.from = plans[i].ID })
					})
				}
			}
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: action, busy: d.busy, disabled: strings.TrimSpace(d.name) == ""}).Clicked() {
				d.submit(a)
			}
		},
	}
}
