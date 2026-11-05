---
name: native-ui-quality-loop
description: Drives the visual, interaction and efficiency quality of a desktop GUI (native or any UI that renders headlessly) to a high bar by closing the loop. Invented-data fixtures, a headless screenshot matrix (screens x light/dark x languages x platforms x sizes), review by cropping against a written rubric, behaviour/i18n/accessibility tests, idle CPU measured with alternating A/B runs, and a final real-window pass in a demo mode. Includes MyGo notes (Tester harness, frame-rate policy, overlays that eat clicks). Use when building or polishing a GUI, when asked for award-level design quality, when screens must be verified without a human watching, or when an app feels heavy while idle. Not for browser-based front ends or backend work. 桌面 GUI 品质循环、截图评审、空闲 CPU 测量。
---

# Native UI quality loop

"The tests pass" says nothing about whether a screen is good. This is the loop that produced a bilingual, dual-theme desktop app that held up under repeated self-review: render it, look at it, test how it behaves, measure what it costs while idle, and repeat until a whole pass finds nothing.

## The loop

1. **Write the bar down before drawing anything.** Turn "award-level" into checkable statements in `references/rubric.md` (hierarchy, rhythm, contrast in both themes, every state, bilingual stress, minimum and large sizes, accessibility, motion restraint, idle cost). Include the brief's fixed choices (for example, a blue primary colour) as rubric items.
2. **Make the UI renderable without the world.** A view takes a backend interface and options (a clock, "open file", "launch at login", "quit"). A fixture backend serves invented data; a fake clock makes time repeatable. An option that is nil **hides the feature** it powers, and tests assert both the presence and the absence (a row that must not appear where the platform does not support it is as much a requirement as one that must).
3. **Build a harness.** Render the real view headlessly at a given size, scale, theme, language and platform; let animations settle; click by visible text; assert on the texts on screen; write PNGs when an environment variable names a directory. `assets/mygo-harness/harness_test.go` is a complete, tested one for MyGo; the shape ports to any toolkit that can render off-screen.
4. **Photograph a matrix.** List the screens as data and reach each the way a person would, by clicking. Include empty, first-run, failed, loading, long-content and many-item states, each in light and dark, in every shipped language, at the smallest and a large size, with each platform's chrome. Skip the matrix with `-short` (it is slow, and slower under `-race`) and run it in its own CI step.
5. **Review by looking.** Open every image at full size, then crop and enlarge anything small: `sips -c HEIGHT WIDTH --cropOffset Y X in.png --out out.png` on macOS (height comes first), or crop with the `image` package or ImageMagick elsewhere. Write each finding as *screen, rubric item, what is wrong, fix*; fix; re-shoot; repeat. Finish with one more full pass after the last fix, because fixes break neighbours.
6. **Test behaviour, not pixels.** Navigation, forms and validation messages, dialogs that ask before destructive actions, settings that persist, nothing leaking raw translation keys, small and large windows, platform flavours. See `references/rubric.md` for the list of cheap invariants.
7. **Measure what it costs while idle.** A window that sits open all day must be near-zero CPU when nothing changes. Assert that a settled screen asks for no more frames, and compare variants with `scripts/ab-cpu.sh` (alternating A/B rounds, CPU time over a window, median). A change that does not beat the spread between runs of the same binary is reverted. `references/perf-measurement.md`.
8. **Finish with the real window.** Run the real binary in a demo mode (an invented world inside the process: no network, nothing installed, nothing kept; flags for first-run, connected state, repeatable traffic, size, page, theme, language) and capture the actual window, in a development-only tour that visits every page. Check what only a real window shows: title bar and window controls, translucency, single-instance behaviour, tray and menus, multi-monitor scaling.
9. **Say what only a real session can verify** (see the `long-horizon-delivery` skill's completion audit).

## Rules that paid for themselves

- **Invented data only.** A screenshot cannot be scanned for secrets, so it must never be made from a real account, subscription or machine. Write images to an ignored temporary directory, never to a tracked path. If one was made from real data, do not open it.
- **Settle before you shoot.** A frame captured mid-transition shows ghosting (digits rolling, fades). Screenshots wait for every animation (a longer settle when the shots variable is set); assertions use a shorter one, which keeps the suite fast.
- **Shoot the second language.** It breaks layouts differently (shorter words, taller lines, different wrapping). Run a key-parity test, a placeholder-parity test, a plural-completeness test, an orphan test (every catalog key is used in source, every key used in source exists) and a "no raw keys on any page" test.
- **Decoration must not take input.** In MyGo, an absolutely positioned decorative element painted above interactive siblings intercepts the pointer events in its bounds: a full-window glow swallowed every click, and a hero sheen and a sidebar selection indicator blocked the controls under them. Draw decoration in the owning element's draw callback instead of adding an element. When a test click "does nothing", suspect something painted on top before suspecting the handler.
- **Ambient motion is slow and cheap.** Slow breathing or drifting motion at the display's refresh rate wastes battery; ask for 30 fps unless something fast is on screen (a spinning arc), and measure the effect. A modal dialog over an animated screen makes the whole screen animate at the fastest rate that anything in it asks for.
- **Nothing animates when nothing changes.** Idle means the view stops asking for frames. Numbers that roll on every data tick look jittery rather than premium; animate state changes, not data ticks.
- **Keep click targets unambiguous in tests.** If a label appears twice (navigation entry and page title), click by a unique label or role rather than by text that matches the first hit.
- **Pages watch only while shown.** A page that subscribes to a stream (connections, logs) starts when it appears and stops when it goes; test that.
- **Prune after polish.** Unused helpers and orphaned catalog keys pile up from abandoned design experiments; staticcheck and the orphan test find them (see `go-release-quality-gates`).

## Anti-patterns

- Judging a screen from the code, or from one theme in one language.
- Fixing a finding without re-shooting (the fix is the likeliest source of the next finding).
- Hard-coding pixel positions that only work at one size or scale.
- Putting platform checks inside widgets instead of options injected at the top.
- Adding an animation without measuring its idle cost, or keeping an optimisation that the A/B run does not show.
- Letting screenshots, demo data or captures with real names into git.

## References

- `references/rubric.md`: the review rubric and the cheap invariants worth testing.
- `references/perf-measurement.md`: idle CPU method, the frame-rate policy, a worked example, how to read noise.
- `references/mygo-notes.md`: the Tester API, platform chrome, builder flags, frame rates, overlays, demo mode and capture tour (with pointers to the `mygo-maintenance` skill for lifetimes and identity).
- `scripts/ab-cpu.sh`: fair CPU comparison of two or more commands.
- `assets/mygo-harness/harness_test.go`: headless harness with screenshot output and the idle test.
