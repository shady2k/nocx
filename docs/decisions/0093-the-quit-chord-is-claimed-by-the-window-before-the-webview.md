# ADR-0093 — The quit chord is claimed by the window, before the webview

- **Status:** Accepted
- **Date:** 2026-10-19
- **Decided by:** the worker on `nocx-1ebrs.13`, from the owner's v0.6.0 clean-Mac pass
  (`nocx-1ebrs.4`) and the narrowing recorded on the bead the same day.
- **Related:** AD-3 (the shell is a thin, swappable host), AD-8 (one owner per behaviour),
  [ADR-0092](0092-a-dom-seat-is-not-an-input-owner.md) (the same question asked about a DOM
  seat, answered by keeping the input where the gesture is decided), and AGENTS.md's
  _two surfaces may never own the same input_.

## Context

On the shipped build, choosing **nocx > Quit nocx** ended the process and pressing **Cmd+Q**
with the command editor focused did nothing. A menu item that works while its shortcut does
not is not a menu problem: the item was reached by a click and the chord was not.

The bead's first reading was that nocx defined no menu, so the item in the menu bar was
AppKit's synthesised application menu and only its key equivalent could route the chord. The
first half is true and the second is not, and the difference is what makes this a decision
rather than a patch:

- `main.go` passes no menu to `application.Options` — and Wails v3.0.0-beta.9's
  `application.Options` has **no `Menu` field at all**, so there was nothing to pass.
- Wails nonetheless installs a main menu on macOS. `App.Run` calls
  `impl.setApplicationMenu(a.applicationMenu)`, and `macosApp.setApplicationMenu` builds
  `DefaultApplicationMenu()` when that is nil. Its first item is the application menu, whose
  `Quit` is a **role** item: selector `terminate:`, label "Quit nocx", accelerator
  `CmdOrCtrl+q`. So the chord the owner pressed was already in the menu bar, with an
  accelerator, and it still did not quit.

What that rules out is worth stating: writing our own Quit item with the same accelerator is
**not** what makes Cmd+Q work, because an item with exactly that accelerator already existed.
What it was missing is an owner — a label, a chord and an action this application chose and
can test — and that is worth having, but it is the second half of this decision.

What makes the chord work is the window. `-[WebviewWindow performKeyEquivalent:]` is invoked
by Cocoa for every modifier-key combination **before the responder chain runs**, and Wails
consults the window's own key bindings there, returning YES when one matches. Its own source
says why that hook exists: "accelerators the WKWebView would otherwise swallow before
NSWindow's keyDown: is ever reached". A chord lost to a WKWebView in the key window is
exactly that, and it is the only mechanism the tree offers that runs before the page does.

## Decision

**1. A quit is decided in one place.** `quitSeam.request` is the only function that decides
nocx is quitting; it logs which key asked and calls the application's own `Quit`. The menu
item and the chord are two keys into it — never two behaviours.

**2. The chord is claimed by the window.** `WebviewWindowOptions.KeyBindings` carries
`cmd+q` and ends in that same decision. It is claimed before the responder chain, so it does
not matter what holds the keyboard: the pane's input element, the command editor, a sidebar
row, or nothing at all. **Focus is therefore not a variable in this behaviour**, which is the
property the defect could not have had.

**3. The chord is spelled the way the window spells it.** Wails matches a registered binding
by re-parsing it and comparing against the accelerator it parses out of the NSEvent, and the
darwin window builds that string itself, lower-case, as `cmd+q`. Both spellings canonicalise
to `Cmd+Q`; the constant uses the event's own spelling so there is one string and not two.

**4. The application menu is nocx's, and the rest of the bar is the platform's.** On macOS
the menu is set from `main.go`: an application menu written here — About, Services, Hide,
Hide Others, Show All, and **Quit nocx** as a plain item carrying the same chord and ending
in the same decision — followed by Wails' own File, Edit, View, Window and Help. The
application menu is written here because its Quit is the item this defect is about: Wails'
version is a role item whose click AppKit sends as `terminate:`, so neither its label nor
its action is a decision this process can log, test or change. The other five menus are
left alone because they answer questions Wails already answers, and a second copy of them
would be a second answer.

**5. Off macOS the chord is not claimed.** `Ctrl+Q` is XON — a key a person sends to the
PROGRAM in the pane to resume output it paused — and no non-macOS window here shows the menu
bar that would advertise it. Claiming it would take a key away from the terminal to buy a
quit that platform already reaches another way.

**6. A quit asks the daemon nothing.** Sessions, including one whose shell integration never
sent its hello, live in `nocx-server`. This window holds no session state, so no session can
hold a quit open; the only thing a quit runs in this process is `WailsApp.shutdown`, which
logs.

## Consequences

The shell now owns one native behaviour it can test without a Mac: the decision behind the
chord, driven through the binding the window installs, and the decision behind the item,
built by the same constants. Neither test presses a key on macOS, and neither simulates
focus — the claim above is that focus cannot change the answer, so a test that faked a
focused element would be asserting a mechanism that no longer decides anything.

**What is not proven by any check in this repository is that AppKit reaches the window's
`performKeyEquivalent:` before the WKWebView consumes the chord on a real machine.** That
ordering is Wails' own documented intent for the hook, not something a Linux CI run can
observe. If Cmd+Q still does nothing on a Mac after this, the remaining lever is a keydown
handler in the renderer that calls the same decision — and it must then be the ONLY owner,
because a renderer handler beside a window binding is precisely the two-surfaces-one-input
defect this record exists to avoid. The renderer claims no `meta+q` handler today (`grep
metaKey` across `frontend/src` finds the save, copy, paste, sidebar, snippet and ask chords,
and none for `q`) and must not grow one while the window holds the chord.

The next person inheriting this should read decision 3 before adding any other accelerator:
the string registered here is compared against an event spelling built elsewhere, and a
second convention for the same chord is the failure mode this record was written against.
