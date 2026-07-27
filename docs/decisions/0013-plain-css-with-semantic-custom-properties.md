# ADR-0013 — Styling architecture: plain CSS with semantic custom properties

- **Status:** Accepted
- **Date:** 2026-07-27
- **Related:** ADR-0012 (SolidJS as the application UI layer), ADR-0005 (WebKitGTK refresh
  pump), design spec `2026-07-27-ui-shell-kit-and-theming-design.md` §5 (styling and
  theming), beads `nocx-xrrl` (tokens), `nocx-8yg.6` (themes)
- **Fills a gap:** no prior ADR covers the CSS architecture. ADR-0012 settled the
  framework and the xterm boundary; the styling decisions needed to implement either
  were deferred here.

## Context

Themes are the next feature (`nocx-8yg.6`) and the current CSS cannot support them.
Measured on `main`:

- **232 hex colour literals**, `27 unique` values — verified with
  `grep -coP '#[0-9a-fA-F]{3,8}' frontend/src/style.css` and
  `grep -oP '#[0-9a-fA-F]{3,8}' frontend/src/style.css | sort -u | wc -l`.
- **22 `rgba()` calls** adding 18 more unique colour variants to the surface palette
  (translucent accent tints at 6%, 9%, 10%, 12%, 18%, 25% — no two call sites agree on
  opacity).
- **Five CSS custom properties declared**, of which only two are design tokens:
  `--nocx-ui` and `--nocx-mono` (both fonts, at `style.css:9-11`). The other three are
  local or platform properties: `--tab-bg` (gradient-coordination variable inside `.tab`
  rules), `--wails-draggable` (platform drag-region annotation, 19 sites),
  `--default-contextmenu` (platform context-menu annotation, 1 site).
- **Two custom properties used through `var()`** — `--nocx-mono` and `--tab-bg` — and
  `--tab-bg` is a local variable, not a global token. The font token is the only shared
  design value the entire stylesheet derives from a variable.
- **One token name that doesn't exist in var() at all:** `--nocx-ui` is declared at
  `:root` but never referenced. It is dead CSS.

### What a second theme cannot do today

Adding a theme file and switching it with `data-theme` would produce a broken UI because
**every colour value is hardcoded.** Examples from the file:

| Selector                            | Property            | Hardcoded | Would-be token             |
| ----------------------------------- | ------------------- | --------- | -------------------------- |
| `html, body`                        | `background`        | `#1a1b26` | `--color-canvas`           |
| `.tab.active`                       | `background`        | `#1f2235` | `--color-surface`          |
| `.tab:hover`                        | `background`        | `#1f2030` | `--color-surface-hover`    |
| `#activitybar`                      | `background`        | `#16161e` | `--color-canvas`           |
| `.cm-item-group-header`             | `color`             | `#9d7cd8` | `--color-accent-secondary` |
| `.update-notice.downloading`        | `border-left-color` | `#e0af68` | `--color-warning`          |
| `.cm-form-actions button.cm-danger` | `background`        | `#f7768e` | `--color-danger`           |
| `.cm-form-error`                    | `color`             | `#ff6b6b` | `--color-error`            |

A theme switch would need to override ~195 selector blocks (every one that contains a
hardcoded `#hex` or `rgba()` value). The selector specificity jungle and hardcoded
entanglements mean a theme file would also need to replicate 2,285 lines of rules,
not supply 30 token assignments. That is not a theme system; it is a fork.

### Requirements

The owner's stated requirement, recorded in the design spec: **all CSS in one place,
driven by variables, because themes come next.** Reference applications named: Warp,
Orca (Tailwind v4 plus shadcn/ui, tokens as CSS variables in
`src/renderer/src/assets/main.css`) and Tabby (SCSS with
`tabby-core/src/theme.vars.scss`). From Orca the relevant structure is the token
layer — semantic variables with explicit theme assignment — not the utility framework
around it.

## Decision

**Plain CSS with semantic custom properties, organised as a `styles/` directory. No
Tailwind, no CSS Modules.**

### 1. Directory structure

```
frontend/src/styles/
├── tokens.css          the vocabulary — names, and derivations that do not vary by theme
├── themes/
│   ├── tokyo-night.css  (the current palette, extracted from the 27 hex values)
│   └── …                further themes are new files, nothing else
├── base.css            reset, typography, shell layout
├── components/         one file per kit component (Button, TextField, Dialog, …)
└── surfaces/           genuinely non-reusable page/view rules (settings, export, …)
```

