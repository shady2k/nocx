# Editor core on CodeMirror 6 — Design

- **Date:** 2026-07-25
- **Status:** Draft — awaiting owner review
- **Session bead:** `nocx-lm7` (Brainstorming: editor core — textarea vs CodeMirror 6)
- **Epic:** `nocx-2gf`
- **Binding contracts:** [ADR-0004](../../docs/decisions/0004-input-ownership-and-editor-abstraction.md)
  (input ownership + pluggable editor), [ADR-0006](../../docs/decisions/0006-marker-only-prompt-mode.md)
  (marker-only prompt), AD-6 (single-owner state), AD-8 (interface-first + DI).

## Context

`frontend/src/editor.ts` is a passive `<textarea>` chosen deliberately in ADR-0004 §3:
it already handles mouse caret placement, selection, multiline, IME, clipboard and
native undo, and the ADR recorded the exit condition in the same sentence —
_"CodeMirror is introduced only when syntax-aware editing or inline widgets justify
it. Avoid `contenteditable`."_

That condition has now been met three times over. The editor is the foundation of
three separate epics, and each wants something a `<textarea>` structurally cannot give:

| Consumer                            | Needs                                                              |
| ----------------------------------- | ------------------------------------------------------------------ |
| `nocx-w7h` — semantic command line  | token colours, async decorations, a popup anchored under the caret |
| `nocx-dw3` — agent mode             | prose editing plus inline mentions of blocks and files             |
| `nocx-4ff.6` — history + completion | a completion popup positioned at a character offset                |

A `<textarea>` renders one uniform colour and exposes no character coordinates. Both
gaps have the same root cause and the same fix: the input surface and the render
surface must be separate layers. That is precisely what CodeMirror 6 is, and building
it by hand — a transparent textarea over a mirrored `<div>` — means hand-rolling caret
measurement and popup positioning, then discarding that work when inline widgets
arrive.

**Measured, not assumed:** CM6 (`state` + `view` + `autocomplete` + `commands` +
`legacy-modes` shell) bundles to **321 KB raw / 102 KB gzip**, against xterm.js at
289 KB / 65 KB and a current total app bundle of 594 KB raw. For a desktop bundle
loaded from disk this is not a constraint.

Reproduce: install `codemirror@6.0.2`, `@codemirror/autocomplete@6.20.3`,
`@codemirror/legacy-modes@6.5.3`, `@codemirror/{state,view,language,commands}` into an
empty package, bundle an entry importing `EditorState`, `Prec`, `EditorView`, `keymap`,
`placeholder`, `drawSelection`, `autocompletion`, `history`, `defaultKeymap`,
`historyKeymap`, `StreamLanguage` and the `shell` mode with
`esbuild --bundle --minify --format=esm`, then `gzip -9`. **W1 pins the exact versions
in `frontend/package.json`; the numbers above are a floor, not a contract** — they omit
whatever the real integration adds.

**Blast radius is small, but larger than the runtime import graph suggests.**
`CommandEditor` has one production importer (`tabs.ts`), but the _observable_ contract
has four consumers, and three of them couple to the widget being a textarea:

| Consumer                                                                 | Couples to                                                                                                            |
| ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| `frontend/src/tabs.ts`                                                   | the public API, plus `.textarea` at `:480-491`                                                                        |
| `frontend/src/editor.test.ts`                                            | `querySelector('textarea')`, `.value`, `.rows`, `.selectionStart/End`                                                 |
| `e2e/command-editor.spec.ts`, `clipboard.spec.ts`, `click-focus.spec.ts` | `.nocx-editor-input` supporting `fill()`, `toHaveValue()`, selection offsets; `elementFromPoint` returning `TEXTAREA` |
| `frontend/src/style.css:658-731`                                         | textarea-specific properties (`resize`, wrapping) on `.nocx-editor-input`                                             |

So "remove the `textarea` getter and consumers are decoupled" is false: the unit test
and the e2e suite are rewritten as part of this work, not after it. That is scoped
below, not discovered later.

## Goal

Replace the internals of `CommandEditor` with CodeMirror 6 while preserving its public
API, so `tabs.ts`, the input-ownership state machine, the command ledger, the DOM
blocks and the atomic-submit handoff are untouched.

## Scope

**In:** W1–W7 below — the behaviour-preserving swap plus the seams the three dependent
epics need.

**Out (tracked, not cut):**

