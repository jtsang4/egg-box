# Review rubric

Use it on every screenshot batch. For each screen, go through every section and write findings as *screen, item, what is wrong, fix*. A batch is done when it produces no findings; then fix, re-shoot, and run one more full batch.

State the target before you start, in observable terms. "Award-level" is not observable; these are:

- One clear focal point per screen, and the same visual language across all screens.
- Every state designed, not only the happy one.
- Two themes and every shipped language equally finished.
- Motion that means something and costs nothing when nothing changes.
- Copy a person would write.

## 1. Hierarchy

- One focal point per screen: the primary action or the main state. Squint test: blur the image mentally; the focal point should still stand out.
- A type scale with at least three clearly different levels (title, section, body, caption), and weight contrast rather than size alone.
- Numbers use tabular figures and are aligned; units are smaller or muted.
- State is never colour alone: pair it with an icon or a word.
- Secondary actions look secondary; destructive actions look different and ask first.

## 2. Rhythm and spacing

- Every gap comes from a spacing scale (a 4 or 8 point base) held in tokens; no one-off numbers.
- Cards share padding, corner radius, border and elevation. Sections have more space between them than their own items do.
- Edges align on a grid. Icons are optically aligned with their text (check the baseline, not the box).
- No orphans or widows in short text; no single word on its own line in a button or a chip.
- A control that sits in a stretching column does not stretch by accident: set grow and alignment explicitly (a text button that should sit at the start, not in the centre).

## 3. Colour and contrast

- The primary hue is consistent (and is the one the brief asked for). Neutrals are a ramp, not a handful of unrelated greys. Success, warning and danger colours are rare.
- Contrast in **both** themes: at least 4.5:1 for body text and 3:1 for large text and for the edges of controls. Tinted backgrounds need darker text in light mode and lighter text in dark mode.
- Colours derived from data (a hash-based hue for a region badge, say) are clamped to the palette so that no value wanders into an off-brand colour.
- Disabled, placeholder and hover states are visibly different and still legible.
- Light mode uses shadow for elevation; dark mode uses a lighter surface and a subtle border highlight. Check both.

## 4. States

For every screen that shows data:

| State | Check |
|---|---|
| First run / empty | explains what to do next, with the action right there |
| Loading | a placeholder of the final size, so nothing jumps when data arrives |
| Success | the confirmation is quiet and does not block |
| Error / failed | says what happened and what to do, in plain words, with a retry; does not show raw error strings or keys |
| Partial | some items failing, some fine |
| Disabled / offline / expired | explained, not just greyed out |
| Long content | names ellipsize (with a tooltip or a way to read the full text), messages wrap, identifiers shorten in the middle |
| Many items | long lists are virtualized; scrolling stays smooth |
| Zero results after a filter | says so, and offers to clear the filter |

Controls show hover, pressed, focus and disabled states.

## 5. Text and languages

- Every visible string comes from the catalog; none is built from fragments that cannot be reordered in another language.
- Numbers, dates, durations, relative times and units are localized; counts use plural forms.
- Shoot every shipped language. CJK text has different widths and line heights; long languages overflow first. Check wrapping, clipping, truncation and button widths in each.
- Titles and labels are short, active and human. Errors say what happened and what to do. No exclamation marks as decoration.

## 6. Sizes and platforms

- The minimum window size shows nothing clipped or overlapping. A large window (1920 by 1080 or more) does not stretch content into absurd line lengths.
- Check at 1x and 2x scale.
- Title bar insets differ: traffic lights on the left on macOS, window controls on the right on Windows. Nothing draws under them and the drag region is where a person expects it.
- Platform vocabulary and conventions are respected (where a setting lives, what the system calls it); features that a platform does not support are absent, not disabled.

## 7. Accessibility

- Every control has a role and a label; icon-only buttons have labels.
- Focus order matches visual order; the focus ring is visible; every action works from the keyboard; Escape closes dialogs and returns focus.
- Hit targets are comfortable (at least 28 to 32 px on desktop).
- Decorative motion respects the system's reduced-motion setting.
- Nothing relies on colour alone, hover alone or a tooltip alone.

## 8. Motion

- Motion explains a change of state or place, and lasts 150 to 300 ms for interface transitions. Direct manipulation uses springs.
- Ambient motion is slow, low-contrast and at a low frame rate; it stops when nothing changes.
- An entrance animation never delays interaction.
- Data does not animate on every tick (numbers rolling each second look jittery); state changes do.
- After a change settles, the view asks for no further frames (test it).

## 9. Craft

- One icon set, one stroke weight, one corner-radius system, one elevation system.
- One-pixel borders are crisp at 1x and 2x.
- Gradients, blur and glow are restrained: one brand moment per screen at most (a hero control, a first-run illustration), not every card.
- Illustrations and brand art are drawn in the theme's colours, not pasted in.

## 10. Feel

- The idle cost is near zero when nothing moves (`perf-measurement.md`).
- First frame appears quickly; nothing shifts as data arrives.
- Scrolling, resizing and theme switching do not stutter.

## Cheap invariants worth a test

These hold the result in place after the review is over.

| Invariant | Test |
|---|---|
| Both languages have the same keys and the same placeholders | iterate both catalogs |
| Plural forms are complete | every `.one` has an `.other`, and the reverse |
| No orphan text, no missing text | parse the package's source for string literals: every key used exists, every catalog key is used |
| No raw keys on any page | visit every page in every language; no on-screen text matches the key shape of the catalog |
| Small and large windows render every page | loop sizes and pages |
| A feature whose option is nil is absent; present when the option is set | one test each way |
| A destructive action asks first, and cancelling changes nothing | click, assert the dialog, cancel, assert no effect |
| A page that watches a stream does so only while shown | show, hide, assert the subscription count |
| A settled screen asks for no more frames | `FramePending` after settling (see the harness) |
| Platform flavour | the same page on each platform shows the right rows |