A theme is a file assigning values to token names, selected by `data-theme` on the root.
Switching `data-theme` replaces every token value with no selector overrides. The file
`tokens.css` is the only file that references colour literals; any colour literal outside
it is a violation of the guard (§4).

### 2. Token layers — exhaustive

#### 2.1 UI colour tokens

Nine base semantic colours, derived from the 27 unique hex values in the current
stylesheet. The design spec (§5.1) lists eight; this expands to nine because the
existing code uses four distinct semantic roles the spec's eight miss (§2.2):

| Token                    | Role                                        | Current value (Tokyo Night) | Usage                                                                                                                                                                                                                                                                                                           |
| ------------------------ | ------------------------------------------- | --------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--color-canvas`         | Deepest page / terminal background          | `#1a1b26`                   | `html,body`, `#app` terminal area, `.tabbar`, `.tabstrip-vertical`, `.clipboard-banner`, `.update-notice`, `.pane-manager` (9 selectors)                                                                                                                                                                        |
| `--color-surface`        | Default component surface                   | `#1f2335`                   | `.tab.active`, `.cm-body`, `#sidebar`, `.st-rail`, `.cm-item.active`, `.st-content`, `.mode-card`, `.st-backup`, `.st-loading`, block containers, tab hover gradient base (16 selectors)                                                                                                                        |
| `--color-surface-hover`  | Surface hover state                         | `#1f2030`                   | `.tab:hover`, `.tabstrip-vertical .tab:hover`, `.tab-add:hover`, `.cm-item:hover`, `.cm-list-empty`, `.mode-card:hover` (7 selectors)                                                                                                                                                                           |
| `--color-surface-raised` | Elevated surface (inputs, chips, dropdowns) | `#292e42`                   | `.chip`, `.pane-manager`, `select`, `input`, `.st-search`, `.block-toolbar`, `.block-overflow-menu`, `.mode-card`, `.mode-card header`, `.st-backup-header`, `.st-import-section` (14 selectors)                                                                                                                |
| `--color-text`           | Primary body text                           | `#c0caf5`                   | Primary text everywhere (27 selectors — tabs, buttons, sidebar, chips, settings, export, blocks)                                                                                                                                                                                                                |
| `--color-text-muted`     | Secondary / label text                      | `#a9b1d6`                   | `.cm-header h1`, `.tab`, `.cm-item-info`, `.tabstrip-vertical .tab`, `.tab-close`, `.cm-item-meta`, `.chip`, `.block-meta`, `.st-section-title`, `.st-loading`, `.st-search-counter`, `.st-mode-card-subtitle`, `.st-card-actions`, `.st-status-text` (24 selectors)                                            |
| `--color-text-dim`       | Disabled / placeholder / tertiary text      | `#565f89`                   | `.cm-list-empty`, `.tab-close`, `.tabstrip-vertical .tab-close`, `.block-overflow-btn`, `.block-permalink`, `.st-search-placeholder`, `.st-badge-default`, input placeholders (12 selectors)                                                                                                                    |
| `--color-border`         | Default dividers and borders                | `#2a2b3d`                   | `.cm-header`, `.cm-header button`, `.cm-list`, `.tab-close`, `.tab-add`, `.tabstrip-vertical`, `.activity-bar`, `#sidebar`, `.clipboard-banner`, `.chip`, `.block`, `.cm-form input`, etc. (40+ selectors)                                                                                                      |
| `--color-accent`         | Primary action / accent                     | `#7aa2f7`                   | `.cm-header button.cm-primary`, active indicators, scrollbar thumb, `.cm-selected`, `.cm-quick-connect`, `.st-settings-save`, `.st-checkbox input:checked`, `.accent-indicator`, `.switch.checked`, `.tab.active .tab-indicator`, chip border/background, search bar border, link/highlight text (34 selectors) |

#### 2.2 Additional semantic colours

The design spec's eight-token set omits four distinct semantic colours the existing
stylesheet uses:

