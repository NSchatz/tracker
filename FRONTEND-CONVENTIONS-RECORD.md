# Frontend conventions: the record

tracker ships **two** user interfaces, and the umbrella's `documentation/frontend-conventions.md`
binds both:

- the **browser map** the Go server serves at `GET /map`, with its vendored assets under `/static`;
- the **Android screen**, the client's single Compose surface.

This file maps every clause **F1 through F11**, **for each of the two surfaces**, to either the
rendered assertion that proves it or a named exemption saying why that clause cannot apply there.
Twenty-two pairs, each mapped exactly once.

It is checked, not merely written. `make verify-ui-record` parses the table below and exits non-zero
naming the clause and surface if any pair is absent, carries both an assertion and an exemption, or
names an assertion **that did not run in the last recorded invocation of its route**. The browser
route writes what it ran to `build/uiverify/last-run-web.json`; the Android route's evidence is the
emulator's own JUnit results under `android/app/build/outputs/androidTest-results/connected/`. A
skipped or failed case does not count as having run.

Run the whole thing with:

```bash
make verify-ui          # the browser map, in a real engine
make verify-ui-android  # the Android screen, on a booted emulator
make verify-ui-refusal  # both routes, with their prerequisite removed
make verify-ui-record   # this file, against what actually ran
```

## What "graded" means here

**F2 is the clause that makes the rest of them mean anything**: a claim about what a person SEES is
graded by the runtime that draws it, never by searching HTML, CSS, Kotlin or a compiled resource
table, because a text search cannot decide what a rule applies to, what won the cascade, or what was
shown rather than merely built. So:

- the browser assertions run in Chromium over the **production** `/map` and `/static` handlers, and
  every number they compare against a threshold is read out of the live engine;
- the Android assertions run as an **instrumented** suite on a booted emulator, with Google's
  Accessibility Test Framework applied to the AccessibilityNodeInfo tree the platform actually built
  and the screenshot the emulator actually painted.

**Every assertion in this table is also demonstrated able to FAIL.** Each one is re-run against the
same surface mutated to break exactly the claim it measures (the served bytes, for the map; a
debug-only `UiMutation`, inert in a release build, for the app), and the route fails if fewer
demonstrations ran than there are claims. A check that cannot go red is not evidence.

That count is enforced in three places, and the third exists because the first two cannot see the
emulator's demonstrations:

- the browser route compares them in-process (`uiverify.Summarise`);
- `make verify-ui-android` finishes with `uiverify android`, which reads the emulator's own JUnit
  results back and refuses unless every assertion this table names on the Android screen ran **and**
  a case named `<assertion>_demonstration` passed beside it;
- `make verify-ui-record` applies the same rule to every assertion in the table, on both surfaces, so
  a row here can never cite a check that was only ever seen passing.

`gradlew connectedDebugAndroidTest` on its own is green whenever the cases that *ran* passed, so
without that second bullet a suite whose twelve demonstrations had been deleted, renamed or
`@Ignore`d would report the whole Android surface green with no evidence that any of its assertions
can fail at all.

## The record

| clause | surface | assertion | exemption |
|---|---|---|---|
| F1 | browser map | AC1-keyboard, AC1-names, AC1-colour-free, AC2-contrast-light, AC2-contrast-dark, AC3-focus, AC4-target-size | - |
| F1 | android screen | AC13_platform_checks_pass_light, AC13_platform_checks_pass_dark, AC13_no_state_by_colour_alone, AC14_operable_without_a_pointer | - |
| F2 | browser map | AC1-keyboard, AC21-policy | - |
| F2 | android screen | AC13_platform_checks_pass_light, AC12_nothing_clipped_at_360dp | - |
| F3 | browser map | AC5-absence | - |
| F3 | android screen | AC15_never_measured_reads_not_recorded | - |
| F4 | browser map | AC6-aggregates | - |
| F4 | android screen | AC15_counters_state_their_set | - |
| F5 | browser map | AC7-one-bad-event | - |
| F5 | android screen | AC16_unreadable_queue_costs_only_itself | - |
| F6 | browser map | AC8-stale | - |
| F6 | android screen | AC17_stopped_reads_last_known | - |
| F7 | browser map | AC9-three-states | - |
| F7 | android screen | AC16_three_states_are_distinct | - |
| F8 | browser map | AC10-explanation | - |
| F8 | android screen | AC12_labels_stay_short, AC12_each_card_opens_its_explanation | - |
| F9 | browser map | AC11-reflow | - |
| F9 | android screen | AC12_nothing_clipped_at_360dp | - |
| F10 | browser map | AC2-contrast-light, AC2-contrast-dark, AC3-theme | - |
| F10 | android screen | AC13_platform_checks_pass_light, AC13_platform_checks_pass_dark | - |
| F11 | browser map | AC21-policy, AC22-egress | - |
| F11 | android screen | - | The Android client renders no browser surface: no WebView, no Custom Tab, no embedded HTML and no androidx.browser dependency. There is no document load for a policy to govern and no browser to report a violation against. `make verify-ui-record` scans the client for a web view and FAILS this exemption the moment one appears. |

## Notes on individual clauses

**F2, browser map.** Two assertions carry it rather than one: `AC1-keyboard` because it drives the
page with real keystrokes and reads `document.activeElement` back out of the engine, and
`AC21-policy` because it asserts the page still FUNCTIONS under its Content-Security-Policy -
Leaflet ran, the inline script ran, the stylesheet applied, a marker was drawn. A silenced page that
formally reported no violation would pass a source check and fails this one.

**F2, android screen.** Likewise: the platform accessibility sweep runs over a hierarchy the
emulator built, and the 360dp layout assertion reads Compose's laid-out geometry against its clipped
geometry, which is a fact about what was drawn and has no source-level equivalent.

**F9, android screen.** The Android surface is a single scrolling column; "phone first" there is the
360dp profile assertion, which forces the device to `1080x2340` at 480dpi (exactly 360dp of width)
and fails on any text clipped or laid out past the display.

**F10, browser map.** `AC3-theme` reads the background colours the engine painted for `body`, `#bar`
and `#panel` at each operating-system preference. It looks for no class and asks the page nothing:
"did it render light" is answered by the rendering.

**F10, android screen.** The client's colour scheme is authored per theme rather than derived from
the wallpaper. Dynamic colour (API 31+) made this screen's contrast a property of whatever picture
the user had set, which no check can grade - and F1 requires AA contrast, graded in both themes.

## MAP-VERIFICATION.md

`MAP-VERIFICATION.md` records a manual browser pass made when a headless-browser harness would have
been a new external dependency. Its rows are annotated with which of them are now machine-graded and
by what. **No row of it is evidence for F1, F2, F3, F4, F5, F6, F7, F9, F10 or F11 any more**: those
are graded by `make verify-ui` and by nothing else. What survives there is the record of what was
once observed, kept because deleting an observation is not the same as superseding it.