- Syntax highlighting, validation, ghost text, completion — `nocx-w7h`.
- Agent mode and its rendering — `nocx-dw3`.
- History storage and persistence — `nocx-4ff.6`.
- Vim keybindings, folding, multi-cursor, structural selection — not requested; YAGNI.

**Behaviour-preserving by contract.** No user-visible feature lands in this epic. Done
means the vitest and e2e suites are green and the editor behaves as it does today, with
three named exceptions:

- **Tests are rewritten, not merely kept green.** `editor.test.ts` and the e2e specs
  assert against textarea internals; they are re-expressed against the public API and
  user-visible behaviour. Per the repo's TDD rule they are rewritten _first_, so each
  work item goes red before it goes green.
- **`e2e/command-editor.spec.ts:61-68` is already failing** — it clicks
  `.nocx-editor-submit`, an element commit `7204aff` removed. It is dead independent of
  this work (`nocx-m7x`) and is deleted, not ported.
- **Two behaviours do change**, both admitted below rather than hidden by the word
  "preserving": wrapped long lines now grow the box, and undo moves from native
  textarea history to CM6's. Both are user-visible and both are recorded in ADR-0010.

The constraint exists because the epic blocks three others: if it grows features, it
slips, and nothing downstream ships.

## Decision

### 1. CodeMirror 6 as the editor core

The visible surface becomes a CM6 `EditorView` mounted inside the existing
`.nocx-editor` card. The editor chrome (cwd chip, time chip) stays as plain DOM
siblings — it is not part of the input surface and moving it into a CM6 panel would
buy nothing.

### 2. The public API is the contract

| Member                                                                                                | Fate                                                  |
| ----------------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| `root`, `mount`, `setCwd`, `setTime`, `show`, `hide`, `focus`, `isVisible`, `rootContains`, `dispose` | unchanged signature and semantics                     |
| `insertText(text)`                                                                                    | unchanged signature; implemented as a CM6 transaction |
| `textarea` getter                                                                                     | **removed** — replaced by `onSelectionEnd(cb)`        |

`rootContains()` keeps working unmodified because CM6's `contentDOM` lives inside
`root`, which is what the focus-bounce in `tabs.ts:407-417` tests against.

### 3. Auto-grow moves from arithmetic to layout

`_grow()` counts `\n` and sets `rows` (1..`MAX_ROWS` = 10). CM6 grows with its content,
so the policy becomes CSS: `max-height` equal to ten lines, `overflow-y: auto` past it.
This is a behaviour _match_, not a behaviour change — but it is the one place where
wrapped long lines will now grow the box where previously they did not, so it is
called out rather than discovered.

### 4. Decoration sets belong to the active `InputTarget`, not to the editor

