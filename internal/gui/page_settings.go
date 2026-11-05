package gui

import (
	"context"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/jtsang4/sing-box-ui/internal/agentapi"
	"github.com/jtsang4/sing-box-ui/internal/app"
	"github.com/jtsang4/sing-box-ui/internal/configgen"
)

// settingsState holds the text being edited in the network section, which is
// applied as a whole.
type settingsState struct {
	from    configgen.Prefs // the preferences the buffers were filled from
	loaded  bool
	dns1    string
	dns2    string
	testURL string
	mtu     string
	port    string

	pending   map[string]bool // switches the service is still applying
	bootKnown bool            // the startup choice was read
	boot      bool
	bootErr   bool
}

// ServiceStartup lets the user choose whether the privileged service starts
// together with the computer.
type ServiceStartup struct {
	Get func() (bool, error)
	Set func(on bool) error
}

func (a *App) settingsPage(c *ui.Context) {
	a.syncSettingsBuffers()
	a.pageFrame(c, a.t("nav.settings"), a.t("settings.sub"), nil, func() {
		ui.Column(c).Gap(18).MaxWidth(940).Children(func() {
			a.appearanceSection(c)
			a.startupSection(c)
			a.connectionSection(c)
			a.networkSection(c)
			a.ruleDataSection(c)
			a.serviceSection(c)
			a.aboutSection(c)
		})
	})
}

// section is a titled card of settings.
func (a *App) section(c *ui.Context, iconName, title, desc string, rows func()) {
	p := a.pl
	a.card(c).Padding(0).Gap(0).Children(func() {
		ui.Row(c).Padding(18, 22, 8, 22).Gap(14).AlignItems(ui.Center).Children(func() {
			a.iconTile(c, iconName, toneBlue, 38)
			ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
				a.heading(c, title)
				if desc != "" {
					a.txt(c, desc, 12.5, 400, p.Text3)
				}
			})
		})
		ui.Column(c).Padding(0, 22, 6, 22).Children(rows)
	})
}

// rowDivider separates the rows of a section.
func (a *App) rowDivider(c *ui.Context) { a.hr(c) }

// ------------------------------------------------------------ appearance

var (
	themeChoices = []string{"", "light", "dark"}
	langChoices  = []string{"", "en", "zh"}
)

func indexOfString(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return 0
}

func (a *App) appearanceSection(c *ui.Context) {
	a.section(c, "sparkles", a.t("settings.appearance"), a.t("settings.appearance.desc"), func() {
		a.settingRow(c, a.t("settings.theme"), a.t("settings.theme.desc"), func() {
			sel := indexOfString(themeChoices, a.set.Theme)
			if a.segmented(c, "set-theme", &sel, []segItem{
				{a.t("settings.theme.system"), "monitor"}, {a.t("settings.theme.light"), "sun"}, {a.t("settings.theme.dark"), "moon"},
			}, segOpts{small: true}) {
				v := themeChoices[sel]
				a.updateSettings(func(s *settingsT) { s.Theme = v })
			}
		})
		a.rowDivider(c)
		a.settingRow(c, a.t("settings.language"), a.t("settings.language.desc"), func() {
			sel := indexOfString(langChoices, a.set.Language)
			if a.segmented(c, "set-lang", &sel, []segItem{
				{a.t("settings.language.system"), ""}, {"English", ""}, {"简体中文", ""},
			}, segOpts{small: true}) {
				v := langChoices[sel]
				a.updateSettings(func(s *settingsT) { s.Language = v })
			}
		})
	})
}

// --------------------------------------------------------------- startup

