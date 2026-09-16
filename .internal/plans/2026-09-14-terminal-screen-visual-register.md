# The terminal screen speaks the kit — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development (recommended)
> or beads-superpowers:executing-plans to implement this plan task-by-task. The tasks ALREADY EXIST as beads —
> `nocx-9bpeq.2` … `nocx-9bpeq.10` under epic `nocx-9bpeq`; claim them with `br update <id> --claim`, never create a
> second set. The tracker is `br`, not `bd` (AGENTS.md). Steps use checkbox (`- [ ]`) syntax for readability.

**Goal:** A person scanning a session in any of the twelve themes sees failures and the running command stand
out while successful commands stay quiet — every block, the composer and the pane drawn in the kit's one
vocabulary, with no emoji, no glyph-as-icon and no locale-formatted clock.

**Architecture:** The spec's single idea — the composer is the next block of the ledger — gives blocks and the
composer one anatomy (meta row + command row). Imperative code (`scrollback/`, `editor.ts`) reaches the kit
through a new `Meta` primitive and vanilla emitters (`createBadge`, `createIconButton`, `createSpinner`,
`createButton`) held to their Solid twins by parity tests; rows become full width so a failure tint and bar
can paint edge to edge; per-theme tokens carry every colour; the lint exemption that let the terminal tree
skip the kit is narrowed last.

**Tech Stack:** TypeScript, SolidJS (kit), plain CSS with semantic custom properties (ADR-0013), CodeMirror 6,
xterm.js, Vitest, Playwright (headless stand in `nocx-e2e:local`), repo lint gates under `frontend/lint-fixtures/`.

**Spec:** `.internal/specs/2026-09-14-terminal-screen-visual-register-design.md` (accepted 2026-09-14).

## Global Constraints

- Every visual change lands in the kit (`frontend/src/ui/`, `styles/components/`), in `styles/tokens.css`, or in
  the twelve `styles/themes/*.css` — never as new rules in `style.css` or a `.style.` write. A surface places, the
  kit paints; a surface may set `opacity` and layout on a kit identity, never background/border/color/font/
  box-shadow/padding (`surface-paints-kit`).
- A colour token is set in all twelve themes; `theme-catalogue.test.ts` covers it. No `color-mix` state
  derivations in component CSS (ADR-0013 §3.1).
- Terminal-screen feature CSS (block rows, composer) lives in `styles/surfaces/`, not `styles/components/`, so
  `surface-paints-kit` sees it.
- Vanilla emitters are named `ui/<component>-element.ts` (never `ui/<component>.ts`: `.ts` resolves before `.tsx`
  and would capture every `./<component>` import). Each has a parity test against its Solid twin.
- Commit format per AGENTS.md: `<type>(<scope>): <subject> (<bead-id>)`, prose body, the Co-Authored-By line.
- A worker runs the unit tests for what it touched and the e2e specs its task CREATES or edits, one spec at a
  time in the container (`PW_PROJECTS=chromium e2e/run-in-container.sh e2e/<spec>`), never the whole suite and
  never `make ci-full` — that is the coordinator's, once, on the merged tree.
- A test may not depend on timing: wait on an observable state (a DOM attribute, a record), never a duration.

## Task order and dependencies

Recorded as `blocks` edges in `br` (T1 closed):

| Task                                        | Bead          | Waits on                      |
| ------------------------------------------- | ------------- | ----------------------------- |
| T2 tokens + undefined-var gate              | nocx-9bpeq.2  | —                             |
| T3 surface roles + danger surface           | nocx-9bpeq.3  | —                             |
| T4 Meta + vanilla emitters + parity         | nocx-9bpeq.4  | T2                            |
| T5 glyphs → kit icons, ⋮ menu → ContextMenu | nocx-9bpeq.5  | T4                            |
| T9 end-to-end, committed red                | nocx-9bpeq.9  | —                             |
| T8 full-width rows, gutter                  | nocx-9bpeq.8  | T2                            |
| T6 block header                             | nocx-9bpeq.6  | T2, T3, T4, T5, T8, nocx-foyr |
| T7 composer                                 | nocx-9bpeq.7  | T2, T4, T8                    |
| T10 lint exemption narrowed                 | nocx-9bpeq.10 | T4, T5, T6, T7, T8            |

T2/T3/T9 can run in parallel (disjoint files). T5 and T8 can run in parallel. T6 and T7 touch disjoint files after
T8 but share `style.css` deletions — run them one after the other.

## Decisions taken while planning (where the plan departs from the spec's letter)

1. **Rail icon size moves from T8 to T2.** Routing `icon-button.css` through `--icon-size-*` already takes `lg`
   from 24px to 20px; its only `lg` users are the activity rail. T2 also moves `--pane-inline-padding` into
   tokens.css at its current 10px; T8 changes the value to `var(--space-4)` with the geometry work.
2. **`--color-danger` changes in seven themes** (catppuccin-latte, dracula, gruvbox-dark, nord, one-dark,
   solarized-dark, solarized-light), and `ayu-dark` chrome and `light` surface-raised move slightly, because §7's
   thresholds cannot be met otherwise: in those themes danger was ≤ 4.5:1 on the bare ground. Nord moves most
   (`#bf616a`→`#cf949c`), softening its destructive controls too. Values are computed in T3.
3. **§6.3 render islands became vanilla emitters for two pieces.** The running `Spinner`: `freezeBlock` replaces the
   header with `replaceChild` and nothing would dispose an island. The composer controls: four writers
   (`GrantController.updateChip`, `setModelChip`, `setRecoveryAction`, `setVisible`) mutate those buttons directly,
   which a Solid island would fight. Both follow §6.2 instead: `createSpinner`, `createButton`, with parity tests.
   The ⋮ menu stays a render island (mounted on open, disposed on close).
4. **The kit `ContextMenu` gains four variances** the block menu needs (`align` end, `anchor` toggling, an item
   `busyLabel`, the height cap) — the kit grows by variants rather than keeping a second menu.
5. **`data-outcome` is written for every settled outcome**, painted only for `failure`: seventeen e2e specs waited
   on the `ok`/`completed` words that disappear, and need a product-written state to wait on instead.
6. **A non-zero exit is a failure, including 130 (Ctrl+C).** Commands have no `cancelled` status; inventing one
   from an exit code would guess at intent. `cancelled` stays the ask kind's.
7. **The grant control loses its accent colour when blocks are marked.** The count is in its text; the colour was a
   surface repainting a kit Button. `data-state` stays as a test hook.
8. **`formatTimestamp` is formatted by hand, not with `Intl('en-GB')`**, because Node's ICU and WebKit's disagree
   ("Sept" vs "Sep").

**Cut, and filed as its own bead:** the duration's hover title showing when the command started (§3.1). Nothing at
the header knows a wall-clock start (`BlockRecord` carries only `durationMs`; the restore contract has no start),
so it needs a backend contract change — tracked on nocx-0tmq5.

---

### Task T2 (nocx-9bpeq.2): Icon sizes and the pane gutter are tokens, and a var() naming no token is a gate failure even with a fallback

Spec §8. Bead notes narrowed the scope: no elevation or motion groups.

**What is true today (read 2026-09-14):**

- `frontend/lint-fixtures/check-css-integrity.mjs:147-166` `collectCustomProperties` drops every `var()` that has a
  fallback (`if (hasFallback) return`), and the `undefined-var` loop at `:800-809` therefore never sees one. The fixture
  enshrines that: `lint-fixtures/css-integrity-fixture/styles/loaded.css:13-16` says "A var() WITH a fallback is legitimate
  and must not be reported."
- Six references name tokens that exist nowhere, so the fallback is all that ever renders (fallback equals the real
  token's value in every case, so fixing them is visually neutral):
  `styles/surfaces/ports.css:144`, `styles/components/record-row.css:73`, `styles/components/collection-view.css:50`
  (`--border-radius-sm, 4px`), `styles/components/pet-preview.css:21` (`--radius-md, 6px`), `:45` (`--radius-sm, 4px`),
  `styles/surfaces/connections.css:39` (`--border-radius-md, 6px`). The real tokens are `--control-radius-sm: 4px` and
  `--control-radius-md: 6px` (`styles/tokens.css:42-43`).
- Fallback references are ALSO the documented shape of a property set from script (`sidebar-width.ts:73`,
  `scrollback/cell-metric.ts:104-134`, `tab-strip.tsx`, `api/api-pane.tsx:594-595`, `api/timing-bar.tsx:109`,
  `skill-view/skill-view-body.tsx:468`, `scrollback/answer-body.ts:148`, `editor.ts:268`, `ui/swatch-picker.tsx:106`).
  Measured: every fallback reference to a name no stylesheet declares is either one of the six above or a name that
  appears as a string literal in a non-test `.ts`/`.tsx` file. So "flag every fallback" would turn the rule off within a
  day; "flag a fallback whose name no stylesheet declares AND no source sets" is exact.
- `styles/components/icon-button.css` sizes glyphs in px: `xs > svg` 13px (`:53-56`), `sm > svg` 16px (`:63-66`), the
  default `> svg` `1em` (`:87-90`, 16.8px under `md`'s `--font-size-lg`), `lg > svg` 24px (`:96-99`).
  `size="lg"` IconButton has exactly two consumers, both the activity rail (`sidebar.tsx:510`, `sidebar.tsx:564`).
- `--pane-inline-padding: 10px` is declared on `.pane` in `styles/base.css:464`; consumers are `base.css:465`,
  `style.css:196-197`, `style.css:815`.

**Deliberate split with T8 (contract):** T2 moves the `--pane-inline-padding` declaration into `tokens.css` and KEEPS
its value at `10px`. Changing it to `var(--space-4)` changes xterm's column count and the frozen block's geometry, which is
T8's measured work with its alignment test. T2 does not touch the block header's padding (T6 does).

**Deviation from the contract to report:** routing `icon-button.css` through `--icon-size-*` (spec §8 values 14/16/20)
necessarily moves `lg` from 24px to 20px, and `lg`'s only consumers are the rail. So the rail's glyph size (spec §8
"the activity rail uses lg (from 24px)") lands in T2, not T8. T8 keeps the gutter only.

**Files:**

- Modify: `frontend/lint-fixtures/check-css-integrity.mjs:147-166` (collect fallback refs), `:800-809` (rule), header
  comment `:15-17`
- Create: `frontend/lint-fixtures/check-css-integrity.test.mjs`
- Create: `frontend/lint-fixtures/css-integrity-fixture/runtime-property.ts`
- Modify: `frontend/lint-fixtures/css-integrity-fixture/styles/loaded.css:13-16`
- Modify: the six sites listed above
- Modify: `frontend/src/styles/tokens.css` (icon sizes, pane gutter), `frontend/src/styles/base.css:464`
- Modify: `frontend/src/styles/components/icon-button.css:48-99`
- Create: `frontend/src/icon-size-tokens.test.ts`

**Interfaces:**

- Consumes: nothing from other tasks.
- Produces (exact names later tasks use): `--icon-size-sm: 14px`, `--icon-size-md: 16px`, `--icon-size-lg: 20px`,
  `--pane-inline-padding` declared in `tokens.css` inside the `:root, [data-theme]` block (value `10px` until T8);
  `checkCSSIntegrity()` reports `undefined-var` for a fallback reference to a name that no stylesheet declares and no
  non-test source under the styles directory's parent sets as a string literal.

**Acceptance Criteria:**

- `undefined-var` reports a `var()` whose name is undefined EVEN WHEN it has a fallback, unless a non-test `.ts`/`.tsx`
  file under the source root names that property as a string literal; `check-css-integrity.test.mjs` proves both
  directions on the fixture; bare references behave exactly as before.
- The six sites use `--control-radius-sm` / `--control-radius-md`; `node lint-fixtures/check-css-integrity.mjs` exits 0
  on the app.
- `tokens.css` declares `--icon-size-sm: 14px`, `--icon-size-md: 16px`, `--icon-size-lg: 20px` and
  `--pane-inline-padding: 10px`; `base.css` no longer declares `--pane-inline-padding`.
- Every `width`/`height` on an `svg` selector in `icon-button.css` is `var(--icon-size-sm|md|lg)`, asserted by
  `icon-size-tokens.test.ts` over the parsed stylesheet.
- Deviation from the bead's text: the bead asks for "a unit test reads computed values in a mounted IconButton". The
  frontend's DOM environment is jsdom, which does not resolve `var()` from stylesheets, so a computed-value assertion
  there would pass or fail on jsdom's gaps rather than on our CSS. The token reaching the element is asserted
  structurally here and in a real browser by T9's end-to-end check.

All commands below run from `frontend/`.

- [ ] **Step 1: Write the failing checker test and its fixture**

`frontend/lint-fixtures/check-css-integrity.test.mjs`:

```js
import { describe, expect, it } from 'vitest'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { checkCSSIntegrity } from './check-css-integrity.mjs'

/**
 * undefined-var, both directions (nocx-9bpeq.2).
 *
 * A fallback used to exempt a reference outright, so `var(--radius-sm, 4px)` —
 * a token that never existed — rendered its fallback for months with the gate
 * green. A fallback is still the right shape for a property set from script
 * (`style.setProperty('--sidebar-width', …)`), and a rule that reported those
 * would be turned off. So the rule is exact: a fallback reference is reported
 * when no stylesheet declares the name AND no source sets it.
 */

const here = fileURLToPath(new URL('.', import.meta.url))
const fixture = resolve(here, 'css-integrity-fixture')

const undefinedVarDetails = () =>
  checkCSSIntegrity({
    entry: resolve(fixture, 'entry.css'),
    stylesDir: resolve(fixture, 'styles'),
    uiDir: resolve(fixture, 'ui'),
  })
    .filter((v) => v.rule === 'undefined-var')
    .map((v) => v.detail)

describe('undefined-var', () => {
  it('reports a bare reference to a property nothing declares', () => {
    expect(undefinedVarDetails().some((d) => d.includes('--fixture-never-declared)'))).toBe(true)
  })

  it('reports a fallback reference to a property nothing declares and nothing sets', () => {
    expect(undefinedVarDetails().some((d) => d.includes('--fixture-also-never-declared'))).toBe(
      true,
    )
  })

  it('does not report a fallback reference to a property a source file sets', () => {
    expect(undefinedVarDetails().some((d) => d.includes('--fixture-runtime-width'))).toBe(false)
  })
})
```

`frontend/lint-fixtures/css-integrity-fixture/runtime-property.ts`:

```ts
/* The property below is set from script, the way sidebar-width.ts sets
   --sidebar-width. Its stylesheet reference carries a fallback and no rule
   declares it, and the integrity checker must NOT report it: the fallback is
   the value before the first write, not the only value there will ever be. */

export function applyFixtureWidth(el: HTMLElement, width: number): void {
  el.style.setProperty('--fixture-runtime-width', `${width}px`)
}
```

In `frontend/lint-fixtures/css-integrity-fixture/styles/loaded.css`, replace lines 13-16:

```css
/* A var() WITH a fallback is reported when nothing declares the name and no
   source sets it — the fallback is then the only value that can ever render. */
.fixture-uses-fallback {
  font-family: var(--fixture-also-never-declared, monospace);
}

/* A fallback for a property a source file sets (runtime-property.ts) is the
   value before the first write, and must not be reported. */
.fixture-uses-runtime-property {
  width: var(--fixture-runtime-width, 240px);
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `./node_modules/.bin/vitest run --config lint-fixtures/vitest.config.lint-fixtures.mjs lint-fixtures/check-css-integrity.test.mjs`
Expected: `1 failed | 2 passed` — "reports a fallback reference to a property nothing declares and nothing sets".

- [ ] **Step 3: Implement**

In `check-css-integrity.mjs`, replace `collectCustomProperties` (`:146-166`):

```js
/** Custom properties declared (`--x: …`) and referenced (`var(--x)`) in a file.
 *  A reference records whether it carries a fallback; the rule decides what a
 *  fallback excuses, not the collector. */
function collectCustomProperties(ast) {
  const declared = new Set()
  const referenced = []

  css.walk(ast, (node) => {
    if (node.type === 'Declaration' && node.property.startsWith('--')) {
      declared.add(node.property)
    }
    if (node.type === 'Function' && node.name === 'var') {
      const first = node.children && node.children.first
      if (!first || first.type !== 'Identifier' || !first.name.startsWith('--')) return
      referenced.push({
        name: first.name,
        hasFallback: node.children.size > 1,
        line: node.loc ? node.loc.start.line : 0,
      })
    }
  })

  return { declared, referenced }
}

/**
 * Custom properties a source file sets from script — every `'--name'`, `"--name"`
 * or `` `--name `` string literal in a non-test .ts/.tsx under `rootAbs`.
 *
 * This is what a fallback legitimately stands in for: `--sidebar-width` has no
 * declaration because sidebar-width.ts writes it inline. A name set with a
 * computed string (`--${x}`) is deliberately not found — spell it literally.
 */
function collectRuntimeProperties(rootAbs) {
  const names = new Set()
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.name === 'node_modules') continue
      const abs = join(dir, entry.name)
      if (entry.isDirectory()) {
        walk(abs)
        continue
      }
      if (!/\.(ts|tsx)$/.test(entry.name) || /\.test\.(ts|tsx)$/.test(entry.name)) continue
      for (const m of readFileSync(abs, 'utf8').matchAll(/['"`](--[a-zA-Z][a-zA-Z0-9-]*)/g)) {
        names.add(m[1])
      }
    }
  }
  if (existsSync(rootAbs)) walk(rootAbs)
  return names
}
```

Replace the `undefined-var` block (`:800-809`):

```js
// ── undefined-var (needs every file's declarations first) ───────────────
// A bare reference must be declared. A reference with a fallback may also be
// set from script instead; one that is neither renders its fallback forever,
// which is how six `--radius-*` references stood for months (nocx-9bpeq.2).
const runtimeProperties = collectRuntimeProperties(resolve(stylesAbs, '..'))
for (const ref of referencedEverywhere) {
  if (declaredEverywhere.has(ref.name)) continue
  if (ref.hasFallback && runtimeProperties.has(ref.name)) continue
  violations.push({
    rule: 'undefined-var',
    file: rel(ref.file),
    line: ref.line,
    detail: ref.hasFallback
      ? `var(${ref.name}, …) names a property no stylesheet declares and no source sets — its fallback is the only value that can ever render`
      : `var(${ref.name}) has no declaration in the loaded cascade and no fallback`,
  })
}
```

In the header comment (`:15-17`) replace the `undefined-var` paragraph with:

```js
 *   undefined-var `font-family: var(--font-family-mono)` where no rule
 *                 declares that property — or `var(--radius-sm, 4px)` where no
 *                 rule declares it and no source sets it, so the fallback is
 *                 all that ever renders. The declaration is invalid at
 *                 computed-value time and the element silently inherits.
```

- [ ] **Step 4: Run the test, then the checker on the app**

Run: `./node_modules/.bin/vitest run --config lint-fixtures/vitest.config.lint-fixtures.mjs lint-fixtures/check-css-integrity.test.mjs`
Expected: `3 passed`.

Run: `sh ./lint-fixtures/gate.sh`
Expected: exit 0 (every integrity rule still fires on the fixture).

Run: `node lint-fixtures/check-css-integrity.mjs`
Expected: exit 1 with exactly six `undefined-var` lines — `ports.css:144`, `record-row.css:73`, `collection-view.css:50`,
`pet-preview.css:21`, `pet-preview.css:45`, `connections.css:39`. Any other line means a script-set property is spelled
with a computed string: make that literal, do not exempt it.

- [ ] **Step 5: Fix the six sites**

```css
/* styles/surfaces/ports.css:144, styles/components/record-row.css:73,
   styles/components/collection-view.css:50, styles/components/pet-preview.css:45 */
border-radius: var(--control-radius-sm);

/* styles/components/pet-preview.css:21, styles/surfaces/connections.css:39 */
border-radius: var(--control-radius-md);
```

Run: `node lint-fixtures/check-css-integrity.mjs`
Expected: exit 0, no output.

- [ ] **Step 6: Commit**

```bash
git add frontend/lint-fixtures/check-css-integrity.mjs frontend/lint-fixtures/check-css-integrity.test.mjs \
  frontend/lint-fixtures/css-integrity-fixture/runtime-property.ts \
  frontend/lint-fixtures/css-integrity-fixture/styles/loaded.css \
  frontend/src/styles/surfaces/ports.css frontend/src/styles/components/record-row.css \
  frontend/src/styles/components/collection-view.css frontend/src/styles/components/pet-preview.css \
  frontend/src/styles/surfaces/connections.css
git commit -m "$(cat <<'EOF'
fix(frontend): a var() naming no token is a gate failure even with a fallback (nocx-9bpeq.2)

The integrity checker excused every var() that carried a fallback, so six
references to --radius-* and --border-radius-*, names no stylesheet has ever
declared, rendered their fallback with the gate green. A fallback is still the
right shape for a property set from script, and reporting those would get the
rule switched off, so a fallback reference is now reported only when no
stylesheet declares the name and no non-test source sets it as a literal. The
six sites move to --control-radius-sm/md, whose values equal the fallbacks.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 7: Write the failing token test**

`frontend/src/icon-size-tokens.test.ts`:

```ts
// @vitest-environment node
// css-tree is loaded via createRequire and has no type declarations;
// every call to it touches a value typed `any`, so no-unsafe-* must
// be disabled at the file level for this test.
/* eslint-disable @typescript-eslint/no-unsafe-assignment,
                      @typescript-eslint/no-unsafe-call,
                      @typescript-eslint/no-unsafe-member-access */
/**
 * Icon sizes and the pane gutter are tokens (nocx-9bpeq.2, spec §8).
 *
 * Structural rather than computed: jsdom does not resolve var() from a
 * stylesheet, so "the token reaches the element" is asserted in a browser by the
 * terminal-screen end-to-end check (nocx-9bpeq.9). What this pins is that the
 * component cannot size a glyph any other way.
 */
import { describe, it, expect } from 'vitest'
import { createRequire } from 'node:module'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const css = createRequire(import.meta.url)('css-tree')
const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)
const read = (p: string): string => readFileSync(resolve(dirname, p), 'utf8')

function declarations(source: string): { selector: string; property: string; value: string }[] {
  const out: { selector: string; property: string; value: string }[] = []
  css.walk(css.parse(source), {
    visit: 'Rule',
    enter(rule: {
      prelude: unknown
      block: { children: { forEach(fn: (d: unknown) => void): void } }
    }) {
      const selector = css.generate(rule.prelude) as string
      rule.block.children.forEach((d: unknown) => {
        const n = d as { type: string; property: string; value: unknown }
        if (n.type !== 'Declaration') return
        out.push({
          selector,
          property: n.property,
          value: (css.generate(n.value) as string).trim(),
        })
      })
    },
  })
  return out
}

describe('icon and gutter tokens', () => {
  const tokens = declarations(read('styles/tokens.css'))
  const valueOf = (name: string): string | undefined =>
    tokens.find((d) => d.property === name)?.value

  it('declares the three icon sizes the spec names', () => {
    expect(valueOf('--icon-size-sm')).toBe('14px')
    expect(valueOf('--icon-size-md')).toBe('16px')
    expect(valueOf('--icon-size-lg')).toBe('20px')
  })

  it('declares the pane gutter in the token layer and nowhere else', () => {
    // 10px until nocx-9bpeq.8 changes it: that change moves xterm's column count.
    expect(valueOf('--pane-inline-padding')).toBe('10px')
    expect(
      declarations(read('styles/base.css')).some((d) => d.property === '--pane-inline-padding'),
    ).toBe(false)
  })

  it('sizes every IconButton glyph through an icon-size token', () => {
    const glyph = declarations(read('styles/components/icon-button.css')).filter(
      (d) => /svg$/.test(d.selector) && (d.property === 'width' || d.property === 'height'),
    )
    expect(glyph.length).toBeGreaterThan(0)
    for (const d of glyph) {
      expect(d.value, `${d.selector} ${d.property}`).toMatch(/^var\(--icon-size-(sm|md|lg)\)$/)
    }
  })
})
```

- [ ] **Step 8: Run it and see it fail**

Run: `./node_modules/.bin/vitest run src/icon-size-tokens.test.ts`
Expected: `3 failed` — `--icon-size-sm` is `undefined`; `--pane-inline-padding` is `undefined` in tokens.css; the first
glyph declaration is `13px`.

- [ ] **Step 9: Implement**

In `frontend/src/styles/tokens.css`, inside the `:root, [data-theme]` block, after `--space-12: 48px;`:

```css
/* ── Icon sizes (spec §8, nocx-9bpeq.2) ───────────────────────────────
     A glyph's box. Not type: the kit's icons carry a viewBox and no intrinsic
     size, so whoever renders one says how big it is, and says it with one of
     these. */
--icon-size-sm: 14px;
--icon-size-md: 16px;
--icon-size-lg: 20px;
```

and in the "Shell layout" section, after `--tab-height: 38px;`:

```css
/* The terminal pane's inline gutter. Declared here rather than on `.pane` so
     every row that must line up with the live terminal reads one number. Its
     value moves xterm's column count and the frozen block's first column, so
     it changes only with the alignment test (nocx-9bpeq.8). */
--pane-inline-padding: 10px;
```

In `frontend/src/styles/base.css`, delete line 464 (`  --pane-inline-padding: 10px;`); line 465
`padding: 0 var(--pane-inline-padding);` stays.

In `frontend/src/styles/components/icon-button.css`, replace lines 53-56, 63-66 and 75-99 with:

```css
.ui-icon-button[data-size='xs'] > svg {
  width: var(--icon-size-sm);
  height: var(--icon-size-sm);
}
```

```css
.ui-icon-button[data-size='sm'] > svg {
  width: var(--icon-size-md);
  height: var(--icon-size-md);
}
```

```css
/* ── The icon ────────────────────────────────────────────────────────────
   An icon-only button owns how big its icon is, and says it with an icon-size
   token (spec §8). The kit's icons carry a viewBox and no intrinsic size, so
   with no rule at all an SVG child renders at the default 300x150. This used to
   be `1em`, tying the glyph to the button's font-size; the md button's 16.8px
   was never a size anybody chose, and a glyph box is not type. */

.ui-icon-button > svg {
  width: var(--icon-size-md);
  height: var(--icon-size-md);
}

/* `lg` sizes the GLYPH and deliberately not the button. Where a rail button's hit
   area comes from is the rail's business — it is placement, and `.activity-bar`
   still declares it. Its only consumer is the activity rail (sidebar.tsx), whose
   glyphs go from 24px to 20px here (spec §8). */
.ui-icon-button[data-size='lg'] > svg {
  width: var(--icon-size-lg);
  height: var(--icon-size-lg);
}
```

- [ ] **Step 10: Run the tests and the gates**

Run: `./node_modules/.bin/vitest run src/icon-size-tokens.test.ts src/ui/icon-button.test.tsx src/sidebar.test.tsx`
Expected: all passed.

Run: `node lint-fixtures/check-css-integrity.mjs && node lint-fixtures/check-css-colors.mjs`
Expected: exit 0.

- [ ] **Step 11: Commit**

```bash
git add frontend/src/icon-size-tokens.test.ts frontend/src/styles/tokens.css frontend/src/styles/base.css \
  frontend/src/styles/components/icon-button.css
git commit -m "$(cat <<'EOF'
feat(frontend): icon sizes and the pane gutter are tokens (nocx-9bpeq.2)

IconButton sized its glyphs in px per variant and the default glyph as 1em,
which made the md glyph 16.8px because the button's font-size said so. The
three sizes the terminal-screen spec names are now tokens, and the component
cannot size a glyph any other way; xs goes 13 to 14px, md settles on 16px, and
lg goes 24 to 20px, which is the activity rail, its only consumer.

The pane gutter moves into the token layer at its current 10px. Its value
feeds xterm's column count, so changing it belongs with the alignment test in
nocx-9bpeq.8, not here.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task T3 (nocx-9bpeq.3): Surface roles have an order every theme obeys, and a test that says so

Spec §7 (roles, the four assertions, thresholds) and §3.3 (`--color-danger-surface` is per theme, never a `color-mix`,
ADR-0013 §3.1).

**What is true today (read and measured 2026-09-14):**

- `frontend/src/theme-catalogue.test.ts` is token-level (css-tree over `styles/themes/*.css`, `@vitest-environment node`).
  It already asserts token parity with tokyo-night (`:166-173`), text at 4.5:1 on eight backgrounds (`:175-188`) and
  border/accent at 3:1 (`:190-195`). It has no CIELAB lightness helper.
- Every theme's `--color-canvas` equals `--terminal-background` except `light.css` (canvas `#e6e8ed`, terminal
  `#ffffff`). The terminal pane paints `--color-canvas` (`styles/base.css:459`), the composer paints `--color-canvas`
  (`style.css:185`), the summon stack paints `--color-canvas` (`style.css:202`).
- Measured with a CIELAB (D65) script against today's theme values, terminal ground = `--terminal-background`:

  | Theme            | chrome ΔL\* (≥ 2) | raised ΔL\* (≥ 3) | `--color-danger` on ground |
  | ---------------- | ----------------- | ----------------- | -------------------------- |
  | ayu-dark         | **1.75**          | 8.79              | 6.76                       |
  | catppuccin-latte | 6.01              | 4.90              | 4.80                       |
  | catppuccin-mocha | 6.56              | 9.43              | 7.08                       |
  | dracula          | 7.89              | 9.76              | 4.53                       |
  | gruvbox-dark     | 4.13              | 7.74              | 4.29                       |
  | light            | 13.06             | **0.00**          | 5.74                       |
  | nord             | 4.11              | 7.60              | 3.05                       |
  | one-dark         | 3.40              | 6.48              | 4.38                       |
  | rose-pine        | 2.40              | 6.52              | 6.07                       |
  | solarized-dark   | 4.45              | 5.77              | 3.25                       |
  | solarized-light  | 4.96              | 3.04              | 4.29                       |
  | tokyo-night      | 5.71              | 10.34             | 6.46                       |

  Assertion 2 fails in ayu-dark; assertion 3 fails in light (raised is the same white as the terminal ground).
  Assertion 4 needs `--color-danger` at 4.5:1 on a tint that is itself ΔL\* ≥ 2 from the ground; seven themes cannot
  reach that with their current danger colour at any tint (their danger is below or barely at 4.5:1 on the bare
  ground), so their `--color-danger` moves — the spec's rule is "a theme that fails is fixed by changing that theme's
  values".

**Values (computed, then verified by a second script against all four assertions and the existing text-contrast
floor):**

| Theme            | `--color-danger-surface` | `--color-danger`          | Other change                                 | ΔL\* chrome | ΔL\* raised | ΔL\* surface | text on surface | danger on surface | min text on raised/chrome |
| ---------------- | ------------------------ | ------------------------- | -------------------------------------------- | ----------- | ----------- | ------------ | --------------- | ----------------- | ------------------------- |
| ayu-dark         | `#161319`                | `#f07178` (unchanged)     | `--color-chrome` `#05080d`→`#04070b`         | 2.05        | 8.79        | 2.51         | 9.79            | 6.43              | 4.55                      |
| catppuccin-latte | `#eee8ee`                | `#cf103a` (was `#d20f39`) |                                              | 6.01        | 4.90        | 2.50         | 6.62            | 4.60              | 4.62                      |
| catppuccin-mocha | `#272233`                | unchanged                 |                                              | 6.56        | 9.43        | 2.51         | 10.65           | 6.65              | 4.62                      |
| dracula          | `#332d38`                | `#fe6262` (was `#ff5555`) |                                              | 7.89        | 9.76        | 2.16         | 12.52           | 4.54              | 4.57                      |
| gruvbox-dark     | `#322b2a`                | `#f8634b` (was `#fb4934`) |                                              | 4.13        | 7.74        | 2.13         | 10.11           | 4.54              | 5.32                      |
| light            | `#fdf6f6`                | unchanged                 | `--color-surface-raised` `#ffffff`→`#f3f4f7` | 13.06       | 3.81        | 2.59         | 16.32           | 5.38              | 4.57                      |
| nord             | `#363945`                | `#cf949c` (was `#bf616a`) |                                              | 4.11        | 7.60        | 2.52         | 9.97            | 4.58              | 4.60                      |
| one-dark         | `#313038`                | `#e07b83` (was `#e06c75`) |                                              | 3.40        | 6.48        | 2.33         | 10.33           | 4.55              | 4.55                      |
| rose-pine        | `#211b28`                | unchanged                 |                                              | 2.40        | 6.52        | 2.46         | 12.72           | 5.77              | 6.14                      |
| solarized-dark   | `#0e2f39`                | `#e27067` (was `#dc322f`) |                                              | 4.45        | 5.77        | 2.11         | 11.55           | 4.55              | 4.57                      |
| solarized-light  | `#fbeedc`                | `#cb3231` (was `#dc322f`) |                                              | 4.96        | 3.04        | 2.29         | 11.37           | 4.55              | 4.65                      |
| tokyo-night      | `#231f2a`                | unchanged                 |                                              | 5.71        | 10.34       | 2.50         | 10.00           | 6.10              | 4.57                      |

Method, so a reviewer can re-derive: the surface is the ground mixed toward the (possibly adjusted) danger at the
smallest whole-percent step that reaches ΔL\* ≥ 2 and both contrasts ≥ 4.5:1; a danger that could not reach it was mixed
toward `--color-text` in 2% steps until it could. Where danger changes, `--color-danger-hover` (an rgba of danger at
0.1) changes to the new rgb. Nord's danger moves furthest (it is desaturated toward its near-white text); that is a
visible change to every destructive control in Nord and is called out for the owner in the commit body. Light's raised
surface becomes slightly grey: over its white terminal ground a menu is now findable, and over its `#e6e8ed` canvas it
stays lighter (ΔL\* 4.22).

**Hand-off (assertion 1 is not in this task):** spec §7 assertion 1 — the computed background of a block row, the live
region and the composer equals `--terminal-background` — needs a real layout, and this test file is token-level
(`@vitest-environment node`). It is asserted in a browser by T9 (nocx-9bpeq.9). The CSS that makes it true is not a
theme value either: the terminal pane (`base.css:459`) belongs to T8, which already moves the pane's gutter into the
rows, and the composer (`style.css:185`) and summon stack (`style.css:202`) belong to T7. Only `light` is affected.

**Files:**

- Modify: `frontend/src/theme-catalogue.test.ts` (header `:10-58`, helpers after `:123`, tests after `:195`)
- Modify: all twelve `frontend/src/styles/themes/*.css` — insert `--color-danger-surface` after the `--color-danger:` line
  (ayu-dark `:44`, catppuccin-latte `:43`, catppuccin-mocha `:43`, dracula `:43`, gruvbox-dark `:43`, light `:38`,
  nord `:44`, one-dark `:42`, rose-pine `:44`, solarized-dark `:44`, solarized-light `:45`, tokyo-night `:45`); change
  `--color-danger` and `--color-danger-hover` in the seven themes above; `ayu-dark.css:23` chrome; `light.css:17` raised.

**Interfaces:**

- Consumes: nothing from T2.
- Produces: `--color-danger-surface` in every theme (T6's failed row); thresholds `MIN_CHROME_DL = 2`,
  `MIN_RAISED_DL = 3`, `MIN_FAILURE_DL = 2` exported by nothing — they live in the test, and changing one is the owner's
  decision (spec §7).

**Acceptance Criteria:**

- `theme-catalogue.test.ts` asserts, for every theme: chrome ΔL\* ≥ 2 from `--terminal-background`; surface-raised
  ΔL\* ≥ 3 from it; `--color-text` and `--color-danger` at 4.5:1 on `--color-danger-surface`, which is ΔL\* ≥ 2 from the
  ground. Before the theme changes it fails on ayu-dark (chrome), light (raised) and all twelve (no danger surface).
- Every theme passes, with changes in theme files only; the existing parity, text-contrast and border tests still pass.
- The file header records the four roles, that assertion 1 lives in T9's end-to-end check, and that the semantic colours
  are now gated only as the failed row uses them.

- [ ] **Step 1: Write the failing assertions**

In `frontend/src/theme-catalogue.test.ts`, after `contrastRatio` (`:123`), add:

```ts
/** CIELAB L* (D65) — perceived lightness, so "these two surfaces can be told
 *  apart" is one number per pair rather than a luminance ratio that means
 *  different things at the dark and light ends. */
function lightness(value: string): number {
  const rgb = hexToRGB(value)
  if (rgb === null) return Number.NaN
  const lin = (c: number): number => {
    const s = c / 255
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
  }
  const y = 0.2126729 * lin(rgb[0]) + 0.7151522 * lin(rgb[1]) + 0.072175 * lin(rgb[2])
  const f = y > 216 / 24389 ? Math.cbrt(y) : ((24389 / 27) * y + 16) / 116
  return 116 * f - 16
}

function deltaL(a: string, b: string): number {
  return Math.abs(lightness(a) - lightness(b))
}

/** The terminal screen's one ground (spec §7). History rows, the live region and
 *  the composer all paint it; everything below is measured against it. */
const GROUND = '--terminal-background'

/** Spec §7 thresholds. A theme that fails is fixed by changing that theme's
 *  values; changing a number here is the owner's decision. */
const MIN_CHROME_DL = 2
const MIN_RAISED_DL = 3
const MIN_FAILURE_DL = 2
```

After the border test (`:190-195`), add:

```ts
it.each(themeIds)('%s sets its chrome apart from the terminal ground', (id) => {
  const t = tokensById.get(id)!
  const d = deltaL(t.get('--color-chrome')!, t.get(GROUND)!)
  expect(Number.isNaN(d), `${id}: chrome or ground is not an opaque hex`).toBe(false)
  expect(d, `${id}: ΔL* chrome vs ${GROUND}`).toBeGreaterThanOrEqual(MIN_CHROME_DL)
})

it.each(themeIds)('%s lifts a floating surface off the terminal ground', (id) => {
  const t = tokensById.get(id)!
  const d = deltaL(t.get('--color-surface-raised')!, t.get(GROUND)!)
  expect(Number.isNaN(d), `${id}: surface-raised or ground is not an opaque hex`).toBe(false)
  expect(d, `${id}: ΔL* surface-raised vs ${GROUND}`).toBeGreaterThanOrEqual(MIN_RAISED_DL)
})

it.each(themeIds)('%s paints a failed row that reads as failed and stays legible', (id) => {
  const t = tokensById.get(id)!
  const surface = t.get('--color-danger-surface')
  expect(surface, `${id}: --color-danger-surface is not declared`).toBeDefined()
  const d = deltaL(surface!, t.get(GROUND)!)
  expect(Number.isNaN(d), `${id}: --color-danger-surface is not an opaque hex`).toBe(false)
  expect(d, `${id}: ΔL* danger-surface vs ${GROUND}`).toBeGreaterThanOrEqual(MIN_FAILURE_DL)
  expect(
    contrastRatio(t.get('--color-text')!, surface!),
    `${id}: text on danger-surface`,
  ).toBeGreaterThanOrEqual(AA_TEXT)
  expect(
    contrastRatio(t.get('--color-danger')!, surface!),
    `${id}: danger on danger-surface`,
  ).toBeGreaterThanOrEqual(AA_TEXT)
})
```

In the header, after item 3 (`:39`), add:

```ts
 * 4. **The terminal screen's surface roles** (spec 2026-09-14 §7). Chrome and
 *    floating surfaces measured against `--terminal-background` in CIELAB ΔL*,
 *    and the failed row's `--color-danger-surface` legible for text and for the
 *    danger status word. The fifth statement of that section — that the rows
 *    actually paint the ground — needs a layout and lives in the terminal-screen
 *    end-to-end check (nocx-9bpeq.9).
```

and replace the "**The semantic colours**" paragraph (`:53-57`) with:

```ts
 * **The semantic colours** (`--color-success`, `--color-warning`,
 * `--color-danger`) on the app's backgrounds. Danger is gated only where the
 * terminal screen uses it — on the failed row's own surface (4 above). light.css
 * still puts success at 2.69:1 on the canvas; that is nocx-foyr.
```

- [ ] **Step 2: Run it and see it fail**

Run: `./node_modules/.bin/vitest run src/theme-catalogue.test.ts`
Expected: failures — `ayu-dark sets its chrome apart from the terminal ground` (1.75), `light lifts a floating surface
off the terminal ground` (0), and `paints a failed row…` for all twelve ("--color-danger-surface is not declared").
The existing tests still pass.

- [ ] **Step 3: Set the theme values**

In every theme file, directly after its `--color-danger:` line, add one line with the value from the table, and change
the listed tokens. Exact edits:

```css
/* ayu-dark.css */
--color-chrome: #04070b; /* :23, was #05080d */
--color-danger-surface: #161319; /* after :44 */

/* catppuccin-latte.css */
--color-danger: #cf103a; /* :43, was #d20f39 */
--color-danger-surface: #eee8ee;
--color-danger-hover: rgba(207, 16, 58, 0.1); /* :66 */

/* catppuccin-mocha.css */
--color-danger-surface: #272233; /* after :43 */

/* dracula.css */
--color-danger: #fe6262; /* :43, was #ff5555 */
--color-danger-surface: #332d38;
--color-danger-hover: rgba(254, 98, 98, 0.1); /* :66 */

/* gruvbox-dark.css */
--color-danger: #f8634b; /* :43, was #fb4934 */
--color-danger-surface: #322b2a;
--color-danger-hover: rgba(248, 99, 75, 0.1); /* :66 */

/* light.css */
--color-surface-raised: #f3f4f7; /* :17, was #ffffff */
--color-danger-surface: #fdf6f6; /* after :38 */

/* nord.css */
--color-danger: #cf949c; /* :44, was #bf616a */
--color-danger-surface: #363945;
--color-danger-hover: rgba(207, 148, 156, 0.1); /* :67 */

/* one-dark.css */
--color-danger: #e07b83; /* :42, was #e06c75 */
--color-danger-surface: #313038;
--color-danger-hover: rgba(224, 123, 131, 0.1); /* :65 */

/* rose-pine.css */
--color-danger-surface: #211b28; /* after :44 */

/* solarized-dark.css */
--color-danger: #e27067; /* :44, was #dc322f */
--color-danger-surface: #0e2f39;
--color-danger-hover: rgba(226, 112, 103, 0.1); /* :67 */

/* solarized-light.css */
--color-danger: #cb3231; /* :45, was #dc322f */
--color-danger-surface: #fbeedc;
--color-danger-hover: rgba(203, 50, 49, 0.1); /* :68 */

/* tokyo-night.css */
--color-danger-surface: #231f2a; /* after :45 */
```

(The `/* … */` annotations above are for the reader of this plan; do not paste them into the theme files.) In
`tokyo-night.css`, above the new line, add the one comment the default theme carries for its token set:

```css
/* The failed row's ground (spec 2026-09-14 §3.3). A solid per-theme value,
     never a color-mix of danger into the ground — ADR-0013 §3.1 measured those.
     Chosen as the smallest step from --terminal-background that a person can see
     (ΔL* ≥ 2) while text and the danger status word keep 4.5:1 on it; the test
     in theme-catalogue.test.ts holds every theme to the same numbers. */
```

- [ ] **Step 4: Run the tests and the gates**

Run: `./node_modules/.bin/vitest run src/theme-catalogue.test.ts src/ui/badge.test.tsx src/ui/match-contrast.test.ts`
Expected: all passed.

Run: `node lint-fixtures/check-css-integrity.mjs && node lint-fixtures/check-css-colors.mjs`
Expected: exit 0 (the new token is declared in every theme, so `theme-scope` and parity stay clean).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/theme-catalogue.test.ts frontend/src/styles/themes/*.css
git commit -m "$(cat <<'EOF'
feat(frontend): every theme carries a failed-row surface and keeps its roles apart from the terminal ground (nocx-9bpeq.3)

The terminal-screen spec draws a failed command as a tinted row, and ADR-0013
measured that a color-mix tint does not survive twelve themes, so the tint is
a per-theme token held to numbers: a person can see it (CIELAB dL* >= 2 from
the terminal background) and both text and the danger status word keep 4.5:1
on it. The same test now separates chrome (dL* >= 2) and floating surfaces
(dL* >= 3) from that ground.

Three existing values could not meet it. Ayu's chrome was 1.75 from its ground
and moves one step darker. Light's raised surface was the same white as its
terminal and becomes #f3f4f7. Seven themes' danger colours sat at or below
4.5:1 on their own ground, so no visible tint could keep them legible; each
moves toward its text colour by the smallest step that passes. Nord moves
furthest and its destructive controls will look softer.

Whether the rows actually paint the terminal background is a layout fact and
is asserted by the end-to-end check in nocx-9bpeq.9.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task T4 (nocx-9bpeq.4): the kit gains `Meta` and vanilla emitters for Badge and IconButton, each held to its Solid twin

Spec: `.internal/specs/2026-09-14-terminal-screen-visual-register-design.md` §6 (and §5.5 for the pet, which is
T6's). Depends on T2 (icon-size tokens) landing first; if T2 changes `icon-button.css`, nothing here has to
change, because the vanilla emitter reuses that stylesheet unchanged.

**Deviations from the spec, decided here:**

- The Badge emitter lives in `ui/badge-element.ts`, not `ui/badge.ts` (§6.2 says `ui/badge.ts`). With a
  `badge.ts` beside `badge.tsx`, the bare specifier `./badge` used by `mode-indicator.ts`, `secret-chip.ts`,
  `record-row.tsx`, `operation-row.tsx`, `watch-badge.tsx`, `grouped-rail.tsx` and `ui/index.ts` would resolve
  to the `.ts` file first. The same rule gives `ui/icon-button-element.ts`.
- The bead asks for a unit test that reads COMPUTED styles. jsdom never resolves `var()` (see the header of
  `src/ui/match-contrast.test.ts`), so the computed half runs in a real browser, in
  `e2e/proof-matrix.spec.ts`, by importing `/src/ui/meta.ts` from the vite server the stand already runs. The
  unit test covers the token half the way `src/ui/badge.test.tsx` does.
- Measured while writing this task (2026-09-14): `--color-danger` on `--terminal-background` is below 4.5:1 in
  gruvbox-dark (4.29), nord (3.05), one-dark (4.38), solarized-dark (3.25) and solarized-light (4.29), and
  `--color-accent` is below 4.5:1 in catppuccin-latte (4.34) and solarized-light (3.41). T4 asserts muted and
  dim in every theme, and danger and accent in `tokyo-night` and `light` only (the bead's "a light and a dark
  theme"). The failing themes are a finding for T3, which the spec's §7 does not yet cover (§7 assertion 4
  measures danger on `--color-danger-surface`, not on the ground where the accent word and a cancelled
  block's dim word sit).
- `scan-kit-identities.mjs` does not see vanilla modules today: it reads only `.tsx` JSX attributes. Extending
  it makes `term-line` an identity, because `ui/answer-markdown.ts:260` stamps the scrollback's own row
  class. Simulated on the tree 2026-09-14: the extension adds exactly two `surface-paints-kit` hits
  (`style.css:1312`, `style.css:1351`), both on `.term-line`, and nothing else. `term-line` is argued into
  `NOT_AN_IDENTITY`, which is what that set exists for.

**Files:**

- Create: `frontend/src/ui/meta.ts`
- Create: `frontend/src/ui/meta.test.ts`
- Create: `frontend/src/styles/components/meta.css`
- Modify: `frontend/src/style.css` (one `@import` beside `badge.css`, line 46)
- Create: `frontend/src/ui/badge-element.ts`
- Create: `frontend/src/ui/badge-element.test.tsx`
- Create: `frontend/src/ui/icon-button-element.ts`
- Create: `frontend/src/ui/icon-button-element.test.tsx`
- Create: `frontend/src/test-support/element-shape.ts`
- Create: `frontend/src/cwd-label.ts`
- Create: `frontend/src/cwd-label.test.ts`
- Modify: `frontend/src/scrollback/blocks.ts:712-719` (remove `cwdLabel`, import it), `:750-754` (author mark)
- Modify: `frontend/src/editor.ts:614-620` (`setCwd` uses the shared `cwdLabel`)
- Modify: `frontend/lint-fixtures/scan-kit-identities.mjs` (vanilla `.ts` modules; `term-line` exception)
- Create: `frontend/lint-fixtures/kit-identity-fixture/vanilla-emitter.ts`
- Modify: `frontend/lint-fixtures/check-kit-identities.mjs`
- Create: `frontend/lint-fixtures/css-integrity-fixture/ui/fixture-vanilla.ts`
- Modify: `frontend/lint-fixtures/css-integrity-fixture/styles/surfaces/fixture-surface.css`
- Modify: `frontend/lint-fixtures/gate.sh` (surface-paints-kit hit count 2 → 3)
- Modify: `e2e/proof-matrix.spec.ts` (Meta computed colours, two themes)
- Modify: `frontend/src/ui/README.md` (Meta row; the vanilla emitter rule)

**Interfaces:**

- Consumes: `BadgeTone` from `ui/badge.tsx`; `IconButtonSize` from `ui/icon-button.tsx`; T2's icon-size
  tokens only through `icon-button.css`.
- Produces (T5, T6, T7 rely on these exact names):
  - `ui/meta.ts`: `export type MetaTone = 'muted' | 'dim' | 'danger' | 'accent'`;
    `export type MetaPart = string | { text: string; emphasis?: 'strong' }`;
    `export interface MetaOptions { tone?: MetaTone; column?: 'duration'; title?: string }`;
    `export function createMeta(parts: readonly MetaPart[], opts?: MetaOptions): HTMLSpanElement`;
    `export function updateMeta(el: HTMLSpanElement, parts: readonly MetaPart[], opts?: MetaOptions): void`.
    DOM: `<span class="ui-meta" data-tone="muted|dim|danger|accent" [data-column="duration"] [title]>` holding
    `<span class="ui-meta__part" [data-emphasis="strong"]>` joined by
    `<span class="ui-meta__sep" aria-hidden="true"> · </span>`.
  - `ui/badge-element.ts`: `export interface BadgeElementOptions { text: string; tone?: BadgeTone; variant?: 'solid'; truncate?: boolean; title?: string; testId?: string }`;
    `export function createBadge(opts: BadgeElementOptions): HTMLSpanElement`.
  - `ui/icon-button-element.ts`: `export interface IconButtonElementOptions { ariaLabel: string; icon: () => Element; size?: IconButtonSize; selected?: boolean; square?: boolean; railIndicator?: boolean; disabled?: boolean; title?: string; tabIndex?: number; type?: 'button' | 'submit' | 'reset'; onClick?: (e: MouseEvent) => void; attrs?: Readonly<Record<\`data-${string}\`, string>> }`;
`export function createIconButton(opts: IconButtonElementOptions): HTMLButtonElement`.
  - `test-support/element-shape.ts`: `export interface ElementShape`; `export function elementShape(node: Node): ElementShape | string`;
    `export function assertSameShape(actual: Element, expected: Element): void`.
  - `cwd-label.ts`: `export function cwdLabel(cwd: string): string`.

**Acceptance Criteria:**

- `createMeta` renders `ui-meta` for each tone, a `strong` part and the duration column. `meta.test.ts` asserts
  the DOM contract, and asserts from the shipped CSS and every theme file that muted and dim reach 4.5:1 on
  `--terminal-background` in all twelve themes and danger and accent do in `tokyo-night` and `light`.
  `e2e/proof-matrix.spec.ts` reads the COMPUTED colour of a mounted `Meta` in `tokyo-night` and `light` and
  finds it equal to the resolved token.
- For every variance of Badge and IconButton, the vanilla element and the Solid component's rendered element
  have the same tag, attribute set (classes, `data-*`, ARIA, `title`, `type`, `tabindex`, `disabled`) and child
  structure. A test in each file proves the comparison FAILS when one side differs.
- `scan-kit-identities.mjs` derives identities from vanilla `ui/*.ts` modules (`el.className = …`,
  `el.classList.add(…)`). `ui-meta` and `ui-meta__part` are found. The kit-identity fixture proves the vanilla
  path, and `gate.sh` proves `surface-paints-kit` fires on a surface repainting a vanilla identity.
- `npm run lint` is green on the real tree after the extension. `term-line` is in `NOT_AN_IDENTITY` with its
  reason.
- One `cwdLabel` exists, in `src/cwd-label.ts`, and both `scrollback/blocks.ts` and `editor.ts` import it.
- The author mark is built by `createBadge` (the tests at `scrollback/blocks.test.ts:109`, `:118`, `:909` and
  `scrollback/turn-children.test.ts:167` still pass unchanged).
- `ui/README.md` has a Meta row, rows for the two emitters, and states the parity rule.
- No `.nocx-chip` consumer is migrated here; T6 and T7 do that.

- [ ] **Step 1: Write the failing shape-helper test and the helper's contract**

Create `frontend/src/test-support/element-shape.ts`:

```ts
// ── Element shape, for emitter parity tests ─────────────────────────────────
//
// A kit component emitted twice — once by Solid, once by a vanilla function for
// imperative code that may not use Solid (ADR-0012) — is two implementations of
// one DOM contract. This reduces an element to what that contract is made of:
// the tag, every attribute and value, and the same for each child, so that two
// emitters can be compared for exact agreement. Event listeners are not part of
// it (Solid delegates them; nothing in the DOM says so).

export interface ElementShape {
  tag: string
  attrs: Array<[string, string]>
  children: Array<ElementShape | string>
}

/** The shape of one node: an element's tag, sorted attributes and children,
 *  or `#text:<content>` for a text node. Comment nodes (Solid leaves markers
 *  for some control flow) are dropped. */
export function elementShape(node: Node): ElementShape | string {
  if (node.nodeType === Node.TEXT_NODE) return `#text:${node.textContent ?? ''}`
  const el = node as Element
  const attrs = Array.from(el.attributes)
    .map((a): [string, string] => [a.name, a.value])
    .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0))
  const children = Array.from(el.childNodes)
    .filter((c) => c.nodeType === Node.ELEMENT_NODE || c.nodeType === Node.TEXT_NODE)
    .map(elementShape)
  return { tag: el.tagName.toLowerCase(), attrs, children }
}

