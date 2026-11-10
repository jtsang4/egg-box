package gui

import (
	"context"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/library"
	"eggbox/internal/subscription"
)

// nodeLinkDialog edits an imported node as the share link it was read from.
// Saving replaces that one node; the import it belongs to stays.
type nodeLinkDialog struct {
	subID, sub string
	// was is the node as it is; was.Link is "" when its link was not kept.
	was  subscription.Node
	link string
	// read is what link says, worked out once per change of it.
	readFor string
	read    *subscription.Node
	err     string
	busy    bool
}

// openNodeLink opens the link of a node of an import for editing.
func (a *App) openNodeLink(subID, subName string, n subscription.Node) {
	a.openDialog(&nodeLinkDialog{subID: subID, sub: subName, was: n, link: n.Link})
}

func (d *nodeLinkDialog) parse() {
	if d.link == d.readFor {
		return
	}
	d.readFor, d.read = d.link, nil
	// Read as saving reads it, so the name shown is the one it will have.
	if n, err := subscription.EditedLink(d.was, d.link); err == nil {
		d.read = &n
	}
}

func (d *nodeLinkDialog) frame(a *App, c *ui.Context) dlgFrame {
	p := a.pl
	d.parse()
	changed := strings.TrimSpace(d.link) != strings.TrimSpace(d.was.Link)
	return dlgFrame{
		title: a.t("nodelink.title"), subtitle: d.was.Name + " · " + d.sub, icon: "link", tone: toneBlue, width: 580,
		body: func() {
			if d.was.Link == "" {
				a.txt(c, a.t("nodelink.none"), 12.5, 400, p.Text2).LineHeight(1.5)
			}
			a.field(c, a.t("nodelink.link"), a.t("nodelink.hint"), func() {
				a.area(c, "nl-link", &d.link, "socks5://127.0.0.1:1080#Gateway", 2, 5).Font("monospace").AutoFocus()
			})
			switch {
			case d.read != nil:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon("check-circle")).Size(14, 14).TextColor(p.Good)
					a.txt(c, d.read.Protocol()+" · "+d.read.Server+":"+strconv.Itoa(d.read.Port)+"  ·  "+d.read.Name, 12.5, 500, p.Text2).SingleLine()
				})
			case strings.TrimSpace(d.link) != "":
				a.txt(c, a.t("err.node_bad_link"), 12.5, 400, p.Warn).LineHeight(1.5)
			}
			if d.err != "" {
				a.errorNote(c, d.err)
			}
		},
		buttons: func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("common.cancel")}).Clicked() {
				a.dlgOpen = false
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("common.save"), busy: d.busy, disabled: d.read == nil || !changed}).Clicked() {
				d.submit(a)
			}
		},
	}
}

func (d *nodeLinkDialog) submit(a *App) {
	if d.busy || d.read == nil {
		return
	}
	d.busy, d.err = true, ""
	sub, key, link := d.subID, d.was.Key(), d.link
	a.job("node-link-"+key, func(ctx context.Context) error {
		_, err := a.be.EditImportedNode(ctx, sub, key, link)
		return err
	}, func(err error) {
		d.busy = false
		if err != nil {
			d.err = a.explain(err)
			return
		}
		if cur, ok := a.dlg.(*nodeLinkDialog); ok && cur == d {
			a.dlgOpen = false
		}
		a.toastInfo(a.t("nodelink.saved"))
	})
}

// openImportedNode opens the node of an import whose key is key for
// editing, as stored. A node that changed or went meanwhile is said to be gone.
func (a *App) openImportedNode(subID, subName, key string) {
	nodes, err := a.be.Nodes(subID)
	if err != nil {
		a.fail(err)
		return
	}
	for _, n := range nodes {
		if n.Key() == key {
			a.openNodeLink(subID, subName, n)
			return
		}
	}
	a.fail(library.ErrNoNode)
}