func (a *App) startupSection(c *ui.Context) {
	a.section(c, "power", a.t("settings.startup"), a.t("settings.startup.desc"), func() {
		first := true
		sep := func() {
			if !first {
				a.rowDivider(c)
			}
			first = false
		}
		if a.opt.ServiceStartup != nil {
			sep()
			a.serviceBootRow(c)
		}
		if a.opt.SetLaunchAtLogin != nil {
			sep()
			on := a.pendingOr("login", "login-item", a.set.LaunchAtLogin)
			if a.toggleRow(c, "set-login", a.t("settings.login"), a.t("settings.login.desc"), &on, false) {
				a.setPending("login", on)
				a.job("login-item", func(context.Context) error { return a.opt.SetLaunchAtLogin(on) }, func(err error) {
					a.clearPending("login")
					if err != nil {
						a.fail(err)
						return
					}
					a.updateSettings(func(s *settingsT) { s.LaunchAtLogin = on })
				})
			}
		}
		sep()
		a.agentToggle(c, "autoconnect", "settings.autoconnect", "settings.autoconnect.desc",
			func(s *agentapi.Settings) bool { return s.GetAutoConnect() },
			func(r *agentapi.UpdateSettingsRequest, v bool) { r.AutoConnect = &v })
		sep()
		on := a.set.CloseToTray
		if a.toggleRow(c, "set-tray", a.t("settings.tray"), a.t("settings.tray.desc"), &on, false) {
			a.updateSettings(func(s *settingsT) { s.CloseToTray = on })
		}
		sep()
		notify := a.set.Notifications
		if a.toggleRow(c, "set-notify", a.t("settings.notify"), a.t("settings.notify.desc"), &notify, false) {
			a.updateSettings(func(s *settingsT) { s.Notifications = notify })
		}
	})
}

// serviceBootRow is the choice to start the privileged service with the
// computer. What the platform says is asked once, off the main thread, and
// kept until the choice changes.
func (a *App) serviceBootRow(c *ui.Context) {
	st := &a.sett
	if !st.bootKnown && !a.busy("boot-get") {
		var on bool
		a.job("boot-get", func(context.Context) error {
			var err error
			on, err = a.opt.ServiceStartup.Get()
			return err
		}, func(err error) {
			st.bootKnown, st.bootErr, st.boot = true, err != nil, on
		})
	}
	if st.bootErr {
		a.settingRow(c, a.t("settings.boot"), a.pk("settings.boot.desc"), func() {
			a.txt(c, a.t("settings.boot.unknown"), 12.5, 500, a.pl.Warn)
		})
		return
	}
	on := st.boot
	if a.toggleRow(c, "set-boot", a.t("settings.boot"), a.pk("settings.boot.desc"), &on, !st.bootKnown) {
		st.boot = on
		a.job("service-boot", func(context.Context) error { return a.opt.ServiceStartup.Set(on) }, func(err error) {
			if err != nil {
				a.fail(err)
				st.bootKnown = false // ask again what the system says
			}
		})
	}
}

// agentToggle is a switch bound to one of the service's own settings.
func (a *App) agentToggle(c *ui.Context, key, title, desc string, get func(*agentapi.Settings) bool, set func(*agentapi.UpdateSettingsRequest, bool)) {
	as := a.snap.AgentSettings
	on := a.pendingOr(key, "agent-"+key, as != nil && get(as))
	if a.toggleRow(c, "set-"+key, a.t(title), a.t(desc), &on, as == nil) && as != nil {
		v := on
		a.setPending(key, v)
		a.job("agent-"+key, func(ctx context.Context) error {
			return a.be.SetAgentSettings(ctx, func(r *agentapi.UpdateSettingsRequest) { set(r, v) })
		}, func(err error) {
			a.clearPending(key)
			a.fail(err)
		})
	}
}

// pendingOr is what a switch shows: the value the user just chose while the
// job applying it runs, otherwise the stored one.
func (a *App) pendingOr(key, job string, stored bool) bool {
	if want, ok := a.sett.pending[key]; ok && a.busy(job) {
		return want
	}
	return stored
}

func (a *App) setPending(key string, v bool) {
	if a.sett.pending == nil {
		a.sett.pending = map[string]bool{}
	}
	a.sett.pending[key] = v
}

func (a *App) clearPending(key string) { delete(a.sett.pending, key) }

// ------------------------------------------------------------ connection