/** Throws with both shapes when the two elements are not the same contract. */
export function assertSameShape(actual: Element, expected: Element): void {
  const a = JSON.stringify(elementShape(actual))
  const b = JSON.stringify(elementShape(expected))
  if (a !== b) {
    throw new Error(`emitters disagree:\n  vanilla: ${a}\n  solid:   ${b}`)
  }
}
```

(The helper is exercised by the parity tests in Steps 5 and 8, including the deliberately failing case; it has
no test file of its own because a test of a comparison function that never compares a real emitter proves
nothing the parity tests do not.)

- [ ] **Step 2: Write the failing Meta test**

Create `frontend/src/ui/meta.test.ts`:

```ts
// @vitest-environment jsdom
import { readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { createMeta, updateMeta } from './meta'

const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)
const CSS = resolve(dirname, '../styles/components/meta.css')
const THEMES = resolve(dirname, '../styles/themes')

function token(themeText: string, name: string): string {
  const match = themeText.match(new RegExp(`--${name}\\s*:\\s*([^;]+);`))
  if (!match) throw new Error(`no --${name} in theme`)
  return match[1].trim()
}

function luminance(hex: string): number {
  const channels = hex
    .replace('#', '')
    .match(/../g)!
    .map((c) => parseInt(c, 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2]
}

function contrast(a: string, b: string): number {
  const [x, y] = [luminance(a), luminance(b)]
  const [hi, lo] = x > y ? [x, y] : [y, x]
  return (hi + 0.05) / (lo + 0.05)
}

/** The declaration block of the first rule whose selector is exactly `selector`. */
function ruleFor(cssText: string, selector: string): string {
  for (const block of cssText.split('}')) {
    const [head, body] = block.split('{')
    if (
      head
        ?.trim()
        .split(/\s*,\s*/)
        .includes(selector) &&
      body
    )
      return body
  }
  throw new Error(`no rule for ${selector}`)
}

describe('createMeta — the DOM contract', () => {
  it('renders one ui-meta with a part per fact and a hidden separator between them', () => {
    const el = createMeta(['dev@staging', 'repos/nocx'])
    expect(el.tagName).toBe('SPAN')
    expect(el.className).toBe('ui-meta')
    expect(el.dataset.tone).toBe('muted')
    const parts = el.querySelectorAll('.ui-meta__part')
    expect(Array.from(parts).map((p) => p.textContent)).toEqual(['dev@staging', 'repos/nocx'])
    const seps = el.querySelectorAll('.ui-meta__sep')
    expect(seps).toHaveLength(1)
    expect(seps[0].getAttribute('aria-hidden')).toBe('true')
    expect(el.textContent).toBe('dev@staging · repos/nocx')
  })

  it('a single part carries no separator', () => {
    const el = createMeta(['repos/nocx'])
    expect(el.querySelectorAll('.ui-meta__sep')).toHaveLength(0)
  })

  it.each(['muted', 'dim', 'danger', 'accent'] as const)('states tone %s on data-tone', (tone) => {
    expect(createMeta(['x'], { tone }).dataset.tone).toBe(tone)
  })

  it('marks a strong part, and only that part', () => {
    const el = createMeta([{ text: 'dev@staging', emphasis: 'strong' }, 'repos/nocx'])
    const [host, cwd] = Array.from(el.querySelectorAll<HTMLElement>('.ui-meta__part'))
    expect(host.dataset.emphasis).toBe('strong')
    expect(cwd.hasAttribute('data-emphasis')).toBe(false)
  })

  it('carries the duration column and the title when asked, and neither when not', () => {
    const col = createMeta(['84ms'], { column: 'duration', title: 'Started 19:32:23' })
    expect(col.dataset.column).toBe('duration')
    expect(col.title).toBe('Started 19:32:23')
    const plain = createMeta(['84ms'])
    expect(plain.hasAttribute('data-column')).toBe(false)
    expect(plain.hasAttribute('title')).toBe(false)
  })
})

describe('updateMeta — the same element, restated', () => {
  it('replaces the parts and every option, removing ones no longer asked for', () => {
    const el = createMeta(['3s'], { tone: 'accent', column: 'duration', title: 'running' })
    updateMeta(el, ['exit 1'], { tone: 'danger' })
    expect(el.textContent).toBe('exit 1')
    expect(el.dataset.tone).toBe('danger')
    expect(el.hasAttribute('data-column')).toBe(false)
    expect(el.hasAttribute('title')).toBe(false)
    expect(el.className).toBe('ui-meta')
  })
})

describe('meta.css — tokens only, and legible where the terminal screen puts it', () => {
  const css = readFileSync(CSS, 'utf8')
  const themes = readdirSync(THEMES).filter((f) => f.endsWith('.css'))

  it('one line, the small register, tabular figures', () => {
    const base = ruleFor(css, '.ui-meta')
    expect(base).toContain('font-size: var(--font-size-2xs)')
    expect(base).toContain('font-variant-numeric: tabular-nums')
    expect(base).toContain('white-space: nowrap')
    expect(base).toContain('text-overflow: ellipsis')
  })

  it.each([
    ['muted', 'color-text-muted'],
    ['dim', 'color-text-dim'],
    ['danger', 'color-danger'],
    ['accent', 'color-accent'],
  ])('tone %s paints --%s', (tone, tok) => {
    expect(ruleFor(css, `.ui-meta[data-tone='${tone}']`)).toContain(`color: var(--${tok})`)
  })

  it('a strong part reads at normal text colour', () => {
    expect(ruleFor(css, ".ui-meta__part[data-emphasis='strong']")).toContain(
      'color: var(--color-text)',
    )
  })

  it.each(themes)('%s: muted and dim reach 4.5:1 on the terminal ground', (file) => {
    const text = readFileSync(resolve(THEMES, file), 'utf8')
    const ground = token(text, 'terminal-background')
    expect(contrast(token(text, 'color-text-muted'), ground)).toBeGreaterThanOrEqual(4.5)
    expect(contrast(token(text, 'color-text-dim'), ground)).toBeGreaterThanOrEqual(4.5)
  })

  it.each(['tokyo-night.css', 'light.css'])(
    '%s: danger and accent reach 4.5:1 on the terminal ground',
    (file) => {
      const text = readFileSync(resolve(THEMES, file), 'utf8')
      const ground = token(text, 'terminal-background')
      expect(contrast(token(text, 'color-danger'), ground)).toBeGreaterThanOrEqual(4.5)
      expect(contrast(token(text, 'color-accent'), ground)).toBeGreaterThanOrEqual(4.5)
    },
  )
})
```

- [ ] **Step 3: Run it to verify it fails**

Run (from `frontend/`): `npx vitest run src/ui/meta.test.ts`
Expected: FAIL — `Failed to resolve import "./meta"`.

- [ ] **Step 4: Implement Meta and its stylesheet**

Create `frontend/src/ui/meta.ts`:

```ts
// Meta — the kit's inline fact: where a command ran, how long it took, and the
// status word when there is one (spec 2026-09-14 §6.1). Muted text, one line,
// tabular figures, parts joined by a separator that assistive tech skips.
//
// Vanilla-emitted, like SecretChip and ModeIndicator: the terminal screen that
// uses it is imperative DOM (ADR-0012), and it is built once per block. No
// Solid version exists until a Solid surface needs one.
//
// Identity `ui-meta`; variance on data-tone, data-column and a part's
// data-emphasis. A surface places it and never repaints it (ui/README).

export type MetaTone = 'muted' | 'dim' | 'danger' | 'accent'
export type MetaPart = string | { text: string; emphasis?: 'strong' }

export interface MetaOptions {
  /** The register: muted by default; dim for a quieter fact; danger for a
   *  failure's word; accent for work in progress. */
  tone?: MetaTone
  /** `duration` gives the element the width floor that keeps a column of
   *  durations aligned. */
  column?: 'duration'
  /** Hover detail — the start time on a duration. */
  title?: string
}

const SEPARATOR = ' · '

function fill(el: HTMLSpanElement, parts: readonly MetaPart[], opts: MetaOptions): void {
  el.dataset.tone = opts.tone ?? 'muted'
  if (opts.column === undefined) el.removeAttribute('data-column')
  else el.dataset.column = opts.column
  if (opts.title === undefined) el.removeAttribute('title')
  else el.title = opts.title

  const children: HTMLSpanElement[] = []
  parts.forEach((part, i) => {
    if (i > 0) {
      const sep = document.createElement('span')
      sep.className = 'ui-meta__sep'
      sep.setAttribute('aria-hidden', 'true')
      sep.textContent = SEPARATOR
      children.push(sep)
    }
    const span = document.createElement('span')
    span.className = 'ui-meta__part'
    if (typeof part === 'string') {
      span.textContent = part
    } else {
      span.textContent = part.text
      if (part.emphasis === 'strong') span.dataset.emphasis = 'strong'
    }
    children.push(span)
  })
  el.replaceChildren(...children)
}

export function createMeta(parts: readonly MetaPart[], opts: MetaOptions = {}): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = 'ui-meta'
  fill(el, parts, opts)
  return el
}

/** Restate an existing Meta — the running duration ticks through this, so the
 *  element (and anything placed relative to it) stays put. */
export function updateMeta(
  el: HTMLSpanElement,
  parts: readonly MetaPart[],
  opts: MetaOptions = {},
): void {
  fill(el, parts, opts)
}
```

Create `frontend/src/styles/components/meta.css`:

```css
/* Meta (ui/README table) — the kit's inline fact on the terminal screen: muted
   text, one line, tabular figures (spec 2026-09-14 §6.1). It replaces the
   boxed `.nocx-chip` family; there is deliberately no background, border or
   padding here — metadata is text. Colours are tokens only (ADR-0013 §4). */

.ui-meta {
  display: inline-block;
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
  font-family: var(--font-family-ui);
  font-size: var(--font-size-2xs);
  /* Equal advance widths: a duration that ticks or a column of exit codes must
     not change width digit by digit (the reason .nocx-chip carried the same
     declaration, nocx-6w4z). */
  font-variant-numeric: tabular-nums;
  -webkit-user-select: none;
  user-select: none;
}

.ui-meta[data-tone='muted'] {
  color: var(--color-text-muted);
}

.ui-meta[data-tone='dim'] {
  color: var(--color-text-dim);
}

.ui-meta[data-tone='danger'] {
  color: var(--color-danger);
}

.ui-meta[data-tone='accent'] {
  color: var(--color-accent);
}

/* Where the fact is a safety question — the remote host the pending command
   will run on (spec §5.2) — the part reads at normal text colour. */
.ui-meta__part[data-emphasis='strong'] {
  color: var(--color-text);
}

/* The width floor a column of durations aligns on: the same floor
   `.cmd-header-duration` gave the chip (style.css), carried by the kit now. */
.ui-meta[data-column='duration'] {
  min-width: 3.25rem;
  text-align: end;
}
```

Modify `frontend/src/style.css` — after line 46 (`@import './styles/components/badge.css';`) add:

```css
@import './styles/components/meta.css';
```

(`min-width: 3.25rem` is 52px at the 16px root that `.cmd-header-duration`'s `min-width: 52px` assumed; the
integrity checker forbids px only for font sizes, and a rem keeps the column with the user's zoom.)

- [ ] **Step 5: Run the Meta test to verify it passes**

Run (from `frontend/`): `npx vitest run src/ui/meta.test.ts`
Expected: PASS (12 themes × 1 + 2 + the DOM and CSS cases).

- [ ] **Step 6: Write the failing Badge parity test**

Create `frontend/src/ui/badge-element.test.tsx`:

```tsx
// @vitest-environment jsdom
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'
import { assertSameShape } from '../test-support/element-shape'
import { Badge, type BadgeProps } from './badge'
import { createBadge, type BadgeElementOptions } from './badge-element'

afterEach(cleanup)

/** Every variance Badge has. A variance added to badge.tsx and not here is
 *  a parity hole — add the row. */
const CASES: Array<[string, BadgeElementOptions, BadgeProps]> = [
  ['default', { text: 'agent' }, { children: 'agent' }],
  ['info', { text: 'agent', tone: 'info' }, { children: 'agent', tone: 'info' }],
  ['success', { text: 'ok', tone: 'success' }, { children: 'ok', tone: 'success' }],
  ['warning', { text: 'w', tone: 'warning' }, { children: 'w', tone: 'warning' }],
  ['danger', { text: 'd', tone: 'danger' }, { children: 'd', tone: 'danger' }],
  ['solid', { text: '3', variant: 'solid' }, { children: '3', variant: 'solid' }],
  ['truncate', { text: 'long', truncate: true }, { children: 'long', truncate: true }],
  ['title', { text: 'x', title: 'why' }, { children: 'x', title: 'why' }],
  ['testid', { text: 'x', testId: 't' }, { children: 'x', 'data-testid': 't' }],
]

function solid(props: BadgeProps): Element {
  const { container } = render(() => <Badge {...props} />)
  return container.firstElementChild!
}

describe('createBadge is the same element Badge renders', () => {
  it.each(CASES)('%s', (_name, vanilla, props) => {
    assertSameShape(createBadge(vanilla), solid(props))
  })

  it('the comparison fails when the two emitters disagree', () => {
    expect(() =>
      assertSameShape(
        createBadge({ text: 'x', tone: 'info' }),
        solid({ children: 'x', tone: 'warning' }),
      ),
    ).toThrow(/emitters disagree/)
  })
})
```

- [ ] **Step 7: Run it to verify it fails**

Run (from `frontend/`): `npx vitest run src/ui/badge-element.test.tsx`
Expected: FAIL — `Failed to resolve import "./badge-element"`.

- [ ] **Step 8: Implement createBadge**

Create `frontend/src/ui/badge-element.ts`:

```ts
// createBadge — Badge (badge.tsx) emitted without Solid, for imperative code
// that builds one per block (ADR-0012; spec 2026-09-14 §6.2). The SAME element
// Badge renders, styled by the same badge.css; badge-element.test.tsx holds the
// two emitters to one DOM contract, variance by variance.
//
// Named `-element` rather than `badge.ts`: beside badge.tsx, a `badge.ts` would
// capture every `from './badge'` import in the kit.

import type { BadgeTone } from './badge'

export interface BadgeElementOptions {
  text: string
  tone?: BadgeTone
  variant?: 'solid'
  truncate?: boolean
  title?: string
  testId?: string
}

export function createBadge(opts: BadgeElementOptions): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = 'ui-badge'
  el.dataset.tone = opts.tone ?? 'neutral'
  if (opts.variant !== undefined) el.dataset.variant = opts.variant
  if (opts.truncate === true) el.dataset.truncate = 'true'
  if (opts.title !== undefined) el.title = opts.title
  if (opts.testId !== undefined) el.dataset.testid = opts.testId
  el.textContent = opts.text
  return el
}
```

- [ ] **Step 9: Run it to verify it passes**

Run (from `frontend/`): `npx vitest run src/ui/badge-element.test.tsx`
Expected: PASS (10 tests). If a case fails, the shape message names the attribute that differs — fix the
emitter, never the case.

- [ ] **Step 10: Write the failing IconButton parity test**

Create `frontend/src/ui/icon-button-element.test.tsx`:

```tsx
// @vitest-environment jsdom
import { cleanup, render } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { assertSameShape } from '../test-support/element-shape'
import { CloseIcon, MoreIcon } from './icons'
import { IconButton, type IconButtonProps } from './icon-button'
import { createIconButton, type IconButtonElementOptions } from './icon-button-element'

afterEach(cleanup)

type SolidCase = Omit<IconButtonProps, 'children'> & Record<`data-${string}`, string>

/** Every variance IconButton has, plus the data-* passthrough the vanilla
 *  emitter exposes as `attrs`. */
const CASES: Array<[string, Omit<IconButtonElementOptions, 'icon'>, SolidCase]> = [
  ['default', { ariaLabel: 'Close' }, { ariaLabel: 'Close' }],
  ['xs', { ariaLabel: 'Close', size: 'xs' }, { ariaLabel: 'Close', size: 'xs' }],
  ['sm', { ariaLabel: 'Close', size: 'sm' }, { ariaLabel: 'Close', size: 'sm' }],
  ['lg', { ariaLabel: 'Close', size: 'lg' }, { ariaLabel: 'Close', size: 'lg' }],
  ['selected', { ariaLabel: 'Files', selected: true }, { ariaLabel: 'Files', selected: true }],
  ['square', { ariaLabel: 'Close', square: true }, { ariaLabel: 'Close', square: true }],
  [
    'rail indicator',
    { ariaLabel: 'Files', railIndicator: true },
    { ariaLabel: 'Files', railIndicator: true },
  ],
  ['disabled', { ariaLabel: 'Close', disabled: true }, { ariaLabel: 'Close', disabled: true }],
  [
    'title',
    { ariaLabel: 'Close', title: 'Close (Esc)' },
    { ariaLabel: 'Close', title: 'Close (Esc)' },
  ],
  ['tabIndex', { ariaLabel: 'Close', tabIndex: -1 }, { ariaLabel: 'Close', tabIndex: -1 }],
  [
    'data attrs',
    { ariaLabel: 'Block actions', attrs: { 'data-block-actions': '' } },
    { ariaLabel: 'Block actions', 'data-block-actions': '' },
  ],
]

function solid(props: SolidCase, icon: () => Element): Element {
  const { container } = render(() => <IconButton {...props}>{icon()}</IconButton>)
  return container.firstElementChild!
}

describe('createIconButton is the same element IconButton renders', () => {
  it.each(CASES)('%s', (_name, vanilla, props) => {
    const icon = () => MoreIcon({}) as Element
    assertSameShape(createIconButton({ ...vanilla, icon }), solid(props, icon))
  })

  it('the comparison fails when the two emitters disagree', () => {
    expect(() =>
      assertSameShape(
        createIconButton({ ariaLabel: 'Close', size: 'xs', icon: () => CloseIcon({}) as Element }),
        solid({ ariaLabel: 'Close', size: 'sm' }, () => CloseIcon({}) as Element),
      ),
    ).toThrow(/emitters disagree/)
  })

  it('fires onClick with the event', () => {
    const onClick = vi.fn()
    const btn = createIconButton({
      ariaLabel: 'Close',
      icon: () => CloseIcon({}) as Element,
      onClick,
    })
    btn.click()
    expect(onClick).toHaveBeenCalledTimes(1)
    expect(onClick.mock.calls[0][0]).toBeInstanceOf(MouseEvent)
  })
})
```

- [ ] **Step 11: Run it to verify it fails**

Run (from `frontend/`): `npx vitest run src/ui/icon-button-element.test.tsx`
Expected: FAIL — `Failed to resolve import "./icon-button-element"`.

- [ ] **Step 12: Implement createIconButton**

Create `frontend/src/ui/icon-button-element.ts`:

```ts
// createIconButton — IconButton (icon-button.tsx) emitted without Solid, for
// imperative code that builds one per block (ADR-0012; spec 2026-09-14 §6.2).
// The SAME element IconButton renders, styled by the same icon-button.css;
// icon-button-element.test.tsx holds the two to one DOM contract.
//
// The glyph is a callable: a ui/icons component called outside a root returns
// a detached SVGElement (terminal-content.ts, workspace-menu.ts do the same).
// `attrs` is the data-* passthrough IconButton gets from its rest props — a
// placement or test hook, never appearance.

import type { IconButtonSize } from './icon-button'

export interface IconButtonElementOptions {
  /** Required — an icon-only control with no accessible name is a defect. */
  ariaLabel: string
  icon: () => Element
  size?: IconButtonSize
  selected?: boolean
  square?: boolean
  railIndicator?: boolean
  disabled?: boolean
  title?: string
  tabIndex?: number
  type?: 'button' | 'submit' | 'reset'
  onClick?: (e: MouseEvent) => void
  attrs?: Readonly<Record<`data-${string}`, string>>
}

export function createIconButton(opts: IconButtonElementOptions): HTMLButtonElement {
  const btn = document.createElement('button')
  btn.className = 'ui-icon-button'
  btn.dataset.size = opts.size ?? 'md'
  if (opts.selected === true) btn.setAttribute('aria-selected', 'true')
  if (opts.square === true) btn.dataset.square = 'true'
  if (opts.railIndicator === true) btn.dataset.railIndicator = 'true'
  btn.setAttribute('aria-label', opts.ariaLabel)
  btn.disabled = opts.disabled === true
  // IconButton writes `title={local.title ?? ''}` — an empty attribute is part
  // of the contract, not an absence.
  btn.setAttribute('title', opts.title ?? '')
  if (opts.tabIndex !== undefined) btn.tabIndex = opts.tabIndex
  btn.type = opts.type ?? 'button'
  for (const [name, value] of Object.entries(opts.attrs ?? {})) btn.setAttribute(name, value)
  const onClick = opts.onClick
  if (onClick) btn.addEventListener('click', (e) => onClick(e))
  btn.append(opts.icon())
  return btn
}
```

- [ ] **Step 13: Run it to verify it passes**

Run (from `frontend/`): `npx vitest run src/ui/icon-button-element.test.tsx`
Expected: PASS (13 tests). A mismatch prints both shapes; the usual one is attribute order-independent value
drift such as `type` or `title` — fix the emitter.

- [ ] **Step 14: Commit the emitters**

```bash
git add frontend/src/ui/meta.ts frontend/src/ui/meta.test.ts frontend/src/styles/components/meta.css \
  frontend/src/style.css frontend/src/ui/badge-element.ts frontend/src/ui/badge-element.test.tsx \
  frontend/src/ui/icon-button-element.ts frontend/src/ui/icon-button-element.test.tsx \
  frontend/src/test-support/element-shape.ts
git commit -m "$(cat <<'EOF'
feat(frontend): the kit gains Meta and vanilla Badge and IconButton emitters (nocx-9bpeq.4)

The terminal screen is imperative DOM (ADR-0012) and built one element per
block, so it has been drawing its metadata with a chip family the kit never
owned. Meta is the kit's inline fact - muted text, one line, tabular figures
- and createBadge / createIconButton give imperative code the two kit
controls it needs without a Solid root per block.

Two emitters of one component can drift, so each pair is held to a single DOM
contract by a parity test that compares tag, attributes and children for every
variance, and proves the comparison fails when the sides differ. The computed
colour half of Meta runs in the browser (proof-matrix), because jsdom does not
resolve var().

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 15: Write the failing cwdLabel test**

Create `frontend/src/cwd-label.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { cwdLabel } from './cwd-label'

describe('cwdLabel — the short form a block and the composer both show', () => {
  it.each([
    ['/home/dev/repos/nocx', 'repos/nocx'],
    ['/home/dev/repos/nocx/', 'repos/nocx'],
    ['/srv', 'srv'],
    ['/', '~'],
    ['~', '~'],
    ['', '~'],
    ['   ', '~'],
  ])('%j → %j', (cwd, label) => {
    expect(cwdLabel(cwd)).toBe(label)
  })
})
```

(Every row is today's behaviour of `scrollback/blocks.ts:714-719`, measured in Step 16 before the move —
including `/` → `~`, which the move must not change.)

- [ ] **Step 16: Confirm the table is today's behaviour, then run the test to verify it fails**

Run (from `frontend/`):

```bash
node -e "const f=(cwd)=>{const path=cwd.trim().replace(/\/+$/,'')||'~';const parts=path.split('/').filter(Boolean);if(path==='~'||parts.length===0)return path;return parts.slice(-2).join('/')};for(const c of ['/home/dev/repos/nocx','/home/dev/repos/nocx/','/srv','/','~','','   '])console.log(JSON.stringify(c),JSON.stringify(f(c)))"
```

Expected output (the function copied from `scrollback/blocks.ts:714-719`):

```
"/home/dev/repos/nocx" "repos/nocx"
"/home/dev/repos/nocx/" "repos/nocx"
"/srv" "srv"
"/" "~"
"~" "~"
"" "~"
"   " "~"
```

If any line differs from the table, the table is wrong — correct the row to the measured value. Then:

Run: `npx vitest run src/cwd-label.test.ts`
Expected: FAIL — `Failed to resolve import "./cwd-label"`.

- [ ] **Step 17: Move cwdLabel and point both callers at it**

Create `frontend/src/cwd-label.ts`:

```ts
/**
 * The short form of a working directory that a command block and the composer
 * both show: the last two path segments, `~` for home, and `~` for a directory
 * the shell never reported. ONE derivation — the block header and the composer
 * each carried a copy (scrollback/blocks.ts, editor.ts), which is how two
 * surfaces start naming the same directory two ways.
 */
export function cwdLabel(cwd: string): string {
  const path = cwd.trim().replace(/\/+$/, '') || '~'
  const parts = path.split('/').filter(Boolean)
  if (path === '~' || parts.length === 0) return path
  return parts.slice(-2).join('/')
}
```

Modify `frontend/src/scrollback/blocks.ts`: delete lines 712-719 (the `// ── CWD display` banner and the
`function cwdLabel`), and add to the imports after line 28:

```ts
import { cwdLabel } from '../cwd-label'
import { createBadge } from '../ui/badge-element'
```

Modify `frontend/src/editor.ts` — replace the body of `setCwd` (lines 614-620):

