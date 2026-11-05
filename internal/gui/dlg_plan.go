package gui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/routing"
)

// planDialog names a plan of the person's own: a copy of the plan in force
// that is theirs to change, or a new name for one they have.
type planDialog struct {
	rename bool
	name   string
	err    string
	busy   bool
}

// openPlanName opens the dialog that saves the plan in force as one of the
// person's own (rename=false) or renames the plan of their own in force.
func (a *App) openPlanName(rename bool) {
	d := &planDialog{rename: rename}
	if rename {
		d.name = a.rv().Plan.Name
	} else {
		d.name = a.t("plan.default_name")
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
	if d.rename {
		id := a.rv().Plan.ID
		a.editRouting(func(s *routing.State) error { return s.RenamePlan(id, name) }, done)
		return
	}
	a.editRouting(func(s *routing.State) error {
		_, err := s.SaveAsMine(name)
		return err
	}, done)
}

func (d *planDialog) frame(a *App, c *ui.Context) dlgFrame {
	title, body, action := a.t("plan.save_as.title"), a.t("plan.save_as.body"), a.t("plan.save_as")
	if d.rename {
		title, body, action = a.t("plan.rename.title"), a.t("plan.rename.body"), a.t("common.save")
	}
	return dlgFrame{
		title: title, subtitle: body, icon: "layers", tone: toneBlue, width: 460,
		body: func() {
			a.field(c, a.t("plan.name"), "", func() {
				in := a.input(c, "pd-name", &d.name, a.t("plan.name.placeholder"))
				in.AutoFocus()
				if in.Submitted() {
					d.submit(a)
				}
			})
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
