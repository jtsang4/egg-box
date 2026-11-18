---
name: gui-changes
description: How to change and check the EggBox desktop interface (internal/gui, cmd/egg-box) with effort proportional to the change - targeted headless interaction tests, screenshots of only the screens the change affects and looking at them, the invariants that hold the UI in place (both languages, small windows, idle frames, decoration that must not take input), and measuring idle CPU only when animation or redraw behaviour changed. Use when editing screens, dialogs, the tray panel, translations, theming or animation. For MyGo lifetimes, identity and threading see mygo-maintenance. 界面改动的测试、截图评审与空闲开销。
---

# GUI changes

The interface is tested without a window (`ui.NewTester`, wrapped by `internal/gui/harness_test.go`) against an invented fixture backend. A change needs proof that it behaves, and, if it changes what is drawn, a look at the screens it changed. It does not need the whole matrix: CI renders every screen in both themes on every run.

## Steps

1. **Behaviour.** Run the tests near your change: `go test -short -count=1 -run 'TestExceptionDialog|TestSettingsPersistChoices' ./internal/gui` (pick by file and name). Add or adjust a test for what you changed: click by visible text, assert the texts on screen. Add `-race` only when the change touches goroutines, `Window.Update` or backend streams; the race GUI suite is very slow.
2. **Look at what you changed.** If pixels change, render just the affected shots: `EGGBOX_SHOTS=/tmp/eggbox-shots go test -count=1 -run 'TestShots/<screen>' ./internal/gui` (the subtest names are the entries of `shots` in `shots_test.go` plus `-light`/`-dark`; `TestPanelShots/panel-<state>` for the tray panel). Open each image at full size; crop with `sips -c H W --cropOffset Y X in.png --out out.png` to inspect small details. Check both themes, and Chinese when text changed. If a fix changes the picture again, re-shoot only that screen.
3. **Small windows and long text.** If layout changed, check the smallest window size the app allows and the other language (it wraps differently). `TestSmallAndLargeWindows` and the dialog tests usually cover this: run them.
4. **Idle cost, only when motion changed.** If you added or changed an animation, frame-rate request or redraw path: a settled screen must ask for no more frames (`FramePending()` after settling; see `live_update_test.go`). Claim a performance improvement only after `scripts/ab-cpu.sh` shows it beyond the run-to-run spread; otherwise revert the "optimisation".

A full visual review against `references/rubric.md` is for new screens or a redesign, not for routine changes.

## Rules that keep paying

- **Invented data only.** Screenshots and the demo use fixtures; never render real accounts or subscriptions, and keep images out of tracked paths (`/tmp`).
- **Decoration must not take input.** An absolutely positioned element painted over controls swallows their clicks. Draw decoration in the owner's `Draw`/`DrawOver`. When a test click "does nothing", suspect something on top before the handler.
- **Phones too.** `Options.Phone` lays the same pages out under a bar of tabs; `a.compact()` branches the layout, and `key.phone` variants of a text (which `App.t` prefers there) say device, not computer. A change to a page shows on a phone: run `TestPhone*` and look at its `TestPhoneShots/phone-<screen>-<lang>-<light|dark>`; keep the desktop's pixels unchanged where the change is for phones only.
- **Both languages, always.** New text goes into `i18n_en.go` and `i18n_zh.go` with the same placeholders; `TestKeysExist`, `TestNoOrphanKeys`, `TestPluralsComplete` and the raw-key sweeps fail otherwise.
- **Options hide features.** A nil option hides the row it powers; platform differences come in through options at the top, never through checks inside widgets. Test presence and absence.
- **Motion is cheap or absent.** Slow ambient motion asks for 30 fps (`Painter.AnimationFrameRate(30)`), fast motion for the display rate, and nothing when nothing changes. Animate state changes, not every data tick. The window draws at the fastest rate anything in it asks, modals included.
- **Pages watch only while shown.** A page subscribing to a stream starts on appear and stops on leave.
- **Ambiguous clicks.** If a label appears twice (navigation and page title), click a unique label.
- **Settle briefly for assertions,** long only for screenshots; the harness already does this.

## The real window

`egg-box --demo` (with `--fresh`, `--connect`, `--seed N`, `--size`, `--page`, `--theme`, `--lang`) runs the real binary against an invented in-process world, nothing installed or kept. Use it when a change involves what only a real window shows (title bar, translucency, tray, single-instance behaviour); a second launch exiting at once is the single-instance lock, not a crash. Do not automate the user's desktop.

## References

- `references/mygo-tester.md` - the Tester API, frame-rate behaviour, overlays, build flags (read when writing a new kind of test).
- `references/rubric.md` - the review rubric, for new screens and redesigns.
- `scripts/ab-cpu.sh` - alternating A/B CPU comparison of two commands (`-r 4 -w 6 -d 12 -- 'cmd A' 'cmd B'`).