```ts
  /** Update the cwd chip text — the same short form a block header shows. */
  setCwd(cwd: string): void {
    this.cwdChip.textContent = `📁 ${cwdLabel(cwd)}`
  }
```

and add to its imports:

```ts
import { cwdLabel } from './cwd-label'
```

(The emoji stays: T7 removes the chip. T4 moves the derivation only.)

- [ ] **Step 18: Migrate the author mark to createBadge**

Modify `frontend/src/scrollback/blocks.ts` — replace lines 750-755 (the `if (author !== 'shell') { … }` body):

```ts
if (author !== 'shell') {
  const mark = createBadge({ text: author, tone: 'info' })
  mark.dataset.author = author
  chipsRow.appendChild(mark)
}
```

- [ ] **Step 19: Run the moved and touched tests**

Run (from `frontend/`):
`npx vitest run src/cwd-label.test.ts src/scrollback/blocks.test.ts src/scrollback/turn-children.test.ts src/editor.test.ts`
Expected: PASS. `blocks.test.ts:109`, `:118`, `:909` and `turn-children.test.ts:167` assert the author mark by
`.ui-badge[data-author]` and must pass unchanged; `blocks.test.ts:403`'s `📁 user/repos` still passes.

- [ ] **Step 20: Commit the derivation move**

```bash
git add frontend/src/cwd-label.ts frontend/src/cwd-label.test.ts frontend/src/scrollback/blocks.ts frontend/src/editor.ts
git commit -m "$(cat <<'EOF'
refactor(frontend): one cwdLabel, and the author mark from the kit's emitter (nocx-9bpeq.4)

The block header and the composer each derived the short directory label, in
two copies that agreed only because nobody had changed one. It lives in one
module now and both import it. The author mark stops setting ui-badge and
data-tone by hand and asks createBadge for the element Badge renders.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 21: Write the failing kit-identity fixture for vanilla modules**

Create `frontend/lint-fixtures/kit-identity-fixture/vanilla-emitter.ts`:

```ts
// Kit-identity fixture: a vanilla-emitted component (ui/meta.ts, ui/secret-chip.ts
// shape). Its identities are what it stamps with `className =` and
// `classList.add(...)`, and the scanner must derive them the way it derives a
// .tsx file's JSX attributes.
export function createFixtureVanilla(): HTMLElement {
  const root = document.createElement('span')
  root.className = 'ui-fixture-vanilla'
  const part = document.createElement('span')
  part.classList.add('ui-fixture-vanilla__part')
  root.append(part)
  // A selector string is not an identity, in a .ts file as in a .tsx one.
  root.querySelector('.ui-fixture-vanilla-qs')
  return root
}
```

Modify `frontend/lint-fixtures/check-kit-identities.mjs` — after the line
`checkPart('root-and-part.tsx', 'ui-fixture-rp__element', 'ui-fixture-rp')` add:

```js
checkFound('ui-fixture-vanilla', 'vanilla-emitter.ts', 'className = on a vanilla-emitted component')
checkPart('vanilla-emitter.ts', 'ui-fixture-vanilla__part', 'ui-fixture-vanilla')
```

and after `checkAbsent('ui-fixture-qs', 'appears only as a querySelector argument')` add:

```js
checkAbsent('ui-fixture-vanilla-qs', 'appears only as a querySelector argument in a .ts file')
```

Run (from `frontend/`): `node lint-fixtures/check-kit-identities.mjs`
Expected: FAIL — `MISSING: "ui-fixture-vanilla" not found — className = on a vanilla-emitted component`.

- [ ] **Step 22: Extend the scanner**

Modify `frontend/lint-fixtures/scan-kit-identities.mjs`:

1. Replace the `NOT_AN_IDENTITY` declaration (`const NOT_AN_IDENTITY = new Set()`) with:

```js
const NOT_AN_IDENTITY = new Set([
  // `ui/answer-markdown.ts` stamps `term-line` on the table rows it renders into a
  // block body, BECAUSE the scrollback owns that class: the row must be a terminal
  // line for copy, selection and grants (`blockOutputText`, `.term-line[data-granted]`)
  // to treat it as one. The component uses the scrollback's vocabulary rather than
  // owning it; reading the stamp as ownership would make style.css's own
  // `.term-line` rules a kit violation (measured 2026-09-14: exactly two hits,
  // style.css:1312 and :1351, and nothing else).
  'term-line',
])
```

and replace the comment paragraph above it that begins `**Empty, and that is the finished state.**` with:

```js
 * **One entry, argued below.** Its first entry was `kit-scope`, the styling scope no
 * component owned; T15 (nocx-pnbd) deleted the class. The second arrived with vanilla
 * modules (nocx-9bpeq.4), and is a component using a class that another owner defines.
```

2. Replace the file filter in `scanKitIdentities`:

```js
    if (!entry.endsWith('.tsx') || entry.includes('.test.') || entry.includes('.spec.')) {
      continue
    }
```

with:

```js
    const isTsx = entry.endsWith('.tsx')
    // Vanilla-emitted components (meta.ts, secret-chip.ts, mode-indicator.ts) own
    // identities too: the class they stamp on the element they return.
    const isTs = entry.endsWith('.ts') && !entry.endsWith('.d.ts')
    if ((!isTsx && !isTs) || entry.includes('.test.') || entry.includes('.spec.')) {
      continue
    }