| Colour               | Role                                          | Usage                                                                                                                                                                     | Token                      |
| -------------------- | --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------- |
| `#9ece6a` (green)    | Success / positive / connected state          | `.cm-connect`, agent status "waiting", `.update-notice.available`, block status "ok", export action                                                                       | `--color-success`          |
| `#e0af68` (amber)    | Warning / downloading / modified / customized | `.update-notice.downloading`, `.st-badge-customized`, `.st-modified-input`, `.st-modified-only-toggle`, `.st-card-customized`, `color: currentColor` for customized label | `--color-warning`          |
| `#f7768e` (pink-red) | Destructive / delete / danger                 | `.cm-danger`, `.cm-remove`, `.st-settings-delete`, `.cm-form-actions button.cm-danger`, `.st-remove-btn`, removed-block state                                             | `--color-danger`           |
| `#9d7cd8` (purple)   | Secondary accent / section / group header     | `.cm-item-group-header`, `.cm-section .cm-section-title`, section title accent in settings                                                                                | `--color-accent-secondary` |

Two more colours appear once each and are edge cases to document:

| Colour                 | Role                      | Location                                  | Recommendation                                                                                                                                                                     |
| ---------------------- | ------------------------- | ----------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `#ff6b6b` (bright red) | Form validation error     | `style.css:301 .cm-form-error`            | `--color-error` — distinct from `--color-danger` (which is for destructive actions, not validation)                                                                                |
| `#ff9e64` (orange)     | Interrupted command state | `style.css:1226 .command-row.interrupted` | Merge into `--color-warning` during migration, since the amber is already used for caution states; if visual distinction matters after migration, split into `--color-interrupted` |

The `#89b4fa / #89b0ff / #89b4ff` cluster (7 usages) and `#b9d870` (1 usage, connect
button hover) are hover-tinted variants of `--color-accent` and `--color-success`
respectively. They are **not** standalone tokens — they are derived via `color-mix()` as
described in §3.

#### 2.3 State colour derivation

Clause §3 covers the policy in detail. The tokens that result from it (all derived
centrally in `tokens.css`):

- `--color-accent-hover`, `--color-accent-active`, `--color-accent-disabled`
- `--color-surface-hover` (already listed above, but derived rather than per-theme)
- `--color-surface-active`, `--color-surface-disabled`
- `--color-success-hover`, `--color-danger-hover`, `--color-warning-hover`
- `--color-focus-ring`
- `--color-scrim` (overlay background, semi-transparent)
- `--color-selection` (text selection background)

#### 2.4 Control metrics

Derived from the existing stylesheet's actual values:

| Token                   | Values (Tokyo Night)                                                                      | Role                                    |
| ----------------------- | ----------------------------------------------------------------------------------------- | --------------------------------------- |
| `--control-height-xs`   | `22px`                                                                                    | Small inline controls, chip height      |
| `--control-height-sm`   | `24px`                                                                                    | Compact buttons, search inputs          |
| `--control-height-md`   | `32px`                                                                                    | Standard button/input height            |
| `--control-height-lg`   | `38px`                                                                                    | Tab bar height, primary actions         |
| `--control-radius-sm`   | `4px`                                                                                     | Small borders (tabs, badges, chips)     |
| `--control-radius-md`   | `6px`                                                                                     | Default border radius (buttons, inputs) |
| `--control-radius-lg`   | `8px`                                                                                     | Large borders (cards, dialogs)          |
| `--control-radius-full` | `9999px`                                                                                  | Pill shapes (tags, indicators)          |
| `--space-1`             | `4px`                                                                                     | Micro spacing                           |
| `--space-2`             | `8px`                                                                                     | Tight spacing                           |
| `--space-3`             | `12px`                                                                                    | Default padding (buttons, tabs)         |
| `--space-4`             | `16px`                                                                                    | Standard gutter (panels, sections)      |
| `--space-6`             | `24px`                                                                                    | Wide gutter                             |
| `--space-8`             | `32px`                                                                                    | Section spacing                         |
| `--space-12`            | `48px`                                                                                    | Page margin                             |
| `--font-size-sm`        | `12px`                                                                                    | Small labels, metadata                  |
| `--font-size-md`        | `14px`                                                                                    | Body text                               |
| `--font-size-lg`        | `16px`                                                                                    | Section titles                          |
| `--font-size-xl`        | `20px`                                                                                    | Page headers                            |
| `--font-family-ui`      | `ui-sans-serif, -apple-system, 'SF Pro Text', system-ui, sans-serif`                      | UI text (was `--nocx-ui`)               |
| `--font-family-mono`    | `ui-monospace, 'SF Mono', Menlo, Monaco, 'Apple Color Emoji', 'Apple Symbols', monospace` | Monospace text (was `--nocx-mono`)      |

