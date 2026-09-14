# The terminal screen's visual register — design

- **Bead:** nocx-9bpeq.1 (T1 of epic nocx-9bpeq)
- **Status:** accepted by the owner in session, 2026-09-14; this document is its written form
- **Crosses:** ADR-0008 (blocks are a keyboard-first ledger, not cards), ADR-0012 ("what is deliberately
  still imperative"), ADR-0013 (tokens; §3.1 colour derivation measured and failed), ADR-0014 (per-primitive
  kit), AD-6 (the terminal's cells and ANSI palette are not touched here)
- **Evidence:** the owner's Warp screenshots; the concept mockups `output/imagegen/nocx-concepts-v2..v5`;
  screenshots of the dev-web stand running one scripted session in tokyo-night, light, solarized-light and
  nord plus a running command (2026-09-14)

## 1. The problem, measured

On the screen nocx is looked at all day, almost nothing is drawn by the kit.

- The block header and the composer are built from the `.nocx-chip` family — app CSS in `style.css:315-443`,
  hand-built in `scrollback/blocks.ts:640-870`, `editor.ts:325-395` and `grant.ts:38`. It is not a kit
  identity, so `surface-paints-kit` cannot see it, and `eslint.config.js:102-114` exempts those files from
  `no-inline-markup` and `no-raw-controls`.
- The folder mark is the emoji 📁 (`blocks.ts:773`, `editor.ts:347,619`); ⋮ × ✕ ⚠ are text glyphs used as
  icons (`blocks.ts:978`, `tab.tsx:312`, `grant.ts:184`, both notices, `banner.tsx:67`, `ui/secret-chip.ts:62`).
- Success is loud and failure is quiet: every successful block carries an `ok` pill; a failed block differs
  only by the word `exit 1` in red.
- The composer shows a clock that ticks every second, formatted with the WebView's default locale
  (`editor.ts:516-525`) — "пн, 14 сент. 21:04:04" in an English UI — and a permanent 3px accent bar that
  means nothing (it follows neither focus nor mode, `style.css:187`).
- In `light.css` the canvas (`#e6e8ed`) and the terminal background (`#ffffff`) differ — the only one of twelve
  themes where they do — so that theme has two candidate grounds for one screen. The chips paint
  `--color-surface-raised`, which in light is lighter than the ground: white boxes on grey.

Warp was the owner's reference, and the comparison is instructive in one way only: Warp is not more
restrained (its syntax colours are louder than ours); it is more consistent. It uses one cheap form per
element — muted text for metadata, one tint for a failure — where nocx uses five boxed ones.

## 2. The idea

**The composer is the next block of the ledger — the one that has not run yet.**

Every block and the composer share one anatomy: a meta row saying where, and a command row saying what. The
eye reads one column; the composer differs from the history only by carrying the caret. This is why the
composer is neither the boxed card every mockup drew (a card is what ADR-0008 decided blocks are not) nor
Warp's outlined chip (a second form for a fact the blocks already state as text).

## 3. Block anatomy

One anatomy for every block kind — a command and an assistant turn alike (`blockKindRules`, nocx-ex636).

```
  repos/nocx                                    84ms  ⋮      ← meta row (Meta, UI font, --font-size-2xs)
  ls frontend/src/ui | head -12                              ← command row (mono, --font-size-terminal)
  action-group.test.tsx                                      ← output
──────────────────────────────────────────────────────────── ← hairline, --color-divider
▌ repos/nocx                             72ms  exit 1  ⋮     ← failed: bar + --color-danger-surface
▌ go test ./internal/nosuchpkg
▌ FAIL ./internal/nosuchpkg [setup failed]
────────────────────────────────────────────────────────────
  dev@staging · srv/nocx                  ◌ 3s          ⋮     ← running: Spinner + elapsed
  sleep 20
```

### 3.1 Meta row

- **Left:** `host · cwd` as one `Meta` (§6.1), muted. The host appears only when the command ran somewhere
  other than this machine (today's location chip rule, nocx-6w4z). The cwd appears on **every** block — a block
  must read on its own when it is reached by search, by prev/next failure, or attached to a question.
  `cwdLabel` exists once (today it exists twice: `blocks.ts:714`, `editor.ts:615-619`).
- **A non-human author** keeps the kit `Badge` (tone `info`) before the location — the one badge in the row,
  because it is a category and not a fact about where.
- **Right, while running:** the kit `Spinner` (`size="sm"`) and the elapsed time, whole seconds
  (`formatRunningDuration`, unchanged). The ask kind's in-progress word (`rules.statusChips.inProgress`) is a
  `Meta` in the accent tone beside the same spinner — one shape for "in progress" (AD-8, as today).
- **Right, when finished:** the duration, then a status word **only when it carries information**:

  | Header status                        | Word                                 | Tone   | Row treatment                                |
  | ------------------------------------ | ------------------------------------ | ------ | -------------------------------------------- |
  | `success`, `settled`                 | none                                 | —      | none                                         |
  | `failure`                            | the kind's word (`exit N`, `failed`) | danger | failure (§3.3)                               |
  | `cancelled`                          | the kind's word                      | dim    | none                                         |
  | `unknown`, `unreconciled`, `entered` | the kind's word                      | dim    | none (their notices keep their own surfaces) |

  The duration keeps its column: tabular numerals and the width floor `.cmd-header-duration` has today move
  onto the `Meta` variance. Hovering the duration shows when the command started (`title`), formatted by
  `ui/format-time.ts` (§5.4) — the "when did this run" half of nocx-0tmq5 that belongs to the person.

- **⋮** is the kit `IconButton` (`size="xs"`) carrying `MoreIcon`, accessible name "Block actions". It keeps its
  box at all times, so nothing moves, and is visible when the block is hovered, selected, or has focus within;
  otherwise `opacity: 0`. Transparent is never unreachable: the button stays in the tab order, a selected
  block shows it, and T5's end-to-end opens its menu with the keyboard alone (ADR-0008). Its menu is the kit `ContextMenu`, mounted as a
  render island while open (§6.3); `buildOverflowMenu`'s own DOM and `.cmd-overflow-*` CSS go.

### 3.2 Command row

Unchanged in content and highlighting (`paintShellInto`, the reference-chip rule, the masked-command rule).
It stays in the mono family at `--font-size-terminal`.

### 3.3 The failed row

`data-outcome="failure"` on `.cmd-block` — set by the one place that settles a header (`settleHeaderRight`).
The row paints `--color-danger-surface` edge to edge and a 3px bar in `--color-danger` at its left edge,
inside the row's own padding (§4), so no text moves. Selection and hover on a failed row keep the bar and let
their own tint win the ground.

`--color-danger-surface` is a **per-theme token**, set in all twelve theme files. It is not a `color-mix` in
component CSS: ADR-0013 §3.1 measured derived state colours and they failed.

Only `failure` gets this. A cancelled command is something the person did; tinting it red would say the
command failed.

### 3.4 Between rows

A hairline in `--color-divider` separates rows (today `--color-surface-hover`, which in tokyo-night is darker
than the surface it is meant to stand out from). Hover and selection keep today's tints.

## 4. Rows are full width

The pane's inline gutter moves from `.pane { padding }` (`styles/base.css:464-465`) into the rows: each block,
the live terminal region and the composer carry `padding-inline: var(--pane-inline-padding)`. Only a
full-width row can paint a failure tint, a hover or a selection to the edge and place the bar at its left
without shifting the text.

The live region's content x, xterm's column count and the frozen block's first column must stay identical
before and after (nocx-dvf6k measures cell geometry to 0.5px). This is therefore not a CSS-only change: it is
T8, with its alignment test, and T6 (the header) depends on it.

`--pane-inline-padding` moves into `tokens.css` and becomes `var(--space-4)` (16px, from 10px). The scrollbar
stays flush with the pane's right edge (`style.css:790-815`).

## 5. The composer

```
────────────────────────────────────────────────────────────  ← hairline, --color-divider (as between rows)
  dev@staging · repos/nocx              gpt-5 ▾   2 blocks    ← meta row: Meta left, ghost Buttons right
  Run  git diff█                                              ← ModeIndicator as the prompt sigil, then CM6
```

### 5.1 Frame

A full-width row on `--terminal-background` with the hairline above it. No accent bar, no border, no card, no
submit arrow, no status line. `.nocx-editor`'s padding and row metrics become tokens; no `.nocx-editor*` rule
remains in `style.css`.

### 5.2 Meta row

- **Left:** the same `Meta` the block uses, in the same x position — `host · cwd` for where the pending command
  will run. When the session is remote, the host part uses the `strong` emphasis (normal text colour) instead
  of muted: in the composer it answers "where does Enter go", which is a safety question (nocx-4ff.35), while
  in a finished block it is history.
- **Right:** the controls that exist today — the model endpoint and model (nocx-rikz5), the attached-blocks
  control (grant, nocx-wcswn) and recovery — as the kit `Button` `variant="ghost"` `size="sm"`, mounted as
  render islands (§6.3). Their behaviour, popovers, disabled rung and accessible names are unchanged. The
  truncation they need today (`.nocx-editor-grant`, `.nocx-editor-model`: width floor, max width, ellipsis,
  full value in title) becomes a `data-truncate` variance on Button, not surface CSS.
- The row keeps a fixed height whether or not any control is showing, for the reason `style.css:282-290`
  records: a row that collapses moves the scrollback at the moment of submit (nocx-i4h04).

### 5.3 Command row and focus

`ModeIndicator` (Run/Ask) stays the prompt sigil in the CM6 gutter (ADR-0004 §3, `ask-entry.ts`), unchanged.

Focus is shown by the caret, which CM6 already draws only while focused, and by the meta row dimming one step
when the composer is not focused (`Meta` tone `dim`). There is no second focus indicator.

### 5.4 Time

The clock is removed: the chip, `startClock`/`stopClock`/`setTime`, and its one-second interval. The system
shows the present; the block keeps when a command ran (§3.1).

Wherever the kit formats a time of day it uses English and a 24-hour clock, never the WebView's default locale:
`ui/format-time.ts` `formatTimestamp` (`:96-98`, today `toLocaleString()`) is changed to that, which also fixes
its three existing callers.

### 5.5 The pet

The pet's default ledges (`pets/overlay.ts:86-90`) become `.tabbar` (bottom), `.pane.active .cmd-block` (top)
and `.pane.active .cmd-block .ui-meta` (top). The composer is not a ledge: the animal does not stand where the
caret is.

## 6. How imperative code gets the kit

ADR-0012 keeps `scrollback/` and `editor.ts` free of Solid for AD-6 reasons. That is a statement about the
framework, not about the kit's vocabulary, and the kit already has the answer: vanilla-emitted components
(`ui/mode-indicator.ts`, `ui/secret-chip.ts`) and Solid render islands (`terminal-content.ts:5600`).

### 6.1 A new primitive: `Meta`

`ui/meta.ts`, identity `ui-meta`, stylesheet `styles/components/meta.css`, a row in `ui/README.md`.

- Inline, one line, ellipsis at its own edge, UI font, `--font-size-2xs`, tabular numerals.
- Children are text parts joined by a separator element `ui-meta__sep` (`·`), so `host · cwd` is one
  element with one ellipsis.
- Variance: `data-tone` = `muted` (default) | `dim` | `danger` | `accent`; `data-emphasis="strong"` on a part
  (normal text colour); `data-column="duration"` for the tabular width floor.
- Emitted by `createMeta(parts, opts)`. No Solid version is written until a Solid surface needs one.

`Meta` replaces every `.nocx-chip` on the terminal screen. `.nocx-chip`, `.nocx-chip-muted/-ok/-fail` and their
CSS are deleted.

### 6.2 Vanilla emitters beside Solid components

For elements built once per block — potentially thousands in a long session — the kit gets vanilla emitters
beside the Solid components: `createBadge` (`ui/badge.ts`) and `createIconButton` (`ui/icon-button.ts`), both
producing the element the `.tsx` component produces and styled by the same stylesheet. The author mark stops
setting `ui-badge` by hand (`blocks.ts:750-754`).

Two emitters of one component can drift. **Each pair has a parity test**: for every variance, the vanilla
element and the Solid component's rendered element have the same tag, classes, `data-*` attributes, ARIA
attributes and child structure. A new variance added to one and not the other fails it.

Icons are already callable from imperative code — a `ui/icons` component called outside a root returns a
detached `SVGElement` (`terminal-content.ts:5609`, `workspace-menu.ts`) — so no icon emitter is needed.

### 6.3 Render islands

For pieces that are rare and long-lived: the composer's controls (one composer per pane), the running block's
`Spinner` (one running block per pane, disposed when the block settles), and the ⋮ `ContextMenu` (mounted on
open, disposed on close). Each island is disposed by the same owner that removes its host element.

## 7. Surfaces on the terminal screen

| Role      | Token                    | Where                                            |
| --------- | ------------------------ | ------------------------------------------------ |
| Ground    | `--terminal-background`  | history rows, live region, composer — one ground |
| Chrome    | `--color-chrome`         | tab strip, activity rail                         |
| Floating  | `--color-surface-raised` | menus and popovers only — never a chip in a row  |
| Failure   | `--color-danger-surface` | failed rows (§3.3)                               |
| Separator | `--color-divider`        | between rows, above the composer                 |

Every theme must satisfy, asserted in `theme-catalogue.test.ts` (T3):

1. The computed background of a block row, the live region and the composer equals `--terminal-background`.
   `light` is the one theme where `--color-canvas` and `--terminal-background` differ, so whichever of the two
   the history paints today, one ground there is wrong; the assertion is what finds out which.
2. `--color-chrome` differs from `--terminal-background` by ΔL\* ≥ 2 (CIELAB).
3. `--color-surface-raised` differs from `--terminal-background` by ΔL\* ≥ 3.
4. `--color-text` and `--color-danger` on `--color-danger-surface` reach 4.5:1, and `--color-danger-surface`
   differs from `--terminal-background` by ΔL\* ≥ 2.

A theme that fails is fixed by changing that theme's values. The thresholds are this spec's; changing one is
the owner's decision.

## 8. Tokens

Added to `tokens.css` (theme-invariant):

- `--icon-size-sm: 14px`, `--icon-size-md: 16px`, `--icon-size-lg: 20px`. `icon-button.css` sizes glyphs only
  through them; the activity rail uses `lg` (from 24px).
- `--pane-inline-padding: var(--space-4)` (moved from `base.css:464`).
- Block rhythm: the header's top padding becomes `var(--space-2)` (from 3px); the command-to-output gap stays
  `var(--space-3)`.

Added to every theme: `--color-danger-surface`.

The six `var(--radius-*)` / `var(--border-radius-*)` references to tokens that do not exist, and the integrity
gate's blind spot for a `var()` with a fallback, are T2's (filed there with file:line).

## 9. What was taken, and what was rejected

**Taken from Warp:** metadata as muted text; the failed row as a bar plus a tint across the whole block; an
open composer that continues the history rather than sitting in a box.

**Taken from the mockups:** success is silent; one anatomy for every block; no status bar; no send arrow; no
clock.

**Rejected:**

- **A pane header carrying host and directory** (v3–v5). The blocks and the composer already say where, and a
  header costs every pane a row of chrome.
- **A boxed composer** (every mockup). It is a card, and it makes the one row that is not yet history look
  like a different kind of thing.
- **`Input: …` recipient labels** (v3–v5). The caret already says where keys go.
- **Actions that exist only on hover.** ADR-0008 is keyboard-first; ⋮ is revealed by selection and focus too.
- **A mark for success** — a check or `ok`.
- **Warp's outlined cwd chip.** A second form for a fact the row states as text.
- **The attention model of v3–v5** (needs input, unread, review). A separate question with its own sources
  (nocx-jiwq, nocx-ms7v.4, nocx-6q1uh); not a visual register.

## 10. Out of scope

Tab strip layout and its active indicator (nocx-mjyvr, nocx-jv3q), what the activity bar contains
(nocx-708q.1), xterm cell rendering and the ANSI palette catalogue (nocx-tuhk, nocx-dvf6k), font family and
size settings (nocx-ybki), block lifecycle ownership (nocx-2v80t), composer attachments beyond restyling the
existing grant control (nocx-kflva).

## 11. How the children use this

| Task             | Takes from this spec                                                                                |
| ---------------- | --------------------------------------------------------------------------------------------------- |
| T2 tokens        | §8                                                                                                  |
| T3 surface roles | §7 — the four assertions, `--color-danger-surface` in all twelve themes                             |
| T4 kit           | §6 — `Meta`, `createBadge`, `createIconButton`, parity tests; `.nocx-chip` deleted; pet ledges §5.5 |
| T5 glyphs        | §3.1 ⋮ and `ContextMenu`; every × ✕ ⚠ to kit icons                                                  |
| T6 block header  | §3                                                                                                  |
| T7 composer      | §5                                                                                                  |
| T8 density       | §4 and the icon sizes of §8                                                                         |
| T9 end-to-end    | §3.1 status table, §3.3, §5.4, §7 assertion 1                                                       |
| T10 gates        | §1 — the exemption narrowed once T4–T8 are at zero                                                  |