```

3. Replace `if (!content.includes('<')) continue` with `if (isTsx && !content.includes('<')) continue`, and in
   the `parse(content, {…})` options replace `jsx: true,` with `jsx: isTsx,`.

4. Immediately before `if (fileRoots.size > 0 || fileParts.size > 0) {` insert:

```js
if (isTs) {
  const add = (cls) => {
    if (NOT_AN_IDENTITY.has(cls)) return
    if (!byClass.has(cls)) byClass.set(cls, new Set())
    byClass.get(cls).add(entry)
    if (cls.includes('__')) fileParts.add(cls)
    else fileRoots.add(cls)
  }
  // el.className = 'a b' / `a ${x}` — the static words only, as for JSX.
  for (const asg of walk(ast, 'AssignmentExpression')) {
    const left = asg.left
    if (
      left.type !== 'MemberExpression' ||
      left.property.type !== 'Identifier' ||
      left.property.name !== 'className'
    ) {
      continue
    }
    const right = asg.right
    if (right.type === 'Literal' && typeof right.value === 'string') words(right.value).forEach(add)
    else if (right.type === 'TemplateLiteral') extractQuasiClasses(right).static.forEach(add)
  }
  // el.classList.add('a', 'b') — literal arguments only.
  for (const call of walk(ast, 'CallExpression')) {
    const callee = call.callee
    if (
      callee.type !== 'MemberExpression' ||
      callee.property.type !== 'Identifier' ||
      callee.property.name !== 'add' ||
      callee.object.type !== 'MemberExpression' ||
      callee.object.property.type !== 'Identifier' ||
      callee.object.property.name !== 'classList'
    ) {
      continue
    }
    for (const arg of call.arguments) {
      if (arg.type === 'Literal' && typeof arg.value === 'string') words(arg.value).forEach(add)
    }
  }
}
```

5. Update the module comment's first paragraph: `by walking the **AST** of every .tsx file in the ui/ directory`
   becomes `by walking the **AST** of every .tsx and vanilla .ts module in the ui/ directory`, and add a bullet
   under the "Only class names that appear as **static** values" paragraph: `In a .ts module: static words
assigned to \`.className\` and literal arguments to \`.classList.add\`.`

Run (from `frontend/`): `node lint-fixtures/check-kit-identities.mjs`
Expected: `OK — … classes across … files, 1 undetermined expression(s)`.

- [ ] **Step 23: Prove rule 3 fires on a vanilla identity (gate fixture)**

Create `frontend/lint-fixtures/css-integrity-fixture/ui/fixture-vanilla.ts`:

```ts
// Rule-3 fixture for a vanilla-emitted component: its identity is derived from
// `className =`, so a surface repainting it must be reported exactly like a
// surface repainting fixture-widget.tsx.
export function createFixtureVanilla(): HTMLElement {
  const el = document.createElement('span')
  el.className = 'fixture-vanilla'
  return el
}
```

Append to `frontend/lint-fixtures/css-integrity-fixture/styles/surfaces/fixture-surface.css`:

```css
/* TIER A for a VANILLA component — reported. `fixture-vanilla` is stamped by
   ui/fixture-vanilla.ts with `className =`, so it is a kit identity by the same
   derivation, and colour is appearance (nocx-9bpeq.4). */
.fixture-host > .fixture-vanilla {
  color: red;
}
```

(`red` is a colour literal, which the colour checker would report — but the colour checker's fixture run is
`--dir=lint-fixtures`, and this file is already a fixture of deliberate violations; if `check-css-colors.mjs`
is run over it in the gate and the count assertions there are exact, use `padding: 2px;` instead, which rule 3
reports as appearance too. Check with Step 24's run.)

Modify `frontend/lint-fixtures/gate.sh` — replace:

```sh
integrity_kit_hits=$(echo "$integrity_check" | grep -c '"rule":"surface-paints-kit"' || true)
if [ "$integrity_kit_hits" -ne 2 ]; then
  echo "CSS INTEGRITY GATE FAILED — expected exactly 2 surface-paints-kit hits (tier A + tier B), got ${integrity_kit_hits}"
  exit 1
fi
```

with:

```sh
integrity_kit_hits=$(echo "$integrity_check" | grep -c '"rule":"surface-paints-kit"' || true)
if [ "$integrity_kit_hits" -ne 3 ]; then
  echo "CSS INTEGRITY GATE FAILED — expected exactly 3 surface-paints-kit hits (tier A + tier B + a vanilla-emitted identity), got ${integrity_kit_hits}"
  exit 1
fi

# The vanilla hit specifically: a scanner that stopped reading .ts modules would
# still produce the other two.
if ! echo "$integrity_check" | grep -q 'fixture-vanilla'; then
  echo "CSS INTEGRITY GATE FAILED — rule 3 did not report a surface repainting a vanilla-emitted identity"
  exit 1
fi
```

- [ ] **Step 24: Run the gates on the fixture and on the real tree**

Run (from `frontend/`): `sh lint-fixtures/gate.sh`
Expected: last line `OK — all 10 lint rules fired; …`.

Run (from `frontend/`): `npm run lint`
Expected: exit 0. With `term-line` excepted, the simulated extension produced no other rule-3 hit and no new
`no-inline-markup` hit (no `.tsx` outside `ui/` names a class a vanilla module stamps — checked 2026-09-14). If
lint reports a hit anyway, it is a surface painting or duplicating a kit identity that was invisible until now:
fix that CSS or markup in this task and name it in the commit body; do not add it to `NOT_AN_IDENTITY` without
the same kind of written argument `term-line` carries.

- [ ] **Step 25: Browser proof of Meta's computed colour**

Modify `e2e/proof-matrix.spec.ts` — append at the end of the file:

```ts
// ═══════════════════════════════════════════════════════════════════════════════
// Meta — the computed colour is the theme's token (nocx-9bpeq.4)
// ═══════════════════════════════════════════════════════════════════════════════
//
// jsdom resolves no var(), so meta.test.ts can only prove the rule names the
// token. This proves the element a surface will actually mount computes to the
// token's value, in a dark and a light theme, without remounting. The module is
// imported from the vite server the stand runs — the same file the app bundles.
test.describe('Meta computed colour', () => {
  for (const theme of ['tokyo-night', 'light']) {
    test(`muted and danger compute to their tokens in ${theme}`, async ({ page }) => {
      const result = await page.evaluate(async (themeId) => {
        document.documentElement.setAttribute('data-theme', themeId)
        const { createMeta } =
          (await import('/src/ui/meta.ts')) as typeof import('../frontend/src/ui/meta')
        const host = document.createElement('div')
        document.body.append(host)
        const muted = createMeta(['repos/nocx'])
        const danger = createMeta(['exit 1'], { tone: 'danger' })
        host.append(muted, danger)
        const probe = (tokenName: string): string => {
          const p = document.createElement('span')
          p.style.color = `var(${tokenName})`
          host.append(p)
          return getComputedStyle(p).color
        }
        const out = {
          muted: getComputedStyle(muted).color,
          danger: getComputedStyle(danger).color,
          mutedToken: probe('--color-text-muted'),
          dangerToken: probe('--color-danger'),
          fontSize: getComputedStyle(muted).fontSize,
          whiteSpace: getComputedStyle(muted).whiteSpace,
        }
        host.remove()
        return out
      }, theme)
      expect(result.muted).toBe(result.mutedToken)
      expect(result.danger).toBe(result.dangerToken)
      expect(result.muted).not.toBe(result.danger)
      expect(result.whiteSpace).toBe('nowrap')
      expect(Number.parseFloat(result.fontSize)).toBeGreaterThan(0)
    })
  }
})
```

(`p.style.color` in a test probe is not product code; the inline-style lint does not scan `e2e/`.) Run:
`PW_PROJECTS=chromium e2e/run-in-container.sh e2e/proof-matrix.spec.ts -g "Meta computed colour"`
Expected: 2 passed. If `import('/src/ui/meta.ts')` 404s, the stand is serving a build rather than vite dev —
read `e2e/stand.ts` for the served root and use the path it serves `src/` under; do not replace the browser
proof with a jsdom one.

- [ ] **Step 26: README rows**

Modify `frontend/src/ui/README.md` — in the "Components we write" table, after the `**ModeIndicator**` row,
add (keeping the table's column alignment; `npm run format` re-pads it):

```md
| **Meta** | `meta.ts` (vanilla-emitted, the terminal screen) | `ui-meta` + `__part/__sep` — the kit's inline fact: where a command ran, how long it took, and a status word when there is one; muted text, one line, tabular figures, parts joined by a separator assistive tech skips (spec 2026-09-14 §6.1). Replaces the boxed `.nocx-chip` family. | `data-tone`: muted \| dim \| danger \| accent; `data-column="duration"` (the width floor a column of durations aligns on); `data-emphasis="strong"` on a part (normal text colour — the remote host in the composer) |
| **createBadge** | `badge-element.ts` (vanilla emitter of Badge) | `ui-badge` — the element Badge renders, for imperative code built once per block | Badge's variance, as options |
| **createIconButton** | `icon-button-element.ts` (vanilla emitter of IconButton) | `ui-icon-button` — the element IconButton renders; the glyph is a callable returning a ui/icons element; `attrs` passes data-* hooks | IconButton's variance, as options |
```

and add a section after `## Identity is what a component renders, not what it is spelled` (at the end of the
file):

```md
## A component emitted twice

Imperative code that may not use Solid (ADR-0012 — the scrollback, the editor) still uses the kit. For a
control it builds once per block, the kit ships a vanilla emitter beside the Solid component: `createBadge`
in `badge-element.ts`, `createIconButton` in `icon-button-element.ts`. Both emitters render the same element
and read the same stylesheet.

Two emitters of one component are two implementations waiting to diverge, so **every pair has a parity test**
(`*-element.test.tsx`): for every variance, `test-support/element-shape.ts` compares tag, attributes and
children, and a case proves the comparison fails when the sides differ. A variance added to one emitter and not
the other fails it. For something rare and long-lived instead — a menu while it is open, the composer's
controls — mount the Solid component as a render island (`render()` / `createComponent`, disposed by whoever
removes its host) rather than writing a second emitter.

`scan-kit-identities.mjs` reads vanilla modules too: a class a `ui/*.ts` module stamps with `className =` or
`classList.add` is a kit identity, so rule 3 protects it from surfaces exactly as it protects JSX.
```

Run (from `frontend/`): `npx prettier --write src/ui/README.md` then `npx prettier --check src/ui/README.md`
Expected: `All matched files use Prettier code style!`

- [ ] **Step 27: Run everything T4 touched, then commit**

Run (from `frontend/`):
`npx vitest run src/ui src/cwd-label.test.ts src/scrollback src/editor.test.ts && npx vitest run --config lint-fixtures/vitest.config.lint-fixtures.mjs && npm run lint && npm run typecheck`
Expected: all PASS, lint exit 0, typecheck exit 0.

```bash
git add frontend/lint-fixtures/scan-kit-identities.mjs frontend/lint-fixtures/kit-identity-fixture/vanilla-emitter.ts \
  frontend/lint-fixtures/check-kit-identities.mjs frontend/lint-fixtures/css-integrity-fixture/ui/fixture-vanilla.ts \
  frontend/lint-fixtures/css-integrity-fixture/styles/surfaces/fixture-surface.css frontend/lint-fixtures/gate.sh \
  e2e/proof-matrix.spec.ts frontend/src/ui/README.md
git commit -m "$(cat <<'EOF'
build(frontend): kit identities include vanilla-emitted modules (nocx-9bpeq.4)

The identity scanner read only JSX attributes in ui/*.tsx, so every vanilla
component - SecretChip, ModeIndicator, BlockReceipt, and now Meta - was
invisible to rule 3, and a surface could repaint them with every gate green.
It now also derives what a ui/*.ts module stamps with className and
classList.add, and the gate proves rule 3 fires on such an identity.

The one class that reading surfaced is term-line, which answer-markdown.ts
stamps because the scrollback owns it; it is argued into NOT_AN_IDENTITY
rather than letting style.css's own row rules read as kit violations. Meta's
computed colour is proven in the browser in two themes, and the README states
the parity rule every emitter pair keeps.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task T5 (nocx-9bpeq.5): no glyph stands in for an icon — the block's ⋮ and every × become kit icon buttons, and the block menu is the kit's ContextMenu

Spec: §3.1 (⋮ and its menu), §6.2–§6.3. **Depends on T4** (`createIconButton`) — the bead today depends only on
T1; add `br dep add nocx-9bpeq.5 nocx-9bpeq.4`. T5 keeps the ⋮ always visible; revealing it on
hover/selection/focus is T6.

**Measured before planning (2026-09-14):**

- Glyph-as-icon sites in non-test `frontend/src`: ⋮ `scrollback/blocks.ts:978`; × `tab.tsx:312`,
  `tab-strip.tsx:680`, `grant.ts:184`, `recovery-notice.tsx:151`, `unreconciled-notice.tsx:133`,
  `integration/notice.tsx:167`; ✕ `banner.tsx:67`; ⚠ and 🔒 `ui/secret-chip.ts:58-62`; 📁 `scrollback/blocks.ts:773`,
  `editor.ts:347`, `editor.ts:619`. Also `+` as the adopt icon at `tab.tsx:297` (not in the checker's set, fixed
  here by hand because it is the same defect). The bead named seven; the tree has thirteen.
- Not icons, and not flagged by the rule's scope: `agent-status.ts:23` (`'✳'`, compared against Claude's screen),
  `notify/notifications-panel.tsx:351` (`×${count}` in a `title` — a multiplication sign), a regexp in
  `overview/overview-model.ts:309`.
- The block menu (`buildOverflowMenu`, `blocks.ts:969-1269`) is still a second implementation of
  `ui/context-menu.tsx`. Moving onto the kit needs four things the kit does not have: a right-aligned anchor
  (the block menu hangs from the ⋮'s right edge), an opener whose own pointerdown is not "outside" (the ⋮
  toggles), items that report async work in place (`Copying…`/`Loading…`, nocx-v13pd — covered by
  `blocks.test.ts` "says it is working while it fetches"), and a height cap with internal scroll (the
  nocx-vnirv.2 contract, today in `style.css:1518-1534` for the block menu only). Plus a stable per-item hook
  (`data-item-id`) because six e2e specs and ~60 unit assertions select items by action.
- `check-menu-icons.mjs` requires an `icon` on every `{id, label, onSelect}` literal, so each block menu item gets
  a mark.
- Rule shape: a standalone AST checker with a baseline and an updater, like `check-menu-icons.mjs` — the newest
  checker pattern here, and one whose baseline carries a reason per entry. The three 📁 sites are baselined with
  the reason "removed by nocx-9bpeq.6/.7"; T6 and T7 shrink the baseline to empty.

**Files:**

- Modify: `frontend/src/ui/context-menu.tsx` (`align`, `anchor`, `busyLabel`, `data-item-id`)
- Modify: `frontend/src/ui/context-menu.test.tsx`
- Modify: `frontend/src/styles/components/context-menu.css` (height cap + scroll; busy item)
- Modify: `frontend/src/scrollback/blocks.ts:955-1269` (⋮ + menu), `:925-927` (`placeHeaderChip`)
- Modify: `frontend/src/style.css:1492-1553` (delete `.cmd-overflow-*`)
- Modify: `frontend/src/terminal-content.ts:3049,3065,3096` (the open-menu guard)
- Modify tests (selector migration): `frontend/src/scrollback/blocks.test.ts`, `frontend/src/scrollback/restored-block.test.ts`,
  `frontend/src/scrollback/turn-children.test.ts`, `frontend/src/terminal-content.test.ts`,
  `frontend/src/ui/menu-geometry.test.ts` (comment), `e2e/agent-ask.spec.ts`, `e2e/agent-dump.spec.ts`,
  `e2e/agent-refusal-stop.spec.ts`, `e2e/ask-about-a-running-command.spec.ts`,
  `e2e/ask-about-full-screen-program.spec.ts`, `e2e/agent-whole-sentence.spec.ts`
- Modify: `frontend/src/tab.tsx:297,312`, `frontend/src/tab-strip.tsx:680`, `frontend/src/banner.tsx:67`,
  `frontend/src/recovery-notice.tsx:151`, `frontend/src/unreconciled-notice.tsx:133`,
  `frontend/src/integration/notice.tsx:167`, `frontend/src/grant.ts:177-196`
- Modify: `frontend/src/ui/secret-chip.ts`, `frontend/src/styles/components/secret-chip.css`, `frontend/src/secret-chip.test.ts`
- Create: `frontend/lint-fixtures/check-glyph-icons.mjs`, `frontend/lint-fixtures/check-glyph-icons.test.mjs`,
  `frontend/lint-fixtures/update-glyph-icons-baseline.mjs`, `frontend/lint-fixtures/glyph-icons-baseline.json`,
  `frontend/lint-fixtures/glyph-icons-fixture/glyphs.tsx`
- Modify: `frontend/package.json` (`lint` chain, `lint:glyph-icons`, `baseline:glyph-icons-update`),
  `frontend/lint-fixtures/gate.sh`
- Create: `e2e/block-actions-keyboard.spec.ts`
- Modify: `frontend/src/ui/README.md` (ContextMenu variance)

**Interfaces:**

- Consumes: `createIconButton` from `ui/icon-button-element.ts` (T4); `MoreIcon`, `CloseIcon`, `PlusIcon`,
  `CopyIcon`, `SquareIcon`, `PinIcon`, `FileIcon`, `ArrowDownUpIcon`, `LockIcon`, `AlertTriangleIcon` from
  `ui/icons`; `ContextMenu` from `ui/context-menu.tsx`.
- Produces (T6 relies on these):
  - `ContextMenuItem.busyLabel?: string` and `ContextMenuItem.onSelect: () => void | Promise<void>`;
    `ContextMenuProps.align?: 'start' | 'end'`; `ContextMenuProps.anchor?: HTMLElement`; each item renders
    `data-item-id="<id>"`.
  - The block actions button: `button.ui-icon-button[data-block-actions]`, `data-size="xs"`,
    `aria-label="Block actions"`; its menu: `.ui-context-menu[data-testid="block-actions-menu"]` with item ids
    `stop`, `grant`, `copy-command`, `copy-output`, `copy-all`, `dump`, `wrap`.
  - `lint-fixtures/check-glyph-icons.mjs`: `export function scanSource(file, source)`,
    `export function scanTree(dir, base)`, `export function violationKey(v)`.

**Acceptance Criteria:**

- `node lint-fixtures/check-glyph-icons.mjs` fails on a ⋮ × ✕ ⚠ or `\p{Extended_Pictographic}` string used as
  element text (JSX text, a string child of a JSX element, a `.textContent`/`.innerText` assignment) in non-test
  `frontend/src`, including `scrollback/`, `editor.ts` and `terminal-content.ts`. It runs in `npm run lint`,
  `gate.sh` proves it fires on the fixture and is silent on the real tree, and its baseline holds exactly the
  three 📁 sites with their reason (T6/T7 empty it).
- The block actions button is `createIconButton` + `MoreIcon`, keyboard-focusable, named "Block actions", and
  opens the kit `ContextMenu`. `e2e/block-actions-keyboard.spec.ts` opens it with Enter from the focused
  button, walks the items with the arrow keys, closes it with Escape, and finds focus back on the button.
- Paired failure path: the same e2e opens the menu from a block whose ⋮ sits at the bottom of a short viewport,
  and the whole menu is inside the viewport with 8px clearance.
- Every current menu behaviour survives, each with its existing test migrated: the item set per block kind and
  state (stop only while active, grant only when available, dump only for a settled answer), the async copy
  that reports work and refuses on a missing record, wrap's effective-state label, close on outside
  pointerdown, on Escape, on picking, on `nocx:block-settled`, and on `overflowMenuClosers` teardown, and a
  second press on ⋮ closing an open menu.
- `.cmd-overflow-btn`, `.cmd-overflow-menu` and `.cmd-overflow-menu-item` exist nowhere in `frontend/src`,
  `e2e/` or CSS.
- The secret chip shows `LockIcon`/`AlertTriangleIcon` instead of 🔒/⚠, and still differs between intact and
  damaged by glyph as well as colour.

- [ ] **Step 1: Write the failing kit ContextMenu tests for the four new variances**

Modify `frontend/src/ui/context-menu.test.tsx` — append inside `describe('ContextMenu', () => { … })`, before
its closing `})`:

```tsx
it('stamps each item with its id, so a caller can address a row by what it does', () => {
  render(() => <ContextMenu open x={10} y={20} items={ITEMS} onClose={() => undefined} />)
  expect(menuItems().map((i) => i.dataset.itemId)).toEqual(['copy', 'reveal'])
})

it("align='end' hangs the menu from x as its RIGHT edge, through the shared clamp", () => {
  const rects = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(
    () =>
      ({
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: 160,
        bottom: 80,
        width: 160,
        height: 80,
      }) as DOMRect,
  )
  try {
    render(() => (
      <ContextMenu open align="end" x={600} y={100} items={ITEMS} onClose={() => undefined} />
    ))
    const el = menu()!
    const expected = clampMenuPosition(
      { x: 600 - 160, y: 100 },
      { width: 160, height: 80 },
      { width: window.innerWidth, height: window.innerHeight },
    )
    expect({
      left: Number.parseFloat(el.style.left),
      top: Number.parseFloat(el.style.top),
    }).toEqual(expected)
  } finally {
    rects.mockRestore()
  }
})

it('a pointerdown on the anchor is not outside — the opener toggles, it does not reopen', () => {
  const close = vi.fn()
  const anchor = document.createElement('button')
  document.body.append(anchor)
  render(() => <ContextMenu open anchor={anchor} x={10} y={20} items={ITEMS} onClose={close} />)
  fireEvent.pointerDown(anchor)
  expect(close).not.toHaveBeenCalled()
  fireEvent.pointerDown(document.body)
  expect(close).toHaveBeenCalledTimes(1)
  anchor.remove()
})

it('an item with busyLabel reports the work in place and closes when it settles', async () => {
  const close = vi.fn()
  let release: () => void = () => {}
  const work = new Promise<void>((resolve) => {
    release = resolve
  })
  const onSelect = vi.fn(() => work)
  render(() => (
    <ContextMenu
      open
      x={10}
      y={20}
      items={items({ copy: { id: 'copy', label: 'Copy path', busyLabel: 'Copying…', onSelect } })}
      onClose={close}
    />
  ))
  fireEvent.click(menuItems()[0])
  expect(onSelect).toHaveBeenCalledTimes(1)
  const busy = menuItems()[0]
  expect(busy.disabled).toBe(true)
  expect(busy.dataset.busy).toBe('')
  expect(busy.textContent).toBe('Copying…')
  expect(close).not.toHaveBeenCalled()

  release()
  await vi.waitFor(() => expect(close).toHaveBeenCalledTimes(1))
})

it('an item without busyLabel keeps the order the focus fix needs: close first, then act', () => {
  const order: string[] = []
  render(() => (
    <ContextMenu
      open
      x={10}
      y={20}
      items={items({
        copy: { id: 'copy', label: 'Copy path', onSelect: () => order.push('select') },
      })}
      onClose={() => order.push('close')}
    />
  ))
  fireEvent.click(menuItems()[0])
  expect(order).toEqual(['close', 'select'])
})
```

Run (from `frontend/`): `npx vitest run src/ui/context-menu.test.tsx`
Expected: FAIL — the new cases fail (`dataset.itemId` undefined; `align`, `anchor`, `busyLabel` are not props —
typecheck errors surface as test failures in vite-plugin-solid only for runtime behaviour, so expect assertion
failures on `itemId`, the left position, `close` called once on the anchor pointerdown, and `disabled`).

- [ ] **Step 2: Implement the kit variances**

Modify `frontend/src/ui/context-menu.tsx`:

1. In `ContextMenuItem`, replace `onSelect: () => void` with:

```ts
  /** The action. It may return a promise — but the menu only WAITS for one when
   *  the item declares `busyLabel`; otherwise it closes first and acts after, the
   *  order the next overlay's focus restore depends on (see releaseFocus). */
  onSelect: () => void | Promise<void>
  /**
   * The item does async work the person must see happen — copying a stored answer,
   * loading a dump (nocx-v13pd). The menu stays open, the row is disabled and reads
   * this label, and the menu closes when the work settles either way. A control that
   * looks clicked and does nothing reads as broken.
   */
  busyLabel?: string
```

2. In `ContextMenuProps`, after `y: number` add:

```ts
  /**
   * Which edge of the menu `x` names. `start` (default) is the left edge — a menu
   * opened at a pointer. `end` is the right edge — a menu hanging from a control's
   * right side, like a block's ⋮. Either way the position goes through the shared clamp.
   */
  align?: 'start' | 'end'
  /**
   * The element that opened the menu. A pointerdown on it is not "outside": the opener
   * is a toggle, and closing on its pointerdown would let the click that follows reopen
   * what the person meant to close.
   */
  anchor?: HTMLElement
```

3. Change the import line to `import { For, Show, createEffect, createSignal, onCleanup, type Component } from 'solid-js'`.

4. At the start of `ContextMenu`'s body, after `let opener: HTMLElement | null = null`, add:

```ts
/** The id of the item whose async work is in flight, if any. */
const [busy, setBusy] = createSignal<string | null>(null)
```

5. In the positioning effect, replace `{ x: props.x, y: props.y },` inside `clampMenuPosition(` with:

```ts
      { x: props.align === 'end' ? props.x - rect.width : props.x, y: props.y },
```

6. In the dismissal effect's `onPointerDown`, replace the body with:

```ts
const el = element
if (!(e.target instanceof Node)) return
if (el?.contains(e.target)) return
if (props.anchor?.contains(e.target)) return
props.onClose()
```

7. Replace the item `<button …>` (the whole element inside `<For>`) with:

```tsx
<button
  type="button"
  class="ui-context-menu__item"
  role="menuitem"
  data-item-id={item.id}
  disabled={busy() === item.id}
  data-busy={busy() === item.id ? '' : undefined}
  onClick={() => {
    if (busy() !== null) return
    if (item.busyLabel === undefined) {
      releaseFocus()
      props.onClose()
      void item.onSelect()
      return
    }
    setBusy(item.id)
    void Promise.resolve(item.onSelect()).finally(() => {
      setBusy(null)
      releaseFocus()
      props.onClose()
    })
  }}
>
  <span class="ui-context-menu__icon" aria-hidden="true">
    <Show when={item.icon} keyed>
      {(Icon) => <Icon />}
    </Show>
  </span>
  <span class="ui-context-menu__label">{busy() === item.id ? item.busyLabel : item.label}</span>
</button>
```

Modify `frontend/src/styles/components/context-menu.css` — in `.ui-context-menu`, after `box-shadow: …;` add:

```css
/* A menu taller than the viewport must still be fully reachable (nocx-vnirv.2):
     the component clamps the shell's corner through menu-geometry.ts, and this cap
     is the other half — the rows scroll within the menu instead of running past the
     window's edge. EDGE_MARGIN_PX is 8, so the cap leaves the clearance the clamp
     enforces. Owned by the kit for every menu; it lived on the block menu alone. */
max-height: calc(100vh - 16px);
overflow-y: auto;
```

and append:

```css
/* An item doing the work it was picked for (ContextMenuItem.busyLabel): it says so
   in its label, and it cannot be picked twice. */
.ui-context-menu__item:disabled {
  cursor: default;
  color: var(--color-text-muted);
}
```

Run (from `frontend/`): `npx vitest run src/ui/context-menu.test.tsx && npx tsc --noEmit -p tsconfig.json`
Expected: PASS; typecheck exit 0 (existing callers return `void`, which satisfies `void | Promise<void>`).

- [ ] **Step 3: Commit the kit change**

```bash
git add frontend/src/ui/context-menu.tsx frontend/src/ui/context-menu.test.tsx frontend/src/styles/components/context-menu.css
git commit -m "$(cat <<'EOF'
feat(frontend): ContextMenu hangs from an edge, toggles from its opener and reports async work (nocx-9bpeq.5)

The block menu is still a second implementation of the kit's menu because the
kit could not do four things it needs: hang from a control's right edge, let
the control that opened it close it again, show an item's async work in place,
and cap its height with internal scroll. The kit grows those as variance -
align, anchor, busyLabel and the height cap - and stamps each row with its id
so a caller addresses a row by what it does rather than by its label.

An item without busyLabel keeps the close-then-act order the next overlay's
focus restore depends on; only a declared async item makes the menu wait.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 4: Migrate the block menu's unit tests first (they go red)**

Run this selector migration (from the repo root) — specific patterns first:

```bash
files="frontend/src/scrollback/blocks.test.ts frontend/src/scrollback/restored-block.test.ts \
  frontend/src/scrollback/turn-children.test.ts frontend/src/terminal-content.test.ts"
sed -i \
  -e "s/\.cmd-overflow-menu-item\[data-action=\"\([a-z-]*\)\"\]/.ui-context-menu__item[data-item-id=\"\1\"]/g" \
  -e "s/\.cmd-overflow-menu \.cmd-overflow-menu-item/[data-testid=\"block-actions-menu\"] .ui-context-menu__item/g" \
  -e "s/'\.cmd-overflow-menu-item'/'[data-testid=\"block-actions-menu\"] .ui-context-menu__item'/g" \
  -e "s/'\.cmd-overflow-menu'/'[data-testid=\"block-actions-menu\"]'/g" \
  -e "s/\.cmd-overflow-btn/[data-block-actions]/g" \
  $files
grep -n "cmd-overflow" $files
```

Expected: the final `grep` prints only the hand-edit sites below. Edit each by hand:

- `blocks.test.ts` "closes menu on outside click" (~`:1068-1100`): replace the `setTimeout` wait and
  `document.body.click()` with `document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))`
  (the kit closes on pointerdown). If jsdom lacks `PointerEvent`, use
  `fireEvent.pointerDown(document.body)` from `@solidjs/testing-library`, which `ui/context-menu.test.tsx` already does.
- `blocks.test.ts` "says it is working while it fetches" (~`:3037-3061`): the item lookup becomes
  `menu.querySelectorAll<HTMLButtonElement>('.ui-context-menu__item')` then `.find((b) => b.textContent === 'Copy output')`;
  the three assertions stay (`dataset.busy === ''`, `disabled`, label changed) — the kit now provides them.
- `blocks.test.ts` the clamp describe (`~:2575-2706`): the menu is portalled by Solid, so the tests read
  `document.querySelector('[data-testid="block-actions-menu"]')`. Replace the anchor arithmetic
  `{ x: buttonRect.right - menuRect.width, y: buttonRect.bottom + 2 }` with the same value (the block passes
  `x: btnRect.right, y: btnRect.bottom + 2, align: 'end'`, which the kit turns into exactly that). Delete
  "measures the menu OUT OF FLOW…" and "a menu taller than the viewport scrolls WITHIN the shell — the CSS
  contract": the first guarded the imperative menu's `style.position = 'fixed'` ordering, which no longer exists
  (the kit's shell is fixed by `context-menu.css`), and the second read `.cmd-overflow-menu` out of `style.css`;
  both properties are now asserted in a browser by Step 11's e2e (viewport containment) and are the kit's.
  Replace the `getBoundingClientRect` stub's class test `classList.contains('cmd-overflow-menu')` wherever it
  remains with `classList.contains('ui-context-menu')`.
- `blocks.test.ts` "opens at body level with fixed positioning…" (~`:2682-2699`): assert
  `menu.closest('body')` is `document.body` and that `menu.parentElement !== el` (portalled, not in the block);
  drop `menu.style.position` (the kit sets no inline position) and keep the `scrollTo` spy.
- `blocks.test.ts:2339` and `:2346` `lastElementChild?.classList.contains('cmd-overflow-btn')` (already rewritten
  to `[data-block-actions]` inside a string that is not a selector): make them
  `lastElementChild?.hasAttribute('data-block-actions')`.
- `blocks.test.ts:3544` (an array of expected class names containing `'cmd-overflow-btn'`): read the test; the
  ⋮ no longer carries that class — remove the entry and add an assertion that the header's right group ends with
  an element that has `data-block-actions`.
- `turn-children.test.ts:169`: `:scope > .cmd-header [data-block-actions]` (the sed does it; verify).
- `terminal-content.test.ts:9656-9680` and `:12179-12182` (synthetic nodes): `frozenButton.className = 'cmd-overflow-btn'`
  → `frozenButton.setAttribute('data-block-actions', '')`; `menu.className = 'cmd-overflow-menu'` →
  `menu.className = 'ui-context-menu'; menu.dataset.testid = 'block-actions-menu'`;
  `menuItem.className = 'cmd-overflow-menu-item'` → `menuItem.className = 'ui-context-menu__item'`.
- Any `document.querySelectorAll('[data-testid="block-actions-menu"]').forEach((m) => m.remove())` cleanup
  (the sed produces it from `.cmd-overflow-menu` cleanups): replace the body with a call that closes through the
  owner — in `blocks.test.ts` import nothing new and use
  `document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))`, which the open kit
  menu answers by disposing itself; removing a Solid-portalled node by hand leaves its root alive.

Run (from `frontend/`): `npx vitest run src/scrollback/blocks.test.ts src/scrollback/restored-block.test.ts src/scrollback/turn-children.test.ts src/terminal-content.test.ts`
Expected: FAIL — the ⋮ is still `.cmd-overflow-btn` with no `data-block-actions`, so `querySelector` returns null.

- [ ] **Step 5: Rebuild the block menu on the kit**

Modify `frontend/src/scrollback/blocks.ts`:

1. Imports — add after the T4 imports:

```ts
import { createComponent } from 'solid-js'
import { render } from 'solid-js/web'
import { ContextMenu, type ContextMenuItem } from '../ui/context-menu'
import { createIconButton } from '../ui/icon-button-element'
import { ArrowDownUpIcon, CopyIcon, FileIcon, MoreIcon, PinIcon, SquareIcon } from '../ui/icons'
```

and remove `import { clampMenuPosition } from '../ui/menu-geometry'` (the kit clamps now; verify no other use with
`grep -n clampMenuPosition frontend/src/scrollback/blocks.ts` → no output).

2. `placeHeaderChip` (`:925-927`):

```ts
function placeHeaderChip(right: Element, chip: Element): void {
  right.insertBefore(chip, right.querySelector('[data-block-actions]'))
}
```

3. Replace the whole `buildOverflowMenu` function (from `function buildOverflowMenu(` to its final `return btn`
   and closing `}`) with:

```ts
function buildOverflowMenu(
  blockEl: HTMLElement,
  command: string,
  answerText?: AnswerTextSource,
  dump?: DumpSource,
  running?: RunningBlockActions,
): HTMLElement {
  /** Disposes the open menu's Solid root, or null while closed. The menu is a
   *  render island: mounted on open, disposed on close (spec 2026-09-14 §6.3). */
  let dispose: (() => void) | null = null

  const closeMenu = (): void => {
    const d = dispose
    dispose = null
    d?.()
  }

  const btn = createIconButton({
    size: 'xs',
    ariaLabel: 'Block actions',
    icon: () => MoreIcon({}) as Element,
    attrs: { 'data-block-actions': '' },
    onClick: (e) => {
      e.stopPropagation()
      e.preventDefault()
      if (dispose !== null) {
        closeMenu()
        return
      }
      openMenu()
    },
  })

  overflowMenuClosers.set(blockEl, closeMenu)
  const onBlockSettled = (): void => {
    closeMenu()
    blockEl.removeEventListener('nocx:block-settled', onBlockSettled)
  }
  blockEl.addEventListener('nocx:block-settled', onBlockSettled)

  // WHERE A BLOCK'S OUTPUT COMES FROM, AND WHY THE TWO KINDS DIFFER (nocx-v13pd).
  // A COMMAND block copies what the terminal DREW: the rows in the DOM are the
  // artefact. An ANSWER block copies what was RECORDED: the DOM is a rendering of
  // the markdown, so a copy scraped from it would quietly differ from what the
  // model said. Copying an answer is therefore async — the item reports the work
  // (busyLabel) — and a fetch that comes back empty REFUSES rather than falling
  // back to the painted text.
  const isAnswer = (): boolean => blockEl.dataset.blockKind === 'ask'

  const storedAnswer = async (): Promise<string | null> => {
    const entryId = blockEl.dataset.entryId
    if (!entryId || !answerText) return null
    return answerText(entryId)
  }

  const refuseCopy = (): void => {
    showToast({
      level: 'warning',
      message: 'The stored answer is not available, so nothing was copied.',
    })
  }

  /** The command as the block shows it: once history.record acks, the MASKED
   *  command in data-recorded-command (ADR-0021). */
  const intent = (): string => blockEl.getAttribute('data-recorded-command') ?? command

  // The label names the EFFECTIVE wrap state: the attribute answers when it is
  // there, and the rendered style answers when the setting decided (see the
  // history of this item in git for the full argument).
  const wrapOn = (): boolean => {
    const attr = blockEl.getAttribute('data-wrap')
    if (attr === 'on') return true
    if (attr === 'off') return false
    const out = blockEl.querySelector<HTMLElement>('.cmd-output')
    return out ? getComputedStyle(out).whiteSpace.startsWith('pre-wrap') : false
  }

  function items(): ContextMenuItem[] {
    const list: ContextMenuItem[] = []
    const answerSettled =
      dump !== undefined &&
      isAnswer() &&
      blockEl.dataset.turnState !== undefined &&
      blockEl.dataset.turnState !== '' &&
      blockEl.dataset.turnState !== 'waiting'
    if (answerSettled) {
      list.push({
        id: 'dump',
        label: 'Show dump',
        icon: FileIcon,
        busyLabel: 'Loading…',
        onSelect: async () => {
          const entryId = blockEl.dataset.entryId
          if (!dump || !entryId) return
          try {
            const result = await dump(entryId)
            const host = document.createElement('div')
            document.body.appendChild(host)
            mountDumpPanel(host, { dump: result, copy: copyToClipboard })
          } catch {
            showToast({ level: 'danger', message: 'Could not load the model dump' })
          }
        },
      })
    }
    if (running?.toggleGrant && (running.grantsAvailable?.() ?? true)) {
      list.push({
        id: 'grant',
        label: running.isGranted?.(blockEl) ? 'Unmark' : 'Ask about this block',
        icon: PinIcon,
        onSelect: () => running.toggleGrant?.(blockEl),
      })
    }
    // Stopping is the only liveness-bound action; granting is not.
    if (running && running.isActive(blockEl)) {
      list.push({
        id: 'stop',
        label: 'Stop',
        icon: SquareIcon,
        onSelect: () => {
          if (running.isActive(blockEl)) running.stop()
        },
      })
    }
    list.push({
      id: 'copy-command',
      label: 'Copy command',
      icon: CopyIcon,
      onSelect: () => clipboardFallback(intent()),
    })
    if (isAnswer()) {
      list.push(
        {
          id: 'copy-output',
          label: 'Copy output',
          icon: CopyIcon,
          busyLabel: 'Copying…',
          onSelect: async () => {
            const stored = await storedAnswer()
            if (stored === null) refuseCopy()
            else clipboardFallback(stored)
          },
        },
        {
          id: 'copy-all',
          label: 'Copy all',
          icon: CopyIcon,
          busyLabel: 'Copying…',
          // The same source as Copy output, deliberately: two items on one block
          // reading one thing from two places is how they start to disagree.
          onSelect: async () => {
            const stored = await storedAnswer()
            if (stored === null) refuseCopy()
            else clipboardFallback(`${intent()}\n${stored}`)
          },
        },
      )
    } else {
      list.push(
        {
          id: 'copy-output',
          label: 'Copy output',
          icon: CopyIcon,
          onSelect: () => clipboardFallback(blockOutputText(blockEl)),
        },
        {
          id: 'copy-all',
          label: 'Copy all',
          icon: CopyIcon,
          onSelect: () => clipboardFallback(`${intent()}\n${blockOutputText(blockEl)}`),
        },
      )
    }
    list.push({
      id: 'wrap',
      label: wrapOn() ? 'Do not wrap' : 'Wrap lines',
      icon: ArrowDownUpIcon,
      onSelect: () => blockEl.setAttribute('data-wrap', wrapOn() ? 'off' : 'on'),
    })
    return list
  }

  function openMenu(): void {
    // Right-aligned to the button, below it: the kit turns `align: 'end'` into
    // `x - width` and clamps through menu-geometry.ts (nocx-vnirv.2).
    const rect = btn.getBoundingClientRect()
    const host = document.createElement('div')
    dispose = render(
      () =>
        createComponent(ContextMenu, {
          open: true,
          align: 'end',
          anchor: btn,
          x: rect.right,
          y: rect.bottom + 2,
          items: items(),
          onClose: closeMenu,
          'data-testid': 'block-actions-menu',
        }),
      host,
    )
  }

  return btn
}
```

Before replacing, read the current function once more against this listing and carry over anything in it not
present above (the item ORDER today is: dump, grant, stop, copy command, copy output, copy all, wrap — kept).
The long comments on the old menu's positioning, out-of-flow measurement and click-listener timing are dropped
with the code they explained.

4. Delete `style.css` lines 1492-1553 (the `/* ── Block overflow menu (P2-9) ── */` banner and the five
   `.cmd-overflow-*` rules). Verify: `grep -n "cmd-overflow" frontend/src/style.css` → no output.

5. `terminal-content.ts:3049`, `:3065`, `:3096`: replace `document.querySelector('.cmd-overflow-menu')` with
   `document.querySelector('.ui-context-menu')`. Any open kit menu owns Escape, not only the block's — the global
   keydown listener is registered at mount (`:3158`) and runs before the menu's own (registered at open), so the
   guard still sees the menu before it disposes itself.

Run (from `frontend/`): `npx vitest run src/scrollback src/terminal-content.test.ts src/ui/context-menu.test.tsx`
Expected: PASS. Two likely failures and their fixes: (a) a test that opens the menu and ends leaves a Solid root
open — close it with Escape in its `afterEach` as Step 4 says; (b) `clipboardFallback`/`copyToClipboard`/
`mountDumpPanel` not in scope — they are module-level in `blocks.ts` today, check the import block.

- [ ] **Step 6: Migrate the e2e selectors**

Run (from the repo root):

```bash
specs="e2e/agent-ask.spec.ts e2e/agent-dump.spec.ts e2e/agent-refusal-stop.spec.ts \
  e2e/ask-about-a-running-command.spec.ts e2e/ask-about-full-screen-program.spec.ts e2e/agent-whole-sentence.spec.ts"
sed -i \
  -e "s/\.cmd-overflow-menu-item\[data-action=\"\([a-z-]*\)\"\]/.ui-context-menu__item[data-item-id=\"\1\"]/g" \
  -e "s/'\.cmd-overflow-menu-item'/'[data-testid=\"block-actions-menu\"] .ui-context-menu__item'/g" \
  -e "s/'\.cmd-overflow-menu'/'[data-testid=\"block-actions-menu\"]'/g" \
  -e "s/\.cmd-overflow-btn/[data-block-actions]/g" \
  $specs
grep -rn "cmd-overflow" e2e frontend/src
```

Expected: no output from `grep`.

- [ ] **Step 7: Write the failing keyboard e2e (with its paired viewport case)**

Create `e2e/block-actions-keyboard.spec.ts`:

```ts
import { expect, promptReady, test } from './harness'

/**
 * e2e: A BLOCK'S ACTIONS ARE A KEYBOARD CONTROL (nocx-9bpeq.5, spec 2026-09-14 §3.1).
 *
 * ADR-0008 makes blocks a keyboard-first ledger, so the ⋮ that holds a block's
 * actions has to work without a pointer: it is a real button with a name, Enter
 * opens the kit's menu with the first row focused, the arrow keys walk it, Escape
 * closes it and gives focus back to the button.
 *
 * How focus ARRIVES on the button is block navigation's job (nocx-4ff.5) and is
 * not what this proves, so the button is focused programmatically — every step
 * after that is a key.
 */

const BLOCK = '.pane.active .cmd-block:not(.cmd-block-running)'
const ACTIONS = '[data-block-actions]'
const MENU = '[data-testid="block-actions-menu"]'
const ITEM = `${MENU} .ui-context-menu__item`

async function runCommand(page: import('./harness').Page, text: string): Promise<void> {
  await page.keyboard.type(text)
  await page.keyboard.press('Enter')
}

test.describe('block actions from the keyboard', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
  })

  test('Enter opens the menu, arrows walk it, Escape closes it and returns focus', async ({
    page,
  }) => {
    await runCommand(page, 'echo block-actions-keyboard')
    const block = page.locator(BLOCK).filter({ hasText: 'echo block-actions-keyboard' }).last()
    await expect(block).toBeVisible({ timeout: 15_000 })

    const actions = block.locator(ACTIONS)
    await expect(actions).toHaveAttribute('aria-label', 'Block actions')
    await actions.focus()
    await expect(actions).toBeFocused()

    await page.keyboard.press('Enter')
    const items = page.locator(ITEM)
    await expect(page.locator(MENU)).toBeVisible()
    await expect(items.first()).toBeFocused()
    await expect(items.first()).toHaveAttribute('data-item-id', 'copy-command')

    await page.keyboard.press('ArrowDown')
    await expect(items.nth(1)).toBeFocused()
    await page.keyboard.press('End')
    await expect(items.last()).toHaveAttribute('data-item-id', 'wrap')
    await expect(items.last()).toBeFocused()

    await page.keyboard.press('Escape')
    await expect(page.locator(MENU)).toHaveCount(0)
    await expect(actions).toBeFocused()

    // Every row carries its mark (check-menu-icons' rule, seen in a browser).
    await page.keyboard.press('Enter')
    const marks = await page.$$eval(`${ITEM} .ui-context-menu__icon svg`, (svgs) => svgs.length)
    expect(marks).toBe(await items.count())
    await page.keyboard.press('Escape')
  })

  test('opened from the bottom of a short viewport, the whole menu stays inside it', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 900, height: 420 })
    for (let i = 0; i < 6; i++) await runCommand(page, `echo filler-${i}`)
    await runCommand(page, 'echo bottom-block')
    const block = page.locator(BLOCK).filter({ hasText: 'echo bottom-block' }).last()
    await expect(block).toBeVisible({ timeout: 15_000 })

    const actions = block.locator(ACTIONS)
    await actions.focus()
    await page.keyboard.press('Enter')
    const menu = page.locator(MENU)
    await expect(menu).toBeVisible()

    const box = (await menu.boundingBox())!
    const viewport = page.viewportSize()!
    expect(box.x).toBeGreaterThanOrEqual(8)
    expect(box.y).toBeGreaterThanOrEqual(8)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width - 8)
    expect(box.y + box.height).toBeLessThanOrEqual(viewport.height - 8)
    await page.keyboard.press('Escape')
  })
})
```

Run (from the repo root) against the tree BEFORE Step 5 to confirm it can fail: `git stash` is shared across
worktrees (see the memory note) — instead check out the Step 3 commit in a scratch worktree, or simply run the
spec now and, if Step 5 is already in, temporarily revert `data-block-actions` in `blocks.ts`:

`PW_PROJECTS=chromium e2e/run-in-container.sh e2e/block-actions-keyboard.spec.ts`
Expected (without Step 5): FAIL — `locator('[data-block-actions]')` resolves to 0 elements.

- [ ] **Step 8: Run the keyboard e2e and the migrated specs green**

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/block-actions-keyboard.spec.ts e2e/agent-ask.spec.ts e2e/agent-dump.spec.ts e2e/agent-refusal-stop.spec.ts e2e/ask-about-a-running-command.spec.ts e2e/ask-about-full-screen-program.spec.ts e2e/agent-whole-sentence.spec.ts e2e/context-menu.spec.ts`
Expected: all passed. `context-menu.spec.ts` must stay green (the kit change touched every menu). If a spec is
red only in the container and layout-sensitive, read AGENTS.md's container note before touching it.

- [ ] **Step 9: Commit the block menu**

```bash
git add frontend/src/scrollback/blocks.ts frontend/src/style.css frontend/src/terminal-content.ts \
  frontend/src/scrollback/blocks.test.ts frontend/src/scrollback/restored-block.test.ts \
  frontend/src/scrollback/turn-children.test.ts frontend/src/terminal-content.test.ts \
  e2e/agent-ask.spec.ts e2e/agent-dump.spec.ts e2e/agent-refusal-stop.spec.ts \
  e2e/ask-about-a-running-command.spec.ts e2e/ask-about-full-screen-program.spec.ts \
  e2e/agent-whole-sentence.spec.ts e2e/block-actions-keyboard.spec.ts
git commit -m "$(cat <<'EOF'
refactor(frontend): a block's actions are the kit's IconButton and ContextMenu (nocx-9bpeq.5)

The block header drew a raw button holding the character U+22EE, and its menu
was a second implementation of the kit's: its own DOM, its own positioning,
its own click-and-Escape listeners and its own CSS, kept in step with
ContextMenu by a comment. The button is now createIconButton with MoreIcon,
and the menu is ContextMenu mounted as a render island on open and disposed
on close, hanging from the button's right edge through the shared clamp.

Every behaviour the old menu had is kept and its test migrated: the item set
per kind and state, the async copy that reports its work and refuses on a
missing record, the effective wrap label, and closing on outside press,
Escape, pick, settle and teardown. Rows are addressed by data-item-id. A new
e2e drives the menu with keys alone and proves a menu opened at the bottom of
a short viewport stays inside it.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 10: The other glyphs — ×, ✕, +, and the secret chip's 🔒/⚠**

Solid sites (replace the text child with the icon element; keep every prop):

- `frontend/src/tab.tsx:312`: `{'\u00d7'}` → `<CloseIcon />`; `tab.tsx:297` `{'+'}` → `<PlusIcon />`; add
  `CloseIcon, PlusIcon` to the existing `import { PinIcon } from './ui/icons'`.
- `frontend/src/tab-strip.tsx:680`: `{'\u00d7'}` → `<CloseIcon />` (add to its `./ui/icons` import).
- `frontend/src/banner.tsx:67`: `✕` → `<CloseIcon />`.
- `frontend/src/recovery-notice.tsx:151`, `frontend/src/unreconciled-notice.tsx:133`,
  `frontend/src/integration/notice.tsx:167`: `{'×'}` → `<CloseIcon />` (each file: add
  `import { CloseIcon } from './ui/icons'`, or `'../ui/icons'` for `integration/notice.tsx`).

`frontend/src/grant.ts:177-196` — replace `dismissButton`:

```ts
  private dismissButton(itemId: string): HTMLButtonElement {
    // The kit's icon button, not a context-menu row class borrowed for a lone ×:
    // the row's identity belongs to ContextMenu, and a glyph is not an icon.
    const button = createIconButton({
      size: 'xs',
      ariaLabel: 'Dismiss this mark',
      icon: () => CloseIcon({}) as Element,
      attrs: { 'data-action': 'dismiss-grant', 'data-item-id': itemId },
      onClick: (event) => {
        event.stopPropagation()
        this.blocks = this.blocks.filter((grant) => grant.itemId !== itemId)
        this.repaintBlocks()
        this.updateChip()
        this.onChange?.(this.blocks)
        if (this.blocks.length === 0 && this.automaticBlock === null) this.panel.hide()
        else this.renderPanel()
      },
    })
    button.addEventListener('mousedown', (event) => event.stopPropagation())
    return button
  }
```

with imports `import { createIconButton } from './ui/icon-button-element'` and `import { CloseIcon } from './ui/icons'`.
(`grant.test.ts:173,195,354` and `terminal-content.test.ts:11928` select by `[data-action="dismiss-grant"]` and
keep working.)

`frontend/src/ui/secret-chip.ts` — replace the `GLYPH` table and the lock construction:

```ts
import { AlertTriangleIcon, LockIcon } from './icons'

/** The mark per variant, as a kit icon. A lock for a reference to a secret; a
 *  warning for the one state where the bytes are NOT what the name says, so the
 *  mark says it too rather than leaving it to the colour (WCAG 1.4.1). */
const GLYPH: Record<SecretChipVariant, { name: 'lock' | 'warning'; icon: () => Element }> = {
  resolved: { name: 'lock', icon: () => LockIcon({}) as Element },
  unresolved: { name: 'lock', icon: () => LockIcon({}) as Element },
  damaged: { name: 'warning', icon: () => AlertTriangleIcon({}) as Element },
}
```

and in `buildChip`:

```ts
const lock = document.createElement('span')
lock.className = 'ui-secret-chip__lock'
lock.setAttribute('aria-hidden', 'true')
lock.dataset.glyph = GLYPH[variant].name
lock.append(GLYPH[variant].icon())
```

`frontend/src/styles/components/secret-chip.css` — replace the `.ui-secret-chip__lock` rule with:

```css
.ui-secret-chip__lock {
  display: inline-flex;
  vertical-align: -0.125em;
  margin-right: 4px;
  opacity: 0.85;
}

/* The slot sizes the glyph: the kit's icons carry a viewBox and no size, and an
   unsized svg is 0x0 in WebKit (nocx-8830c). One em of the chip's own type. */
.ui-secret-chip__lock svg {
  width: 1em;
  height: 1em;
}
```

`frontend/src/secret-chip.test.ts` — the glyph comparison (`:64`) becomes:

```ts
const glyph = (el: HTMLElement) =>
  el.querySelector<HTMLElement>('.ui-secret-chip__lock')?.dataset.glyph
expect(glyph(intact)).toBe('lock')
expect(glyph(damaged)).toBe('warning')
expect(glyph(damaged)).not.toBe(glyph(intact))
expect(damaged.querySelector('.ui-secret-chip__lock svg')).not.toBeNull()
```

Run (from `frontend/`): `npx vitest run src/secret-chip.test.ts src/grant.test.ts src/tab.test.tsx src/tab-strip.test.tsx src/banner.test.tsx src/recovery-notice.test.tsx src/unreconciled-notice.test.tsx src/integration`
Expected: PASS. A test asserting the old text (`'×'`, `'✕'`, `'+'`) as `textContent` is asserting the glyph;
change it to assert the accessible name and the presence of an `svg` child, and name the test in the commit body.

- [ ] **Step 11: Write the glyph checker's own tests (red)**

Create `frontend/lint-fixtures/check-glyph-icons.test.mjs`:

```js
import { describe, expect, it } from 'vitest'
import { scanSource } from './check-glyph-icons.mjs'

/**
 * The glyph-icons checker's own tests (nocx-9bpeq.5).
 *
 * A character standing in for an icon — ⋮ × ✕ ⚠ or an emoji — renders, passes
 * every other gate and looks like a second icon vocabulary beside ui/icons. The
 * rule: such a string, used as an element's TEXT, is a violation. Both directions
 * are asserted; a rule that reported a multiplication sign in a title, or a
 * constant compared against a program's screen, would be turned off.
 */

describe('must trip', () => {
  it('JSX text that is a glyph', () => {
    const hits = scanSource('s.tsx', 'export const A = () => <button>✕</button>')
    expect(hits.map((h) => h.glyph)).toEqual(['✕'])
  })

  it('a string literal child of a JSX element', () => {
    const hits = scanSource('s.tsx', "export const A = () => <b>{'\\u00d7'}</b>")
    expect(hits.map((h) => h.glyph)).toEqual(['×'])
  })

  it('a textContent assignment, in a .ts module, with an emoji at the start', () => {
    const hits = scanSource(
      's.ts',
      'export function f(el: HTMLElement, x: string) { el.textContent = `📁 ${x}` }',
    )
    expect(hits.map((h) => h.glyph)).toEqual(['📁'])
  })

  it('an innerText assignment of a vertical ellipsis', () => {
    const hits = scanSource(
      's.ts',
      "export function f(el: HTMLElement) { el.innerText = '\\u22EE' }",
    )
    expect(hits.map((h) => h.glyph)).toEqual(['⋮'])
  })

  it('a textContent: property in an object literal', () => {
    const hits = scanSource('s.ts', "export const o = { textContent: '⚠ broken' }")
    expect(hits.map((h) => h.glyph)).toEqual(['⚠'])
  })
})

describe('must stay silent', () => {
  it('a glyph in a title attribute (a multiplication sign)', () => {
    expect(
      scanSource('s.tsx', 'export const A = (n: number) => <b title={`x ×${n}`}>x</b>'),
    ).toEqual([])
  })

  it('a constant compared against a screen', () => {
    expect(
      scanSource('s.ts', "const IDLE = '✳'\nexport const idle = (s: string) => s === IDLE"),
    ).toEqual([])
  })

  it('a regular expression', () => {
    expect(scanSource('s.ts', 'export const r = /^[>❯›»$#%λ⯈▶]{1,2}$/u')).toEqual([])
  })

  it('an icon component', () => {
    expect(
      scanSource(
        's.tsx',
        "import { CloseIcon } from './ui/icons'\nexport const A = () => <b><CloseIcon /></b>",
      ),
    ).toEqual([])
  })

  it('a comment', () => {
    expect(scanSource('s.ts', '// the old ✕ button\nexport const a = 1')).toEqual([])
  })
})
```

Run (from `frontend/`): `npx vitest run --config lint-fixtures/vitest.config.lint-fixtures.mjs lint-fixtures/check-glyph-icons.test.mjs`
Expected: FAIL — `Cannot find module './check-glyph-icons.mjs'`.

- [ ] **Step 12: Implement the checker, its baseline, updater and fixture**

Create `frontend/lint-fixtures/check-glyph-icons.mjs`:

```js
#!/usr/bin/env node
/**
 * Glyph-icons checker — no character stands in for an icon (nocx-9bpeq.5).
 *
 * The kit's icons are components in src/ui/icons. A ⋮ × ✕ ⚠ or an emoji written as
 * an element's TEXT is a second icon vocabulary: it renders at the font's whim, has
 * no size the kit controls, and passed every other gate here — the block header's ⋮
 * and its 📁 were built inside files the raw-control lint exempts (ADR-0012's
 * imperative code), which is exactly why this rule exempts no path.
 *
 * What counts as element text, deliberately:
 *   - JSX text, and a string/template literal that is a direct child of a JSX element;
 *   - the right side of an assignment to `.textContent` or `.innerText`;
 *   - the value of a `textContent:` property in an object literal.
 * A glyph anywhere else — a title attribute, a constant compared against a program's
 * screen, a regular expression — is not an icon and is not reported. A table of glyphs
 * assigned later through a lookup is not seen; documented, not chased.
 *
 * Policy: violations are baselined with a reason; one the baseline does not list is
 * new and fails lint. The baseline may only shrink — regenerate with
 * `npm run baseline:glyph-icons-update`, which refuses to grow and copies reasons.
 *
 * Invocation (from frontend/):
 *   node lint-fixtures/check-glyph-icons.mjs              # scan src/, baseline applied
 *   node lint-fixtures/check-glyph-icons.mjs <file...>    # exactly these files, NO baseline
 */
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from '@typescript-eslint/parser'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'glyph-icons-baseline.json')

/** The first glyph in `text` that is standing in for an icon, or null. */
const GLYPH = /[\u22EE\u00D7\u2715\u26A0]|\p{Extended_Pictographic}/u
function glyphIn(text) {
  const m = GLYPH.exec(text)
  return m ? m[0] : null
}

function walk(node, visit, parent = null) {
  if (!node || typeof node !== 'object') return
  visit(node, parent)
  for (const key of Object.keys(node)) {
    if (key === 'parent') continue
    const child = node[key]
    if (Array.isArray(child)) {
      for (const c of child) if (c && typeof c.type === 'string') walk(c, visit, node)
    } else if (child && typeof child.type === 'string') {
      walk(child, visit, node)
    }
  }
}

/** The static text of a string literal or a template's quasis, or null. */
function literalText(node) {
  if (!node) return null
  if (node.type === 'Literal' && typeof node.value === 'string') return node.value
  if (node.type === 'TemplateLiteral') return node.quasis.map((q) => q.value.cooked ?? '').join('')
  return null
}

function isTextMember(node) {
  return (
    node?.type === 'MemberExpression' &&
    !node.computed &&
    node.property.type === 'Identifier' &&
    (node.property.name === 'textContent' || node.property.name === 'innerText')
  )
}

/**
 * @returns {Array<{file:string,line:number,glyph:string,context:string,reason:string}>}
 *   A file that fails to parse yields one `context: 'PARSE'` entry — fail closed.
 */
export function scanSource(file, source) {
  let ast
  try {
    ast = parse(source, {
      ecmaVersion: 'latest',
      sourceType: 'module',
      ecmaFeatures: { jsx: file.endsWith('.tsx') },
      loc: true,
      range: true,
    })
  } catch (err) {
    return [
      { file, line: 0, glyph: '', context: 'PARSE', reason: String(err.message).split('\n')[0] },
    ]
  }
  const hits = []
  const report = (node, text, context) => {
    const glyph = glyphIn(text)
    if (glyph === null) return
    hits.push({ file, line: node.loc.start.line, glyph, context, reason: '' })
  }
  walk(ast, (node, parent) => {
    if (node.type === 'JSXText') {
      report(node, node.value, 'jsx-text')
      return
    }
    if (node.type === 'JSXExpressionContainer' && parent?.type === 'JSXElement') {
      const text = literalText(node.expression)
      if (text !== null) report(node, text, 'jsx-child')
      return
    }
    if (node.type === 'AssignmentExpression' && isTextMember(node.left)) {
      const text = literalText(node.right)
      if (text !== null) report(node, text, 'text-assignment')
      return
    }
    if (
      node.type === 'Property' &&
      !node.computed &&
      ((node.key.type === 'Identifier' && node.key.name === 'textContent') ||
        (node.key.type === 'Literal' && node.key.value === 'textContent'))
    ) {
      const text = literalText(node.value)
      if (text !== null) report(node, text, 'text-property')
    }
  })
  return hits
}

export function scanTree(dir, base) {
  const hits = []
  const walkDir = (d) => {
    for (const entry of readdirSync(d, { withFileTypes: true })) {
      const full = join(d, entry.name)
      if (entry.isDirectory()) {
        if (entry.name === 'node_modules' || entry.name === 'dist' || entry.name === 'generated')
          continue
        walkDir(full)
      } else if (
        (entry.name.endsWith('.ts') || entry.name.endsWith('.tsx')) &&
        !entry.name.endsWith('.test.ts') &&
        !entry.name.endsWith('.test.tsx') &&
        !entry.name.endsWith('.d.ts')
      ) {
        hits.push(...scanSource(relative(base, full), readFileSync(full, 'utf8')))
      }
    }
  }
  walkDir(dir)
  return hits
}

/** Stable across line moves: file, glyph and where it was used. */
export function violationKey(v) {
  return `${v.file}:${v.glyph}:${v.context}`
}

function loadBaseline() {
  const map = new Map()
  try {
    const data = JSON.parse(readFileSync(BASELINE_PATH, 'utf8'))
    for (const v of data.violations) map.set(violationKey(v), v)
  } catch {
    // No baseline — every violation is an error.
  }
  return map
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const updateMode = process.env.NOCX_BASELINE_UPDATE === '1'
  const fileArgs = process.argv.slice(2).filter((a) => a.endsWith('.ts') || a.endsWith('.tsx'))
  const hits =
    fileArgs.length > 0
      ? fileArgs.flatMap((f) => scanSource(f, readFileSync(f, 'utf8')))
      : scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR)
  const baseline = fileArgs.length > 0 || updateMode ? new Map() : loadBaseline()
  // Count per key, so two identical glyphs in one file and context need two entries.
  const seen = new Map()
  const unbaselined = []
  for (const h of hits) {
    const key = violationKey(h)
    const n = (seen.get(key) ?? 0) + 1
    seen.set(key, n)
    const allowed = baseline.get(key)?.count ?? (baseline.has(key) ? 1 : 0)
    if (h.context === 'PARSE' || n > allowed) unbaselined.push(h)
  }
  for (const h of hits) {
    if (h.context === 'PARSE') console.error(`  PARSE ERROR: ${h.file}: ${h.reason}`)
    else console.log(`${h.file}:${h.line}: "${h.glyph}" as ${h.context}`)
  }
  if (unbaselined.length > 0) {
    console.error(`Glyph-icon violations: ${hits.length} total, ${unbaselined.length} new.`)
    for (const h of unbaselined)
      console.error(`  NEW: ${h.file}:${h.line} "${h.glyph}" as ${h.context}`)
    console.error(
      'A character is standing in for an icon. Use a component from src/ui/icons inside the kit',
      'control that holds it (IconButton, createIconButton). A baseline entry needs a reason:',
      '`npm run baseline:glyph-icons-update` refuses to grow.',
    )
    process.exitCode = 1
  } else if (hits.length > 0) {
    console.error(`Glyph-icon violations: ${hits.length} (all baselined).`)
  }
}
```

Create `frontend/lint-fixtures/update-glyph-icons-baseline.mjs`:

```js
#!/usr/bin/env node
/**
 * Regenerate `lint-fixtures/glyph-icons-baseline.json` from the current tree.
 *
 * Usage: npm run baseline:glyph-icons-update   (from frontend/)
 *
 * Refuses to write a baseline that grows — a key with more occurrences than the old
 * file allowed, or a key it did not list. Shrink and no-change are the only
 * directions; reasons are copied forward by key.
 */
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scanTree, violationKey } from './check-glyph-icons.mjs'

const __dirname = dirname(fileURLToPath(import.meta.url))
const FRONTEND_DIR = resolve(__dirname, '..')
const BASELINE_PATH = resolve(__dirname, 'glyph-icons-baseline.json')

const hits = scanTree(resolve(FRONTEND_DIR, 'src'), FRONTEND_DIR).filter(
  (h) => h.context !== 'PARSE',
)
const old = new Map()
if (existsSync(BASELINE_PATH)) {
  for (const v of JSON.parse(readFileSync(BASELINE_PATH, 'utf8')).violations)
    old.set(violationKey(v), v)
}

const counts = new Map()
for (const h of hits) {
  const key = violationKey(h)
  const entry = counts.get(key) ?? { file: h.file, glyph: h.glyph, context: h.context, count: 0 }
  entry.count += 1
  counts.set(key, entry)
}

const growth = [...counts.entries()].filter(([k, v]) => v.count > (old.get(k)?.count ?? 0))
if (growth.length > 0) {
  console.error('Refusing to write: the tree has glyph icons the baseline does not allow:')
  for (const [, v] of growth)
    console.error(`  NEW: ${v.file} "${v.glyph}" as ${v.context} ×${v.count}`)
  process.exit(1)
}

const violations = [...counts.entries()]
  .map(([k, v]) => ({ ...v, reason: old.get(k)?.reason ?? '' }))
  .sort((a, b) => (a.file === b.file ? (a.glyph < b.glyph ? -1 : 1) : a.file < b.file ? -1 : 1))

writeFileSync(
  BASELINE_PATH,
  JSON.stringify(
    {
      '//': [
        'DO NOT EDIT MANUALLY except to write a reason. Regenerate with `npm run baseline:glyph-icons-update`.',
        '',
        'Every entry is a character standing in for an icon as element text (nocx-9bpeq.5).',
        'The baseline may only shrink. Its normal state is empty.',
      ],
      violations,
    },
    null,
    2,
  ) + '\n',
)
console.log(`Baseline written: ${violations.length} entries.`)
```

Create `frontend/lint-fixtures/glyph-icons-baseline.json` (the three 📁 sites, which T6 and T7 delete with the
chips that carry them — verify the keys with Step 13's run before trusting this file):

```json
{
  "//": [
    "DO NOT EDIT MANUALLY except to write a reason. Regenerate with `npm run baseline:glyph-icons-update`.",
    "",
    "Every entry is a character standing in for an icon as element text (nocx-9bpeq.5).",
    "The baseline may only shrink. Its normal state is empty."
  ],
  "violations": [
    {
      "file": "src/editor.ts",
      "glyph": "📁",
      "context": "text-assignment",
      "count": 2,
      "reason": "The composer's cwd chip; removed with the chip by nocx-9bpeq.7 (spec 2026-09-14 §5.2)."
    },
    {
      "file": "src/scrollback/blocks.ts",
      "glyph": "📁",
      "context": "text-assignment",
      "count": 1,
      "reason": "The block header's cwd chip; removed with the chip by nocx-9bpeq.6 (spec 2026-09-14 §3.1)."
    }
  ]
}
```

Create `frontend/lint-fixtures/glyph-icons-fixture/glyphs.tsx`:

```tsx
// Glyph-icons fixture — negative fixtures for check-glyph-icons.mjs (nocx-9bpeq.5).
//
// Intentional violations (the gate asserts these fire):
//   close-x      — a × as a button's JSX child
//   more-dots    — a ⋮ assigned to textContent
//   folder-emoji — an emoji at the start of a textContent template
//
// Must stay silent:
//   times-title  — × in a title attribute (a multiplication sign)
//   idle-marker  — a glyph constant compared against text

import { CloseIcon } from '../../src/ui/icons'

export function CloseX() {
  return <button aria-label="close-x">{'\u00d7'}</button>
}

export function moreDots(el: HTMLElement): void {
  el.textContent = '\u22EE' // more-dots
}

export function folderEmoji(el: HTMLElement, label: string): void {
  el.textContent = `📁 ${label}` // folder-emoji
}

export function TimesTitle(props: { n: number }) {
  return (
    <b title={`times-title ×${props.n}`}>
      <CloseIcon />
    </b>
  )
}

const IDLE = '✳'
export const idleMarker = (screen: string): boolean => screen.startsWith(IDLE)
```

Modify `frontend/package.json` scripts: append ` && node lint-fixtures/check-glyph-icons.mjs` to the end of
`"lint"`, and add:

```json
    "lint:glyph-icons": "node lint-fixtures/check-glyph-icons.mjs",
    "baseline:glyph-icons-update": "node lint-fixtures/update-glyph-icons-baseline.mjs",
```

Modify `frontend/lint-fixtures/gate.sh` — insert before `# ── Kit identity fixture check`:

```sh
# ── Glyph-icons fixture check (nocx-9bpeq.5) ─────────────────────────────
# A character written as an element's text where an icon belongs. The fixture's
# three intentional uses must fire; a multiplication sign in a title and a glyph
# constant compared against a screen must stay silent, because a rule that
# reported those would be turned off. No path is exempt — the block header's ⋮
# lived in a file the raw-control lint exempts.
glyph_check=$(node "${fixture_dir}/check-glyph-icons.mjs" \
  "${fixture_dir}/glyph-icons-fixture/glyphs.tsx" 2>&1 || true)

glyph_hits=$(echo "$glyph_check" | grep -c '^lint-fixtures/glyph-icons-fixture' || true)
if [ "$glyph_hits" -ne 3 ]; then
  echo "GLYPH-ICONS GATE FAILED — expected exactly 3 glyph icons in the fixture, got ${glyph_hits}"
  exit 1
fi

if echo "$glyph_check" | grep -q 'PARSE ERROR'; then
  echo "GLYPH-ICONS GATE FAILED — the fixture did not parse"
  exit 1
fi

if ! node "${fixture_dir}/check-glyph-icons.mjs" >/dev/null 2>&1; then
  echo "GLYPH-ICONS GATE FAILED — the rule reports un-baselined glyphs on the real tree"
  exit 1
fi
```

and change the final `echo "OK — …menu-icons verified (11 integrity rules)"` to
`echo "OK — …menu-icons + glyph-icons verified (11 integrity rules)"`.

- [ ] **Step 13: Run the checker's tests, the fixture gate and the real tree**

Run (from `frontend/`):
`npx vitest run --config lint-fixtures/vitest.config.lint-fixtures.mjs lint-fixtures/check-glyph-icons.test.mjs`
Expected: PASS (10 tests).

Run (from `frontend/`): `NOCX_BASELINE_UPDATE=1 node lint-fixtures/check-glyph-icons.mjs`
Expected output — exactly the three baselined sites (line numbers may have moved):

```
src/editor.ts:347: "📁" as text-assignment
src/editor.ts:617: "📁" as text-assignment
src/scrollback/blocks.ts:765: "📁" as text-assignment
```

If anything else prints, it is a glyph Step 10 missed — fix it, do not baseline it.

Run (from `frontend/`): `sh lint-fixtures/gate.sh && npm run lint`
Expected: gate ends `OK — …`; lint exit 0 and stderr `Glyph-icon violations: 3 (all baselined).`

- [ ] **Step 14: README and the final run**

Modify `frontend/src/ui/README.md` — in the ContextMenu row's variance cell, append:
``; `align`: start \| end (x names the menu's left or right edge — a pointer, or a control's right side); `anchor` (the opener: its pointerdown is not outside, so it toggles); an item's `busyLabel` (the menu waits, the row is disabled and reads the label, closes when the work settles); every row carries `data-item-id` ``.

Run (from `frontend/`):
`npx prettier --write src/ui/README.md && npx vitest run && npx vitest run --config lint-fixtures/vitest.config.lint-fixtures.mjs && npm run lint && npm run typecheck`
Expected: all PASS; lint exit 0; typecheck exit 0.

- [ ] **Step 15: Commit**

```bash
git add frontend/src/tab.tsx frontend/src/tab-strip.tsx frontend/src/banner.tsx frontend/src/recovery-notice.tsx \
  frontend/src/unreconciled-notice.tsx frontend/src/integration/notice.tsx frontend/src/grant.ts \
  frontend/src/ui/secret-chip.ts frontend/src/styles/components/secret-chip.css frontend/src/secret-chip.test.ts \
  frontend/lint-fixtures/check-glyph-icons.mjs frontend/lint-fixtures/check-glyph-icons.test.mjs \
  frontend/lint-fixtures/update-glyph-icons-baseline.mjs frontend/lint-fixtures/glyph-icons-baseline.json \
  frontend/lint-fixtures/glyph-icons-fixture/glyphs.tsx frontend/package.json frontend/lint-fixtures/gate.sh \
  frontend/src/ui/README.md
git commit -m "$(cat <<'EOF'
build(frontend): no character stands in for an icon, and a gate that exempts no path (nocx-9bpeq.5)

Thirteen places drew an icon as text - a multiplication sign on every dismiss
button, a heavy X on the clipboard banner, a plus on the adopt button, a
padlock and a warning sign in the secret chip, and a folder emoji in the cwd
chips. Each now holds a component from ui/icons inside the kit control around
it; the grant panel's dismiss stops borrowing a context-menu row class for a
lone glyph and becomes createIconButton.

check-glyph-icons reports a glyph or emoji used as element text, in any file:
the block header's vertical ellipsis lived in code the raw-control lint
exempts, which is exactly the hole. It is scoped to text so that a
multiplication sign in a title and a constant compared against a program's
screen stay silent. The baseline holds the three folder-emoji sites with the
beads that remove them (nocx-9bpeq.6, nocx-9bpeq.7).

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

<!-- Plan section: T9, T7, T10 of epic nocx-9bpeq. Drafted read-only against the tree at 8ed43569. -->

### Task T9: the end-to-end check, committed red first (nocx-9bpeq.9)

Ready now: it depends only on T1. It is written before T4–T8 so that each of them turns part of it green.

**Files:**

- Create: `e2e/terminal-screen-register.spec.ts`
- Create: `e2e/screenshot-board.mjs` (a script, not a test: `e2e/check-coverage.mjs` only collects `*.spec.ts`)
- Create: `e2e/screenshot-board.sh`

**Interfaces:**

- Consumes (from the tree today): `test`, `expect`, `promptReady`, `openControlPlane` from `e2e/harness.ts`;
  `readStand` from `e2e/stand.ts`; the Settings theme path `e2e/theme-switch.spec.ts` already proves
  (`.ui-settings-row[data-key="ui.theme"] select`, `Meta+,`, `Meta+w`); `scanKitIdentities(uiDir)` from
  `frontend/lint-fixtures/scan-kit-identities.mjs`, which returns `{ byClass: Map<string, Set<string>> }`.
- Consumes (from later tasks, by name only; the spec stays red until they exist):
  - T6: `.cmd-block[data-outcome="failure"]`, and a status element `.cmd-header .ui-meta[data-tone="danger"]`.
  - T3: `--color-danger-surface`.
  - T7: no clock; the composer paints `--terminal-background`.
  - T8: the live region's ground.
- Produces: a spec whose `test.fail()` markers are removed one at a time by the child that turns each test
  green. Playwright reports a marked test that passes as a failure ("Expected to fail, but passed"), so the
  merged-tree gate tells the coordinator which marker is stale.

**Acceptance Criteria:**

- `e2e/terminal-screen-register.spec.ts` has one sanity test that passes today. It proves the probes the spec
  relies on (canvas compositing, WCAG contrast, the theme path, the theme mirror, the kit identity scan).
- It has six tests marked `test.fail()`, each naming in a comment the bead or beads whose work turns it green:
  1. A successful block renders no status element. → T6
  2. A failed block carries `data-outcome="failure"`, and its status text reaches 4.5:1 against its computed
     background, in every theme of `KNOWN_THEME_IDS`. → T3 + T6
  3. No text node in a block header or the composer's chrome matches `\p{Extended_Pictographic}` or ⋮ × ✕ ⚠.
     → T5, T6, T7
  4. The composer shows no clock. → T7
  5. Every control in a block header or the composer carries a kit identity class from `scanKitIdentities`.
     → T4–T7
  6. In every theme, the computed ground of a successful block row, the composer and the live region equals
     `--terminal-background`. → T3, T7, T8
- `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/terminal-screen-register.spec.ts` reports `7 passed`,
  because an expected failure counts as a pass.
- `e2e/screenshot-board.sh` against a running `make dev-web` writes three theme PNGs and `board.png` under
  `${TMPDIR:-/tmp}/nocx-screenshot-board/<timestamp>/`.
  - It writes nothing into the repository.
  - It opens its own tab and closes it again.
  - It does not change the persisted `ui.theme`.
  - Once the epic is done, the owner's acceptance of the board is recorded on nocx-9bpeq.

**Why `test.fail()` and not `fixme` or `skip`:** `test.fail()` still runs the body in the container and in CI.
It is the marker this suite already used for a picker that did not exist yet (`e2e/api-import.spec.ts:289`).
A skipped test cannot report that it has started passing.

**Why the theme is switched through Settings and reset over the wire:**

- The test switches through Settings because that is the product path, proven by `e2e/theme-switch.spec.ts`.
- `resetStand()` (`e2e/harness.ts:236-330`) does not reset settings, and `theme-switch.spec.ts` asserts Tokyo
  Night's canvas on entry. So `afterEach` restores `ui.theme` with a direct `settings.set` on the control plane,
  which works even when the page is broken.

- [ ] **Step 1: Write the spec**

```ts
// e2e/terminal-screen-register.spec.ts
//
// The terminal screen speaks the kit, in every theme (nocx-9bpeq, spec
// .internal/specs/2026-09-14-terminal-screen-visual-register-design.md).
//
// COMMITTED RED (nocx-9bpeq.9). Six tests below are marked `test.fail()` and name
// the child whose work turns them green. The child that makes one pass deletes
// its marker in the same commit; if it forgets, Playwright reports "Expected to
// fail, but passed" and the merged-tree gate says which one.
//
// The first test is NOT marked. It proves the probes the other six stand on, so
// a red marked test cannot be a broken harness wearing an expected failure.
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { openControlPlane, promptReady, test, expect, type Page } from './harness'
import { readStand } from './stand'

const REPO = path.resolve(__dirname, '..')
const INPUT = '.pane.active .nocx-editor-input'
const SETTLED = '.pane.active .cmd-block:not(.cmd-block-running)'
const COMPOSER = '.pane.active .nocx-editor'
const COMPOSER_CHROME = '.pane.active .nocx-editor-chrome'
const LIVE = '.pane.active .xterm-live-container'
const THEME_SELECT = '.ui-settings-row[data-key="ui.theme"] select'
// The names later tasks produce (spec §3.1, §3.3). One line each, so a rename is one edit.
const FAILED_ROW = '.cmd-block[data-outcome="failure"]'
const STATUS = '.cmd-header .ui-meta[data-tone="danger"]'
const OK = 'true #t9-ok'
const FAIL = 'false #t9-fail'

/** A mirror of KNOWN_THEME_IDS (frontend/src/renderers/theme-bootstrap.ts:48).
 *  Mirrored rather than imported: importing renderer source would pull the
 *  frontend's module graph into Playwright's Node process (harness.ts says why).
 *  The sanity test reads the source and fails if the two drift. */
const THEMES = [
  'tokyo-night',
  'light',
  'ayu-dark',
  'catppuccin-latte',
  'catppuccin-mocha',
  'dracula',
  'gruvbox-dark',
  'nord',
  'one-dark',
  'rose-pine',
  'solarized-dark',
  'solarized-light',
] as const

type RGB = [number, number, number]
type Probes = {
  ground(selector: string): RGB
  tokenColour(name: string): RGB
  textContrast(selector: string): number
  contrastOf(fg: string, bg: string): number
}

/** Installed before the app loads. Colours are resolved by PAINTING them on a
 *  1×1 canvas rather than by parsing computed strings: engines serialise
 *  color-mix() differently, and the canvas is the one parser both agree on. */
function installProbes(): void {
  const canvas = document.createElement('canvas')
  canvas.width = 1
  canvas.height = 1
  const ctx = canvas.getContext('2d', { willReadFrequently: true })!
  const SENTINEL = 'rgba(1, 2, 3, 0)'
  const paint = (layers: readonly string[]): [number, number, number] => {
    ctx.globalCompositeOperation = 'copy'
    ctx.fillStyle = '#ffffff'
    ctx.fillRect(0, 0, 1, 1)
    ctx.globalCompositeOperation = 'source-over'
    for (const layer of layers) {
      ctx.fillStyle = SENTINEL
      ctx.fillStyle = layer
      if (ctx.fillStyle === SENTINEL) continue // unparseable: the engine kept the sentinel
      ctx.fillRect(0, 0, 1, 1)
    }
    const d = ctx.getImageData(0, 0, 1, 1).data
    return [d[0], d[1], d[2]]
  }
  const layersOf = (el: Element): string[] => {
    const out: string[] = []
    for (let n: Element | null = el; n; n = n.parentElement) {
      out.unshift(getComputedStyle(n).backgroundColor)
    }
    return out
  }
  const channel = (v: number): number => {
    const s = v / 255
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  const luminance = ([r, g, b]: [number, number, number]): number =>
    0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
  const contrast = (a: [number, number, number], b: [number, number, number]): number => {
    const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
    return (hi + 0.05) / (lo + 0.05)
  }
  const one = (selector: string): Element => {
    const el = document.querySelector(selector)
    if (!el) throw new Error(`probe: nothing matches ${selector}`)
    return el
  }
  const probes = {
    ground: (selector: string) => paint(layersOf(one(selector))),
    tokenColour: (name: string) =>
      paint([getComputedStyle(document.documentElement).getPropertyValue(name).trim()]),
    textContrast: (selector: string) => {
      const el = one(selector)
      const layers = layersOf(el)
      return contrast(paint([...layers, getComputedStyle(el).color]), paint(layers))
    },
    contrastOf: (fg: string, bg: string) => contrast(paint([bg, fg]), paint([bg])),
  }
  ;(window as unknown as { __t9: typeof probes }).__t9 = probes
}

const probe = <K extends keyof Probes>(page: Page, name: K, ...args: Parameters<Probes[K]>) =>
  page.evaluate(
    ([n, a]) => {
      const p = (window as unknown as { __t9: Record<string, (...x: unknown[]) => unknown> }).__t9
      return p[n as string](...(a as unknown[]))
    },
    [name, args] as const,
  ) as Promise<ReturnType<Probes[K]>>

const near = (a: RGB, b: RGB): boolean => a.every((v, i) => Math.abs(v - b[i]) <= 1)

async function setTheme(page: Page, id: string): Promise<void> {
  if ((await page.evaluate(() => document.documentElement.getAttribute('data-theme'))) === id)
    return
  await page.keyboard.press('Meta+,')
  await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
  await expect(page.locator(THEME_SELECT)).toBeVisible()
  await page.selectOption(THEME_SELECT, id)
  await page.waitForFunction((t) => document.documentElement.getAttribute('data-theme') === t, id)
  await page.keyboard.press('Meta+w')
  await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('')
}

/** Open the app and leave one successful and one failed block settled above an idle composer. */
async function twoBlocks(page: Page): Promise<void> {
  await page.addInitScript(installProbes)
  await page.goto('/')
  await promptReady(page)
  for (const command of [OK, FAIL]) {
    await page.locator(INPUT).fill(command)
    await page.keyboard.press('Enter')
    await expect(page.locator(SETTLED, { hasText: command })).toHaveCount(1, { timeout: 15_000 })
    await promptReady(page)
  }
  // Hover tints a row; the pointer rests on the tab strip, not on a block.
  await page.mouse.move(1, 1)
}

function kitIdentities(): Set<string> {
  const out = execFileSync(
    process.execPath,
    [
      '--input-type=module',
      '-e',
      "import { scanKitIdentities } from './frontend/lint-fixtures/scan-kit-identities.mjs';" +
        "process.stdout.write(JSON.stringify([...scanKitIdentities('frontend/src/ui').byClass.keys()]))",
    ],
    { cwd: REPO, encoding: 'utf8' },
  )
  return new Set(JSON.parse(out) as string[])
}

test.afterEach(async () => {
  // Settings are not part of resetStand, and theme-switch.spec.ts asserts Tokyo
  // Night on entry. Over the wire, so a broken page cannot skip it.
  const stand = readStand()
  const wire = await openControlPlane(stand.port, stand.token)
  try {
    await wire.call('settings.set', { key: 'ui.theme', value: 'tokyo-night' })
  } finally {
    wire.close()
  }
})

test('the probes this spec stands on measure what they claim', async ({ page }) => {
  await twoBlocks(page)

  expect(await probe(page, 'contrastOf', '#000000', '#ffffff')).toBeCloseTo(21, 1)
  expect(await probe(page, 'contrastOf', '#777777', '#777777')).toBeCloseTo(1, 5)

  // Half-transparent black over whatever the body paints: compositing, not parsing.
  const body = await probe(page, 'ground', 'body')
  await page.evaluate(() => {
    const d = document.createElement('div')
    d.id = 't9-half'
    d.style.background = 'rgba(0, 0, 0, 0.5)'
    document.body.append(d)
  })
  const half = await probe(page, 'ground', '#t9-half')
  expect(near(half, body.map((v) => Math.round(v * 0.5)) as RGB)).toBe(true)

  // The mirror has not drifted from the source.
  const source = readFileSync(path.join(REPO, 'frontend/src/renderers/theme-bootstrap.ts'), 'utf8')
  const declared = [
    ...(source.match(/KNOWN_THEME_IDS[^[]*\[([^\]]*)\]/)?.[1] ?? '').matchAll(/'([^']+)'/g),
  ].map((m) => m[1])
  expect(declared).toEqual([...THEMES])

  // The theme path changes a resolved token, not only the attribute.
  await setTheme(page, 'dracula')
  expect(await probe(page, 'tokenColour', '--terminal-background')).toEqual([40, 42, 54])

  // The identity scan found the kit.
  expect(kitIdentities().has('ui-button')).toBe(true)
})

test('a successful command says nothing about its outcome', async ({ page }) => {
  test.fail() // nocx-9bpeq.6 turns this green; that commit deletes this line.
  await twoBlocks(page)
  const header = page.locator(SETTLED, { hasText: OK }).locator('.cmd-header')
  await expect(header.locator('[data-tone="danger"], [data-tone="dim"]')).toHaveCount(0)
  const words = await header.evaluate((h) =>
    [...h.querySelectorAll('*')]
      .filter((el) => el.children.length === 0)
      .map((el) => el.textContent?.trim() ?? ''),
  )
  expect(words.filter((w) => /^(ok|exit \d+|completed)$/.test(w))).toEqual([])
})

test('a failed command is marked, and its status is legible in every theme', async ({ page }) => {
  test.fail() // nocx-9bpeq.3 (--color-danger-surface) and nocx-9bpeq.6 (the row); the later of the two deletes this line.
  test.setTimeout(120_000)
  await twoBlocks(page)
  const row = page.locator(SETTLED, { hasText: FAIL })
  await expect(row).toHaveAttribute('data-outcome', 'failure')
  for (const theme of THEMES) {
    await setTheme(page, theme)
    const status = `${FAILED_ROW} ${STATUS}`
    await expect(page.locator(status)).toHaveText(/exit 1/)
    const ratio = await probe(page, 'textContrast', status)
    expect(ratio, `${theme}: status text contrast`).toBeGreaterThanOrEqual(4.5)
    const ground = await probe(page, 'ground', FAILED_ROW)
    const token = await probe(page, 'tokenColour', '--color-danger-surface')
    expect(near(ground, token), `${theme}: failed row ground is --color-danger-surface`).toBe(true)
  }
})

test('no emoji and no glyph stands in for an icon on the terminal screen', async ({ page }) => {
  test.fail() // nocx-9bpeq.5, .6 and .7 each remove some; the last of them deletes this line.
  await twoBlocks(page)
  const offenders = await page.evaluate(
    ([headers, chrome]) => {
      const bad = /[\p{Extended_Pictographic}⋮×✕⚠]/u
      const found: string[] = []
      for (const root of document.querySelectorAll(`${headers}, ${chrome}`)) {
        const walk = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
        for (let n = walk.nextNode(); n; n = walk.nextNode()) {
          if (bad.test(n.textContent ?? '')) found.push(n.textContent ?? '')
        }
      }
      return found
    },
    ['.pane.active .cmd-header', COMPOSER_CHROME] as const,
  )
  expect(offenders).toEqual([])
})

test('the composer shows no clock', async ({ page }) => {
  test.fail() // nocx-9bpeq.7 turns this green; that commit deletes this line.
  await twoBlocks(page)
  await expect(page.locator(`${COMPOSER} .nocx-editor-time`)).toHaveCount(0)
  await expect(page.locator(COMPOSER_CHROME)).not.toHaveText(/\d{1,2}:\d{2}/)
})

test('every control in a block header and the composer is a kit component', async ({ page }) => {
  test.fail() // nocx-9bpeq.4–.7; the last of them deletes this line.
  await twoBlocks(page)
  const identities = kitIdentities()
  const controls = await page.evaluate(
    ([scope]) =>
      [...document.querySelectorAll(scope)]
        .filter((el) => !(el as HTMLElement).isContentEditable)
        .map((el) => ({ html: el.outerHTML.slice(0, 120), classes: [...el.classList] })),
    [
      ['.pane.active .cmd-header', COMPOSER]
        .flatMap((s) =>
          [
            'button',
            'input',
            'select',
            'textarea',
            '[role="button"]',
            '[tabindex]:not([tabindex="-1"])',
          ].map((c) => `${s} ${c}`),
        )
        .join(', '),
    ] as const,
  )
  expect(controls.length).toBeGreaterThan(0)
  const unkitted = controls.filter((c) => !c.classes.some((k) => identities.has(k)))
  expect(unkitted.map((c) => c.html)).toEqual([])
})

test('history, live terminal and composer stand on one ground in every theme', async ({ page }) => {
  test.fail() // nocx-9bpeq.3, .7 and .8; the last of them deletes this line.
  test.setTimeout(120_000)
  await twoBlocks(page)
  const okRow = `${SETTLED}:not([data-outcome="failure"])`
  for (const theme of THEMES) {
    await setTheme(page, theme)
    const ground = await probe(page, 'tokenColour', '--terminal-background')
    expect(near(await probe(page, 'ground', okRow), ground), `${theme}: block row`).toBe(true)
    expect(near(await probe(page, 'ground', COMPOSER), ground), `${theme}: composer`).toBe(true)
  }
  // A builtin that waits: the live region exists only while something runs, and
  // `sleep` is not on every stand's PATH (pets.spec.ts).
  await page.locator(INPUT).fill('read -r _')
  await page.keyboard.press('Enter')
  await expect(page.locator(LIVE)).toBeVisible()
  for (const theme of THEMES) {
    await setTheme(page, theme)
    const ground = await probe(page, 'tokenColour', '--terminal-background')
    expect(near(await probe(page, 'ground', LIVE), ground), `${theme}: live region`).toBe(true)
  }
  await page.locator(LIVE).click()
  await page.keyboard.press('Control+c')
  await promptReady(page)
})
```

- [ ] **Step 2: Typecheck, lint and run it**

Run: `npm run typecheck && npx eslint e2e/terminal-screen-register.spec.ts`
Expected: exit 0.

Run: `node e2e/check-coverage.mjs`
Expected: exit 0 (the new file is collected by the default projects).

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/terminal-screen-register.spec.ts`
Expected: `7 passed`.

- If the sanity test is red, the harness is wrong: fix the probe. Never add a marker to it.
- If a marked test reports "Expected to fail, but passed", the tree already satisfies it. Read the passing
  assertion before deleting the marker.

Run: `PW_PROJECTS=webkit e2e/run-in-container.sh e2e/terminal-screen-register.spec.ts`
Expected: `7 passed`. WebKit is where color-mix serialisation differs, which is why the probes paint rather
than parse.

- [ ] **Step 3: Write the screenshot board**

```js
// e2e/screenshot-board.mjs
//
// The owner's review board for nocx-9bpeq: one scripted session, three themes,
// one image. NOT a test — it asserts nothing and check-coverage.mjs does not
// collect it. Run through e2e/screenshot-board.sh.
//
// It drives the DEV-WEB stand (make dev-web, 127.0.0.1:5180), not the e2e stand:
// the e2e stand exists only inside a Playwright run (e2e/stand.ts), and a second
// launcher for it is the drift that file was written to end. The dev-web stand
// is somebody's own session, so this script opens its OWN tab and closes it,
// and switches themes with the attribute only — the persisted ui.theme setting
// is theirs and is never written.
import { chromium } from 'playwright'
import { mkdirSync, writeFileSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

const BASE = process.env.BOARD_URL ?? 'http://127.0.0.1:5180/'
const OUT = process.env.BOARD_OUT ?? '/out'
const THEMES = ['tokyo-night', 'light', 'solarized-light']
const SESSION = ['true #board-ok', 'false #board-fail', "printf '%s\\n' one two three", 'ls /']

mkdirSync(OUT, { recursive: true })
const browser = await chromium.launch()
try {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 2,
  })
  await page.goto(BASE)
  await page.waitForSelector('.pane.active .nocx-editor-input', { timeout: 30_000 })
  const before = await page.locator('.nocx-tab').count()
  await page.keyboard.press('Meta+t')
  await page.waitForFunction((n) => document.querySelectorAll('.nocx-tab').length === n + 1, before)
  await page.waitForSelector('.pane.active .nocx-editor-input')
  for (const command of SESSION) {
    await page.locator('.pane.active .nocx-editor-input').fill(command)
    await page.keyboard.press('Enter')
    await page
      .locator('.pane.active .cmd-block:not(.cmd-block-running)', { hasText: command })
      .waitFor({ timeout: 15_000 })
    await page.waitForSelector('.pane.active .nocx-editor-input')
  }
  await page.mouse.move(1, 1)
  const shots = []
  for (const theme of THEMES) {
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)
    await page.waitForFunction(
      () =>
        getComputedStyle(document.documentElement).getPropertyValue('--terminal-background') !== '',
    )
    const file = join(OUT, `${theme}.png`)
    await page.screenshot({ path: file })
    shots.push({ theme, file })
  }
  await page.keyboard.press('Meta+w') // close the tab this script opened
  const board = await browser.newPage({ viewport: { width: 3 * 800 + 80, height: 620 } })
  const cells = shots
    .map(
      ({ theme, file }) =>
        `<figure><img src="data:image/png;base64,${readFileSync(file).toString('base64')}"><figcaption>${theme}</figcaption></figure>`,
    )
    .join('')
  await board.setContent(
    `<style>body{margin:0;padding:20px;display:flex;gap:20px;background:#888;font:14px system-ui}figure{margin:0;width:800px}img{width:800px;display:block}figcaption{padding:6px 0;color:#fff}</style>${cells}`,
  )
  await board.screenshot({ path: join(OUT, 'board.png'), fullPage: true })
  writeFileSync(
    join(OUT, 'README.txt'),
    `nocx-9bpeq screenshot board\nsource: ${BASE}\nthemes: ${THEMES.join(', ')}\n`,
  )
  console.log(`board written to ${OUT}`)
} finally {
  await browser.close()
}
```

```bash
#!/usr/bin/env bash
# e2e/screenshot-board.sh — capture the nocx-9bpeq review board from a running
# `make dev-web`, in the e2e image (it carries the browsers Playwright pins; the
# host may have none). Output goes to the temp dir, never into the repository.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="nocx-e2e:local"
out="${TMPDIR:-/tmp}/nocx-screenshot-board/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$out"
if ! curl -fsS "http://127.0.0.1:${NOCX_WEB_PORT:-5180}/" >/dev/null; then
  echo "screenshot-board: no dev-web stand on 127.0.0.1:${NOCX_WEB_PORT:-5180} — run 'make dev-web' first" >&2
  exit 1
fi
docker build -q -f "$repo_root/e2e/Dockerfile" -t "$image" "$repo_root/e2e" >/dev/null
docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e BOARD_URL="http://127.0.0.1:${NOCX_WEB_PORT:-5180}/" -e BOARD_OUT=/out \
  -v "$repo_root/node_modules:/board/node_modules:ro" \
  -v "$repo_root/e2e/screenshot-board.mjs:/board/board.mjs:ro" \
  -v "$out:/out" -w /board "$image" node board.mjs
echo "$out/board.png"
```

- [ ] **Step 4: Run the board once**

Run: `chmod +x e2e/screenshot-board.sh && e2e/screenshot-board.sh`. Needs `make dev-web` running.
Expected output:

- the last line is a path ending `/board.png`;
- the directory holds `tokyo-night.png`, `light.png`, `solarized-light.png` and `board.png`;
- the stand's tab count is the same after the run as before it.

Run: `npx eslint e2e/screenshot-board.mjs && npx prettier --check e2e/screenshot-board.mjs e2e/screenshot-board.sh e2e/terminal-screen-register.spec.ts`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add e2e/terminal-screen-register.spec.ts e2e/screenshot-board.mjs e2e/screenshot-board.sh
git commit -m "test(e2e): the terminal screen's register, committed red with the child that turns each part green (nocx-9bpeq.9)

<body: why test.fail and not skip; why the theme goes through Settings and resets over the wire; why the board drives dev-web and never the e2e stand>

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

---

### Task T8: Rows are full width — the pane gutter moves into the rows (nocx-9bpeq.8)

Spec: §4 and §8 (icon sizes). Lands BEFORE T6.

**Why this is not a CSS-only change.** Today `.pane { padding: 0 var(--pane-inline-padding) }`
(`frontend/src/styles/base.css:455-467`) insets everything, `.scrollback-layout` cancels the TRAILING inset
with `margin-right: calc(-1 * var(--pane-inline-padding))` (`frontend/src/style.css:783-816`) so the
scrollbar is flush, and xterm's grid is fitted to `.scrollback-area`'s `clientWidth`
(`TerminalContent.usableViewport`, `frontend/src/terminal-content.ts:4301-4319`), which is only correct
because nothing inside the scroller carries an inline inset (`frontend/src/scrollback/trailing-edge.test.ts`,
nocx-vydj, nocx-mvbne). Moving the gutter INTO the rows re-inserts an inset inside the scroller, so the fit
must subtract it — otherwise the grid is `2 × gutter` wider than the box it is drawn in and its last columns
are cut by `.xterm-inner { overflow: hidden }` (the nocx-vydj defect, returned).

**The geometry after this task.**

- `.pane` has no inline padding. `.scrollback-layout` has no negative margin. The scroller spans the pane,
  and its stable scrollbar gutter is the pane's trailing edge (flush, as today).
- Every direct child of `.scrollback-inner` (blocks, the separator, the restore boundary, the live region)
  is `box-sizing: border-box; padding-inline: var(--pane-inline-padding)`. A frozen `.cmd-output` and the
  live `.xterm-screen` therefore share one content box: `area.clientWidth − 2 × gutter`.
- The grid is fitted to that content box: `usableViewport.width = (area.clientWidth || viewport.width) −
liveInlineInsetPx`, where the inset is READ off `.xterm-live-container` (the same discipline as
  `_bodyPaddingPx`, `frontend/src/scrollback/controller.ts:419-424`) — the number lives only in the
  stylesheet.
- `.xterm-live-container.live-running` today sets `padding: top 0 bottom`, which would reset the inline
  inset to 0 (specificity 0,2,0 beats 0,1,0). It becomes `padding-block`.
- The summon stack is `left: 0; right: 0`, and its answer blocks carry the same row inset.
- The composer (`.nocx-editor`, a flex child of the pane) carries the gutter itself:
  `padding: 10px var(--pane-inline-padding) 12px`. Its accent border and chrome are T7's; this task only
  replaces the inset the pane used to give it.
- A block nested in a turn (`.cmd-children > .cmd-block`) is NOT a row and gets no inset — the rule is
  `.scrollback-inner > *`, never `.cmd-block`.

**Known consequence, stated rather than discovered:** the terminal grid loses `2 × 16 − 10 = 22px` of width
against today (today: 10px leading inset, 0 trailing). At the default 13px mono cell (≈7.8–8.6px wide) that
is 2–3 columns. Full-screen programs get the same side margins as the ledger.

**Files:**

- Modify: `frontend/src/styles/tokens.css` — `--pane-inline-padding: var(--space-4);` (T2 declared it at
  `10px`)
- Modify: `frontend/src/styles/base.css:455-485` — `.pane` loses `padding`; the rule
  `.pane:has(> .surface-host) { padding: 0; }` and its comment are deleted (nothing left to cancel)
- Modify: `frontend/src/style.css` — `.nocx-editor` (182-189), `.nocx-summon-stack` (194-197),
  `.scrollback-layout` (783-816), `.xterm-live-container.live-running` (906-922),
  `.xterm-live-container.live-running > .cmd-answer-typing` (935-940); add the row rule after
  `.scrollback-inner` (873)
- Modify: `frontend/src/scrollback/controller.ts:419-424` area — add `liveInlineInsetPx`
- Modify: `frontend/src/terminal-content.ts:4301-4319` — `usableViewport` subtracts the inset
- Rewrite: `frontend/src/scrollback/trailing-edge.test.ts`
- Modify: `frontend/src/terminal-content.test.ts:2488-2506` (summon stack contract) and add one geometry test
- Modify: `frontend/src/scrollback/controller.test.ts` (describe at 686) — one test
- Modify: `e2e/grid-width.spec.ts` — the grid fills the LIVE CONTENT BOX, not the scroller
- Create: `e2e/row-gutter.spec.ts`

**Interfaces:**

- Consumes (T2): `--pane-inline-padding` declared in `frontend/src/styles/tokens.css`; `--icon-size-lg: 20px`;
  `--space-4: 16px` (already in tokens.css:46-52).
- Produces (T6, T7 rely on it):
  - `ScrollbackController.liveInlineInsetPx: number` (getter) — sum of the live container's computed
    `padding-left` and `padding-right`, 0 where nothing is laid out.
  - CSS contract: `.scrollback-inner > *` and `.nocx-summon-answers > *` are `box-sizing: border-box;
padding-inline: var(--pane-inline-padding)`. A row may paint its own background edge to edge and draw
    anything at `inset-inline-start: 0` without moving its text. **T6 must not give `.cmd-block` an inline
    padding of its own.**
  - `.nocx-editor` owns `padding-inline: var(--pane-inline-padding)`; T7 rewrites the rest of that rule.

**Acceptance Criteria:**

- Pane gutter and rail glyph size come only from tokens: no `padding` on `.pane`; `--pane-inline-padding` is
  `var(--space-4)` in tokens.css. (The rail's glyph size is T2's: it routed `icon-button.css` through `--icon-size-*`.)
- `e2e/row-gutter.spec.ts` (chromium and webkit): after a pane resize, the frozen block's first `.term-line`
  left x equals the live `.xterm-screen` left x (±0.5px); `cols === floor(liveContentWidth / cellWidth)` where
  `cellWidth` is `--term-cell-width`; a `.cmd-block`'s border box spans the scroller's content width; the
  scroller's right edge equals the pane's right edge (±0.5px).
- `e2e/grid-width.spec.ts` still proves overhang ≤ 0 and fill > 0.98, measured against the live content box.
- A unit test proves `usableViewport` subtracts the live inset both when the scroller reports a width and on
  the fallback path (delivered viewport).

- [ ] **Step 1: Write the failing controller test**

In `frontend/src/scrollback/controller.test.ts`, inside `describe('the echoed command line leaves the live
region too (nocx-w1n4)', …)` (it owns `rendererWithGeometry`), after the test `'reserves the block body
padding around the rows, and caps the WHOLE box'`:

```ts
it('reports the inline inset the grid must not be fitted into (nocx-9bpeq.8)', () => {
  // Rows carry the pane gutter now, the live region included, so the grid's
  // box is the scroller MINUS that inset. Read off the element, like the body
  // padding above: the stylesheet stays the one place the number lives.
  const { renderer } = rendererWithGeometry()
  const pane = document.createElement('div')
  const controller = new ScrollbackController({
    pane,
    renderer,
    snapshotStore: new CommandSnapshotStore(),
  })
  document.body.appendChild(pane)
  expect(controller.liveInlineInsetPx).toBe(0)
  controller.xtermLiveContainer.style.paddingLeft = '16px'
  controller.xtermLiveContainer.style.paddingRight = '16px'
  expect(controller.liveInlineInsetPx).toBe(32)
  controller.blockManager.clearAll()
  pane.remove()
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd frontend && npx vitest run src/scrollback/controller.test.ts -t "inline inset"`
Expected: FAIL — `expected undefined to be +0`.

- [ ] **Step 3: Add the getter**

In `frontend/src/scrollback/controller.ts`, directly below `_bodyPaddingPx()`:

```ts
  /** The inline inset the live region wears as a ROW of the ledger
   *  (nocx-9bpeq.8): the pane gutter moved from `.pane` into every row so a
   *  row can paint to the edge. The grid is drawn inside that inset, so the fit
   *  must not count it — read off the element, like `_bodyPaddingPx`, so the
   *  stylesheet stays the one place the number lives. Zero wherever there is no
   *  layout to read (jsdom without inline styles). */
  get liveInlineInsetPx(): number {
    const cs = getComputedStyle(this.xtermLiveContainer)
    const left = parseFloat(cs.paddingLeft)
    const right = parseFloat(cs.paddingRight)
    return (Number.isFinite(left) ? left : 0) + (Number.isFinite(right) ? right : 0)
  }
```

- [ ] **Step 4: Run it and watch it pass**

Run: `cd frontend && npx vitest run src/scrollback/controller.test.ts -t "inline inset"`
Expected: PASS.

- [ ] **Step 5: Write the failing fit test**

In `frontend/src/terminal-content.test.ts`, inside `describe('TerminalContent geometry handoff and PTY
resize policy (nocx-cwnz0)', …)`, after its first test:

```ts
it('fits the grid to the live content box, not to the scroller (nocx-9bpeq.8)', async () => {
  const client = makeClient()
  const { content, teardown } = await mountTerminal(makeClipboard(), {}, client)
  const withScrollback = content as unknown as { scrollback: ScrollbackController }
  const renderer = rendererOf(content)
  const raf = globalThis.requestAnimationFrame
  globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
    cb(0)
    return 0
  }
  try {
    const live = withScrollback.scrollback.xtermLiveContainer
    live.style.paddingLeft = '16px'
    live.style.paddingRight = '16px'
    /* eslint-disable @typescript-eslint/unbound-method */
    // Fallback path: jsdom reports clientWidth 0, so the delivered viewport
    // is the guess — and the rows' inset is still not the grid's.
    content.viewportChanged({ width: 1000, height: 400 })
    expect(renderer.fitViewport).toHaveBeenLastCalledWith(
      expect.objectContaining({ width: 968, height: 400 }),
    )
    // Measured path: the scroller's content width minus the same inset.
    Object.defineProperty(withScrollback.scrollback.scrollbackArea, 'clientWidth', {
      value: 800,
      configurable: true,
    })
    content.viewportChanged({ width: 1000, height: 400 })
    expect(renderer.fitViewport).toHaveBeenLastCalledWith(
      expect.objectContaining({ width: 768, height: 400 }),
    )
    /* eslint-enable @typescript-eslint/unbound-method */
  } finally {
    globalThis.requestAnimationFrame = raf
    teardown()
  }
})
```

- [ ] **Step 6: Run it and watch it fail**

Run: `cd frontend && npx vitest run src/terminal-content.test.ts -t "live content box"`
Expected: FAIL — last call carried `width: 1000`.

- [ ] **Step 7: Subtract the inset in `usableViewport`**

In `frontend/src/terminal-content.ts`, replace the two lines computing `width` and the return:

```ts
const cap = this.scrollback?.runningLiveCap
const height = cap ?? (area && area.clientHeight > 0 ? area.clientHeight : viewport.height)
// THE GRID'S BOX IS THE LIVE ROW'S CONTENT BOX (nocx-9bpeq.8). Rows carry
// the pane gutter, the live region included, so the scroller's clientWidth
// is the grid width PLUS that inset — fitting to clientWidth would put the
// last columns under `.xterm-inner`'s overflow, which is nocx-vydj again.
// Subtracted on the fallback path too: the delivered box is the pane's.
const outer = area && area.clientWidth > 0 ? area.clientWidth : viewport.width
const width = Math.max(0, outer - (this.scrollback?.liveInlineInsetPx ?? 0))
return { ...viewport, width, height }
```

Update the comment block above `refitIfResized` (4262-4273) so its sentence "`usableViewport` reads
`.scrollback-area`'s clientWidth" reads "`usableViewport` reads `.scrollback-area`'s clientWidth minus the
live row's inline inset".

- [ ] **Step 8: Run both tests**

Run: `cd frontend && npx vitest run src/terminal-content.test.ts src/scrollback/controller.test.ts`
Expected: PASS, including the existing nocx-cwnz0 and nocx-zn4d fit tests (they set no padding, so the
inset is 0 and their widths are unchanged).

- [ ] **Step 9: Rewrite the stylesheet contract test (red first)**

Replace `frontend/src/scrollback/trailing-edge.test.ts` below its imports with:

```ts
type Rule = { selectors: string[]; body: string }

const HERE = import.meta.dirname ?? '.'
const STYLE_ENTRY = resolve(HERE, '..', 'style.css')
const BASE_ENTRY = resolve(HERE, '..', 'styles/base.css')
const TOKENS_ENTRY = resolve(HERE, '..', 'styles/tokens.css')

/** Top-level rules only, comments stripped. An at-rule block is skipped
 *  whole. Lifted from cmd-output-wrap.test.ts. */
function topLevelRules(css: string): Rule[] {
  const rules: Rule[] = []
  const source = css.replace(/\/\*[\s\S]*?\*\//g, '')
  let depth = 0
  let head = ''
  let body = ''
  for (const ch of source) {
    if (ch === '{') {
      depth++
      if (depth === 1) {
        body = ''
        continue
      }
    } else if (ch === '}') {
      depth--
      if (depth === 0) {
        const selector = head.trim()
        if (!selector.startsWith('@')) {
          rules.push({ selectors: selector.split(',').map((s) => s.trim()), body })
        }
        head = ''
        continue
      }
    }
    if (depth === 0) head += ch
    else body += ch
  }
  return rules
}

const RULES: Rule[] = [
  ...topLevelRules(readFileSync(TOKENS_ENTRY, 'utf8')),
  ...topLevelRules(readFileSync(BASE_ENTRY, 'utf8')),
  ...topLevelRules(readFileSync(STYLE_ENTRY, 'utf8')),
]

/** Every declaration the shipped cascade gives `selector` exactly, later
 *  rules winning. */
function shipped(selector: string, property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    if (!rule.selectors.includes(selector)) continue
    const m = rule.body.match(pattern)
    if (m) found = m[1].trim()
  }
  return found
}

const INLINE = [
  'padding',
  'padding-inline',
  'padding-left',
  'padding-right',
  'padding-inline-start',
  'padding-inline-end',
]

describe('rows are full width, and the gutter lives in them (nocx-9bpeq.8)', () => {
  it('the gutter is one token on the spacing scale', () => {
    expect(shipped(':root', '--pane-inline-padding')).toBe('var(--space-4)')
  })

  it('the pane insets nothing, so the scroller and its scrollbar reach the pane edge', () => {
    for (const property of INLINE) expect(shipped('.pane', property)).toBeNull()
    // Nothing is left to cancel, so no cancellation may survive: a negative
    // margin with no padding to pay for it pushes the scroller past the pane.
    expect(shipped('.scrollback-layout', 'margin-right')).toBeNull()
    expect(shipped('.scrollback-layout', 'margin-left')).toBeNull()
  })

  it('the scroller takes no padding, because its clientWidth is where the fit starts', () => {
    for (const property of INLINE) expect(shipped('.scrollback-area', property)).toBeNull()
    expect(shipped('.scrollback-area', 'scrollbar-gutter')).toBe('stable')
  })

  it('every row of the ledger carries the same inset, as a border-box', () => {
    // One rule for every child of the stack — blocks, separator, restore
    // boundary and the live region — so a frozen column and the live column
    // cannot sit on different edges, and a block nested in a turn (not a
    // child of the stack) gets none.
    expect(shipped('.scrollback-inner > *', 'padding-inline')).toBe('var(--pane-inline-padding)')
    expect(shipped('.scrollback-inner > *', 'box-sizing')).toBe('border-box')
    for (const selector of ['.cmd-block', '.xterm-live-container']) {
      for (const property of INLINE) expect(shipped(selector, property)).toBeNull()
    }
  })

  it('the running region states only its block padding, never resetting the inset', () => {
    // A `padding` shorthand here (0,2,0) would override the row inset (0,1,0)
    // with 0 and put the running grid on a different edge from the block it
    // freezes into.
    expect(shipped('.xterm-live-container.live-running', 'padding')).toBeNull()
    expect(shipped('.xterm-live-container.live-running', 'padding-block')).toBe(
      'var(--cmd-output-pad-top) var(--cmd-output-pad-bottom)',
    )
  })

  it('the summon stack is full width and its answers wear the row inset', () => {
    expect(shipped('.nocx-summon-stack', 'left')).toBe('0')
    expect(shipped('.nocx-summon-stack', 'right')).toBe('0')
    expect(shipped('.nocx-summon-answers > *', 'padding-inline')).toBe('var(--pane-inline-padding)')
    expect(shipped('.nocx-summon-answers > *', 'box-sizing')).toBe('border-box')
  })

  it('the composer carries the gutter itself', () => {
    expect(shipped('.nocx-editor', 'padding')).toBe('10px var(--pane-inline-padding) 12px')
  })
})
```

Keep the file's header comment, rewritten to say what changed: the scrollbar is flush because the pane no
longer pads, and the three-fact contract is now "the pane does not pad; the scroller does not pad; every
row pads by the same token and the fit subtracts it".

- [ ] **Step 10: Run it and watch it fail**

Run: `cd frontend && npx vitest run src/scrollback/trailing-edge.test.ts`
Expected: FAIL on every `it` (tokens value `10px`, `.pane` padding present, margin-right present, no row rule,
`padding` shorthand on `.live-running`, stack `left: var(--pane-inline-padding)`, editor `10px 14px 12px`).

- [ ] **Step 11: Change the stylesheets**

`frontend/src/styles/tokens.css` — the declaration T2 added:

```css
/* The ledger's side gutter. Rows carry it (style.css `.scrollback-inner > *`,
     `.nocx-summon-answers > *`, `.nocx-editor`), not the pane, so a row can paint
     to the edge (spec 2026-09-14 §4, nocx-9bpeq.8). */
--pane-inline-padding: var(--space-4);
```

`frontend/src/styles/base.css` — `.pane` becomes (the `padding` line removed; if T2 left a
`--pane-inline-padding` declaration here, it is removed too):

```css
.pane {
  position: absolute;
  inset: 0;
  overflow: hidden;
  background: var(--color-canvas);
  visibility: hidden;
  pointer-events: none;
  display: flex;
  flex-direction: column;
  --default-contextmenu: hide;
}
```

and delete the whole `.pane:has(> .surface-host) { padding: 0; }` rule with its comment.

`frontend/src/style.css`:

```css
.nocx-editor {
  position: relative;
  flex: none;
  background: var(--color-canvas);
  border-top: 1px solid var(--color-surface-raised);
  border-left: 3px solid var(--color-accent);
  /* The pane no longer insets its children (nocx-9bpeq.8): the composer is a
     row and carries the ledger's gutter itself. T7 rewrites the rest. */
  padding: 10px var(--pane-inline-padding) 12px;
}
```

In `.nocx-summon-stack`, replace `left: var(--pane-inline-padding); right: var(--pane-inline-padding);` with
`left: 0; right: 0;` and add directly after the `.nocx-summon-answers` rule:

```css
/* An answer in the stack is a row of the ledger before it is seated into
   one, so it wears the row inset now rather than inheriting a pane padding
   that no longer exists (nocx-9bpeq.8). */
.nocx-summon-answers > * {
  box-sizing: border-box;
  padding-inline: var(--pane-inline-padding);
}
```

`.scrollback-layout` loses `margin-right` and its comment; the comment becomes one paragraph: the scrollbar
is the pane's trailing edge because the pane does not pad; the gutter is in the rows (nocx-mvbne,
nocx-9bpeq.8).

After `.scrollback-inner { … }`:

```css
/* EVERY ROW WEARS THE GUTTER, AND THE STACK DOES NOT (nocx-9bpeq.8).

   The pane's side gutter moved from `.pane` into the rows so a row can paint
   its own ground edge to edge — a failed block's tint, a selection — and put a
   mark at its left edge without moving its text. One rule for every child of
   the stack, the live region included, so the frozen column and the live
   column are one content box by construction. A block nested in a turn is not
   a child of the stack and is not inset again.

   The grid is fitted INSIDE this inset: `usableViewport` subtracts the live
   row's computed inline padding from the scroller's clientWidth. */
.scrollback-inner > * {
  box-sizing: border-box;
  padding-inline: var(--pane-inline-padding);
}
```

`.xterm-live-container.live-running`: replace `padding: var(--cmd-output-pad-top) 0
var(--cmd-output-pad-bottom);` with `padding-block: var(--cmd-output-pad-top) var(--cmd-output-pad-bottom);`
and add to its comment: "`padding-block`, never the shorthand: the shorthand's `0` would reset the row inset".

`.xterm-live-container.live-running > .cmd-answer-typing`: `left: 0;` → `left: var(--pane-inline-padding);`
(the stand-in stands at the first column, which is inside the inset now).

- [ ] **Step 12: Update the summon-stack contract in `terminal-content.test.ts`**

In `describe('summoned editor overlay stylesheet contract (nocx-92gfl)', …)` (2488-2506) replace

```ts
expect(pane?.[1] ?? '').toMatch(/--pane-inline-padding\s*:\s*10px/)
expect(stack).toMatch(/position\s*:\s*absolute/)
expect(stack).toMatch(/left\s*:\s*var\(--pane-inline-padding\)/)
expect(stack).toMatch(/right\s*:\s*var\(--pane-inline-padding\)/)
```

with

```ts
// The pane insets nothing; the stack is full width and its rows carry the
// gutter (nocx-9bpeq.8, asserted in scrollback/trailing-edge.test.ts).
expect(pane?.[1] ?? '').not.toMatch(/(^|;)\s*padding\s*:/)
expect(stack).toMatch(/position\s*:\s*absolute/)
expect(stack).toMatch(/left\s*:\s*0/)
expect(stack).toMatch(/right\s*:\s*0/)
```

- [ ] **Step 13: Run the unit suites touched**

Run: `cd frontend && npx vitest run src/scrollback/trailing-edge.test.ts src/terminal-content.test.ts src/scrollback/controller.test.ts src/scrollback/cmd-output-wrap.test.ts src/ui/icon-button.test.tsx src/sidebar.test.tsx`
Expected: PASS.

- [ ] **Step 14: Update `e2e/grid-width.spec.ts` to measure the live content box**

Replace the `geometry` evaluate body with:

```ts
const geometry = () =>
  page.evaluate(() => {
    const pane = document.querySelector('.pane.active')
    const live = pane?.querySelector('.xterm-live-container') as HTMLElement | null
    const screen = pane?.querySelector('.xterm-screen') as HTMLElement | null
    if (!live || !screen) return { overhang: 1, fill: 0, settled: false }
    // THE GRID'S BOX IS THE LIVE ROW'S CONTENT BOX (nocx-9bpeq.8): rows
    // carry the pane gutter, so the scroller's clientWidth is the grid width
    // plus that inset on both sides.
    const cs = getComputedStyle(live)
    const box = live.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)
    const width = screen.getBoundingClientRect().width
    const overhang = Math.round(width - box)
    const fill = width / box
    return { overhang, fill, settled: overhang <= 0 && fill > 0.98 }
  })
```

and add one sentence to the header comment: the box is the live row's content box since nocx-9bpeq.8.

- [ ] **Step 15: Write the failing alignment e2e**

Create `e2e/row-gutter.spec.ts`:

```ts
/**
 * Rows are full width, and moving the gutter into them moved nothing a person
 * reads (nocx-9bpeq.8, spec 2026-09-14 §4).
 *
 * The pane used to inset everything; the ledger's rows carry that inset now so
 * a row can paint to the edge. The risk is geometry, not colour: xterm's cols
 * come from a width, the frozen block reproduces the grid at a measured cell
 * width (nocx-dvf6k), and the scrollbar must stay the pane's edge (nocx-mvbne).
 * Every wait is on a settled layout, never on a duration.
 */
import { test, expect, promptReady } from './harness'
import type { Page } from './harness'

const GUTTER_TOLERANCE = 0.5

async function measure(page: Page) {
  return page.evaluate(() => {
    const pane = document.querySelector<HTMLElement>('.pane.active')!
    const area = pane.querySelector<HTMLElement>('.scrollback-area')!
    const inner = pane.querySelector<HTMLElement>('.scrollback-inner')!
    const live = pane.querySelector<HTMLElement>('.xterm-live-container')!
    const screen = pane.querySelector<HTMLElement>('.xterm-screen')!
    const blocks = pane.querySelectorAll<HTMLElement>('.scrollback-inner > .cmd-block')
    const block = blocks[blocks.length - 1] ?? null
    const line = block?.querySelector<HTMLElement>('.cmd-output .term-line') ?? null
    const cs = getComputedStyle(live)
    const liveContent = live.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)
    const cellWidth = parseFloat(getComputedStyle(inner).getPropertyValue('--term-cell-width'))
    const areaRect = area.getBoundingClientRect()
    return {
      frozenLeft: line ? line.getBoundingClientRect().left : null,
      liveLeft: screen.getBoundingClientRect().left,
      cols: Math.round(screen.getBoundingClientRect().width / cellWidth),
      expectedCols: Math.floor(liveContent / cellWidth),
      cellWidth,
      blockWidth: block ? block.getBoundingClientRect().width : null,
      areaContentWidth: area.clientWidth,
      areaLeft: areaRect.left,
      blockLeft: block ? block.getBoundingClientRect().left : null,
      areaRight: areaRect.right,
      paneRight: pane.getBoundingClientRect().right,
    }
  })
}

async function settled(page: Page, previousCols: number | null) {
  // Settled = a cell width is published and the grid fills its box exactly
  // as the fit computes it; after a resize, also that the cols moved.
  await expect
    .poll(
      async () => {
        const m = await measure(page)
        return (
          m.cellWidth > 0 &&
          m.frozenLeft !== null &&
          m.cols === m.expectedCols &&
          (previousCols === null || m.cols !== previousCols)
        )
      },
      { timeout: 15_000 },
    )
    .toBe(true)
  return measure(page)
}

function assertAligned(m: Awaited<ReturnType<typeof measure>>) {
  expect(Math.abs((m.frozenLeft ?? NaN) - m.liveLeft)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  expect(m.cols).toBe(m.expectedCols)
  // The row spans the scroller's content box: its tint can reach the edge.
  expect(Math.abs((m.blockLeft ?? NaN) - m.areaLeft)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  expect(Math.abs((m.blockWidth ?? NaN) - m.areaContentWidth)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
  // The scrollbar is the pane's trailing edge (nocx-mvbne).
  expect(Math.abs(m.areaRight - m.paneRight)).toBeLessThanOrEqual(GUTTER_TOLERANCE)
}

test('the frozen column and the live column share one edge, before and after a resize', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)
  await page.keyboard.type('printf "gutter-probe\\n"')
  await page.keyboard.press('Enter')
  await expect(
    page.locator('.pane.active .scrollback-inner > .cmd-block .term-line').first(),
  ).toContainText('gutter-probe', { timeout: 30_000 })
  await promptReady(page)

  const before = await settled(page, null)
  assertAligned(before)

  await page.setViewportSize({ width: 960, height: 800 })
  const after = await settled(page, before.cols)
  assertAligned(after)
})
```

- [ ] **Step 16: Run the new spec in the container (red on the pre-change tree, green now)**

To see it red, stash nothing — run it against the tree from Step 10 first if still available, or trust Step 10's
unit red. Then:

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/row-gutter.spec.ts e2e/grid-width.spec.ts`
Expected: 2 passed.
Run: `PW_PROJECTS=webkit e2e/run-in-container.sh e2e/row-gutter.spec.ts e2e/grid-width.spec.ts`
Expected: 2 passed. (A worker runs these two specs it changed and never the suite — AGENTS.md "The gate belongs
to whoever integrates".)

- [ ] **Step 17: Static gates**

Run: `npm --prefix frontend run lint`
Expected: exit 0 (`check-css-integrity` in particular: no `undefined-var`, no surface painting a kit identity).
Run: `npm --prefix frontend run typecheck`
Expected: exit 0.

- [ ] **Step 18: Commit**

```bash
git add frontend/src/styles/tokens.css frontend/src/styles/base.css frontend/src/style.css \
  frontend/src/scrollback/controller.ts \
  frontend/src/terminal-content.ts frontend/src/scrollback/trailing-edge.test.ts \
  frontend/src/terminal-content.test.ts frontend/src/scrollback/controller.test.ts \
  e2e/grid-width.spec.ts e2e/row-gutter.spec.ts
git commit -F - <<'EOF'
feat(frontend): rows carry the pane gutter, so a row can paint to the edge (nocx-9bpeq.8)

The pane inset everything by 10px and the scrollback layout cancelled the
trailing half so the scrollbar sat flush. A failed block's tint and its bar
need a row that reaches the edge, so the gutter moves into the rows: every
child of the scrollback stack, the summoned answers and the composer carry
--pane-inline-padding, now var(--space-4), and the pane pads nothing.

That re-inserts an inset inside the scroller, which is the one thing the fit
depended on not existing (nocx-vydj). usableViewport now subtracts the live
row's computed inline padding, read off the element like the body padding,
so the grid is fitted to the box it is drawn in. The running region states
padding-block instead of the shorthand, whose 0 would have reset the inset.

The grid is 22px narrower than before. row-gutter.spec.ts watches the frozen
and live columns share an edge across a resize, cols equal the fit, and the
scrollbar stay the pane's edge. The rail's glyph is --icon-size-lg.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task T6: A block header is quiet when the command succeeded and unmistakable when it failed (nocx-9bpeq.6)

Spec: §3 (anatomy, status table, failed row, between rows), §5.5 (pet ledges), §8 (header rhythm).
Depends on T1, T2, T3, T4, T5, T8, nocx-foyr.

**What changes, in one place each.**

- `createHeader` (`frontend/src/scrollback/blocks.ts:727-870`) no longer settles anything. It builds
  `.cmd-header > .cmd-header-meta` holding the "where" `Meta` (left) and `.cmd-header-right` (right), then
  the command row. The running state gets a vanilla `Spinner` and a duration `Meta`; the ask kind's waiting
  state gets `.cmd-header-waiting` (a feature placement span) holding a Spinner and an accent `Meta`.
- `durationChip` / `terminalChip` / `settleHeaderRight` (640-712) are replaced by **one** exported-in-module
  function `settleBlockOutcome(block, kind, durationMs, outcome)`. It is the one place that writes the
  right group AND `block.dataset.outcome`. Every former caller — the frozen builder
  (`createCommandBlock`, 1461-1480), `completeRestoredBlock` (2020-2025) and the ask close (2865-2866) — calls
  it with the BLOCK, after the header is attached.
- `TerminalChipSpec { ok, text }` becomes `TerminalOutcomeSpec { outcome: 'success' | 'failure' |
'cancelled'; text: string }`. The kinds keep their words (`ok`, `exit N`, `completed`, `failed`,
  `stopped`); the header renders the word only when `outcome !== 'success'`.
- `data-outcome` is set on `.cmd-block` for **every** settled outcome (`success`, `failure`, `cancelled`) and
  removed when the kind states none. Only `failure` is painted. Setting it for success is a deliberate
  extension of spec §3.3: the success word leaves the DOM, and twenty e2e specs wait on "completed"/"ok" — the
  attribute is the observable that replaces the word, never a class a test invents.
- All `.cmd-header*` and `.cmd-block` row rules move from `frontend/src/style.css` (1008-1143, 1253-1276) to a
  new **surface** stylesheet `frontend/src/styles/surfaces/command-block.css`. Surface, not component:
  `styles/components/` is the kit's layer and is exempt from `surface-paints-kit`
  (`frontend/lint-fixtures/check-css-integrity.mjs:663-669`); a block places kit components (Meta, Badge,
  IconButton, Spinner), so its stylesheet must be one the gate reads. `opacity` is placement by the gate's
  own list (`APPEARANCE_PROPERTIES`, :307-314), so revealing the ⋮ from a surface is allowed.

**Spinner: vanilla, not a render island.** Spec §6.3 names a render island for the running Spinner. The
running header is not disposed by any owner: `freezeBlock` replaces the whole element with `replaceChild`
(blocks.ts:1635-1637), and the visual freeze may be deferred (u7uh.8) — an island mounted there would leak
one Solid root per command. `Spinner` is static markup with no reactive state (`ui/spinner.tsx`: one span,
class, role, aria-label, data-size), so it gets a vanilla emitter `createSpinner` beside it, held to the
Solid component by a parity test — the T4 pattern (§6.2), not a new mechanism.

**Status table, as implemented** (spec §3.1; read from `BLOCK_KIND_RULES`, blocks.ts:241-353):

| Kind       | Header status / facts                    | `outcome`   | Word rendered      | Meta tone | Row        |
| ---------- | ---------------------------------------- | ----------- | ------------------ | --------- | ---------- |
| command    | `success`/`failure` with exit 0          | `success`   | none               | —         | none       |
| command    | exit ≠ 0                                 | `failure`   | `exit N`           | `danger`  | tint + bar |
| command    | `entered`, `unreconciled`, exitCode null | none        | none               | —         | none       |
| ask        | `success`                                | `success`   | none (`completed`) | —         | none       |
| ask        | `failure`                                | `failure`   | `failed`           | `danger`  | tint + bar |
| ask        | `cancelled`                              | `cancelled` | `stopped`          | `dim`     | none       |
| ask        | anything else                            | none        | none               | —         | none       |
| text, tool | —                                        | none        | none               | —         | none       |

The spec's "`unknown`, `unreconciled`, `entered` → the kind's word, dim" has no word to render: no kind
declares one for those statuses today, so they render nothing, exactly as now.

**Files:**

- Create: `frontend/src/ui/spinner-element.ts`, `frontend/src/ui/spinner-element.test.tsx`
- Create: `frontend/src/styles/surfaces/command-block.css`
- Create: `e2e/block-outcome.spec.ts`
- Modify: `frontend/src/scrollback/blocks.ts:195-260` (spec types, command/ask `terminal` rules),
  `640-870` (chips → Meta, settle, createHeader), `1461-1480` and `1566-1585` (builders),
  `2020-2025` (`completeRestoredBlock`), `2254-2262` (`_startTicker`), `2657-2680` (`stopWaiting`),
  `2862-2866` (ask close)
- Modify: `frontend/src/style.css` — delete 1008-1143 (`.cmd-block` row rules through `.cmd-header-exit`) and
  1253-1276 (tool-kind header rules); add `@import './styles/surfaces/command-block.css';` in the surfaces
  block (after `skill-view.css`, line 121)
- Modify: `frontend/src/pets/overlay.ts:86-90`
- Modify: `frontend/src/ui/README.md` — Spinner row notes the vanilla emitter and its parity rule
- Modify (tests, listed exhaustively in Steps 11-13): `frontend/src/scrollback/blocks.test.ts`,
  `frontend/src/scrollback/restored-block.test.ts`, `frontend/src/scrollback/turn-children.test.ts`,
  `frontend/src/panes-layout.test.ts`, `frontend/src/terminal-content.test.ts`, and 17 e2e specs

**Interfaces:**

- Consumes:
  - T4 `frontend/src/ui/meta.ts`: `createMeta(parts: readonly MetaPart[], opts?: MetaOptions): HTMLSpanElement`;
    `updateMeta(el: HTMLSpanElement, parts: readonly MetaPart[], opts?: MetaOptions): void`;
    `type MetaTone = 'muted' | 'dim' | 'danger' | 'accent'`; `type MetaPart = string | { text: string;
emphasis?: 'strong' }`; `interface MetaOptions { tone?: MetaTone; column?: 'duration'; title?: string }`.
    **Assumed markup** (T4 must match, or this task's selectors change): root `span.ui-meta[data-tone]`
    (`data-column` when set), each part `span.ui-meta__part`, separators `span.ui-meta__sep`.
  - T4 `frontend/src/cwd-label.ts`: `cwdLabel(cwd: string): string` (replaces blocks.ts:714-719).
  - T4 `frontend/src/ui/badge-element.ts` `createBadge` (the author mark already uses it after T4 — untouched here).
  - T5: the ⋮ is `createIconButton({ size: 'xs', … })` + `MoreIcon`, appended last in `.cmd-header-right`, its
    menu a kit `ContextMenu` island. `placeHeaderChip` inserts before `right.querySelector(':scope >
.ui-icon-button')` (T5 must leave that the only IconButton in the group).
  - T3: `--color-danger-surface` in all twelve themes. T8: rows are full width; `.cmd-block` must not set inline padding.
  - `frontend/src/ui/spinner.tsx`: `SpinnerProps { label: string; size?: 'sm' | 'md' }`.
- Produces:
  - `createSpinner(props: SpinnerProps): HTMLSpanElement` from `frontend/src/ui/spinner-element.ts`.
  - DOM contract (T9's end-to-end and every e2e spec read it):
    - `.cmd-block[data-outcome="success"|"failure"|"cancelled"]`, absent when the kind states no outcome
    - where: `.cmd-block > .cmd-header > .cmd-header-meta > .ui-meta` (parts: `[host, cwdLabel]` or `[cwdLabel]`)
    - duration: `.cmd-header-right > .ui-meta[data-column="duration"]`
    - status word: `.cmd-header-right > .ui-meta:not([data-column])`
    - running: `.cmd-header-right > .ui-spinner`; ask waiting: `.cmd-header-right > .cmd-header-waiting`
  - Pet ledge selector `.pane.active .cmd-block .ui-meta`.

**Acceptance Criteria:**

- A successful block renders no status element and carries `data-outcome="success"`.
- A failed block carries `data-outcome="failure"`, the tint (`--color-danger-surface`) and the 3px
  `--color-danger` bar inside its own box; in every theme its status text reaches 4.5:1 on the block's
  computed background, and it differs from a successful block by more than colour (the bar and the word).
- A running block shows the kit Spinner and the elapsed time; a cancelled turn shows `stopped` in the dim tone
  with no tint.
- Block actions stay reachable from the keyboard while visually quiet: the ⋮ is in the tab order at
  `opacity: 0` and focusing it reveals it.
- No rule for `.cmd-header*` remains in `frontend/src/style.css`; no `.nocx-chip`, `cmd-header-cwd`,
  `cmd-header-location`, `cmd-header-duration`, `cmd-header-exit`, `cmd-header-spinner`, `cmd-answer-waiting`
  or `cmd-header-chips` string remains in `frontend/src/scrollback/`, `frontend/src/pets/` or `e2e/`.
- `createSpinner` and `<Spinner>` produce the same tag, classes, attributes and children for both sizes.
- nocx-foyr is closed before this lands (edge).

- [ ] **Step 1: Write the failing Spinner parity test**

Create `frontend/src/ui/spinner-element.test.tsx`:

```tsx
// The vanilla Spinner is the Solid one's twin, and the pair is held together
// by this test rather than by care (spec 2026-09-14 §6.2): a variance added to
// one side only must fail here, not on the day a surface notices.
import { describe, expect, it } from 'vitest'
import { render } from 'solid-js/web'
import { Spinner, type SpinnerProps } from './spinner'
import { createSpinner } from './spinner-element'

function fromSolid(props: SpinnerProps): Element {
  const host = document.createElement('div')
  const dispose = render(() => <Spinner {...props} />, host)
  const el = host.firstElementChild!
  dispose()
  return el
}

function shape(el: Element) {
  return {
    tag: el.tagName,
    classes: [...el.classList].sort(),
    attrs: [...el.attributes]
      .filter((a) => a.name !== 'class')
      .map((a) => [a.name, a.value])
      .sort(),
    children: el.childElementCount,
  }
}

describe('createSpinner is <Spinner>’s twin', () => {
  const cases: SpinnerProps[] = [
    { label: 'Running' },
    { label: 'Running', size: 'sm' },
    { label: 'Loading', size: 'md' },
  ]
  for (const props of cases) {
    it(`matches for ${JSON.stringify(props)}`, () => {
      expect(shape(createSpinner(props))).toEqual(shape(fromSolid(props)))
    })
  }
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd frontend && npx vitest run src/ui/spinner-element.test.tsx`
Expected: FAIL — cannot resolve `./spinner-element`.

- [ ] **Step 3: Write the emitter**

Create `frontend/src/ui/spinner-element.ts`:

```ts
// Spinner, emitted without Solid (spec 2026-09-14 §6.2) — for a header the
// scrollback builds per command and discards by replacement, where a render
// island would have no owner to dispose it. Same identity, same stylesheet
// (styles/components/spinner.css), held to <Spinner> by spinner-element.test.tsx.
import type { SpinnerProps } from './spinner'

export function createSpinner(props: SpinnerProps): HTMLSpanElement {
  const el = document.createElement('span')
  el.className = 'ui-spinner'
  el.setAttribute('role', 'status')
  el.setAttribute('aria-label', props.label)
  el.dataset.size = props.size ?? 'md'
  return el
}
```

Add to the Spinner row in `frontend/src/ui/README.md` (Variance column): "`createSpinner` (`spinner-element.ts`)
is the vanilla twin for imperative surfaces, held to it by a parity test".

- [ ] **Step 4: Run it and watch it pass**

Run: `cd frontend && npx vitest run src/ui/spinner-element.test.tsx`
Expected: 3 passed.

- [ ] **Step 5: Write the failing header tests**

In `frontend/src/scrollback/blocks.test.ts`, replace the whole `describe('the header’s right-hand group has
one owner (nocx-hoeq3)', …)` (3422-3577) with the block below. It keeps that describe's helpers
(`settledCommand`, `closedTurn`) and states the new contract:

```ts
describe('the header states an outcome only when it is news (nocx-9bpeq.6, nocx-hoeq3)', () => {
  beforeAll(async () => {
    await shellHighlightReady
  })

  function settledCommand(durationMs: number, exitCode: number): HTMLElement {
    return createCommandBlock(
      'command',
      1,
      'df -h',
      '/home/dev',
      '',
      '<span class="term-line">out</span>',
      durationMs,
      exitCode,
      exitCode === 0 ? 'success' : 'failure',
      () => document.createElement('div'),
      noopSelect,
      freshStore(),
      'shell',
    )
  }

  function closedTurn(ms: number, status: 'success' | 'failure' | 'cancelled' = 'success') {
    let t = 0
    const { manager } = newManager(() => t)
    const turn = manager.addAnswerBlock('how much disk is free?', '/home/dev')
    turn.append('41G free')
    t = ms
    turn.close(status, status === 'failure' ? 'the model did not answer' : undefined)
    return turn.el
  }

  const status = (el: HTMLElement) =>
    el.querySelector<HTMLElement>(
      ':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])',
    )
  const duration = (el: HTMLElement) =>
    el.querySelector<HTMLElement>(
      ':scope > .cmd-header .cmd-header-right > .ui-meta[data-column="duration"]',
    )

  it('a command that succeeded says how long it took and nothing about how it went', () => {
    const el = settledCommand(27, 0)
    expect(el.dataset.outcome).toBe('success')
    expect(status(el)).toBeNull()
    expect(duration(el)?.textContent).toBe('27ms')
  })

  it('a command that failed says so in the danger tone, and the row is marked failed', () => {
    const el = settledCommand(5, 1)
    expect(el.dataset.outcome).toBe('failure')
    expect(status(el)?.textContent).toBe('exit 1')
    expect(status(el)?.dataset.tone).toBe('danger')
  })

  it('a turn uses its own words through the same function', () => {
    const ok = closedTurn(1200, 'success')
    expect(ok.dataset.outcome).toBe('success')
    expect(status(ok)).toBeNull()
    expect(duration(ok)?.textContent).toBe('1.2s')

    const failed = closedTurn(10, 'failure')
    expect(failed.dataset.outcome).toBe('failure')
    expect(status(failed)?.textContent).toBe('failed')
    expect(status(failed)?.dataset.tone).toBe('danger')

    const stopped = closedTurn(10, 'cancelled')
    expect(stopped.dataset.outcome).toBe('cancelled')
    expect(status(stopped)?.textContent).toBe('stopped')
    expect(status(stopped)?.dataset.tone).toBe('dim')
  })

  it('a block with no outcome of its own states none, and carries no data-outcome', () => {
    for (const s of ['entered', 'unreconciled'] as const) {
      const el = createCommandBlock(
        'command',
        1,
        'ssh box',
        '~',
        '',
        '',
        null,
        null,
        s,
        () => document.createElement('div'),
        noopSelect,
        freshStore(),
        'shell',
      )
      expect(el.dataset.outcome).toBeUndefined()
      expect(status(el)).toBeNull()
      expect(el.querySelector('.ui-spinner')).toBeNull()
    }
  })

  it('settling twice restates the group rather than growing a second copy', () => {
    const el = settledCommand(27, 0)
    completeRestoredLike(el, 2, 40)
    expect(el.dataset.outcome).toBe('failure')
    expect(el.querySelectorAll(':scope > .cmd-header .cmd-header-right > .ui-meta').length).toBe(2)
    expect(status(el)?.textContent).toBe('exit 2')
  })

  it('the ⋮ stays last in the group whatever settles after it', () => {
    const turn = closedTurn(1234)
    const right = turn.querySelector(':scope > .cmd-header .cmd-header-right')!
    expect(right.lastElementChild?.classList.contains('ui-icon-button')).toBe(true)
  })

  it('the where-meta names host and directory as one element, and never an emoji', () => {
    const el = createCommandBlock(
      'command',
      1,
      'ls',
      '/srv/app/current',
      'dev@staging',
      '',
      3,
      0,
      'success',
      () => document.createElement('div'),
      noopSelect,
      freshStore(),
      'shell',
    )
    const where = el.querySelector<HTMLElement>(
      ':scope > .cmd-header > .cmd-header-meta > .ui-meta',
    )!
    expect([...where.querySelectorAll('.ui-meta__part')].map((p) => p.textContent)).toEqual([
      'dev@staging',
      'app/current',
    ])
    expect(where.textContent).not.toMatch(/\p{Extended_Pictographic}/u)
  })

  it('a running block shows the kit spinner and a ticking duration', () => {
    const el = createRunningBlock(
      1,
      'sleep 10',
      '~',
      '',
      () => document.createElement('div'),
      noopSelect,
      freshStore(),
    )
    const spinner = el.querySelector(':scope > .cmd-header .cmd-header-right > .ui-spinner')
    expect(spinner?.getAttribute('data-size')).toBe('sm')
    expect(duration(el)?.textContent).toBe('0s')
    expect(el.dataset.outcome).toBeUndefined()
  })
})
```

Add this helper at file scope next to `noopSelect` (it drives the same public seam `completeRestoredBlock` uses,
without a manager):

```ts
/** Re-settle a built command block through the one outcome owner. */
function completeRestoredLike(el: HTMLElement, exitCode: number, durationMs: number): void {
  settleBlockOutcome(el, 'command', durationMs, {
    status: exitCode === 0 ? 'success' : 'failure',
    exitCode,
  })
}
```

and add `settleBlockOutcome` to the import from `./blocks` (Step 7 exports it — `check-dead-exports` accepts an
export used by a test only if the gate's allowlist says so; if it does not, export it for tests through the
existing `__test` pattern in blocks.ts when one exists, otherwise keep the helper test-local by settling via
`manager.completeRestoredBlock` on a restored block).

- [ ] **Step 6: Run them and watch them fail**

Run: `cd frontend && npx vitest run src/scrollback/blocks.test.ts -t "outcome only when it is news"`
Expected: FAIL — `dataset.outcome` undefined, `.ui-meta` not found, `settleBlockOutcome` not exported.

- [ ] **Step 7: Rewrite the kinds' outcome rules**

In `frontend/src/scrollback/blocks.ts` replace `TerminalChipSpec` and the `terminal` doc in
`HeaderRightRules`:

```ts
/** How a settled block ended, as its kind SAYS it (nocx-hoeq3, nocx-9bpeq.6).
 *  The outcome goes on the block (`data-outcome`) whatever it is; the WORD is
 *  rendered only when it is news — success is silent (spec 2026-09-14 §3.1). */
interface TerminalOutcomeSpec {
  readonly outcome: 'success' | 'failure' | 'cancelled'
  readonly text: string
}
```

`readonly terminal: (outcome: BlockOutcome) => TerminalOutcomeSpec | null` in `HeaderRightRules`.

Command rule body:

```ts
if (status === 'entered' || status === 'unreconciled' || exitCode === null) return null
return exitCode === 0
  ? { outcome: 'success', text: 'ok' }
  : { outcome: 'failure', text: `exit ${exitCode}` }
```

Ask rule body:

```ts
if (status === 'success') return { outcome: 'success', text: ASK_STATUS_CHIPS.done }
if (status === 'failure') return { outcome: 'failure', text: ASK_STATUS_CHIPS.failed }
if (status === 'cancelled') return { outcome: 'cancelled', text: ASK_STATUS_CHIPS.cancelled }
return null
```

- [ ] **Step 8: Replace the chips and the settle with Meta and one outcome owner**

Replace blocks.ts 640-719 (`durationChip`, `terminalChip`, `settleHeaderRight`, local `cwdLabel`) with:

```ts
// ── The header's right-hand group and the block's outcome: one owner ───────

/** THE duration fact, for every kind and both states that show one. The
 *  TEXT is the caller's: a running command shows whole seconds, a finished one
 *  the precise figure (nocx-hoeq3). The column variance keeps durations in a
 *  tabular column across blocks. */
function durationMeta(text: string): HTMLSpanElement {
  return createMeta([text], { tone: 'muted', column: 'duration' })
}

/**
 * Settle a block: its right-hand group and its `data-outcome`, from the kind's
 * rules, in one place (nocx-hoeq3, nocx-9bpeq.6).
 *
 * Called with the BLOCK, after its header is attached — at build for a block
 * whose outcome was already known, at close for a turn, at replay for a
 * restored command. IDEMPOTENT: the settled facts are cleared first, so a
 * second settle restates the group instead of growing it.
 */
export function settleBlockOutcome(
  block: HTMLElement,
  kind: BlockKind,
  durationMs: number | null,
  outcome: BlockOutcome,
): void {
  const right = block.querySelector<HTMLElement>(':scope > .cmd-header .cmd-header-right')
  if (!right) return
  for (const stale of right.querySelectorAll(
    ':scope > .ui-meta, :scope > .ui-spinner, :scope > .cmd-header-waiting',
  )) {
    stale.remove()
  }
  delete block.dataset.outcome
  const rules = blockKindRules(kind).headerRight
  for (const slot of rules.chips) {
    if (slot === 'duration') {
      if (durationMs !== null) placeHeaderChip(right, durationMeta(formatDuration(durationMs)))
      continue
    }
    const spec = rules.terminal(outcome)
    if (!spec) continue
    block.dataset.outcome = spec.outcome
    if (spec.outcome === 'success') continue
    placeHeaderChip(
      right,
      createMeta([spec.text], { tone: spec.outcome === 'failure' ? 'danger' : 'dim' }),
    )
  }
}
```

Add imports at the top of blocks.ts: `import { createMeta, updateMeta } from '../ui/meta'`,
`import { createSpinner } from '../ui/spinner-element'`, `import { cwdLabel } from '../cwd-label'`.

`placeHeaderChip` (≈ 900) becomes:

```ts
function placeHeaderChip(right: Element, chip: Element): void {
  right.insertBefore(chip, right.querySelector(':scope > .ui-icon-button'))
}
```

- [ ] **Step 9: Rebuild `createHeader`**

Replace `createHeader` (727-870) with a version whose signature drops `durationMs` and `exitCode` (the settle
owns them) and whose meta row is Meta:

```ts
/**
 * Create the header row for a block (spec 2026-09-14 §3): a meta row — where,
 * then the right-hand group — above the command. Metadata is text, not chips.
 * A settled block's outcome is NOT decided here: the builder calls
 * settleBlockOutcome once the header is attached, so there is one owner.
 */
function createHeader(
  kind: BlockKind,
  command: string,
  cwd: string,
  location: string,
  status: HeaderStatus,
  store: CommandSnapshotStore,
  author: CommandAuthor = 'shell',
): HTMLElement {
  const header = div('cmd-header')
  const rules = blockKindRules(kind)
  const metaRow = div('cmd-header-meta')

  // (keep the existing author-mark block here unchanged — T4 made it createBadge)

  // WHERE: host (when not this machine, nocx-6w4z) and directory, one Meta, so
  // the pair has one ellipsis and reads as one fact. No icon: the row is text.
  const where: MetaPart[] = []
  if (location) where.push(location)
  if (cwd) where.push(cwdLabel(cwd))
  if (where.length > 0) metaRow.appendChild(createMeta(where, { tone: 'muted' }))

  const right = div('cmd-header-right')
  if (status === 'running') {
    // Live elapsed time beside the kit spinner (nocx-6w4z). The ticker
    // updates this Meta in place.
    right.appendChild(createSpinner({ label: 'Running', size: 'sm' }))
    right.appendChild(durationMeta(formatRunningDuration(0)))
  } else if (status === 'waiting' && rules.statusChips) {
    // The ask kind's in-progress word beside the same spinner a running
    // command wears — one shape for "in progress" (AD-8). One placement span,
    // so whoever ends the wait removes one thing.
    const waiting = document.createElement('span')
    waiting.className = 'cmd-header-waiting'
    waiting.appendChild(createSpinner({ label: rules.statusChips.inProgress, size: 'sm' }))
    waiting.appendChild(createMeta([rules.statusChips.inProgress], { tone: 'accent' }))
    right.appendChild(waiting)
  }
  metaRow.appendChild(right)
  header.appendChild(metaRow)

  // (the command-row block — `cmdSpan` with highlighting — is unchanged)
  header.appendChild(cmdSpan)
  return header
}
```

(Import `type MetaPart` from `../ui/meta`.) Callers:

- `createCommandBlock` (≈1461): `createHeader(kind, command, cwd, location, status, store, author)`; after
  `wrapper.appendChild(header)` add `settleBlockOutcome(wrapper, kind, durationMs, { status, exitCode })`.
  The `right` lookup for the ⋮ stays `header.querySelector('.cmd-header-right')`.
- `createRunningBlock` (≈1566): `createHeader('command', command, cwd, location, 'running', store, author)`.
- `completeRestoredBlock` (2020-2025): delete the `right` lookup; `settleBlockOutcome(block, 'command',
durationMs, { status, exitCode })`.
- Ask close (2865-2866): `settleBlockOutcome(el, 'ask', now() - startedAt, { status, exitCode: null })`.
- `_startTicker` (2254-2262):

```ts
  private _startTicker(el: HTMLElement): void {
    this._stopTicker()
    const meta = el.querySelector<HTMLSpanElement>(
      ':scope > .cmd-header .cmd-header-right > .ui-meta[data-column="duration"]',
    )
    const started = this._cmdStartTime
    if (!meta || started === null) return
    this._ticker = setInterval(() => {
      updateMeta(meta, [formatRunningDuration(this._now() - started)], { tone: 'muted', column: 'duration' })
    }, 1000)
  }
```

- `stopWaiting` (2674-2677):

```ts
    const stopWaiting = (): void => {
      el.querySelector(':scope > .cmd-header .cmd-header-right > .cmd-header-waiting')?.remove()
```

Delete the now-unused `div('cmd-header-chips')` usage and any reference to `cmd-header-exit-ok/-fail` in comments.

- [ ] **Step 10: Run the header tests**

Run: `cd frontend && npx vitest run src/scrollback/blocks.test.ts -t "outcome only when it is news"`
Expected: 8 passed.

- [ ] **Step 11: Move and rewrite the block stylesheet**

Create `frontend/src/styles/surfaces/command-block.css`:

```css
/* A block of the ledger: its row, its header, its outcome (spec 2026-09-14 §3).
   A SURFACE stylesheet on purpose: the block places kit components — Meta,
   Badge, IconButton, Spinner — and styles/components/ is exempt from the gate
   that keeps a surface from repainting them. Rows are full width and carry the
   pane gutter (style.css `.scrollback-inner > *`, nocx-9bpeq.8); nothing here
   gives a block an inline padding of its own. */

.cmd-block {
  margin: 0;
  border: 0;
  border-top: 1px solid var(--color-divider);
  border-radius: 0;
  background: transparent;
  overflow: visible;
  position: relative;
}

.cmd-block:first-child {
  border-top: 0;
}

/* THE FAILED ROW (§3.3): a per-theme tint edge to edge and a bar at the row's
   left edge, inside its own box so no text moves. Before hover and selection,
   so their tint wins the ground and the bar stays. */
.cmd-block[data-outcome='failure'] {
  background: var(--color-danger-surface);
}

.cmd-block[data-outcome='failure']::before {
  content: '';
  position: absolute;
  inset-block: 0;
  inset-inline-start: 0;
  width: 3px;
  background: var(--color-danger);
  pointer-events: none;
}

/* A failed block nested in a turn has no row inset: its bar sits over the
   turn's containment gutter instead of over its own first column. */
.cmd-children > .cmd-block[data-outcome='failure']::before {
  inset-inline-start: calc(-1 * var(--space-3));
}

.cmd-block:hover {
  background: color-mix(in srgb, var(--color-text), transparent 97%);
}

.cmd-block.cmd-block-selected {
  background: color-mix(in srgb, var(--color-accent), transparent 94%);
}

.cmd-block.cmd-block-selected:hover {
  background: color-mix(in srgb, var(--color-accent), transparent 91%);
}

.cmd-header {
  display: flex;
  flex-direction: column;
  gap: 2px;
  /* Bottom: the gap between the command and what it printed, on the header
     because a running block has no .cmd-output. Top: the row's rhythm (§8). */
  padding: var(--space-2) 0 var(--space-3);
  pointer-events: auto;
  -webkit-user-select: none;
  user-select: none;
}

/* Where on the left, the right-hand group pushed out by its own auto margin —
   never space-between, which mis-spaces any child count but two (nocx-a44m). */
.cmd-header-meta {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}

.cmd-header-right {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: none;
  margin-left: auto;
}

.cmd-header-waiting {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
}

/* The ⋮ is quiet until the block is looked at, and never unreachable: opacity
   keeps its box and its tab stop (ADR-0008). Placement, by the gate's own list. */
.cmd-block > .cmd-header .cmd-header-right > .ui-icon-button {
  opacity: 0;
}

.cmd-block:hover > .cmd-header .cmd-header-right > .ui-icon-button,
.cmd-block.cmd-block-selected > .cmd-header .cmd-header-right > .ui-icon-button,
.cmd-block:focus-within > .cmd-header .cmd-header-right > .ui-icon-button {
  opacity: 1;
}

.cmd-header-text {
  font-family: var(--font-family-mono);
  font-size: var(--font-size-terminal);
  color: var(--color-text);
  white-space: pre-wrap;
  overflow-wrap: break-word;
  -webkit-user-select: text;
  user-select: text;
}

/* ONE TOOL CALL reads as a margin note (ADR-0040, ADR-0028 decision 4). */
.cmd-block[data-block-kind='tool'] .cmd-header {
  padding-bottom: 2px;
}

.cmd-block[data-block-kind='tool'] .cmd-header-text {
  font-size: var(--font-size-sm);
  color: var(--color-text-muted);
}

.cmd-block[data-block-kind='tool'][data-effect='mutate-destructive'] .cmd-header-text,
.cmd-block[data-block-kind='tool'][data-effect='privilege-change'] .cmd-header-text {
  color: var(--color-warning);
}
```

In `frontend/src/style.css`: delete lines 1008-1143 (`.cmd-block` through `.cmd-header-exit`, including
`.cmd-header-cwd`, `.cmd-header-duration`, `.cmd-header-spinner`, its media query and `@keyframes
cmd-spin-pulse`, and `.cmd-answer-waiting`) — keep `.cmd-answer-typing` and its keyframes, which the live
region and turns still use — and delete 1253-1276 (tool-kind header rules). Carry the load-bearing comments
(nocx-6w4z cursor, content-visibility history) into the new file's `.cmd-block` rule. Add
`@import './styles/surfaces/command-block.css';` after `@import './styles/surfaces/skill-view.css';`.

- [ ] **Step 12: Update the pet ledges**

`frontend/src/pets/overlay.ts:86-90`:

```ts
const DEFAULT_LEDGES: readonly LedgeSource[] = [
  { selector: '.tabbar', edge: 'bottom' },
  { selector: '.pane.active .cmd-block', edge: 'top' },
  // The meta a block wears (spec §5.5). The composer is not a ledge: the
  // animal does not stand where the caret is.
  { selector: '.pane.active .cmd-block .ui-meta', edge: 'top' },
]
```

`e2e/pets.spec.ts:53`: `.pane.active .nocx-chip` → `.pane.active .cmd-block .ui-meta`, and its comment "a chip
the block wears" → "the meta a block wears".

- [ ] **Step 13: Sweep the remaining tests onto the new DOM**

Mapping, applied mechanically (the ⋮ / `cmd-overflow-*` lines are T5's and are NOT touched here):

| Before                                                                          | After                                                                                                               |
| ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `X.querySelector('.cmd-header-exit')?.textContent).toBe('ok' \| 'completed')`   | `X.dataset.outcome).toBe('success')` and status element `toBeNull()`                                                |
| `'.cmd-header-exit'` / `'.cmd-header-exit-fail'` text `'exit N'` \| `'failed'`  | `':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])'` text, plus `dataset.outcome === 'failure'` |
| text `'stopped'`                                                                | same status selector, text `'stopped'`, `dataset.outcome === 'cancelled'`                                           |
| `.className).toBe('nocx-chip nocx-chip-ok cmd-header-exit cmd-header-exit-ok')` | delete — class lists are not the contract any more; assert `dataset.tone` on the status Meta                        |
| `'.cmd-header-exit'` `toBeNull()` / `-ok` / `-fail` `toBeNull()`                | status selector `toBeNull()` and `dataset.outcome` undefined                                                        |
| `'.cmd-header-duration'`                                                        | `':scope > .cmd-header .cmd-header-right > .ui-meta[data-column="duration"]'`                                       |
| `'.cmd-header-spinner'`                                                         | `':scope > .cmd-header .cmd-header-right > .ui-spinner'`                                                            |
| `'.cmd-answer-waiting'`                                                         | `'.cmd-header-waiting'`                                                                                             |
| `'.cmd-header-cwd'` text `'📁 dev/projects'`, `classList.contains('nocx-chip')` | `':scope > .cmd-header > .cmd-header-meta > .ui-meta'` last `.ui-meta__part` text `'dev/projects'`                  |
| `'.cmd-header-location'` text host                                              | where Meta's first `.ui-meta__part` text host                                                                       |
| `'.cmd-header-location'` `toBeNull()` / count 0                                 | where Meta has exactly one `.ui-meta__part`                                                                         |
| `'.cmd-header-chips'`                                                           | `'.cmd-header-meta'`                                                                                                |
| `extractRuleBlock(css, 'cmd-header-chips')` from `STYLE_ENTRY`                  | read `styles/surfaces/command-block.css`, block `cmd-header-meta`                                                   |

Unit sites (verified 2026-09-14 with `grep -n`; line numbers are before this task):

- `frontend/src/scrollback/blocks.test.ts`: 60, 71, 73, 79, 147-152, 172, 192, 326, 346, 423, 460, 720,
  843-851, 889, 1475-1478, 1510, 1542-1543, 1576, 1677, 2312, 2338, 2344-2345, 2390-2391, 2747-2748, 2816-2828,
  3413 (and 3422-3577 replaced in Step 5)
- `frontend/src/scrollback/restored-block.test.ts`: 425-429, 434-436, 445
- `frontend/src/scrollback/turn-children.test.ts`: 175, 252, 254, 389
- `frontend/src/panes-layout.test.ts`: 1076
- `frontend/src/terminal-content.test.ts`: 1782, 2380-2425 (the SSH header order test: where Meta before
  `.cmd-header-right`), 2444-2460 (stylesheet contract reads the new surface file), 7695-7698, 12002-12023, 12831. **Not** 8325 — that is the composer's `.nocx-chip`, T7's.

e2e sites:

- `e2e/agent-answer-stream.spec.ts:140`; `e2e/agent-ask.spec.ts:37` (comment), `648`, `809-810`, `1010`,
  `1033`; `e2e/agent-dump.spec.ts:116`; `e2e/agent-notes.spec.ts:152`; `e2e/agent-policy.spec.ts:180`;
  `e2e/agent-reads-the-screen.spec.ts:279`; `e2e/agent-refusal-stop.spec.ts:131`, `271`, `366`;
  `e2e/agent-restore.spec.ts:261-262`; `e2e/agent-turn.spec.ts:266-268`, `387`;
  `e2e/agent-whole-sentence.spec.ts:50-54` (comment), `450`, `510`, `515-524`;
  `e2e/assistant-intake.spec.ts:139`; `e2e/ask-about-a-running-command.spec.ts:259`, `459`, `463`;
  `e2e/ask-about-full-screen-program.spec.ts:408`; `e2e/nocxify-journey.spec.ts:459-461`, `476`, `501`, `524`,
  `532-533`, `611`; `e2e/notification-block-finished.spec.ts:186`, `223`;
  `e2e/notification-centre-choice.spec.ts:68`; `e2e/skill-ageing.spec.ts:189`, `257`;
  `e2e/skill-install-by-asking.spec.ts:567`; `e2e/skills.spec.ts:140`; `e2e/pets.spec.ts:53` (Step 12).

In e2e, `expect(X.locator('.cmd-header-exit')).toHaveText('completed', { timeout })` becomes
`expect(X).toHaveAttribute('data-outcome', 'success', { timeout })` with the same timeout: the attribute is
written by the same settle, at the same moment, as the word was.

Close the sweep with the zero check:

Run: `grep -rnE "nocx-chip|cmd-header-(cwd|location|duration|exit|spinner|chips)|cmd-answer-waiting" frontend/src/scrollback frontend/src/pets frontend/src/panes-layout.test.ts e2e`
Expected: no output. (`frontend/src/editor.ts`, `grant.ts` and their tests still match — the composer is T7's.)

Run: `grep -nE "^\.cmd-header|^\.cmd-block[ .:{\[]" frontend/src/style.css`
Expected: only `.cmd-block[data-granted]`, `.cmd-block .term-line[data-granted]`, `.cmd-block-grant-flash`, the
`cmd-output` wrap rules and `.cmd-children > .cmd-block` — no `.cmd-header`.

- [ ] **Step 14: Run the unit suites touched**

Run: `cd frontend && npx vitest run src/scrollback src/pets src/ui/spinner-element.test.tsx src/panes-layout.test.ts src/terminal-content.test.ts`
Expected: PASS.

- [ ] **Step 15: Write the outcome e2e**

Create `e2e/block-outcome.spec.ts`:

```ts
/**
 * A person scanning the ledger sees the failure and not the successes
 * (nocx-9bpeq.6, spec 2026-09-14 §3). Watched through the product: a real shell
 * runs `true` and `false`; every theme is applied the way Settings applies it
 * (the data-theme attribute on the root); contrast is read off computed styles.
 * No wait is on a duration.
 */
import { test, expect, promptReady } from './harness'
import type { Page } from './harness'

const THEMES = [
  'tokyo-night',
  'light',
  'ayu-dark',
  'catppuccin-latte',
  'catppuccin-mocha',
  'dracula',
  'gruvbox-dark',
  'nord',
  'one-dark',
  'rose-pine',
  'solarized-dark',
  'solarized-light',
]

async function run(page: Page, command: string) {
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0, { timeout: 30_000 })
  await promptReady(page)
}

test('success is silent, failure is legible, in every theme', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  await run(page, 'true')
  await run(page, 'false')

  const blocks = page.locator(
    '.pane.active .scrollback-inner > .cmd-block[data-block-kind="command"]',
  )
  const ok = blocks.nth(-2)
  const bad = blocks.nth(-1)
  const statusOf = (b: typeof ok) =>
    b.locator(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])')

  await expect(ok).toHaveAttribute('data-outcome', 'success')
  await expect(statusOf(ok)).toHaveCount(0)
  await expect(bad).toHaveAttribute('data-outcome', 'failure')
  await expect(statusOf(bad)).toHaveText('exit 1')

  for (const theme of THEMES) {
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)
    const measured = await bad.evaluate((block) => {
      const parse = (c: string) => (c.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number)
      const lum = ([r, g, b]: number[]) => {
        const f = (v: number) => {
          const s = v / 255
          return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
        }
        return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
      }
      const status = block.querySelector(
        ':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])',
      )!
      const fg = lum(parse(getComputedStyle(status).color))
      const bg = lum(parse(getComputedStyle(block).backgroundColor))
      const bar = getComputedStyle(block, '::before')
      return {
        ratio: (Math.max(fg, bg) + 0.05) / (Math.min(fg, bg) + 0.05),
        barWidth: bar.width,
        barColor: bar.backgroundColor,
        dangerSurfaceSet:
          getComputedStyle(block).getPropertyValue('--color-danger-surface').trim() !== '',
      }
    })
    expect(measured.dangerSurfaceSet, theme).toBe(true)
    expect(measured.ratio, theme).toBeGreaterThanOrEqual(4.5)
    // Not by colour alone: the bar is drawn.
    expect(measured.barWidth, theme).toBe('3px')
  }
})

test('the quiet ⋮ is still reachable from the keyboard', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  await run(page, 'true')
  const block = page.locator('.pane.active .scrollback-inner > .cmd-block').last()
  const dots = block.locator(':scope > .cmd-header .cmd-header-right > .ui-icon-button')
  await page.mouse.move(0, 0)
  await expect(dots).toHaveCSS('opacity', '0')
  await dots.focus()
  await expect(dots).toBeFocused()
  await expect(dots).toHaveCSS('opacity', '1')
})
```

(`blocks.nth(-2)` — Playwright's `nth(-1)` is the last; use `.nth(await blocks.count() - 2)` if the pinned
Playwright rejects negative indices other than -1.)

- [ ] **Step 16: Run it in the container**

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/block-outcome.spec.ts e2e/pets.spec.ts e2e/notification-block-finished.spec.ts`
Expected: all passed.
Run: `PW_PROJECTS=webkit e2e/run-in-container.sh e2e/block-outcome.spec.ts`
Expected: 2 passed. (The other 16 swept e2e specs are the coordinator's merged-tree run.)

- [ ] **Step 17: Static gates**

Run: `npm --prefix frontend run lint`
Expected: exit 0 — `check-css-integrity` reads `surfaces/command-block.css` and finds no kit identity painted
(only `opacity` on `.ui-icon-button`); `check-dead-exports` finds `createSpinner` used.
Run: `npm --prefix frontend run typecheck`
Expected: exit 0.

- [ ] **Step 18: Commit**

```bash
git add frontend/src/ui/spinner-element.ts frontend/src/ui/spinner-element.test.tsx frontend/src/ui/README.md \
  frontend/src/styles/surfaces/command-block.css frontend/src/style.css frontend/src/scrollback \
  frontend/src/pets/overlay.ts frontend/src/panes-layout.test.ts frontend/src/terminal-content.test.ts e2e
git commit -F - <<'EOF'
feat(frontend): a block header is quiet on success and unmistakable on failure (nocx-9bpeq.6)

Every successful block wore an ok pill and a failed one differed only by
the word in red, all of it in a chip family the kit did not own. The header
now states where as one Meta, the duration as a Meta in a tabular column,
and a status word only when it is news: exit N and failed in the danger
tone, stopped dimmed, success silent.

The outcome has one owner. settleBlockOutcome replaces the chip builders and
is called with the block, after its header is attached, from all three
settle points, so data-outcome and the right-hand group cannot disagree. The
attribute is set for every outcome, success included, because the success
word left the DOM and the e2e specs that waited on it needed an observable
the product writes, not one a test invents. Only failure is painted: a
per-theme tint and a bar inside the row, which T8 made full width.

The running spinner is the kit's, emitted without Solid beside its twin and
held to it by a parity test: the running header is discarded by replacement
with no owner to dispose a render island. The block's CSS moves to a surface
stylesheet so the gate that stops a surface repainting kit components reads
it. The pet stands on block meta, never on the composer.

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task T7: the composer is drawn from tokens and the kit, with no clock (nocx-9bpeq.7)

Depends on T4 (`createMeta`, `updateMeta`, `cwdLabel`) and T2 (tokens). It lands its own kit piece (`createButton`)
because no other task needs one.

**Files:**

- Modify:
  - `frontend/src/ui/format-time.ts:84-99` (`formatTimestamp`) and `frontend/src/ui/format-time.test.ts:72-81`
  - `frontend/src/ui/operation-row.test.tsx:290-295` and `frontend/src/skill-view/open-skill.test.ts:328,368`: they
    pin `toLocaleString()`
  - `frontend/src/ui/button.tsx` (the `truncate` prop), `frontend/src/styles/components/button.css`,
    `frontend/src/ui/button.test.tsx`
  - `frontend/src/editor.ts`:
    - `:160-200` fields
    - `:235-247` clock docblock and field
    - `:325-395` chrome construction
    - `:498-526` clock methods
    - `:537-645` setters
    - `:990-1010` `show()`
    - `:1144-1155` `hide()`
    - `:1176-1190` `dispose()`
  - `frontend/src/grant.ts:32-40` (the chip it creates when none is passed)
  - `frontend/src/terminal-content.ts:3459-3465`, `:5413`, `:5557` (the controls-row selector)
  - `frontend/src/style.css`: remove every `.nocx-editor*` rule, `.nocx-freeze-dismiss`, and
    `.nocx-chip*` once unused; add an `@import`
  - `frontend/src/pets/overlay.ts:86-90` (export `DEFAULT_LEDGES`)
  - Tests: `frontend/src/editor.test.ts`, `frontend/src/editor-recovery.test.ts`, `frontend/src/grant.test.ts`,
    `frontend/src/terminal-content.test.ts`, `frontend/src/block-cursor.test.ts:98`, `frontend/src/pets/overlay.test.ts`
  - e2e selectors:
    - `e2e/shell-mode.spec.ts:235`
    - `e2e/agent-ask.spec.ts:131`
    - `e2e/ask-attaches-the-frozen-screen.spec.ts:38,279`
    - `e2e/assistant-readiness.spec.ts:78,264-280`
    - `e2e/notification-centre-grouping.spec.ts:462`
    - `e2e/ops-indicator.spec.ts:124`
    - `e2e/ask-gesture.ts:44`
    - `e2e/agent-reads-the-screen.spec.ts:404,409,491`
    - `e2e/local-drop.spec.ts:96`
    - `e2e/upload.spec.ts:123`
- Create: `frontend/src/ui/button-element.ts`, `frontend/src/ui/button-element.test.tsx`,
  `frontend/src/styles/surfaces/composer.css`
- Docs: `frontend/src/ui/README.md` (a Button row note for `createButton` and `truncate`)

**Interfaces:**

- Consumes:
  - from T4: `createMeta(parts: readonly MetaPart[], opts?: MetaOptions): HTMLSpanElement`,
    `updateMeta(el: HTMLSpanElement, parts: readonly MetaPart[], opts?: MetaOptions): void`,
    `type MetaPart = string | { text: string; emphasis?: 'strong' }`,
    `MetaOptions { tone?: MetaTone; column?: 'duration'; title?: string }`, and `cwdLabel(cwd: string): string`
    from `frontend/src/cwd-label.ts`;
  - from T2: `--control-height-xs`, `--space-*`, `--color-divider`, `--terminal-background`;
  - from T8, if it landed first: a `padding-inline: var(--pane-inline-padding)` declaration on `.nocx-editor`.
- Produces:
  - `createButton(opts: CreateButtonOptions): HTMLButtonElement` in `frontend/src/ui/button-element.ts`:
    - `CreateButtonOptions { label: string; variant?: ButtonVariant; size?: ButtonSize; truncate?: boolean; title?: string; ariaLabel?: string; disabled?: boolean; onClick: (e: MouseEvent) => void }`
    - T10 uses it in `grant.ts`.
  - `ButtonProps.truncate?: boolean`, which renders `data-truncate="true"`.
  - Composer DOM hooks:
    - `.nocx-editor-chrome` is the row;
    - `.nocx-editor-context` is the left group, holding one `ui-meta`;
    - `.nocx-editor-controls` is the right group; the grant mount and the freeze marker host move there from
      `.nocx-editor-chrome-left`;
    - controls carry `data-control="recovery" | "model-endpoint" | "model" | "grant"`.
  - `export const DEFAULT_LEDGES` from `frontend/src/pets/overlay.ts`.

**Deviation from the spec, stated:** spec §6.3 says the composer's controls are Solid render islands. They are
vanilla-emitted instead (`createButton`, held to `<Button>` by a parity test).

- Four writers mutate these elements imperatively every time state changes: `GrantController.updateChip`
  (`grant.ts:127-146`: textContent, title, aria-label, `dataset.state`), `setModelChip` (`editor.ts:563-613`:
  textContent, title, aria-label, `disabled`), `setRecoveryAction` and `GrantController.setVisible`
  (`style.display`).
- A Solid island would either have its DOM overwritten by them or require moving all four onto signals, which
  is an ownership change the spec did not ask for.
- The spec's own rule for elements written from imperative code (§6.2) is the vanilla emitter with a parity
  test.

**Naming, stated:** the emitter is `ui/button-element.ts`, not `ui/button.ts`.

- Under `moduleResolution: bundler` and Vite's default extensions, `.ts` resolves before `.tsx`.
- A `ui/button.ts` would capture all 47 `from './button'` / `'../ui/button'` imports and break them.

**Acceptance Criteria:**

- The clock is gone: no `timeChip`, `startClock`, `stopClock`, `setTime` or `.nocx-editor-time` anywhere under
  `frontend/src` or `e2e`. A unit test asserts the chrome row holds no text matching `\d{1,2}:\d{2}` after `show()`.
- `formatTimestamp` returns English day-month-year and 24-hour time, `14 Sep 2026, 19:32:23`, built without ICU,
  and a unit test pins it while `Intl.DateTimeFormat` and `Date.prototype.toLocaleString` are stubbed to Russian.
  Its two production callers (`skill-view/skill-view-header.tsx:85`, `ui/operation-row.tsx:296`) and their
  tests follow.
- `createButton` and `<Button>` produce the same tag, classes, `data-*`, `type`, `title`, ARIA attributes and
  text for every variance the composer uses and for `truncate`. Adding a variance to one side only fails the
  test.
- The composer's context is one `ui-meta`:
  - local: `[cwdLabel(cwd)]`;
  - remote: `[{ text: location, emphasis: 'strong' }, cwdLabel(cwd)]`;
  - tone `muted` while focus is inside the composer, `dim` otherwise;
  - title is the full cwd.
- Recovery, model endpoint, model and grant are `createButton({ variant: 'ghost', size: 'sm' })`. The two model
  chips and the grant also set `truncate: true`.
  - Every existing behaviour test still passes on the new hooks: click routing, the disabled rung, hidden until
    set, and labels owned by GrantController.
- No `.nocx-editor*`, `.nocx-freeze-dismiss` or `.nocx-editor-grant` / `-model` rule remains in `style.css`.
  `styles/surfaces/composer.css` holds them and is reachable from `style.css`.
- `npm run lint` is green, including `surface-paints-kit`, which now sees `ui-button` in the composer.
- No accent bar: `composer.css` declares no `border-left`. The top edge is `1px solid var(--color-divider)`, the
  ground is `var(--terminal-background)`, and the chrome row has a fixed `height: var(--control-height-xs)`.
- `e2e/assistant-readiness.spec.ts` still proves the chrome row does not change height when the model controls
  appear.
- No selector in `DEFAULT_LEDGES` matches any element inside `.nocx-editor` (unit test).
- `.nocx-chip`:
  - If `grep -rn "nocx-chip" frontend/src e2e` finds nothing outside `style.css` after this task, the family's
    CSS is deleted here.
  - Otherwise it stays, and the close reason names the remaining consumers for T6.
- ModeIndicator behaviour is unchanged: `ask-entry` tests pass.

- [ ] **Step 1: Failing test for `formatTimestamp`**

Replace `frontend/src/ui/format-time.test.ts:72-81` with:

```ts
describe('the exact moment, for the hover behind the label', () => {
  it('is English with a 24-hour clock whatever locale the engine defaults to', () => {
    // A Russian default, the way the owner's WebView answered: the clock chip
    // read "пн, 14 сент. 21:04:04" in an English UI (spec §1, §5.4).
    const RealFormat = Intl.DateTimeFormat
    const format = vi
      .spyOn(Intl, 'DateTimeFormat')
      .mockImplementation(
        (locales?: string | string[], options?: Intl.DateTimeFormatOptions) =>
          new RealFormat(locales ?? 'ru-RU', options),
      )
    const toLocale = vi.spyOn(Date.prototype, 'toLocaleString').mockImplementation(function (
      this: Date,
    ) {
      return new RealFormat('ru-RU', { dateStyle: 'short', timeStyle: 'medium' }).format(this)
    })
    try {
      expect(formatTimestamp(new Date(2026, 8, 14, 19, 32, 23).getTime())).toBe(
        '14 Sep 2026, 19:32:23',
      )
      expect(formatTimestamp(new Date(2026, 0, 5, 0, 4, 9).getTime())).toBe('5 Jan 2026, 00:04:09')
    } finally {
      format.mockRestore()
      toLocale.mockRestore()
    }
  })

  it('says nothing for a non-time', () => {
    expect(formatTimestamp(Number.NaN)).toBe('')
  })
})
```

Add `vi` to the file's vitest import: `import { describe, expect, it, vi } from 'vitest'`.

Run: `cd frontend && npx vitest run src/ui/format-time.test.ts`
Expected: FAIL. The output is `14.09.2026, 19:32:23` or the host locale's form, not `14 Sep 2026, 19:32:23`.

- [ ] **Step 2: Implement it**

Replace `formatTimestamp` (`frontend/src/ui/format-time.ts:84-99`) with:

```ts
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const two = (n: number): string => String(n).padStart(2, '0')

/** The exact moment, in English, on a 24-hour clock, in the reader's time zone.
 *
 *  Two callers, and they want it for opposite reasons. An operation row wants
 *  the hover detail behind a relative label; a RECORD (the Skills card) wants
 *  the date a thing was taken, read months later. One owner of the absolute
 *  form either way.
 *
 *  Built by hand, not through Intl: the UI is English, and the engine's
 *  default locale is the WebView's — which is how "пн, 14 сент. 21:04:04"
 *  reached an English screen (spec 2026-09-14 §5.4). Even with an explicit
 *  'en-GB', ICU versions disagree on the month ("Sep" vs "Sept"), and the two
 *  engines this ships on carry different ICUs. */
export function formatTimestamp(at: number): string {
  if (!Number.isFinite(at)) return ''
  const d = new Date(at)
  return `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}, ${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`
}
```

Update the callers' tests so they pin the owner rather than the engine:

- `frontend/src/ui/operation-row.test.tsx:293`: `new Date(ENDED).toLocaleString(),` → `formatTimestamp(ENDED),`,
  and add `import { formatTimestamp } from './format-time'`.
- `frontend/src/skill-view/open-skill.test.ts:328` and `:368`:
  `new Date('2026-09-03T12:00:00Z').toLocaleString()` → `formatTimestamp(Date.parse('2026-09-03T12:00:00Z'))`,
  and add `import { formatTimestamp } from '../ui/format-time'`.

Run: `cd frontend && npx vitest run src/ui/format-time.test.ts src/ui/operation-row.test.tsx src/skill-view/open-skill.test.ts`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/ui/format-time.ts frontend/src/ui/format-time.test.ts frontend/src/ui/operation-row.test.tsx frontend/src/skill-view/open-skill.test.ts
git commit -m "fix(frontend): a moment the kit prints is English on a 24-hour clock, not the webview's locale (nocx-9bpeq.7)

<body: the Russian clock in an English UI; why by hand rather than Intl (ICU disagrees on Sep/Sept across the two engines); the two callers>

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 4: Failing tests for Button `truncate` and `createButton` parity**

Append to `frontend/src/ui/button.test.tsx`:

```tsx
describe('Button truncate (nocx-9bpeq.7)', () => {
  it('yields to its row: data-truncate is set only when asked', () => {
    const { container } = render(() => (
      <>
        <Button onClick={vi.fn()} size="sm" truncate>
          long
        </Button>
        <Button onClick={vi.fn()} size="sm">
          short
        </Button>
      </>
    ))
    const [long, short] = [...container.querySelectorAll('button')]
    expect(long.dataset.truncate).toBe('true')
    expect(short.hasAttribute('data-truncate')).toBe(false)
  })

  it('the stylesheet ellipsises a small truncating button on one line', () => {
    const css = readFileSync(new URL('../styles/components/button.css', import.meta.url), 'utf8')
    const rule =
      /\.ui-button\[data-size='sm'\]\[data-truncate='true'\]\s*\{([^}]*)\}/.exec(css)?.[1] ?? ''
    expect(rule).toMatch(/text-overflow:\s*ellipsis/)
    expect(rule).toMatch(/white-space:\s*nowrap/)
    expect(rule).toMatch(/overflow:\s*hidden/)
    expect(rule).toMatch(/display:\s*inline-block/)
  })
})
```

Create `frontend/src/ui/button-element.test.tsx`:

```tsx
// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { Button, type ButtonSize, type ButtonVariant } from './button'
import { createButton, type CreateButtonOptions } from './button-element'

afterEach(() => cleanup())

/** Everything the kit's contract is made of, and nothing the engine adds. */
function shape(el: Element): Record<string, unknown> {
  const attrs = [...el.attributes]
    .filter((a) => a.name !== 'class')
    .map((a) => [a.name, a.value] as const)
    .sort(([x], [y]) => x.localeCompare(y))
  return {
    tag: el.tagName,
    classes: [...el.classList].sort(),
    attrs,
    text: el.textContent,
    children: [...el.children].map(shape),
  }
}

const CASES: ReadonlyArray<Omit<CreateButtonOptions, 'onClick'>> = [
  { label: 'Enable command editor', variant: 'ghost', size: 'sm' },
  {
    label: 'openrouter',
    variant: 'ghost',
    size: 'sm',
    truncate: true,
    title: 'openrouter',
    ariaLabel: 'Answers with openrouter. Open Endpoints.',
  },
  { label: 'm-a', variant: 'ghost', size: 'sm', truncate: true, disabled: true },
  { label: 'Plain' },
  { label: 'Primary', variant: 'primary' },
]

describe('createButton is the Button, emitted without Solid (spec §6.2)', () => {
  for (const c of CASES) {
    it(`matches <Button> for ${JSON.stringify(c)}`, () => {
      const { container } = render(() => (
        <Button
          onClick={vi.fn()}
          variant={c.variant as ButtonVariant | undefined}
          size={c.size as ButtonSize | undefined}
          truncate={c.truncate}
          title={c.title}
          ariaLabel={c.ariaLabel}
          disabled={c.disabled}
        >
          {c.label}
        </Button>
      ))
      const solid = container.querySelector('button')!
      const vanilla = createButton({ ...c, onClick: vi.fn() })
      expect(shape(vanilla)).toEqual(shape(solid))
    })
  }

  it('fails when a variance exists on one side only — the check is not vacuous', () => {
    const vanilla = createButton({
      label: 'x',
      variant: 'ghost',
      size: 'sm',
      truncate: true,
      onClick: vi.fn(),
    })
    vanilla.dataset.extra = 'drift'
    const { container } = render(() => (
      <Button onClick={vi.fn()} variant="ghost" size="sm" truncate>
        x
      </Button>
    ))
    expect(shape(vanilla)).not.toEqual(shape(container.querySelector('button')!))
  })

  it('routes a click to onClick exactly once', () => {
    const onClick = vi.fn()
    createButton({ label: 'x', onClick }).click()
    expect(onClick).toHaveBeenCalledTimes(1)
  })
})
```

Run: `cd frontend && npx vitest run src/ui/button.test.tsx src/ui/button-element.test.tsx`
Expected: FAIL. `truncate` does not exist on `ButtonProps`, `./button-element` does not resolve, and the CSS rule
is missing.

- [ ] **Step 5: Implement `truncate` and `createButton`**

In `frontend/src/ui/button.tsx`:

- add to `ButtonProps`, after `tabIndex`:

```ts
  /**
   * The button yields to its row and ellipsises its label instead of growing
   * or wrapping (nocx-9bpeq.7). Defined for `size="sm"`, its only consumer —
   * the composer's controls, where a model id is long and a wrapped control
   * would move the scrollback that hangs from the row. Whoever sets it keeps
   * the whole value in `title` and the accessible name.
   */
  truncate?: boolean
```

- add `'truncate'` to `knownKeys`;
- add to the element, after the `data-size` spread:

```tsx
      data-truncate={local.truncate === true ? 'true' : undefined}
```

Append to `frontend/src/styles/components/button.css`, after the `data-size='sm'` rule:

```css
/* ── Truncate — the button yields to its row (nocx-9bpeq.7) ────────────
   `text-overflow` applies to a block container's own inline content; inside
   the base rule's inline-flex the label is an anonymous flex item and would
   hard-clip mid-glyph. So a truncating button is inline-block, with the line
   box as tall as the control so the label stays centred. Defined for the
   small size, the only consumer; its row places the width. */
.ui-button[data-size='sm'][data-truncate='true'] {
  display: inline-block;
  min-width: 0;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  text-align: start;
  line-height: var(--control-height-xs);
}
```

Create `frontend/src/ui/button-element.ts`:

```ts
// createButton — the kit's Button, emitted without Solid (spec 2026-09-14 §6.2).
//
// For imperative surfaces whose elements are rewritten by imperative owners:
// the composer's controls, whose text, title, accessible name and disabled
// state are written by setModelChip, setRecoveryAction and GrantController on
// every state change. A Solid island there would have its DOM written under
// it. The element is Button's — same identity, same data-* variance, same
// stylesheet — and button-element.test.tsx holds the two emitters to one shape.
//
// Named button-element.ts, not button.ts: `.ts` resolves before `.tsx`, and a
// button.ts would capture every `from './button'` import in the tree.

import type { ButtonSize, ButtonVariant } from './button'

export interface CreateButtonOptions {
  label: string
  variant?: ButtonVariant
  size?: ButtonSize
  truncate?: boolean
  title?: string
  ariaLabel?: string
  disabled?: boolean
  onClick: (e: MouseEvent) => void
}

export function createButton(opts: CreateButtonOptions): HTMLButtonElement {
  const el = document.createElement('button')
  el.className = 'ui-button'
  el.dataset.variant = opts.variant ?? 'default'
  if (opts.size && opts.size !== 'md') el.dataset.size = opts.size
  if (opts.truncate === true) el.dataset.truncate = 'true'
  el.type = 'button'
  el.disabled = opts.disabled === true
  el.title = opts.title ?? ''
  if (opts.ariaLabel !== undefined) el.setAttribute('aria-label', opts.ariaLabel)
  el.textContent = opts.label
  el.addEventListener('click', (e) => opts.onClick(e))
  return el
}
```

Run: `cd frontend && npx vitest run src/ui/button.test.tsx src/ui/button-element.test.tsx`
Expected: PASS.

- If a parity case fails on attribute order or on `disabled`/`title` serialisation, change `createButton` to
  match what `<Button>` renders.
- Never loosen `shape`.

Add under the Button row's variance cell in `frontend/src/ui/README.md`:

`data-truncate` (sm only: one line, ellipsis, the row places the width); **`createButton`** (`button-element.ts`)
is the same element emitted without Solid for imperatively-written controls, held to `<Button>` by
`button-element.test.tsx`.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/ui/button.tsx frontend/src/ui/button.test.tsx frontend/src/ui/button-element.ts frontend/src/ui/button-element.test.tsx frontend/src/styles/components/button.css frontend/src/ui/README.md
git commit -m "feat(ui-kit): Button truncates on request, and createButton emits it without Solid under a parity test (nocx-9bpeq.7)

<body: why vanilla and not a render island (four imperative writers); why button-element.ts and not button.ts (.ts resolves before .tsx)>

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 7: Failing tests for the composer**

In `frontend/src/editor.test.ts`:

- replace the tests at `:316-358` (setCwd and the four location-chip tests) and `:361-381` (setTime, clock
  order);
- keep `:383-389` with its selector changed as below.

```ts
const context = (container: ParentNode) =>
  container.querySelector<HTMLElement>('.nocx-editor-context .ui-meta')!

it('the context is one kit Meta naming the directory (spec §5.2)', () => {
  const { ed, container } = setup()
  ed.show()
  expect(context(container).textContent).toContain('~')
  ed.setCwd('/home/dev/projects')
  expect(context(container).textContent).toContain('dev/projects')
  expect(context(container).title).toBe('/home/dev/projects')
  expect(container.textContent).not.toContain('📁')
})

it('an SSH prompt names the host first, in the strong emphasis (nocx-3779, spec §5.2)', () => {
  const { ed, container } = setup()
  ed.show()
  ed.setCwd('/srv/nocx')
  ed.setLocation('root@192.168.0.57')
  const strong = context(container).querySelector('[data-emphasis="strong"]')
  expect(strong?.textContent).toBe('root@192.168.0.57')
  expect(context(container).textContent).toContain('srv/nocx')
})

it('a local session names no host at all (nocx-3779)', () => {
  const { ed, container } = setup()
  ed.show()
  ed.setLocation('')
  expect(context(container).querySelector('[data-emphasis="strong"]')).toBeNull()
})

it('the context dims when focus leaves the composer and returns when it comes back', () => {
  const { ed, view, container } = setup()
  ed.show()
  view.contentDOM.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
  expect(context(container).dataset.tone).toBe('muted')
  view.contentDOM.dispatchEvent(
    new FocusEvent('focusout', { bubbles: true, relatedTarget: document.body }),
  )
  expect(context(container).dataset.tone).toBe('dim')
})

it('the chrome row holds no clock (spec §5.4)', () => {
  const { ed, container } = setup()
  ed.show()
  const chrome = container.querySelector<HTMLElement>('.nocx-editor-chrome')!
  expect(chrome.textContent).not.toMatch(/\d{1,2}:\d{2}/)
  expect('setTime' in ed).toBe(false)
})

it('orders the context before the controls, and the controls are kit Buttons', () => {
  const { ed, container } = setup()
  ed.show()
  const row = [...container.querySelector<HTMLElement>('.nocx-editor-chrome')!.children]
  const ctx = container.querySelector('.nocx-editor-context')!
  const controls = container.querySelector('.nocx-editor-controls')!
  expect(row.indexOf(ctx)).toBeLessThan(row.indexOf(controls))
  for (const c of controls.querySelectorAll('[data-control]')) {
    expect(c.classList.contains('ui-button')).toBe(true)
    expect((c as HTMLElement).dataset.variant).toBe('ghost')
    expect((c as HTMLElement).dataset.size).toBe('sm')
  }
})
```

Replace `:1104-1112` (the model chip vocabulary test) with:

```ts
it('is a kit ghost Button that truncates, in the controls group (spec §5.2)', () => {
  const { ed } = setup()
  ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
  for (const chip of chipsOf(ed)) {
    expect(chip.classList.contains('ui-button')).toBe(true)
    expect(chip.dataset.truncate).toBe('true')
    expect(chip.closest('.nocx-editor-controls')).not.toBeNull()
  }
})
```

Replace `:1117-1127` (grant identity and position) with:

```ts
it('is the last control in the row, and is a kit Button', () => {
  const { ed } = setup()
  const controls = ed.root.querySelector('.nocx-editor-controls')!
  const grant = controls.querySelector<HTMLElement>('[data-control="grant"]')!
  expect(grant.classList.contains('ui-button')).toBe(true)
  expect([...controls.children].indexOf(grant)).toBe(controls.children.length - 1)
  ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
  expect([...controls.children].indexOf(grant)).toBe(controls.children.length - 1)
})
```

Apply this selector map to every remaining occurrence in `editor.test.ts`, `editor-recovery.test.ts`,
`terminal-content.test.ts` and the e2e files listed under **Files**. Every one is a lookup by role; none asserts
the old appearance.

| Old                                                     | New                                           |
| ------------------------------------------------------- | --------------------------------------------- |
| `.nocx-editor-recovery`                                 | `[data-control="recovery"]`                   |
| `.nocx-editor-grant`                                    | `[data-control="grant"]`                      |
| `.nocx-editor-model`                                    | `[data-control^="model"]`                     |
| `.nocx-editor-chrome-left`                              | `.nocx-editor-controls`                       |
| `.nocx-editor-cwd`, `.nocx-editor-location` (text read) | `.nocx-editor-context .ui-meta`               |
| `classList.contains('nocx-chip')`                       | `classList.contains('ui-button')` on controls |

```bash
sed -i \
  -e 's/\.nocx-editor-recovery/[data-control="recovery"]/g' \
  -e 's/\.nocx-editor-grant/[data-control="grant"]/g' \
  -e 's/\.nocx-editor-model/[data-control^="model"]/g' \
  -e 's/\.nocx-editor-chrome-left/.nocx-editor-controls/g' \
  -e 's/\.nocx-editor-cwd/.nocx-editor-context .ui-meta/g' \
  frontend/src/editor.test.ts frontend/src/editor-recovery.test.ts frontend/src/terminal-content.test.ts \
  e2e/shell-mode.spec.ts e2e/agent-ask.spec.ts e2e/ask-attaches-the-frozen-screen.spec.ts \
  e2e/assistant-readiness.spec.ts e2e/notification-centre-grouping.spec.ts e2e/ops-indicator.spec.ts \
  e2e/ask-gesture.ts e2e/agent-reads-the-screen.spec.ts e2e/local-drop.spec.ts e2e/upload.spec.ts
grep -rn "nocx-editor-location\|nocx-chip\|nocx-editor-time" frontend/src/*.test.ts e2e
```

Then rewrite by hand each line the grep still prints.

`terminal-content.test.ts`:

- `:1776-1780` and `:1794-1797`: the location chip.
  - SSH: `tab.pane.querySelector('.nocx-editor-context [data-emphasis="strong"]')?.textContent` is
    `root@192.168.0.57`.
  - Local: that query returns `null`.
- `:2472-2485`: the clock stylesheet test. Delete it with its `describe` (`nocx-a44m`'s clock no longer exists),
  and add:

```ts
describe('the composer chrome keeps its row height whatever it holds (nocx-i4h04, spec §5.2)', () => {
  it('declares a fixed height, never a floor, and never distributes its children', () => {
    const css = stripComments(readFileSync(COMPOSER_STYLE, 'utf8'))
    const chrome = stripComments(extractRuleBlock(css, 'nocx-editor-chrome') ?? '')
    expect(chrome).toMatch(/(^|;|\s)height\s*:\s*var\(--control-height-xs\)/)
    expect(chrome).not.toMatch(/min-height/)
    expect(chrome).not.toMatch(/justify-content\s*:\s*(space-between|space-around|space-evenly)/)
  })
})
```

Also define `const COMPOSER_STYLE = resolve(srcDir, 'styles/surfaces/composer.css')` beside `STYLE_ENTRY`
(`:28`).

- `:2494-2497`: the overlay regex reads `composer.css`. Replace `css.match(` with
  `stripComments(readFileSync(COMPOSER_STYLE, 'utf8')).match(`.
- `:8325`: `child.classList.contains('nocx-chip')` → `child.hasAttribute('data-control')`.
- `:9668`: `chrome.className = 'nocx-editor-chrome'` stays. The class still names the row.

`frontend/src/block-cursor.test.ts:98`: `resolve(import.meta.dirname ?? '.', 'style.css')` →
`resolve(import.meta.dirname ?? '.', 'styles/surfaces/composer.css')`.

`frontend/src/grant.test.ts:141`: `classList.contains('nocx-chip')` → `classList.contains('ui-button')`, and add
`expect(controller.chip.dataset.truncate).toBe('true')`.

`e2e/assistant-readiness.spec.ts:264-280`:

- `const cwd = page.locator('.pane.active .nocx-editor-cwd')` →
  `const context = page.locator('.pane.active .nocx-editor-context .ui-meta')`;
- replace its last assertion (control height equals cwd chip height) with:

```ts
// One line, ellipsised: a control fits inside the row's fixed height. A
// wrapped id would be taller than the row and the height above would move.
const row = (await chrome.boundingBox())!.height
expect((await chips.last().boundingBox())!.height).toBeLessThanOrEqual(row)
await expect(context).toBeVisible()
```

Add to `frontend/src/pets/overlay.test.ts`:

```ts
import { DEFAULT_LEDGES } from './overlay'
import { CommandEditor } from '../editor'

describe('the composer is not ground (spec §5.5)', () => {
  it('no default ledge selector matches anything inside the composer', () => {
    const pane = document.createElement('div')
    pane.className = 'pane active'
    document.body.append(pane)
    const ed = new CommandEditor({ submit: vi.fn(), cancel: vi.fn() })
    ed.mount(pane)
    ed.show()
    ed.setLocation('root@host')
    ed.setModelChip({ kind: 'ready', endpoint: 'openrouter', model: 'm-a' })
    for (const { selector } of DEFAULT_LEDGES) {
      const inside = [...document.querySelectorAll(selector)].filter((el) => ed.root.contains(el))
      expect(inside, selector).toEqual([])
    }
    ed.dispose()
    pane.remove()
  })
})
```

Add the `// @vitest-environment jsdom` header, if the file lacks it, and `vi` to its vitest import.

Run: `cd frontend && npx vitest run src/editor.test.ts src/editor-recovery.test.ts src/grant.test.ts src/pets/overlay.test.ts src/block-cursor.test.ts`
Expected: FAIL.

- `.nocx-editor-context` does not exist.
- `DEFAULT_LEDGES` is not exported.
- `composer.css` is missing.
- `setTime` still exists.

- [ ] **Step 8: Implement the composer**

`frontend/src/editor.ts`:

Imports, after `:27`:

```ts
import { createButton } from './ui/button-element'
import { createMeta, updateMeta, type MetaPart } from './ui/meta'
import { cwdLabel } from './cwd-label'
```

Fields `:165-200`:

- delete `timeChip`, `chromeLeft`, `locationChip`, `cwdChip`, and the `clock` field with its docblock
  (`:235-247`);
- keep `grantChip`, `recoveryChip`, `modelEndpointChip`, `modelChip`, `_modelChipTargets`, `_onModelChipClick`,
  `_location`;
- add:

```ts
  /** The row's two groups (spec §5.2): where the pending command runs, and the
   *  controls for the target Enter reaches. */
  private context: HTMLElement
  private controls: HTMLElement
  /** One kit Meta: host (strong, remote only) · directory. */
  private contextMeta: HTMLSpanElement
  private _cwd = '~'
  private _focused = false
```

Replace the chrome construction `:329-394` with:

```ts
// ── Editor chrome: the meta row (spec 2026-09-14 §5.2) ──────────────
// The same anatomy a block header has: where on the left, and here the
// controls for the target Enter reaches on the right. Placement only —
// every element in it is the kit's (styles/surfaces/composer.css).
this.chrome = document.createElement('div')
this.chrome.className = 'nocx-editor-chrome'

this.context = document.createElement('div')
this.context.className = 'nocx-editor-context'
this.contextMeta = createMeta(['~'], { tone: 'dim', title: '~' })
this.context.append(this.contextMeta)

this.controls = document.createElement('div')
this.controls.className = 'nocx-editor-controls'

// Recovery: hidden in the healthy state; one label in an exception state.
// The control IS the action — one click, no popover (nocx-atyf.2).
this.recoveryChip = createButton({
  label: '',
  variant: 'ghost',
  size: 'sm',
  onClick: () => this._recoveryOnClick?.(),
})
this.recoveryChip.dataset.control = 'recovery'
this.recoveryChip.style.display = 'none'

// The model that will answer, and the way to change it (nocx-rikz5). Hidden
// until setModelChip is called with a state, so the row never grows on its
// own; the row's height is fixed regardless (nocx-6c546, nocx-i4h04).
this.modelEndpointChip = createButton({
  label: '',
  variant: 'ghost',
  size: 'sm',
  truncate: true,
  onClick: () => {
    const page = this._modelChipTargets.endpoint
    if (page) this._onModelChipClick?.(page)
  },
})
this.modelEndpointChip.dataset.control = 'model-endpoint'
this.modelEndpointChip.style.display = 'none'

this.modelChip = createButton({
  label: '',
  variant: 'ghost',
  size: 'sm',
  truncate: true,
  onClick: () => {
    const page = this._modelChipTargets.model
    if (page) this._onModelChipClick?.(page)
  },
})
this.modelChip.dataset.control = 'model'
this.modelChip.style.display = 'none'

// The grant control: the editor owns the element and its place; GrantController
// owns everything it says (nocx-5u3oz.13).
this.grantChip = createButton({
  label: '',
  variant: 'ghost',
  size: 'sm',
  truncate: true,
  onClick: () => this._onGrantChipClick?.(),
})
this.grantChip.dataset.control = 'grant'
this.grantChip.style.display = 'none'

this.controls.append(this.recoveryChip, this.modelEndpointChip, this.modelChip, this.grantChip)
this.chrome.append(this.context, this.controls)
this.root.appendChild(this.chrome)

// Focus dims the context (spec §5.3). focusin/focusout on the root rather
// than CM6's focus tracking: a click on a control keeps the composer
// "focused", and a move to another pane does not.
this.root.addEventListener('focusin', this.onFocusIn)
this.root.addEventListener('focusout', this.onFocusOut)
```

Add the handlers and the one writer of the context, beside `renderLocation`, replacing `setCwd` (`:614-620`)
and `renderLocation` (`:638-646`):

```ts
  private readonly onFocusIn = (): void => {
    this._focused = true
    this.renderContext()
  }

  private readonly onFocusOut = (e: FocusEvent): void => {
    if (e.relatedTarget instanceof Node && this.root.contains(e.relatedTarget)) return
    this._focused = false
    this.renderContext()
  }

  /** Update the directory the pending command will run in. */
  setCwd(cwd: string): void {
    this._cwd = cwd.trim() || '~'
    this.renderContext()
  }

  /** The context's one writer: host (strong — it answers "where does Enter
   *  go", spec §5.2) · directory, dimmed when the composer is not focused. The
   *  host is routed from the one locationLine derivation; empty for a local
   *  session, where its absence is the information. ADR-0024 §6: no stream
   *  sequence promotes or revokes it. */
  private renderContext(): void {
    const parts: MetaPart[] = this._location
      ? [{ text: this._location, emphasis: 'strong' }, cwdLabel(this._cwd)]
      : [cwdLabel(this._cwd)]
    updateMeta(this.contextMeta, parts, {
      tone: this._focused ? 'muted' : 'dim',
      title: this._cwd,
    })
  }
```

In `setLocation` (`:629-632`), replace `this.renderLocation()` with `this.renderContext()`.

`setRecoveryAction` (`:537-547`) and `setModelChip` (`:563-613`) stay as written. They already write
`style.display`, `textContent`, `title`, `aria-label` and `disabled` on the same fields, which are now kit Buttons.

Delete `startClock`, `stopClock` and `setTime` (`:502-527`), the `this.startClock()` call in `show()` (`:1004`),
the `this.stopClock()` call and its comment in `hide()` (`:1146-1149`), and the `this.stopClock()` call and its
comment in `dispose()` (`:1177-1180`). In `dispose()`, add:

```ts
this.root.removeEventListener('focusin', this.onFocusIn)
this.root.removeEventListener('focusout', this.onFocusOut)
```

`frontend/src/grant.ts:32-40`: the chip it makes for itself is the same kit control.

```ts
this.chip =
  options.chip ??
  createButton({
    label: '',
    variant: 'ghost',
    size: 'sm',
    truncate: true,
    onClick: () => this.toggle(),
  })
if (this.ownsChip) this.chip.dataset.control = 'grant'
```

Add `import { createButton } from './ui/button-element'` and delete the `className` and `type` lines.

`frontend/src/terminal-content.ts`:

- `:3459`: `'.nocx-editor-grant'` → `'[data-control="grant"]'`;
- `:3464`, `:5413`, `:5557`: `'.nocx-editor-chrome-left'` → `'.nocx-editor-controls'`.

`frontend/src/pets/overlay.ts:86`: `const DEFAULT_LEDGES` → `export const DEFAULT_LEDGES`.

Create `frontend/src/styles/surfaces/composer.css`:

- move verbatim from `style.css` the `.nocx-editor` rule family (`:182-189`, the overlay rule `:250-258`,
  `:445-459`, `:546-730`: every rule whose selector begins `.nocx-editor`) and `.nocx-freeze-dismiss`
  (`:367-374`);
- rewrite the chrome rules as below;
- delete `.nocx-editor-time`, `.nocx-editor-grant*` and `.nocx-editor-model*` (`:309-313`, `:393-443`).

```css
/* The composer — the next block of the ledger, the one that has not run yet
   (spec .internal/specs/2026-09-14-terminal-screen-visual-register-design.md §5).
   A SURFACE, deliberately not styles/components/: it places kit components and
   must never repaint them, and surface-paints-kit only watches files outside the
   kit layer. */

.nocx-editor {
  position: relative;
  flex: none;
  /* The terminal screen's one ground (spec §7), not the canvas. */
  background: var(--terminal-background);
  /* The same seam as between two blocks (spec §3.4). No accent bar: it followed
     neither focus nor mode, so it said nothing (spec §1). */
  border-top: 1px solid var(--color-divider);
  padding-block: var(--space-2) var(--space-3);
  /* Inline padding is the ROW's, and T8 (nocx-9bpeq.8) owns it. If T8 has
     landed, its declaration moved here verbatim with the rest of this rule. */
}

/* The meta row. A FIXED height, never a floor: the scrollback hangs from the
   composer's bottom edge, so a row that grew when a control appeared would move
   it at the moment of submit (nocx-i4h04, nocx-6c546). The kit Button at size
   sm is exactly this tall, and Meta is shorter. */
.nocx-editor-chrome {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  height: var(--control-height-xs);
  margin-bottom: var(--space-1);
  -webkit-user-select: none;
  user-select: none;
}

.nocx-editor-context {
  display: flex;
  align-items: center;
  flex: 1 1 auto;
  min-width: 0;
}

.nocx-editor-controls {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 1 auto;
  min-width: 0;
  margin-left: auto;
}

/* Widths are placement. The floor keeps the grant count from twitching as it
   changes; the ceilings bound the longest id (nocx-hp8p2.5, nocx-rikz5). The
   ellipsis itself is Button's (data-truncate). */
.nocx-editor-controls > [data-control='grant'] {
  flex: 0 1 auto;
  min-width: 13rem;
  max-width: 18rem;
}

.nocx-editor-controls > [data-control='model-endpoint'],
.nocx-editor-controls > [data-control='model'] {
  max-width: 16rem;
}
```

`frontend/src/style.css`:

- add `@import './styles/surfaces/composer.css';` after `@import './styles/surfaces/skill-view.css';` (`:119`);
- delete the moved rules.
- If `grep -rn "nocx-chip" frontend/src e2e --include='*.ts' --include='*.tsx' --include='*.css' | grep -v 'style.css'`
  prints nothing, delete `.nocx-chip`, `.nocx-chip-muted`, `.nocx-chip-ok` and `.nocx-chip-fail` (`:315-365`).
- If it prints anything, leave the family, and name the lines it printed in this bead's close reason. They are
  T6's.

Run: `cd frontend && npx vitest run src/editor.test.ts src/editor-recovery.test.ts src/grant.test.ts src/pets/overlay.test.ts src/block-cursor.test.ts src/terminal-content.test.ts src/ui`
Expected: PASS.

Run: `cd frontend && npm run typecheck && npm run lint && npm run lint:fixture-check`
Expected: exit 0.

- `surface-paints-kit` must be silent on `composer.css`.
- `unreachable` must be silent, because the new file is `@import`ed.

Run: `grep -rn "timeChip\|startClock\|stopClock\|setTime\|nocx-editor-time\|📁" frontend/src e2e --include='*.ts' --include='*.tsx' --include='*.css'`
Expected: no output.

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/assistant-readiness.spec.ts e2e/shell-mode.spec.ts e2e/agent-ask.spec.ts e2e/ask-attaches-the-frozen-screen.spec.ts e2e/agent-reads-the-screen.spec.ts e2e/ops-indicator.spec.ts e2e/local-drop.spec.ts e2e/upload.spec.ts e2e/notification-centre-grouping.spec.ts e2e/pets.spec.ts e2e/terminal-screen-register.spec.ts`
Expected: all pass.

- In `terminal-screen-register.spec.ts`, 'the composer shows no clock' now reports "Expected to fail, but
  passed". Delete its `test.fail()` line in this commit.
- If 'history, live terminal and composer stand on one ground' now passes too (T3 and T8 already merged),
  delete that marker as well.
- A layout-sensitive red that only the container shows is read against CI before it is "fixed" (AGENTS.md).

- [ ] **Step 9: Commit**

```bash
git add frontend/src/editor.ts frontend/src/grant.ts frontend/src/terminal-content.ts frontend/src/pets/overlay.ts \
  frontend/src/style.css frontend/src/styles/surfaces/composer.css \
  frontend/src/editor.test.ts frontend/src/editor-recovery.test.ts frontend/src/grant.test.ts \
  frontend/src/terminal-content.test.ts frontend/src/block-cursor.test.ts frontend/src/pets/overlay.test.ts \
  e2e/shell-mode.spec.ts e2e/agent-ask.spec.ts e2e/ask-attaches-the-frozen-screen.spec.ts e2e/assistant-readiness.spec.ts \
  e2e/notification-centre-grouping.spec.ts e2e/ops-indicator.spec.ts e2e/ask-gesture.ts e2e/agent-reads-the-screen.spec.ts \
  e2e/local-drop.spec.ts e2e/upload.spec.ts e2e/terminal-screen-register.spec.ts
git commit -m "feat(frontend): the composer is the next row of the ledger, drawn by the kit, with no clock (nocx-9bpeq.7)

<body: the meta row (Meta context, ghost Button controls); the clock removed on the owner's decision; focus dims the context; composer.css as a surface so rule 3 watches it; the data-control hooks; which test.fail markers this deleted>

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

---

### Task T10: the lint exemption stops exempting the kit's vocabulary (nocx-9bpeq.10)

Lands last: it depends on T4–T8, and its counters must read zero on the merged tree.

**Files:**

- Modify: `frontend/eslint.config.js:98-128` (`EXEMPT_PATTERNS`, `isExempt`), `:136-240` (`no-raw-controls`)
- Modify: `frontend/lint-fixtures/nocx-no-raw-controls.tsx` (an imperative case), `frontend/lint-fixtures/gate.sh`
  (a scrollback fixture)
- Modify: `frontend/lint-fixtures/scan-kit-identities.mjs` and `frontend/lint-fixtures/check-kit-identities.mjs`,
  only if T4 did not already make the scanner read vanilla `ui/*.ts` emitters (Step 1 decides)
- Modify, for the sites the rule now reports:
  - `frontend/src/grant.ts:158-166` (dismiss-all);
  - `frontend/src/scrollback/blocks.ts:31-50` (the legacy copy fallback);
  - `frontend/src/files/upload-picker.ts:40` (the browser picker);
  - whatever else Step 3 prints that T5–T7 did not already remove.

**Interfaces:**

- Consumes: `createButton` (T7), `createIconButton` (T4), and the kit identities of `ui-meta` and `ui-button`
  (T4, T7).
- Produces:
  - rule messages `rawCreateElement` (new) and `innerHTML` (now limited to non-framework-neutral files);
  - the helpers `isKitOrTest(rel)` and `isFrameworkNeutral(rel)`, replacing `isExempt` inside `no-raw-controls`.
    `no-inline-markup` keeps `isExempt`: it inspects JSX only, and those files have none.

**What was measured (2026-09-14, tree 8ed43569):**

- `no-raw-controls` visits only `JSXOpeningElement` and `AssignmentExpression` (`eslint.config.js:236-239`). The
  terminal-owned files are `.ts` and contain no JSX, so the path exemption was never what let them build raw
  controls: the rule had no visitor for `document.createElement`, in any file. Narrowing the exemption alone
  would change nothing. The rule needs the visitor.
- `document.createElement('button'|'select'|'textarea'|'input')` outside `ui/` and tests today:
  - `scrollback/blocks.ts` ×9 (41, 976, 1019, 1091, 1111, 1132, 1174, 1208, 1220);
  - `editor.ts` ×4 (349, 357, 367, 376);
  - `grant.ts` ×3 (35, 160, 178);
  - `files/upload-picker.ts` ×1 (40).

  T5 removes the ⋮ and the menu items, T7 removes the editor's four and grant.ts:35, and T5 removes
  grant.ts:178 (the × dismiss). Left for this task: `blocks.ts:41`, `grant.ts:160` and `upload-picker.ts:40`.

- `surface-paints-kit` already scans `style.css`. It runs over every file reachable from the entry except
  `styles/components/` and `base.css` (`check-css-integrity.mjs:663-676`), and `style.css` is the entry. Its
  real blind spot is on the identity side: `scanKitIdentities` reads only `ui/*.tsx`
  (`scan-kit-identities.mjs:221`), so an identity emitted only from a vanilla `ui/*.ts` module (`ui-meta`,
  `ui-mode-indicator`, `ui-secret-chip`) is invisible to rule 3.

**Acceptance Criteria:**

- `no-raw-controls` reports `*.createElement('button'|'select'|'textarea'|'input')` in every file outside `ui/`
  and tests, including `scrollback/`, `editor.ts`, `terminal-content.ts` and `renderers/`.
- `innerHTML` stays allowed in the framework-neutral files and in no others. The comment names why: the frozen
  block is serialised HTML by design (ADR-0012), in `scrollback/blocks.ts` and `scrollback/shell-paint.ts`.
- `lint-fixtures/gate.sh` writes a temporary `src/scrollback/__gate_raw_controls.ts`, asserts
  `nocx/no-raw-controls` fires on its `document.createElement('button')` and does NOT fire on its `innerHTML`,
  and removes the file under a trap.
- `lint-fixtures/nocx-no-raw-controls.tsx` carries an imperative `document.createElement('select')` case that
  the gate's rule list sees fire.
- `scanKitIdentities` returns identities assigned in vanilla `ui/*.ts` emitters (`el.className = 'ui-…'`).
  `check-kit-identities.mjs` has a `.ts` fixture proving it. `ui-meta` and `ui-button` are both reported on the
  real tree.
- Each remaining exemption in the source carries `-- <reason>` on its `eslint-disable-next-line`:
  - the off-screen copy `<textarea>` in `blocks.ts`;
  - the hidden file `<input>` that raises the browser picker in `upload-picker.ts`.

  A bead is filed for the first: `blocks.ts` keeps a second clipboard implementation beside the injected
  `ClipboardAccess` (`clipboard.ts:78`).

- `grant.ts` dismiss-all is `createButton({ variant: 'ghost', size: 'sm' })`.
- `raw-controls-baseline.json` still has `"violations": []`.
- `npm run lint` and `npm run lint:fixture-check` exit 0 on the merged tree.
- `e2e/terminal-screen-register.spec.ts` carries no `test.fail()` marker, and every test in it passes in the
  container. The coordinator's merged gate is the one run of the full e2e.

- [ ] **Step 1: Decide whether the identity scanner still needs vanilla modules**

Run: `cd frontend && node --input-type=module -e "import {scanKitIdentities} from './lint-fixtures/scan-kit-identities.mjs'; const k=[...scanKitIdentities('src/ui').byClass.keys()]; console.log(['ui-meta','ui-button','ui-mode-indicator'].map(c=>c+':'+k.includes(c)).join(' '))"`

- Expected, if T4 extended the scanner: `ui-meta:true ui-button:true ui-mode-indicator:true`. Skip to Step 3.
- Expected, if it did not: `ui-meta:false ui-button:true ui-mode-indicator:false`. Do Step 2.

- [ ] **Step 2 (only if Step 1 printed `ui-meta:false`): make the scanner read vanilla emitters**

Create `frontend/lint-fixtures/kit-identity-fixture/vanilla-emitter.ts`:

```ts
// Fixture: a vanilla-emitted kit component (like ui/mode-indicator.ts) assigns
// its identity with className. The scanner must find it.
export function createVanillaWidget(): HTMLElement {
  const el = document.createElement('span')
  el.className = 'fixture-vanilla-root'
  const part = document.createElement('span')
  part.className = 'fixture-vanilla-root__part'
  el.append(part)
  // A lookup, not an identity: must stay invisible.
  el.querySelector('.fixture-vanilla-lookup')
  return el
}
```

Append to `frontend/lint-fixtures/check-kit-identities.mjs`, before the errors are reported:

```js
checkFound(
  'fixture-vanilla-root',
  'vanilla-emitter.ts',
  'className assignment in a vanilla ui/*.ts emitter',
)
checkFound('fixture-vanilla-root__part', 'vanilla-emitter.ts', 'a part assigned with className')
if (byClass.has('fixture-vanilla-lookup')) {
  errors.push('FOUND: "fixture-vanilla-lookup" — a querySelector string was read as an identity')
}
```

Run: `cd frontend && node lint-fixtures/check-kit-identities.mjs`
Expected: FAIL with `MISSING: "fixture-vanilla-root"`.

In `frontend/lint-fixtures/scan-kit-identities.mjs`:

- change the entry filter at `:221` to accept `.ts` as well as `.tsx`, still excluding `.test.` and `.spec.`;
- add, inside the per-file loop after the JSX attribute walk, an `AssignmentExpression` walk that records a
  class name when all of these hold:
  - the target is a `MemberExpression` whose property is `className`;
  - the right-hand side is a string `Literal`, or a `TemplateLiteral` with no expressions;
  - the class is recorded against the file basename by the same function the JSX branch uses for a static
    `class=`;
- parse `.ts` files with `jsx: false`.

```js
for (const node of walk(ast, 'AssignmentExpression')) {
  const { left, right } = node
  if (left.type !== 'MemberExpression' || left.property.type !== 'Identifier') continue
  if (left.property.name !== 'className') continue
  const text =
    right.type === 'Literal' && typeof right.value === 'string'
      ? right.value
      : right.type === 'TemplateLiteral' && right.expressions.length === 0
        ? right.quasis[0].value.cooked
        : null
  if (text === null) continue
  for (const cls of text.split(/\s+/).filter(Boolean)) record(entry, cls)
}
```

`record(entry, cls)` is the existing function the JSX branch calls. If that branch inlines the `byClass` and
`identities` updates instead, extract them into `record` in the same edit, and change nothing else about them.

Run: `cd frontend && node lint-fixtures/check-kit-identities.mjs && npm run lint`
Expected: exit 0.

- If `check-css-integrity` now reports new `surface-paints-kit` hits, a surface is repainting a vanilla kit
  component. That is a real finding: fix the surface CSS in this task.
- Do not narrow the scanner to silence it.

- [ ] **Step 3: Failing gate for the narrowed rule**

Append to `frontend/lint-fixtures/nocx-no-raw-controls.tsx`, before the `export`:

```tsx
// Rule: nocx/no-raw-controls — a raw control built imperatively (nocx-9bpeq.10).
// The rule used to see JSX only, so every imperative surface could build one.
function ImperativeRawSelect() {
  return document.createElement('select')
}
```

Append to `frontend/lint-fixtures/gate.sh`, after the dependency-direction block:

```sh
# ── Raw controls inside the terminal-owned tree (nocx-9bpeq.10) ─────────────
# The rule is path-scoped, so only a file inside src/scrollback/ can prove the
# narrowed exemption: a createElement('button') there MUST fire, and innerHTML
# there must NOT — the frozen block is serialised HTML by design (ADR-0012).
raw_fixture="src/scrollback/__gate_raw_controls.ts"
cleanup_raw_fixture() { rm -f "$raw_fixture"; }
trap cleanup_raw_fixture EXIT INT TERM
cat > "$raw_fixture" <<'FIXTURE'
// Temporary fixture written by lint-fixtures/gate.sh. If you are reading this in
// a working tree, the gate crashed between writing and removing it; delete it.
export function gateRawControl(): HTMLElement {
  const host = document.createElement('div')
  host.innerHTML = '<span class="term-line"></span>'
  host.append(document.createElement('button'))
  return host
}
FIXTURE
raw_check=$(npx eslint --no-ignore "$raw_fixture" 2>&1 || true)
cleanup_raw_fixture
trap - EXIT INT TERM

if ! echo "$raw_check" | grep -q "createElement('button')\|nocx/no-raw-controls"; then
  echo "RAW CONTROLS GATE FAILED — createElement('button') inside scrollback/ was not reported"
  exit 1
fi
if echo "$raw_check" | grep -q 'innerHTML assignment'; then
  echo "RAW CONTROLS GATE FAILED — innerHTML inside scrollback/ was reported; the frozen block's HTML is by design"
  exit 1
fi
```

Run: `cd frontend && sh lint-fixtures/gate.sh`
Expected: FAIL with `RAW CONTROLS GATE FAILED — createElement('button') inside scrollback/ was not reported`.

- [ ] **Step 4: Narrow the exemption and add the visitor**

Replace `frontend/eslint.config.js:98-129` with:

```js
// ─── Path-based exemption patterns (ADR-0014, ADR-0012) ────────────────────────────
// Two different reasons a file is exempt, and they do not exempt the same thing.
//
// The kit and the tests are exempt from everything: the kit is where native controls
// legitimately live, and a test builds whatever it is testing.
//
// The FRAMEWORK-NEUTRAL files are ADR-0012's "deliberately still imperative" set:
// terminal-owned code kept free of Solid for AD-6. That is a statement about the
// framework, never about the kit's vocabulary — but the exemption was once read that
// way, and a raw ⋮ button, a hand-rolled chip family and an emoji folder accumulated
// under it with every gate green (nocx-9bpeq). So these files are exempt only from
// the innerHTML check, because the frozen block is serialised HTML by design, and
// they are NOT exempt from building raw controls.
const KIT_AND_TEST_PATTERNS = [
  (rel) => rel.includes('/src/ui/'),
  (rel) => /\.(test|spec)\.(ts|tsx)$/.test(rel),
  (rel) => rel.includes('/test-support/'),
]

const FRAMEWORK_NEUTRAL_PATTERNS = [
  (rel) => rel.endsWith('/src/tabs.ts'),
  (rel) => rel.endsWith('/src/tab-content.ts'),
  (rel) => rel.endsWith('/src/terminal-content.ts'),
  (rel) => rel.includes('/src/renderers/'),
  (rel) => rel.includes('/src/scrollback/'),
  (rel) => rel.endsWith('/src/editor.ts'),
  (rel) => rel.endsWith('/src/gutter.ts'),
  (rel) => /\/src\/input-[\w-]+\.ts$/.test(rel),
  (rel) => rel.endsWith('/src/dispatcher.ts'),
  (rel) => rel.endsWith('/src/command-ledger.ts'),
  (rel) => rel.endsWith('/src/clipboard.ts'),
  (rel) => rel.endsWith('/src/frame.ts'),
  (rel) => rel.endsWith('/src/submit.ts'),
  (rel) => rel.endsWith('/src/ipc.ts'),
]

const EXEMPT_PATTERNS = [...KIT_AND_TEST_PATTERNS, ...FRAMEWORK_NEUTRAL_PATTERNS]

function isExempt(relPath) {
  return EXEMPT_PATTERNS.some((p) => p(relPath))
}

function isKitOrTest(relPath) {
  return KIT_AND_TEST_PATTERNS.some((p) => p(relPath))
}

function isFrameworkNeutral(relPath) {
  return FRAMEWORK_NEUTRAL_PATTERNS.some((p) => p(relPath))
}
```

In `no-raw-controls`:

- add a message to `meta.messages`:

```js
          rawCreateElement:
            "document.createElement('{{tag}}') builds a raw control. Use the kit's emitter from 'ui/' (createButton, createIconButton) — the imperative exemption is about Solid, not about the kit. See ADR-0014, nocx-9bpeq.",
```

- replace `if (isExempt(rel)) return {}` (`:160`) with:

```js
if (isKitOrTest(rel)) return {}
const frameworkNeutral = isFrameworkNeutral(rel)
```

- add, beside `checkInnerHTML`:

```js
// Imperative raw controls: el = document.createElement('button'). The rule saw
// JSX only, so every imperative surface could build one (nocx-9bpeq.10). Every
// `input` counts — its type is set after construction, where the AST cannot
// follow it.
const RAW_CREATED = new Set(['button', 'select', 'textarea', 'input'])
function checkCreateElement(node) {
  const callee = node.callee
  if (callee.type !== 'MemberExpression' || callee.property.type !== 'Identifier') return
  if (callee.property.name !== 'createElement') return
  const arg = node.arguments[0]
  if (!arg || arg.type !== 'Literal' || typeof arg.value !== 'string') return
  const tag = arg.value.toLowerCase()
  if (!RAW_CREATED.has(tag)) return
  const id = hashNode(sourceCode, node)
  if (!isBaselined(id)) {
    context.report({ node, messageId: 'rawCreateElement', data: { tag } })
  }
}
```

- replace the returned visitor map (`:236-239`) with:

```js
return {
  JSXOpeningElement: checkJSX,
  // The frozen block is serialised HTML by design (ADR-0012) — innerHTML stays
  // allowed where that serialiser lives, and nowhere else.
  ...(frameworkNeutral ? {} : { AssignmentExpression: checkInnerHTML }),
  CallExpression: checkCreateElement,
}
```

Run: `cd frontend && sh lint-fixtures/gate.sh`
Expected: exit 0.

- [ ] **Step 5: Bring the real tree to zero**

Run: `cd frontend && npx eslint src 2>&1 | grep -B3 "builds a raw control"`
Expected on a tree where T5 and T7 have merged: 3 reports, at `scrollback/blocks.ts:41`, `grant.ts:160` and
`files/upload-picker.ts:40`.

- Any other report is a site T5–T7 left behind: convert it here with `createButton` or `createIconButton`, the
  same way as below.

`frontend/src/grant.ts:158-166`, dismiss-all:

```ts
const dismissAll = createButton({
  label: 'Dismiss all',
  variant: 'ghost',
  size: 'sm',
  onClick: (event) => {
    event.stopPropagation()
    this.blocks = []
    this.repaintBlocks()
    this.updateChip()
    this.onChange?.(this.blocks)
    this.panel.hide()
  },
})
dismissAll.dataset.action = 'dismiss-all-grants'
```

Delete the old element's `type`, `className`, `dataset`, `textContent` and `addEventListener` lines. Keep
`footer.appendChild(dismissAll)`.

`frontend/src/scrollback/blocks.ts:41`:

```ts
// eslint-disable-next-line nocx/no-raw-controls -- not a control: an off-screen buffer for the legacy execCommand('copy') path when navigator.clipboard refuses. A second clipboard implementation beside ClipboardAccess; see the bead filed with this commit.
const ta = document.createElement('textarea')
```

`frontend/src/files/upload-picker.ts:40`:

```ts
// eslint-disable-next-line nocx/no-raw-controls -- not a control on screen: the hidden file input is the only way a browser raises its native picker, and it is removed when the picker settles.
const input = doc.createElement('input')
```

File the follow-up bead. Area `terminal`, parent none, with a `discovered-from` edge to nocx-9bpeq.10:

```bash
br create -t bug -p 3 -l terminal --title "scrollback/blocks.ts copies to the clipboard through its own fallback beside the injected ClipboardAccess" \
  -d "blocks.ts:31-50 (copyToClipboardImpl) writes navigator.clipboard and falls back to an off-screen textarea + execCommand('copy'), while clipboard.ts:78 (createClipboardAccess) is the composition root's one clipboard owner (AD-8). Two implementations of one concept. Found by nocx-9bpeq.10, which had to exempt the textarea from nocx/no-raw-controls with a reason rather than remove it.

## Acceptance Criteria
- Block menu copy goes through the injected ClipboardAccess; copyToClipboardImpl is deleted with its eslint-disable line.
- A unit test fails the copy when ClipboardAccess rejects and asserts the person is told (paired with one where it succeeds)."
br dep add <new-id> nocx-9bpeq.10 --type discovered-from
```

Run: `cd frontend && npm run lint && npm run lint:fixture-check && npx vitest run src/grant.test.ts src/files && npm run typecheck`
Expected: exit 0. `grep -c '"violations": \[\]' lint-fixtures/raw-controls-baseline.json` prints `1`.

- [ ] **Step 6: The e2e carries no marker any more**

Run: `grep -n "test.fail()" e2e/terminal-screen-register.spec.ts`
Expected: no output.

- If a line prints, run the spec. If it reports "Expected to fail, but passed", delete that line.
- If the test is still red, the epic is not done: name the failing assertion in the report and do not close.

Run: `PW_PROJECTS=chromium e2e/run-in-container.sh e2e/terminal-screen-register.spec.ts && PW_PROJECTS=webkit e2e/run-in-container.sh e2e/terminal-screen-register.spec.ts`
Expected: `7 passed` in each.

- [ ] **Step 7: Commit**

```bash
git add frontend/eslint.config.js frontend/lint-fixtures/gate.sh frontend/lint-fixtures/nocx-no-raw-controls.tsx \
  frontend/lint-fixtures/scan-kit-identities.mjs frontend/lint-fixtures/check-kit-identities.mjs \
  frontend/lint-fixtures/kit-identity-fixture/vanilla-emitter.ts \
  frontend/src/grant.ts frontend/src/scrollback/blocks.ts frontend/src/files/upload-picker.ts \
  e2e/terminal-screen-register.spec.ts .beads/issues.jsonl
git commit -m "build(frontend): the imperative exemption covers Solid, not the kit — raw controls are reported in the terminal tree too (nocx-9bpeq.10)

<body: the rule had no createElement visitor, so narrowing paths alone would have changed nothing; innerHTML stays allowed where the frozen serialiser lives; the scanner reads vanilla emitters; the two reasoned exemptions and the clipboard bead>

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```