#### 2.5 Shell layout tokens

| Token                  | Value (Tokyo Night)             | Role                           |
| ---------------------- | ------------------------------- | ------------------------------ |
| `--sidebar-width`      | `240px`                         | Sidebar panel width            |
| `--activity-bar-width` | `48px`                          | Activity bar width             |
| `--tab-height`         | `38px`                          | Tab strip item height          |
| `--page-header-height` | TBD during shell implementation | Page header height (spec §6.1) |

#### 2.6 Terminal tokens

Mapped from the current `ITheme` object at `renderers/xterm.ts:152-176`:

| Token                      | Current value | Role                         |
| -------------------------- | ------------- | ---------------------------- |
| `--terminal-background`    | `#1a1b26`     | Terminal canvas              |
| `--terminal-foreground`    | `#c0caf5`     | Default text                 |
| `--terminal-cursor`        | `#c0caf5`     | Cursor colour                |
| `--terminal-cursor-accent` | `#1a1b26`     | Cursor text colour (inverse) |
| `--terminal-selection`     | `#364A82`     | Text selection background    |
| `--terminal-ansi-0`        | `#1a1b26`     | Black                        |
| `--terminal-ansi-1`        | `#f7768e`     | Red                          |
| `--terminal-ansi-2`        | `#9ece6a`     | Green                        |
| `--terminal-ansi-3`        | `#e0af68`     | Yellow                       |
| `--terminal-ansi-4`        | `#7aa2f7`     | Blue                         |
| `--terminal-ansi-5`        | `#bb9af7`     | Magenta                      |
| `--terminal-ansi-6`        | `#7dcfff`     | Cyan                         |
| `--terminal-ansi-7`        | `#a9b1d6`     | White                        |
| `--terminal-ansi-8`        | `#414868`     | Bright Black                 |
| `--terminal-ansi-9`        | `#f7768e`     | Bright Red                   |
| `--terminal-ansi-10`       | `#9ece6a`     | Bright Green                 |
| `--terminal-ansi-11`       | `#e0af68`     | Bright Yellow                |
| `--terminal-ansi-12`       | `#7aa2f7`     | Bright Blue                  |
| `--terminal-ansi-13`       | `#bb9af7`     | Bright Magenta               |
| `--terminal-ansi-14`       | `#7dcfff`     | Bright Cyan                  |
| `--terminal-ansi-15`       | `#c0caf5`     | Bright White                 |

These 20 tokens (5 base + 16 ANSI) mirror the `ITheme` shape that xterm.js consumes. The
theme adapter (§5.4 of the design spec) resolves them via `getComputedStyle` and passes
the resulting `ITheme` object to `term.options.theme`.

This list is exhaustive. Every colour literal in the current stylesheet maps to one of
these 20 terminal tokens or the 13 UI colour tokens above. No colour value falls through.

### 3. Derived and state colours — centrally derived via color-mix()

**Policy: `tokens.css` derives state colours from the base tokens using `color-mix()`.
Per-theme files do not define state colours unless a `@supports` fallback is active.**

Rationale: if every theme author had to define 6–7 state variants per UI colour, a
theme file would grow from ~30 token assignments to ~120. That overhead would discourage
theme creation and produce inconsistent hover/active saturation across themes. Central
derivation guarantees uniform interaction behaviour regardless of theme.

The derivation in `tokens.css` works as a single overlay table:

```css
/* tokens.css — state colour derivation */
:root,
[data-theme] {
  --color-accent-hover: color-mix(in srgb, var(--color-accent), white 10%);
  --color-accent-active: color-mix(in srgb, var(--color-accent), white 20%);
  --color-accent-disabled: color-mix(in srgb, var(--color-accent), transparent 40%);

  /* NOT derived — see §3.1. The real hover is DARKER than the surface, so no
     percentage of white produces it. Assigned per theme instead. */
  --color-surface-active: color-mix(in srgb, var(--color-surface), white 12%);
  --color-surface-disabled: color-mix(in srgb, var(--color-surface), transparent 30%);

  --color-success-hover: color-mix(in srgb, var(--color-success), white 10%);
  --color-danger-hover: color-mix(in srgb, var(--color-danger), white 15%);
  --color-warning-hover: color-mix(in srgb, var(--color-warning), white 10%);

  --color-focus-ring: color-mix(in srgb, var(--color-accent), transparent 40%);
  --color-scrim: color-mix(in srgb, black, transparent 50%);
  --color-selection: color-mix(in srgb, var(--color-accent), transparent 70%);
}
```

