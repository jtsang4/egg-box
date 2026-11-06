package gui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"eggbox/internal/sysproxy"
)

// proxyBanners warn, while connected, about system proxy settings that point
// at a local port that is not this application's: one that nothing listens on
// cuts off every program that uses the system proxy, which looks to the person
// like the connection failing for some programs only. The settings are not
// the application's to change; the banner says what is wrong and how to put
// it right.
func (a *App) proxyBanners(c *ui.Context) {
	if !a.snap.Phase.Online() {
		return
	}
	for _, is := range a.snap.ProxyIssues {
		key := "proxy-" + is.Key()
		// A program listening behind an idle service's proxy changes nothing now.
		if a.proxyDismissed[key] || (is.State == sysproxy.Foreign && !is.Active) {
			continue
		}
		is := is
		msg, t := "proxy.foreign", toneBlue
		if is.State == sysproxy.Dead {
			msg, t = "proxy.dead.idle", toneWarn
			if is.Active {
				msg = "proxy.dead"
			}
		}
		text := a.t(msg, "services", strings.Join(is.Services(), a.t("proxy.list.sep")), "endpoint", is.Endpoint())
		a.alertBanner(c, key, t, text, true, func() {
			if is.State == sysproxy.Dead && a.platform == "darwin" {
				if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("proxy.copy"), icon: "copy"}).Clicked() {
					c.WriteClipboard(sysproxy.DarwinCommands([]sysproxy.Issue{is}))
					a.toastInfo(a.t("proxy.copied"))
				}
			}
			if a.opt.OpenProxySettings != nil {
				if a.btn(c, btnSpec{kind: btnSecondary, small: true, label: a.t("proxy.open")}).Clicked() {
					a.opt.OpenProxySettings()
				}
			}
			if a.btn(c, btnSpec{kind: btnGhost, small: true, label: a.t("banner.dismiss")}).Clicked() {
				a.proxyDismissed[key] = true
			}
		})
	}
}