func (a *App) connectionSection(c *ui.Context) {
	a.section(c, "shield-check", a.t("settings.connection"), a.t("settings.connection.desc"), func() {
		a.agentToggle(c, "netchange", "settings.netchange", "settings.netchange.desc",
			func(s *agentapi.Settings) bool { return s.GetReconnectOnNetworkChange() },
			func(r *agentapi.UpdateSettingsRequest, v bool) { r.ReconnectOnNetworkChange = &v })
		a.rowDivider(c)
		a.agentToggle(c, "resume", "settings.resume", "settings.resume.desc",
			func(s *agentapi.Settings) bool { return s.GetResumeAfterCrash() },
			func(r *agentapi.UpdateSettingsRequest, v bool) { r.ResumeAfterCrash = &v })
		a.rowDivider(c)
		refresh := a.set.AutoRefresh
		if a.toggleRow(c, "set-autorefresh", a.t("settings.autorefresh"), a.t("settings.autorefresh.desc"), &refresh, false) {
			a.updateSettings(func(s *settingsT) { s.AutoRefresh = refresh })
		}
		a.rowDivider(c)
		reapply := a.set.ReapplyAfterRefresh
		if a.toggleRow(c, "set-reapply", a.t("settings.reapply"), a.t("settings.reapply.desc"), &reapply, false) {
			a.updateSettings(func(s *settingsT) { s.ReapplyAfterRefresh = reapply })
		}
	})
}

// --------------------------------------------------------------- network

var (
	testIntervals = []int{5, 10, 30, 60}
	tunStacks     = []string{"system", "mixed", "gvisor"}
)

// syncSettingsBuffers refills the edit buffers when the stored preferences
// changed behind them (and the user has nothing pending).
func (a *App) syncSettingsBuffers() {
	st := &a.sett
	if st.loaded && st.from == a.prefs {
		return
	}
	if st.loaded && a.prefsDirty() {
		// The user is typing: keep it, but remember what it is based on.
		st.from = a.prefs
		return
	}
	st.loaded, st.from = true, a.prefs
	st.dns1, st.dns2, st.testURL = a.prefs.DomesticDNS, a.prefs.RemoteDNS, a.prefs.TestURL
	st.mtu = strconv.Itoa(a.prefs.MTU)
	st.port = strconv.Itoa(a.prefs.MixedPort)
}

// bufferPrefs is the preferences with the edits applied, and what is wrong.
func (a *App) bufferPrefs() (configgen.Prefs, []string) {
	st := &a.sett
	p := a.prefs
	p.DomesticDNS, p.RemoteDNS, p.TestURL = strings.TrimSpace(st.dns1), strings.TrimSpace(st.dns2), strings.TrimSpace(st.testURL)
	var notes []string
	if n, err := strconv.Atoi(strings.TrimSpace(st.mtu)); err == nil {
		p.MTU = n
	} else {
		notes = append(notes, a.t("settings.net.bad_mtu"))
	}
	if n, err := strconv.Atoi(strings.TrimSpace(st.port)); err == nil {
		p.MixedPort = n
	} else {
		notes = append(notes, a.t("settings.net.bad_port"))
	}
	norm, adj := p.Normalize()
	if len(adj) > 0 && len(notes) == 0 {
		notes = append(notes, a.t("settings.net.invalid"))
	}
	_ = norm
	return p, notes
}

func (a *App) prefsDirty() bool {
	st := &a.sett
	p := a.prefs
	return strings.TrimSpace(st.dns1) != p.DomesticDNS || strings.TrimSpace(st.dns2) != p.RemoteDNS ||
		strings.TrimSpace(st.testURL) != p.TestURL || strings.TrimSpace(st.mtu) != strconv.Itoa(p.MTU) ||
		strings.TrimSpace(st.port) != strconv.Itoa(p.MixedPort)
}