The exact mix percentages are a starting point derived by matching the existing
`#89b4fa` (accent hover ≈ `color-mix(in srgb, #7aa2f7, white 10%)`) and
`#1f2030` (surface hover ≈ `color-mix(in srgb, #1f2335, white 6%)`). The migration
bead (`nocx-xrrl.2`) should verify and adjust these before committing.

#### 3.1 The derivations do not work — none of them, measured

Central derivation was the right instinct and the wrong answer for this palette.
Measured during `nocx-xrrl.2`, not one of the six state colours matches what
`color-mix()` produces from its base:

| Token                    | In the app             | Derived   | Why it differs                                                 |
| ------------------------ | ---------------------- | --------- | -------------------------------------------------------------- |
| `--color-surface-hover`  | `#1f2030`              | `#2c3041` | **Darker** than the surface; no percentage of white reaches it |
| `--color-success-hover`  | `#b9d870`              | `#a8d379` | 17 off in red                                                  |
| `--color-accent-hover`   | `#89b4fa`              | `#87abf8` | subtle, but not the same colour                                |
| `--color-surface-active` | `#2a2b3d`              | `#2f3343` | different shade                                                |
| `--color-danger-hover`   | `rgba(247,118,142,.1)` | `#f88b9f` | a translucent **overlay**, not a solid colour                  |
| `--color-warning-hover`  | `rgba(224,175,104,.1)` | `#e3b777` | same — a different rendering model                             |

The last two are the interesting ones. They are not shades at all: the app tints
by laying a 10% wash over whatever is beneath, which composites differently
against every background. A derivation cannot express that, and a solid colour
that merely looks similar on one surface will be wrong on the next.

None of this is a defect in the palette. A hover that darkens is a legitimate
choice on a dark theme, and a translucent tint is a legitimate technique. What was
wrong was this ADR's assumption, written before anyone checked, that interaction
colours are the base colour plus some white.

**So state colours are assigned per theme, not derived.** A theme file gains a
dozen more lines and gains, in exchange, the ability to express a darkening hover
and a translucent tint. `color-mix()` remains available for a theme author who
wants it; it is no longer the mechanism the system depends on, so the WebKit floor
below is a constraint on theme authors rather than on the architecture.

The generic lesson is worth keeping: a derivation rule is a claim about the
palette, and claims about data get checked against the data.

#### WebKit floor for color-mix()

`color-mix()` is required by the central derivation. The minimum versions that support
it:

| Platform        | Minimum version                  | Source                                                                                 |
| --------------- | -------------------------------- | -------------------------------------------------------------------------------------- |
| macOS WKWebView | Safari 16.2 / macOS Ventura 13.1 | WebKit Feature Status, MDN browser-compat data                                         |
| iOS WKWebView   | iOS 16.2                         | WebKit Feature Status                                                                  |
| Linux WebKitGTK | 2.40                             | WebKitGTK release notes; `color-mix()` landed in 2.39.x dev builds and shipped in 2.40 |
| Windows         | WebView2 (Chromium 111+)         | Can I Use; Chromium supports `color-mix()` since M111                                  |

The runtime floor this ADR declares:

- **Linux WebKitGTK: 2.40.** Our CI/build system carries 2.52.5, but the app runs
  against the user's system WebKitGTK, which can be older. The floor is 2.40, not
  2.52.5.
- **macOS: Ventura 13.1.** This is a _declared_ minimum, declared by this ADR. The
  app does not currently declare a macOS minimum; we now state one for the purposes
  of this decision. If the deployment target is lowered below 13.1, the derivation
  policy below applies.

If either floor is not met — i.e. the app runs on macOS < 13.1, WebKitGTK < 2.40,
or `CSS.supports('color', 'color-mix(in srgb, red, blue)')` returns false — the
fallback is **explicit per-theme state values**, emitted as `@supports` fallback CSS:

```css
/* themes/tokyo-night.css — fallback for environments without color-mix() */
@supports not (background: color-mix(in srgb, red, blue)) {
  :root,
  [data-theme='tokyo-night'] {
    --color-accent-hover: #89b4fa;
    --color-surface-hover: #1f2030;
    /* … every state token repeated per theme … */
  }
}
```

