# MyGo notes for the quality loop

For lifetimes, identity, keys, threading and `go tool mygo vet`, see the `mygo-maintenance` skill. This file is about verifying and polishing a MyGo interface. The details were checked against v0.7.4; read the pinned source for yours:

```sh
d="$(go list -m -f '{{.Dir}}' github.com/egoist/mygo)"
less "$d/ui/doc.go"        # the model of views, elements, input
less "$d/ui/headless.go"   # the Tester
less "$d/ui/paint.go"      # the Painter and its frame-rate requests
```

## The headless Tester

`ui.NewTester(view, width, height)` runs the real view and renders it on the CPU with no window. `assets/mygo-harness/harness_test.go` wraps it. The calls that matter:

| Call | Use |
|---|---|
| `SetDark(bool)`, `SetScale(f)`, `SetSize(w, h)` | the appearance, the HiDPI scale (1 or 2) and the window size |
| `SetTitleBar(ui.TitleBar{Height, Left, Right})` | the room taken by the window controls of a window with a hidden title bar. macOS traffic lights: `{Height: 28, Left: 78}`; Windows controls: `{Height: 32, Right: 138}`. The app reads it with `Context.TitleBar()` |
| `SetVibrancy`, `SetDirection(ui.RightToLeft)`, `SetZoom` | translucency, right-to-left layout, content zoom |
| `Frame()` | render another frame |
| `Advance(d)` | move the Tester's clock and run the frames due. **The clock stands still until `Advance` moves it**, so animations are deterministic: `Advance(150ms)` is a known point in a 200 ms animation |
| `FramePending()` | does the view still ask for a frame? False for a settled static screen |
| `Image()` | the last frame as `*image.RGBA` |
| `Texts()`, `HasText(s)`, `Find(s)` | what is on screen: element text and labels |
| `Click(s)` | click the centre of the element showing text or labelled `s`. Returns an error when none shows it, or when its centre is outside the window (a way to catch controls pushed off-screen at a small size) |
| `Press`, `Move`, `Scroll`, `Key`, `Type` | lower-level input |

Things learned the slow way:

- `Click` and `HasText` match text and labels, **not placeholders**: a search field is found by its label, not by its hint text.
- Two elements with the same text (a navigation entry and a page title) make `Click("Nodes")` ambiguous. Click a unique label, or give the control a distinct accessible label.
- When a click fails, print the texts on screen (the harness's `digest`); the failure then explains itself.
- Settling 2.5 simulated seconds at 60 fps means about 150 software-rendered frames; a suite that settles that long for every assertion becomes slow (55 seconds for a full matrix in one project). Settle about a second for assertions and long only for screenshots.
- A test option such as `Sync: true` that runs "post to the main thread" callbacks inline makes the app deterministic under test; in production they go through `Window.Update`, which is asynchronous.

## Drawing and frame rates

- `Element.Draw(func(p *ui.Painter, r ui.Rect))` paints on an element after its background and before its children; `DrawOver` paints after its children. The callbacks only paint, and may run more than once a frame. Prefer them to extra absolutely positioned elements for decoration.
- `Painter.AnimationFrame()` asks for a repaint at the display's rate; call it each frame while the drawing moves. `Painter.AnimationFrameRate(fps)` is for drawings that look as smooth at `fps` as at the display's rate; it **rounds up to a rate the display shows evenly** (60, 40 or 30 on a 120 Hz display; 60 or 30 on a 60 Hz one). `Painter.After(d)` asks for a repaint after `d`. An element out of view asks for nothing. `Painter.Now()` is the time to animate from.
- The window draws at the fastest rate that anything in it asks for, and a modal draws over a screen that keeps animating. Choose rates per frame from state (60 while the fast arc is on screen, 30 otherwise) and see `perf-measurement.md`.
- An absolutely positioned element painted above interactive siblings takes the pointer events in its bounds. That includes a "decorative" full-window glow, a hero sheen and a selection indicator behind a list. Draw them in the owner's `Draw` instead.
- A control inside a stretching `Column` stretches: wrap it in a `Row` with start justification when it should keep its natural width (a text button that came out centred is this).

## Building and running

```sh
go tool mygo vet .                                  # stored build values, goroutine captures
go tool mygo build -platform darwin/universal -skip-dmg -skip-build-command -sign "$ID" -o DIR
go tool mygo build -platform windows/amd64 -skip-installer -skip-build-command -o DIR
MYGO_LDFLAGS="-X pkg.Version=1.2.3" go tool mygo build ...   # extra linker flags for the application
```

- The application builds and tests with `CGO_ENABLED=0`, and the Windows application builds from any operating system.
- `-skip-dmg`, `-skip-installer` and `-skip-build-command` leave packaging and pre-build steps to your own scripts (see the packaging reference of the `go-release-quality-gates` skill).

## Platform helpers

- `mygo.Shell.OpenPath(path)` opens a file with the system's default application: the way to offer a bundled text such as the third-party notices from the interface. Offer it only where the build carries the file (a nil option hides the row).
- `mygo.App.RequestSingleInstanceLock()` returns false in a second instance, which hands its command line to the first and should exit. A second launch that "exits immediately" is the lock doing its job, not a crash.

## Demo mode and capture tour

Give the application a demo mode that runs against an invented world inside the process (no network, nothing installed, nothing kept): `--demo`, with `--fresh` (start as a first run), `--connect` (be in the connected state), `--seed N` (repeatable traffic), `--size WxH`, `--page`, `--theme`, `--lang`. It is how a human, and the final real-window pass, see every state without the real service.

Behind a development-only build tag, add a capture tour that, for each theme, navigates to each page, waits for the animations (about 1.3 seconds), calls `Window.CapturePage()` on the real window and writes the PNG; it does the same for secondary windows such as a tray panel. Run it on the real binary and review the images with the rubric. Keep the images out of git and never run the tour against real data.
