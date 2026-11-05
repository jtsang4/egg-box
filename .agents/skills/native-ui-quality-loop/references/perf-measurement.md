# Measuring idle cost

A window that stays open all day, or a tray app that sits in the background, is judged on what it costs while nothing is happening. Ambient animation at the display's refresh rate (60 or 120 times a second) is the usual culprit, and it is invisible in tests that only check pixels.

## What to measure

Per state, in a fresh process each time (the same demo mode, the same seed, so the content is identical):

| State | Expectation |
|---|---|
| A page with no animation | close to 0 % of one core |
| The main screen with its ambient animation | low single digits, and it must come from something you chose |
| A first-run dialog or modal over an animated screen | no worse than the screen beneath it by much |
| Window hidden or minimized | about 0 % (nothing draws) |
| Tray or panel only | about 0 % |

Real figures from one app on one laptop, to give the order of magnitude (they will differ on yours): a first-run dialog over an animated hero cost about 23 % of a core, the main screen about 5 % idle and about 10 % with a live animation, and a page without the animation about 0.6 %. The cause was one breathing halo asking for 60 frames a second everywhere, including underneath the modal.

## Method

1. **Use CPU time, not a percentage someone else computed.** `top` formats and sampling windows vary, and `ps %cpu` is a decayed average. Take the process's cumulative CPU seconds (`ps -o time=`) at the start and end of a window and divide by the window. `scripts/ab-cpu.sh` does exactly this.
2. **Warm up first** (5 to 6 seconds), then measure 10 to 30 seconds. On Linux `ps` reports whole seconds, so use at least 30.
3. **Alternate the variants** (A B, then B A, then A B, ...) over several rounds and compare medians, so a machine that is warming up, throttling or busy does not favour either side. Do not run measurements while tests or builds are running.
4. **Know the spread.** The same binary measured twice can differ by a factor of two when the machine is busy. A change smaller than the spread between rounds of the same variant has no measured effect.
5. **Revert what does not win.** An optimisation the A/B run does not show is complexity for nothing.

```sh
scripts/ab-cpu.sh -r 4 -w 6 -d 12 -- \
  './build/old --demo --connect --seed 1' \
  './build/new --demo --connect --seed 1'
# illustrative output, one line per command with the figure of each round:
# A  old   26.8 27.1 26.5 26.9   median 26.9 % of one core
# B  new   16.7 16.4 16.5 16.6   median 16.6 % of one core
```

Each round starts every command afresh (`bash -c "exec <command>"`, output discarded), alternates the order between rounds, and stops processes with TERM and then KILL. The commands must be a program that runs by itself with a real window or none; a visible window may flash on screen for the rounds, so keep them few.

A worked example: lowering one ambient animation from 60 to 30 frames a second cut a state from about 26.8 % to about 16.5 % (A/B/A/B), roughly 38 %, with no visible difference. A further "improvement", splitting the hero into a static layer and an animated one, showed no gain in the same runs and was reverted.

## Frame-rate policy

- Ask for the display's rate only for fast motion: a spinning arc, a drag, anything whose movement is visibly choppy at half rate.
- Ask for 30 frames a second for slow ambient motion: breathing glows, drifting rings, slow waves, level meters.
- Ask for none when nothing moves, and make sure the view really stops asking (test it with the harness's idle test).
- Choose the rate **per frame from state**: for example 60 while the fast arc is on screen and 30 otherwise.
- The window draws at the fastest rate that anything in it asks for. One 60 fps element makes everything beneath it, and every dialog over it, animate at 60. In MyGo, `Painter.AnimationFrameRate(fps)` rounds up to a rate the display shows evenly (60, 40 or 30 on a 120 Hz display; 60 or 30 on a 60 Hz one).
- Cache or split static layers only when a measurement shows it helps.
- Backend events must not redraw on every message. Coalesce them: an atomic "pending" flag and a timer of about 24 ms turn a burst of notifications into one redraw, from any goroutine.

## Guarding it afterwards

- **Idle test.** Settle the screen, then assert the view asks for no more frames (`Tester.FramePending()` in MyGo), with a control case: a view that animates must make the same assertion fail, or the test proves nothing. See `assets/mygo-harness/harness_test.go`.
- **Watchers.** A page that subscribes to a live stream starts when shown and stops when hidden; test it with a counter in the fixture backend.
- **Slow tests.** The headless matrix steps the animation clock frame by frame; settling for 2.5 simulated seconds at 60 fps with full software rasterization takes a while. Settle briefly for assertions and long only when producing screenshots, and skip the matrix under `-short`.