This fallback is mechanically generated by the build from the theme's base values,
not hand-maintained. The migration bead should verify the build-time generator.

#### What is prohibited

- `opacity` on a whole component as a state-colour substitute — it dims every
  descendant element, not just the surface.
- Ad-hoc `color-mix()` calls at individual call sites — that is how two components
  end up with different hover shades.
- Hardcoded alternate hex values that should be derived states (e.g. `#89b4fa`
  instead of `var(--color-accent-hover)`).

### 4. The guard: an allowed colour grammar

Rejecting `#hex` and `rgb()` declarations alone is insufficient: named colours,
`oklch()`, `lab()`, `color()`, gradient stops, `box-shadow` properties, SVG
presentation attributes, inline `style` props, and
`color-mix(in srgb, var(--accent), red 20%)` all smuggle literals through.

**Permitted grammar** (outside `themes/`):

```
colour-value ::= var(--token)
               | currentColor
               | transparent
               | inherit
               | white | black          /* achromatic mix anchors — §4.1 */
               | color-mix( <mix-params> )
```

where `<mix-params>` contains a colour space and operands, and every colour operand
must itself be one of the permitted values (no palette literals inside `color-mix()`).

#### 4.1 Why `white` and `black` are permitted, and only there

The state derivation in §3 mixes towards `white` and towards `transparent`. Without an
explicit carve-out the derivation table violates this ADR's own grammar in every lighten
row, which was true of this document's first draft — a grammar that forbids its own
canonical example is not enforceable.

`white`, `black` and `transparent` are **achromatic anchors**, not palette choices: they
carry no theme identity and cannot encode a brand colour. Every other named colour
(`red`, `rebeccapurple`, …) stays prohibited, because those _are_ palette.

The carve-out is narrow on purpose: `white`/`black` are permitted **only as operands of
`color-mix()`**. `color: white` on a declaration is a violation — it should be
`var(--color-text)`.

Same reasoning for `--color-scrim`, which the §3 table writes as `rgba(0, 0, 0, 0.5)`:
that is an achromatic overlay, but `rgba()` is not in the grammar. Write it as
`color-mix(in srgb, black, transparent 50%)` so the one rule covers it, and so a theme
that wants a warmer scrim can override the token rather than the rule.

**Implementation requirements** for the guard (`nocx-xrrl.4`):

1. Parses CSS declarations, not text — a `#hex` inside a comment must pass, one
   declaring a colour must fail.
2. Scans JSX `style={'...'}` and `style={{...}}` props for colour property values.
3. Scans SVG `fill` and `stroke` attributes.
4. Produces a **baseline violation list** at the time the guard is first enabled,
   keyed by file and line, which may only shrink.
5. The baseline must be **empty** before the ticket `nocx-xrrl` closes.
6. Build failure on any violation outside the baseline.

### 5. Colours that never appear in a CSS file

Four escape routes bypass a CSS-file scanner. Each has a stated policy:

#### 5.1 Settings values from Go (`internal/settings/`)

A settings value may carry a **token name** (as a string), never a colour literal —
unless the setting is explicitly a user-chosen colour (e.g. "custom background colour"
or "accent colour picker"). In the latter case the value is stored as a hex string and
applied via `style.setProperty()` at the composition root, not embedded in a stylesheet.

#### 5.2 xterm's injected stylesheet

`index.html` keeps `style-src 'unsafe-inline'` for xterm's runtime-injected styles.
This stylesheet is owned by xterm.js; the guard does not scan it. The theme adapter
(design spec §5.4) is responsible for making xterm's palette resolve from
`--terminal-*` tokens, which places the terminal's own CSS into the token system
without scanning xterm's shipped styles.

#### 5.3 Scrollback serializer (`scrollback/serializer.ts`)

`serializer.ts:11-29` contains a hardcoded ANSI colour table (16 hex strings matching
Tokyo Night). `paletteToRGB()` at lines 37–50 generates `rgb(...)` strings for colour
cube indices 16–231 and grayscale indices 232–255 via arithmetic, and `attrsToStyle()`
at lines 142–173 emits the style string into serializer output.

These are **terminal content semantics**, not UI colours. The serializer's ANSI 0–15
table must come from the same theme as the terminal (§5.5 of the design spec — the
scrollback is snapshot-themed). Indices 16–255 and truecolour values are content bytes
and their algorithmic conversion stays. The guard exempts `scrollback/` entirely.