func (a *App) networkSection(c *ui.Context) {
	st := &a.sett
	a.section(c, "sliders", a.t("settings.network"), a.t("settings.network.desc"), func() {
		regions := a.prefs.RegionGroups
		if a.toggleRow(c, "set-regions", a.t("settings.regions"), a.t("settings.regions.desc"), &regions, false) {
			p := a.prefs
			p.RegionGroups = regions
			a.savePrefs(p)
		}
		a.rowDivider(c)
		sniff := a.prefs.Sniff
		if a.toggleRow(c, "set-sniff", a.t("settings.sniff"), a.t("settings.sniff.desc"), &sniff, false) {
			p := a.prefs
			p.Sniff = sniff
			a.savePrefs(p)
		}
		a.rowDivider(c)
		a.settingRow(c, a.t("settings.interval"), a.t("settings.interval.desc"), func() {
			items := make([]segItem, len(testIntervals))
			sel := 1
			for i, m := range testIntervals {
				items[i] = segItem{label: a.t("settings.minutes", "n", strconv.Itoa(m))}
				if m == a.prefs.TestIntervalMin {
					sel = i
				}
			}
			if a.segmented(c, "set-interval", &sel, items, segOpts{small: true}) {
				p := a.prefs
				p.TestIntervalMin = testIntervals[sel]
				a.savePrefs(p)
			}
		})
		a.rowDivider(c)
		a.settingRow(c, a.t("settings.stack"), a.t("settings.stack.desc"), func() {
			sel := indexOfString(tunStacks, a.prefs.TunStack)
			if a.segmented(c, "set-stack", &sel, []segItem{
				{a.t("settings.stack.system"), ""}, {a.t("settings.stack.mixed"), ""}, {"gVisor", ""},
			}, segOpts{small: true}) {
				p := a.prefs
				p.TunStack = tunStacks[sel]
				a.savePrefs(p)
			}
		})
		a.rowDivider(c)
		ui.Column(c).Gap(14).Padding(14, 0, 14, 0).Children(func() {
			ui.Row(c).Gap(14).Children(func() {
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					a.field(c, a.t("settings.dns.domestic"), a.t("settings.dns.domestic.hint"), func() { a.input(c, "set-dns1", &st.dns1, "223.5.5.5") })
				})
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					a.field(c, a.t("settings.dns.remote"), a.t("settings.dns.remote.hint"), func() { a.input(c, "set-dns2", &st.dns2, "https://1.1.1.1/dns-query") })
				})
			})
			ui.Row(c).Gap(14).AlignItems(ui.Start).Children(func() {
				ui.Column(c).Grow(2).Basis(0).Children(func() {
					a.field(c, a.t("settings.testurl"), a.t("settings.testurl.hint"), func() { a.input(c, "set-testurl", &st.testURL, "https://www.gstatic.com/generate_204") })
				})
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					a.field(c, a.t("settings.mtu"), a.t("settings.mtu.hint"), func() { a.input(c, "set-mtu", &st.mtu, "1500") })
				})
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					a.field(c, a.t("settings.port"), a.t("settings.port.hint"), func() { a.input(c, "set-port", &st.port, "0") })
				})
			})
			a.networkApply(c)
		})
	})
}

// networkApply shows the apply and reset buttons while there is a pending edit.
func (a *App) networkApply(c *ui.Context) {
	if !a.prefsDirty() {
		return
	}
	p, notes := a.bufferPrefs()
	ui.Column(c.Key("net-apply")).Gap(10).Children(func() {
		if len(notes) > 0 {
			a.errorNote(c, strings.Join(notes, " "))
		}
		ui.Row(c).Gap(10).Justify(ui.End).Children(func() {
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("settings.net.reset")}).Clicked() {
				a.sett.loaded = false
				a.syncSettingsBuffers()
			}
			if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("settings.net.apply"), disabled: len(notes) > 0, busy: a.busy("prefs")}).Clicked() {
				a.savePrefs(p)
			}
		})
	})
}

// -------------------------------------------------------------- rule data

var (
	mirrors       = []string{"auto", "github", "jsdelivr"}
	ruleIntervals = []int{0, 24, 72, 168}
)