This is the one forward-looking addition, and it is deliberate. Three consumers want
three different render behaviours on the same surface: shell syntax, agent prose with
mentions, and async semantic decorations. If the editor owns the decoration set, each
new mode edits the editor — exactly what ADR-0004 §3 forbids ("New capabilities are
added by registering a target, never by editing the editor").

**Correction (2026-07-25 review).** An earlier draft of this spec claimed `InputTarget`
"already declares `complete?()` and `history?()`". It does not. ADR-0004 §3 _describes_
that richer interface, but `frontend/src/input-target.ts:9-13` implements only:

```ts
export interface InputTarget {
  readonly id: string
  readonly label: string
  submit(doc: string, ctx: SubmitContext): Promise<void>
}
```

The ADR text was mistaken for the code. This matters, because the cheap-seam argument
rested on it: adding `editorExtensions?()` is not widening an interface that already
carries optional capabilities — it is the first optional member, and it makes every
target compile-time dependent on the editor engine.

**Therefore `editorExtensions?()` is NOT introduced in this epic.** It is deferred to
`nocx-w7h`, which is the first consumer that actually needs it and can therefore design
its shape against a real requirement. Three reasons:

1. AGENTS.md forbids speculative features. With the premise corrected, this is one —
   a member no registered target would populate.
2. A CM6 `Extension` is far broader than a decoration set: it can install keymaps,
   state fields, transaction filters, event handlers and themes. Handing that to an
   arbitrary target lets a target override the W2 key invariants. Making it safe needs
   an allow-list or compartment boundary and a precedence rule — design work that
   belongs with its first real use, not ahead of it.
3. `tabs.ts` does not use `InputTargetRegistry` at all: it constructs a concrete
   `ShellInputTarget` (`tabs.ts:297`) and calls it directly, and the registry has no
   change notification (`input-target.ts:47-63`). "The editor reconfigures on
   `setActive()`" would therefore require a new registry contract and composition-root
   wiring — a second epic's worth of work smuggled into a behaviour-preserving one.

What this epic owes the future is narrower and real: **the editor must not hard-code its
language or decoration set into `CommandEditor`'s constructor.** Keeping the extension
list a constructor parameter costs nothing, presumes no interface, and is the actual
thing that makes W6 cheap later.

## Invariants that must survive

Each of these has already cost a debugging session. They are acceptance criteria, not
reminders.

1. **Submit path — CONTESTED, resolve before W1.** The recorded lesson
   (`lesson-nocx-input-editor-do-not-hand-roll`) says route through `term.paste()`,
   which wraps only when the shell enabled mode 2004; `tabs.ts:295-296` claims we
   already do. Neither is true: `input-target.ts:33-34,42` hand-rolls
   `ESC[200~ … ESC[201~` and its own comment (`:30-32`) calls the resulting leak
   acceptable. Three sources, at most one correct — tracked as `nocx-hi2`. The
   invariant this epic can actually hold is weaker and testable: **whatever the submit
   path is on the day W1 lands, it is unchanged by W1.** Fixing `nocx-hi2` is a
   separate change with its own test, before or after, never inside the swap.
2. **Atomic handoff ordering** — the editor is hidden **before** anything is sent
   (ADR-0004 §2), so the shell paints the committed command once. Note the current code
   clears first and hides second (`editor.ts:92-95`: value → rows → `hide()` → `submit()`);
   the ADR constrains only hide-before-send, and W1 preserves the observed order rather
   than a paraphrase of it.
3. **The editor card wins hit-testing over `.xterm-link-layer`** (z-index 2) — otherwise
   the mouse dies inside the editor (`nocx-4ff.16`). Recorded as
   `.nocx-editor { z-index: 20 }`, but that rule is **not in the stylesheet**
   (`style.css:658` has no z-index; only comments at `:1086-1087` reference it). What
   actually stacks the editor today is unknown — `nocx-0oc`. W1 must determine it,
   because a CM6 swap changes the element tree underneath and could disturb whatever
   accident is currently holding.
4. **Fail-open** — nothing in this epic may create a state where the editor is shown
   while the shell is not at a clean prompt, or where the user is trapped in an
   invisible prompt (ADR-0004 §1, ADR-0006 §5).
5. **Ctrl-C with a real selection still copies**; with no selection it clears the draft
   and interrupts the shell (`editor.ts:113-119`).

## Work items

### W0 — _(removed)_

The de-risk spike is gone. Its deliverable was a findings note rather than working
software, and it meant doing the integration twice — while deciding nothing that is not
already decided, since CM6 is settled and W7 records it. **W1 behind a flag is the same
experiment with the code kept**, and the seams the spike would have poked by hand are
better expressed as things a test asserts.

Its six checks became acceptance criteria on W1, verified _first_, before the rest of the
swap is polished: focus interplay with xterm and both `rootContains` call sites; keymap
precedence; IME composition; hit-testing over the editor card; geometry under
`visibility:hidden`; and reproduction on WKWebView rather than only Chromium.

If one of them turns out to demand a different migration shape, that is a finding on the
epic and W1 is re-planned — the same outcome a spike would have produced, with working
code already in hand.

### W1 — Swap the editor internals

- **Change:** Replace the `<textarea>` in `CommandEditor` with an `EditorView`. Keep
  every public member per the table above; drop `_grow()` in favour of the CSS policy.
  Pin the CM6 package versions in `frontend/package.json`. The extension list is a
  **constructor parameter**, not a literal inside the class — this is the whole of what
  this epic owes the later per-target decoration work.
- **First, rewrite `editor.test.ts`** against the public API: no `querySelector('textarea')`,
  no `.value`/`.rows`/`.selectionStart` pokes. It goes red, then W1 makes it green.
- **Acceptance:** the rewritten unit suite is green; the editor renders, accepts typing,
  places the caret on click, edits mid-line and handles multiline as before; the submit
  path is byte-identical to before the swap (see invariant 1); `show()`/`hide()` keep the
  `_shownOnce` layout reservation — first hide uses `display:none`, every later hide uses
  `visibility:hidden` so the flex box keeps its height and the pane does not jump.
- **Explicitly verify under `visibility:hidden`:** CM6 measures its own layout, and a
  hidden view can cache wrong geometry. Assert the editor is correctly sized and
  focusable on the show that follows a hide.

### W2 — Keymap at highest precedence

- **Change:** Bind Enter (submit), Shift-Enter (newline), Escape (clear draft without
  interrupting), Ctrl-C (copy if a selection exists, else clear + `actions.cancel()`)
  inside `Prec.highest(keymap.of([...]))`.
- **Why:** CM6's `defaultKeymap` binds Enter and Escape. Without explicit precedence
  Enter inserts a newline and Ctrl-C stops interrupting the shell — a silent regression
  in the two most-used keys at a prompt.
- **Acceptance:** unit tests assert each binding's decision, including the Ctrl-C
  selection branch; an e2e asserts one submit produces exactly one block.

### W3 — Selection and copy-on-select seam

- **Change:** Remove the `textarea` getter. Add `onSelectionEnd(cb: (text: string) => void)`,
  which fires with the selected text when a selection gesture completes — it does not
  copy anything itself. Rewrite `tabs.ts:480-491` to register a callback that applies
  the existing `shouldCopy` policy and the clipboard write.
- **Why:** the DOM mechanics of "a selection finished" belong inside the editor; the
  policy of "should this be copied" belongs outside it.
- **Acceptance:** selecting text in the editor copies under the same `shouldCopy` rules
  as today; no consumer reaches into the editor's DOM.

### W4 — Reconcile `insertText` with the focus-bounce

- **Change:** Make the two redirect paths consistent. `tabs.ts:441-442` (block selected)
  focuses **and** inserts the character; `tabs.ts:466` (focus drifted) only focuses and
  comments that it "lets the key event propagate normally".
- **Diagnose before fixing — the failure mode is not yet established.** Focusing an
  element inside a bubbling `keydown` handler does not retarget the event that is
  already in flight, so the drift path plausibly _loses_ the first character rather
  than doubling it; the earlier draft of this spec asserted double-insertion without
  evidence. Which one actually happens is browser- and engine-dependent, so W4 starts
  by writing the test and observing, on both the textarea (before W1) and CM6 (after).
- **Acceptance:** with a block selected, and again with focus drifted to `<body>`,
  typing one printable character puts exactly one character in the document — and the
  test exists for both engines so the migration cannot silently change the answer.

### W5 — Rebuild the test surface

- **Change:** Rewrite the hit-testing assertions in `e2e/command-editor.spec.ts` that
  expect `elementFromPoint` to return a `TEXTAREA`. Add an IME composition e2e case.
  Decide vitest browser mode (`nocx-foz`).
- **Why:** jsdom performs no layout, so every CM6 behaviour that depends on measurement
  — caret coordinates, popup placement, wrapping — is untestable there. The
  hit-testing bug in `nocx-4ff.16` is exactly the class of defect that hides without a
  real browser, and IME has **zero** coverage today: the document-level keydown
  redirect at `tabs.ts:432` can destroy composition state silently.
- **Acceptance:** the e2e suite is green against the new surface; a composition
  sequence produces the composed text once; `nocx-foz` is closed with a decision either
  way, recorded with its reasoning.

### W6 — _(removed)_

The per-target extension seam moved to `nocx-w7h`. Rationale in the Decision section:
the premise that `InputTarget` already carried optional members was wrong, `tabs.ts`
does not use the registry, the registry has no change notification, and an unconstrained
CM6 `Extension` can override the W2 key invariants. What remains here is the part with
no interface cost — W1's constructor-parameter extension list.

### W7 — ADR-0010 _(runs FIRST, before W1)_

- **Change:** Write `docs/decisions/0010-*.md`: editor core is CodeMirror 6; it revises
  ADR-0004 §3. Record that the `contenteditable` ban targeted the **hand-rolled**
  variant and remains in force for it — CM6 is the mitigation for the very IME, undo
  and selection problems that motivated the ban. Record the measured bundle cost, the
  two admitted behaviour changes (wrapped-line growth, undo semantics), and that
  per-target decoration ownership is deferred to `nocx-w7h`.
- **Why first:** ADR-0004 §3 is _accepted_ and says avoid `contenteditable`. Writing
  W1–W5 against it and documenting afterwards means every intermediate commit
  contradicts an adopted decision with nothing authorizing the change. The ADR is the
  authorization, so it precedes the code (AGENTS.md: "if an AD is wrong, change it
  deliberately rather than routing around it").
- **Acceptance:** ADR merged and ADR-0004 §3 annotated as revised, so a future reader
  cannot follow the old sentence into a hand-rolled overlay.

## Testing

- **Unit (vitest):** keymap decisions including the Ctrl-C selection branch; the
  `insertText` transaction; the auto-grow policy; draft and selection surviving a
  hide→show cycle. _(An earlier draft also listed "target reconfiguration" here — a
  leftover from the removed W6. There is one target in this epic; nothing reconfigures.)_
- **e2e (Playwright):** click-to-place-caret, mid-line edit, multiline, submit paints
  the command exactly once, copy-on-select, hit-testing over the editor card, and IME
  composition.
- **Static:** `tsc --noEmit` — analysis, not a test.
- **Gate:** the pre-commit hook (`gofumpt`, `golangci-lint`, `go test -race`, prettier,
  eslint, `tsc`, vitest) plus CI on the PR.

## Risks

| Risk                                                                                                                                                                                                                                    | Mitigation                                                                                                                                                                                                            |
| --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Focus interplay between CM6 and xterm; the ownership machine is focus-based                                                                                                                                                             | first acceptance criterion on W1, verified before the rest of the swap is polished                                                                                                                                    |
| CM6's `defaultKeymap` shadows Enter / Escape / Ctrl-C                                                                                                                                                                                   | W2, `Prec.highest`, covered by unit tests                                                                                                                                                                             |
| IME composition broken by the document-level keydown redirect                                                                                                                                                                           | W5 adds the first IME coverage this repo has had                                                                                                                                                                      |
| jsdom cannot test CM6 view behaviour                                                                                                                                                                                                    | W5 forces the `nocx-foz` decision instead of leaving it implicit                                                                                                                                                      |
| `insertText` double-inserts via the two redirect paths                                                                                                                                                                                  | W4                                                                                                                                                                                                                    |
| Scope creep turns a refactor into a feature epic and blocks three others                                                                                                                                                                | behaviour-preserving is an acceptance criterion, not an intention                                                                                                                                                     |
| Undo semantics shift from native to CM6's history                                                                                                                                                                                       | accepted and documented in ADR-0010; visible but minor                                                                                                                                                                |
| wterm renderer has **no** read-only: `renderers/wterm.ts:114` is a documented no-op ("@wterm/dom has no disableStdin"), so under wterm the prompt relies on focus alone while `tabs.ts:380-394` assumes `setReadOnly(true)` took effect | pre-existing asymmetry, not caused by this epic, but W1 must establish what stops wterm forwarding keystrokes while the editor owns input — otherwise "parity" is unverifiable and the claim is dropped from the epic |
| CM6 measures its own layout; the editor spends most of its life under `visibility:hidden` (`_shownOnce`)                                                                                                                                | explicit W1 acceptance criterion on size and focusability across a hide→show cycle                                                                                                                                    |
| Enter / Escape / Ctrl-C firing during IME composition                                                                                                                                                                                   | W2 handlers must ignore composing events; W5 asserts it                                                                                                                                                               |

## Build order and bead mapping

`W7 (ADR-0010) → W1 → W6 → W2 → W3 → W4 → W5`

W7 first because the ADR authorizes the migration and must exist before any code
contradicts ADR-0004 §3. Then W1, whose acceptance criteria carry the risky seams the
removed spike was meant to probe. W6 lands immediately after so the migration ships one
visible improvement rather than none. Then the remaining items, each rewriting its own
tests first per the TDD rule. W5 last
collects what only a real browser can assert — it is not a cleanup step, it owns the
IME coverage this repo has never had.

W6 was removed; the seam moves to `nocx-w7h`.

**Resolve before W1 starts:** `nocx-hi2` (the contested submit path). Not necessarily
_fixed_ first — but the epic must know which of the three contradicting sources is
correct, because "the submit path is unchanged by W1" is meaningless until we know what
it currently is.

All work items are children of epic `nocx-2gf`. Discovered defects filed separately:
`nocx-hi2` (bracketed paste), `nocx-0oc` (missing z-index), `nocx-m7x` (dead e2e).
Downstream: `nocx-w7h` and `nocx-dw3` unblock on this epic; `nocx-foz` is resolved
inside W5.

## References

- ADR-0004 §3 — the `<textarea>` choice and its stated exit condition.
- ADR-0006 — marker-only prompt mode.
- `.internal/specs/2026-07-24-warp-editable-command-input-design.md` — the design that
  built the current editor.
- Memory `lesson-nocx-input-editor-do-not-hand-roll` — the `term.paste()` invariant.
- Epic `nocx-2gf`; session bead `nocx-lm7`.