#### 5.4 SVG assets

Icon components under `ui/icons/` use `currentColor` for `fill` and `stroke` so they
follow the token colouring their container. A build-time test asserts no icon SVG
contains a hardcoded colour value. Externally sourced SVG assets (imported as
components or as files) are reviewed at build time.

### 6. Why not Tailwind

Tailwind CSS is a reasonable choice on its own terms. We decline it for reasons of
**fit**, not bundle size (the usual argument):

- **Distributed visual decisions.** Tailwind places colour, spacing, and typography
  choices into long class strings in JSX. The owner's stated requirement is "all CSS
  in one place, driven by variables." Tailwind's model is the opposite: visual values
  live beside markup, spread across every component file. Checking what colour a
  surface is means reading the JSX, not the CSS.
- **Doubles the migration surface.** The existing 2,285-line stylesheet must be
  converted to tokens regardless. Tailwind would add a second conversion (tokens →
  utility classes → JSX strings) on top of the first, and every migration bead would
  need to reconcile both. A plain-CSS migration is simpler because it is one
  mechanical rewrite per selector block.
- **Theme work means policing.** A Tailwind theme file (`tailwind.config` / `@theme`)
  defines the palette, but `text-[#abc]` and `bg-[#def]` arbitrary values bypass it.
  Preventing those requires a separate lint rule — at which point Tailwind's
  ergonomic argument (no custom CSS) is gone and we are maintaining two systems.
- **Orca's relevant pattern is the token layer, not the framework.** Orca uses
  Tailwind v4 but its theme system works through CSS variables (`@theme inline { …
}` mapping to `var(--background)`, `var(--foreground)`, etc.). That variable layer
  is the architecture we are copying; the utility framework around it is decorative
  to this decision.

### 7. Why not CSS Modules

CSS Modules scope selectors per component file, which addresses the naming-collision
problem. They do not address either requirement:

- **Tokens.** CSS Modules have no mechanism for shared token values. A `:root`
  variable in a module file does not export to other modules. Theme switching would
  still need a global `tokens.css` or a separate variable layer.
- **One place for CSS.** The owner explicitly wants all CSS in one directory, not
  scattered beside component files. CSS Modules embed CSS in the component tree by
  design.

### 8. Theme switching: data-theme and startup ordering

Theme selection uses `data-theme` on the root element. This interacts with ADR-0005's
WebKitGTK refresh pump and ADR-0012's xterm boundary:

- `data-theme` is applied **synchronously before the Solid render** by a bootstrap
  theme resolver (design spec §5.4). This ensures the first frame renders with
  correct token values; the 42 ms pump does not race with theme application.

#### 8.1 The bootstrap cache, and why it is not a second source of truth

"Synchronous" and "authoritative in Go" cannot both hold naively, and the first drafts of
the design spec and of `docs/frontend-state-ownership.md` each assumed one without seeing
the other. The selected theme is a Go setting (`ui.theme`) — it must survive reinstall,
travel with export/import, and appear in the generated settings screen. But Go settings
arrive over the WebSocket **asynchronously**, long after the first frame.

Resolution, stated once here so neither document has to guess:

- The last accepted theme id is written to the **versioned local UI record** as a
  **bootstrap cache** whenever the accepted Go value changes.
- At startup the resolver reads that cache **synchronously**, applies `data-theme`, and
  the first frame is correct.
- When the Go snapshot arrives it is reconciled. If it differs, the theme is re-applied
  and live terminal controllers are notified through `applyTheme`.
- With no cache — genuine first run — the built-in default theme is applied.

This does not violate the "mirror, never re-persist" rule in
`docs/frontend-state-ownership.md` §3, and the distinction is worth being precise about
because it is the kind of exception that quietly becomes a second source of truth: the
cache is **never read as authority**, is never written by user action, and always loses to
Go on reconcile. It exists to colour one frame, not to answer "what theme is selected".

The visible consequence is bounded and acceptable: if a user changes the theme on another
machine and syncs, the first frame after launch shows the previous theme and then
corrects. The alternative — an unstyled or default-themed first frame on **every** launch —
is worse.

- The terminal adapter resolves `--terminal-*` tokens into `ITheme` at construction
  time, passes the concrete object into `Terminal`, and subscribes to theme changes
  before construction completes. ADR-0005's pump stays wholly inside the terminal
  controller and is never driven from reactive state.