func (a *App) ruleDataSection(c *ui.Context) {
	a.section(c, "database", a.t("settings.ruledata"), a.t("settings.ruledata.desc"), func() {
		a.settingRow(c, a.t("settings.mirror"), a.t("settings.mirror.desc"), func() {
			sel := indexOfString(mirrors, a.set.RuleMirror)
			if a.segmented(c, "set-mirror", &sel, []segItem{
				{a.t("settings.mirror.auto"), ""}, {"GitHub", ""}, {"jsDelivr", ""},
			}, segOpts{small: true}) {
				v := mirrors[sel]
				a.updateSettings(func(s *settingsT) { s.RuleMirror = v })
			}
		})
		a.rowDivider(c)
		a.settingRow(c, a.t("settings.ruleinterval"), a.t("settings.ruleinterval.desc"), func() {
			sel := 0
			for i, h := range ruleIntervals {
				if h == a.set.RuleUpdateHours {
					sel = i
				}
			}
			if a.segmented(c, "set-ruleint", &sel, []segItem{
				{a.t("settings.never"), ""}, {a.t("settings.daily"), ""}, {a.t("settings.3days"), ""}, {a.t("settings.weekly"), ""},
			}, segOpts{small: true}) {
				v := ruleIntervals[sel]
				a.updateSettings(func(s *settingsT) { s.RuleUpdateHours = v })
			}
		})
	})
}

// ---------------------------------------------------------------- service

var logLevels = []string{"error", "warn", "info", "debug"}

func (a *App) serviceSection(c *ui.Context) {
	p := a.pl
	st := a.snap.Status
	a.section(c, "server", a.t("settings.service"), a.pk("settings.service.desc"), func() {
		a.kv(c, a.t("settings.service.state"), func() {
			t, label := a.serviceState()
			a.dot(c, t, 8, t == toneGood)
			a.txt(c, label, 13, 600, p.Text)
		})
		if st != nil {
			a.rowDivider(c)
			a.kvText(c, a.t("settings.service.version"), strOr(st.GetAgentVersion(), "—"))
			a.rowDivider(c)
			a.kvText(c, a.t("settings.service.core"), "sing-box "+strOr(st.GetCoreVersion(), "—"))
			if st.GetAuthorizedUser() != "" {
				a.rowDivider(c)
				a.kvText(c, a.t("settings.service.user"), st.GetAuthorizedUser())
			}
			if st.GetTunInterface() != "" && a.snap.Phase.Online() {
				a.rowDivider(c)
				a.kvText(c, a.t("settings.service.tun"), st.GetTunInterface())
			}
			if st.GetCleanup() == agentapi.CleanupState_CLEANUP_STATE_FAILED {
				a.rowDivider(c)
				a.cleanupRow(c)
			}
		}
		a.rowDivider(c)
		a.settingRow(c, a.t("settings.loglevel"), a.t("settings.loglevel.desc"), func() {
			cur := 2
			if as := a.snap.AgentSettings; as != nil {
				cur = indexOfString(logLevels, as.GetLogLevel())
			}
			w := ui.Row(c.Key("loglevel-wrap")).Disabled(a.snap.AgentSettings == nil)
			w.Children(func() {
				if a.segmented(c, "set-loglevel", &cur, []segItem{
					{a.t("settings.loglevel.error"), ""}, {a.t("settings.loglevel.warn"), ""},
					{a.t("settings.loglevel.info"), ""}, {a.t("settings.loglevel.debug"), ""},
				}, segOpts{small: true}) {
					v := logLevels[cur]
					a.job("agent-loglevel", func(ctx context.Context) error {
						return a.be.SetAgentSettings(ctx, func(r *agentapi.UpdateSettingsRequest) { r.LogLevel = &v })
					}, func(err error) { a.fail(err) })
				}
			})
		})
		if a.snap.Phase == app.PhaseNoAgent && a.opt.Authorize != nil {
			a.rowDivider(c)
			ui.Row(c).Padding(14, 0).Children(func() {
				if a.btn(c, btnSpec{kind: btnPrimary, label: a.t("hero.authorize"), icon: "shield-check", busy: a.busy("authorize")}).Clicked() {
					a.authorize()
				}
			})
		}
	})
}