- The scrollback serializer (§5.3) reads ANSI 0–15 from the theme, not from its
  hardcoded table. Freeze blocks are snapshot-themed at capture time.

## Rationale

### Why not SCSS / Sass

SCSS variables were evaluated and rejected. SCSS compiles to static values; it cannot
switch at runtime with `data-theme`, which is the entire point. CSS custom properties
are the only variable mechanism that responds to a runtime attribute change without
recompilation. Tabby's SCSS approach works because Tabby does not use `data-theme`
runtime switching.

### Why not a CSS-in-JS library

Every CSS-in-JS approach evaluated (emotion, styled-components, vanilla-extract, Linaria)
either requires a runtime (defeats the point of static CSS in one place) or generates
scoped class names that scatter rules across files. Neither fits "all CSS in one place."

### The cost of deriving states centrally

Central `color-mix()` derivation costs precisely in the environments that don't support
it: older macOS (< 13.1) and older WebKitGTK (< 2.40). The `@supports` fallback
generates per-theme state values, which are mechanically derivable from the base
palette and should be produced by the build, not written by hand. On WebKit versions
that do support it (which covers the entire practical deployment), the cost is zero —
`color-mix()` is a CSS function evaluated at paint time.

## Consequences

**Costs:**

1. A mechanical migration of roughly 254 colour-value sites (232 hex + 22 rgba) to
   `var(--token)` references, spread across 289 rule blocks in the single style file.
2. Restructuring from one file to the `styles/` directory tree — the one-file
   structure necessarily collapses during migration because `tokens.css` must be
   loaded before `base.css` and `surfaces/`, and the single file today has no load
   ordering contract.
3. A lint guard (the allowed-grammar checker) that must be written, not configured.
   It requires CSS parsing and JSX/attribute scanning, which no off-the-shelf ESLint
   plugin provides.
4. A build-time `@supports` fallback generator for the (currently not exercised)
   no-color-mix scenario.
5. The scrollback serializer's ANSI table and xterm's theme object must stop being
   literal palettes and start reading from `--terminal-*` tokens — these are not
   style.css edits but TypeScript changes in the same ticket.

**Benefits:**

1. Adding a theme is one new file under `themes/` assigning ~33 base tokens (9 UI
   semantic + 1 additional semantic + 5 terminal base + 16 ANSI + 2 layout), from
   which all state values derive. No selector overrides, no specificity war.
2. The CI guard makes a colour leak a build error with a documented remedy (add
   a token, or declare a new one if the colour is genuinely new).
3. The surface palette becomes auditable: 13 UI colour tokens cover every hex value
   in the current stylesheet, and any new colour entering production must first enter
   the token vocabulary.
4. The font tokens (`--font-family-ui`, `--font-family-mono`) are the only survivors
   from the old custom-property set; the dead `--nocx-ui` declaration and the three
   platform-only properties are dropped from the token namespace.

## Revisit when

- **A second UI framework enters the stack.** If a non-CSS styling mechanism (e.g.
  a canvas-based renderer, or a native WebView for a particular view) introduces its
  own colour system, the token layer must either bridge to it or be extended. This
  ADR is falsified if the token layer becomes one of two competing colour systems
  rather than the single source.
- **macOS deployment target is set below 13.1** (the WKWebView version that first
  shipped `color-mix()` in Safari 16.2). Then the `@supports` fallback becomes the
  primary path, and the central derivation costs real bytes. A different balance —
  explicit per-theme states as the default — may be cheaper.
- **The guard baseline grows for three consecutive beads.** The allowed-grammar
  checker enumerates current violations at first enable and permits only shrinkage.
  If the list grows across three beads, the guard is not working and the architecture
  is not being followed — revisit whether a structural enforcement (e.g. separate
  build step, separate directory) is needed instead.
- **An Orca-style utility-framework layer is adopted for a subset of components.**
  If a later decision introduces Tailwind or similar for a scoped use case (e.g.
  documentation or generated UI), the token layer survives and the framework consumes
  it through CSS variables — but the guard must be updated to permit `@theme` or
  equivalent in that scope. The architecture survives; the guard's scope changes.
- **The component kit (`nocx-vxqj`) reveals that 30%+ of the colour-token assignments
  are unique to one component.** If the tokens turn out to be aliases rather than
  shared values, the layer adds complexity without leverage and should be collapsed.