// cleanupRow warns that the network changes of a previous run were not all
// undone, and offers to retry.
func (a *App) cleanupRow(c *ui.Context) {
	p := a.pl
	st := a.snap.Status
	ui.Row(c).Padding(14, 0).Gap(14).AlignItems(ui.Center).Children(func() {
		a.iconTile(c, "alert", toneWarn, 34)
		ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
			a.txt(c, a.t("settings.cleanup.title"), 13.5, 650, p.Text)
			a.txt(c, strOr(st.GetCleanupDetail(), a.t("settings.cleanup.body")), 12, 400, p.Text3).LineHeight(1.45)
		})
		if a.btn(c, btnSpec{kind: btnSoft, label: a.t("settings.cleanup.retry"), busy: a.busy("cleanup")}).Clicked() {
			a.job("cleanup", func(ctx context.Context) error { return a.be.RetryCleanup(ctx) }, func(err error) { a.fail(err) })
		}
	})
}

// serviceState is the colour and the word of the privileged service.
func (a *App) serviceState() (tone, string) {
	switch {
	case a.snap.Phase == app.PhaseNoAgent:
		return toneWarn, a.t("settings.service.unreachable")
	case a.snap.Status == nil:
		return toneNeutral, a.t("settings.service.unknown")
	}
	switch a.snap.Status.GetService() {
	case agentapi.ServiceState_SERVICE_STATE_READY:
		return toneGood, a.t("settings.service.ready")
	case agentapi.ServiceState_SERVICE_STATE_STARTING:
		return toneBlue, a.t("settings.service.starting")
	}
	return toneWarn, a.t("settings.service.degraded")
}

// kv is a labelled value row: the label on the left, content on the right.
func (a *App) kv(c *ui.Context, label string, value func()) {
	ui.Row(c).Padding(13, 0).Gap(16).AlignItems(ui.Center).Children(func() {
		a.txt(c, label, 13.5, 500, a.pl.Text2).Grow(1)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(value)
	})
}

func (a *App) kvText(c *ui.Context, label, value string) {
	a.kv(c, label, func() {
		a.txt(c, value, 13, 500, a.pl.Text).FontFeatures("tnum").SingleLine().Selectable()
	})
}

func strOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ------------------------------------------------------------------ about

func (a *App) aboutSection(c *ui.Context) {
	p := a.pl
	a.section(c, "life-buoy", a.t("settings.about"), a.t("settings.about.desc"), func() {
		a.kvText(c, a.t("settings.about.app"), "Sing-Box UI "+strOr(a.opt.Version, "dev"))
		a.rowDivider(c)
		a.kvText(c, a.t("settings.about.platform"), a.platformName())
		a.rowDivider(c)
		ui.Row(c).Padding(14, 0).Gap(10).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				a.txt(c, a.t("settings.diag"), 13.5, 600, p.Text)
				a.txt(c, a.t("settings.diag.desc"), 12, 400, p.Text3).LineHeight(1.45)
			})
			if a.btn(c, btnSpec{kind: btnSecondary, label: a.t("settings.diag.export"), icon: "download", busy: a.busy("diag"), disabled: a.opt.SaveFile == nil}).Clicked() {
				a.exportDiagnostics()
			}
		})
		a.rowDivider(c)
		ui.Row(c).Padding(14, 0).Gap(10).Children(func() {
			ui.Column(c).Grow(1).Basis(0).Gap(3).Children(func() {
				a.txt(c, a.t("settings.welcome"), 13.5, 600, p.Text)
				a.txt(c, a.t("settings.welcome.desc"), 12, 400, p.Text3)
			})
			if a.btn(c, btnSpec{kind: btnGhost, label: a.t("settings.welcome.show")}).Clicked() {
				a.openWelcome()
			}
		})
	})
}

// platformName is the operating system in words.
func (a *App) platformName() string {
	switch a.platform {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	}
	return a.platform
}

// exportDiagnostics collects the report in a job and asks where to save it.
func (a *App) exportDiagnostics() {
	var name string
	var data []byte
	a.job("diag", func(ctx context.Context) error {
		var err error
		name, data, err = a.be.ExportDiagnostics(ctx)
		return err
	}, func(err error) {
		if err != nil {
			a.fail(err)
			return
		}
		path, err := a.opt.SaveFile(name, data)
		switch {
		case err != nil:
			a.fail(err)
		case path != "":
			a.toastInfo(a.t("settings.diag.saved"))
		}
	})
}
