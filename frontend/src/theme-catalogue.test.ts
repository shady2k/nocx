// @vitest-environment node
// css-tree is loaded via createRequire and has no type declarations, and the
// node builtins below are @ts-expect-error imports because @types/node is not
// installed; every call to either touches an untyped value, so no-unsafe-* must
// be disabled at the file level for this test.
/* eslint-disable @typescript-eslint/no-unsafe-assignment,
                      @typescript-eslint/no-unsafe-call,
                      @typescript-eslint/no-unsafe-member-access,
                      @typescript-eslint/no-unsafe-argument */
/**
 * The theme catalogue gate.
 *
 * A theme is one file (ADR-0013 §2), but adding one touches three other places:
 * the `@import` in style.css, `KNOWN_THEME_IDS` in renderers/theme-bootstrap.ts,
 * and the `ui.theme` options in internal/settings/settings.go. Miss any of them
 * and the failure is silent in a specific way — the file is valid CSS the browser
 * loads and never applies, or an id the picker offers that bootstrap rewrites back
 * to the default. Nothing throws. This file is what notices.
 *
 * It asserts three things about every file in styles/themes/:
 *
 * 1. **Registration.** Imported by style.css and known to bootstrap. The Go half
 *    of the same invariant — that `ui.theme` offers exactly these ids — is
 *    asserted from the other side in internal/settings/theme_catalogue_test.go,
 *    because each side should fail in its own test run rather than one language's
 *    test reaching across the repo to police the other.
 *
 * 2. **Token parity with the default theme.** A theme that omits a token does not
 *    fall back to a sensible value: `var(--color-surface-raised)` with no
 *    declaration anywhere resolves to nothing and the property is dropped, so a
 *    menu loses its background and keeps its text. Every theme must declare the
 *    same token names tokyo-night.css declares — no more, no fewer.
 *
 * 3. **Contrast.** Every text token against every background token it can land
 *    on, at WCAG 1.4.3 AA (4.5:1); the control border against the canvas at
 *    1.4.11 (3:1). The floors are not aspirational — they were measured against
 *    the two themes that already existed, and the one cell that failed
 *    (light.css, dim text on chrome, 4.43:1) was fixed rather than exempted.
 *
 * 4. **The terminal screen's surface roles** (spec 2026-09-14 §7). Chrome and
 *    floating surfaces measured against `--terminal-background` in CIELAB ΔL*,
 *    and the failed row's `--color-danger-surface` legible for text and for the
 *    danger status word. The fifth statement of that section — that the rows
 *    actually paint the ground — needs a layout and lives in the terminal-screen
 *    end-to-end check (nocx-9bpeq.9).
 *
 * ## What this deliberately does NOT gate
 *
 * **The terminal palette.** `--terminal-ansi-0…15` and the foreground/background
 * pair are the published palette of the theme they are named after; they ARE the
 * theme, and a Solarized whose colours have been corrected for contrast is not
 * Solarized. Measured across the catalogue, terminal foreground-on-background runs
 * from 4.13:1 (Solarized Light, canonical) to 17.4:1. That range is recorded here
 * rather than enforced, so nobody reads the silence as coverage.
 *
 * **`--color-divider`** — a seam between regions, not a control outline, and at
 * 1.23:1 in both original themes it is deliberately below any text or control
 * floor (see the note in tokyo-night.css).
 *
 * **The semantic colours** (`--color-success`, `--color-warning`,
 * `--color-danger`) on the app's backgrounds. Danger is gated only where the
 * terminal screen uses it — on the failed row's own surface (4 above). light.css
 * still puts success at 2.69:1 on the canvas; that is nocx-foyr.
 */
import { describe, it, expect } from 'vitest'
import { KNOWN_THEME_IDS, DEFAULT_THEME_ID } from './renderers/theme-bootstrap'
import { createRequire } from 'node:module'
import { readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'

const css = createRequire(import.meta.url)('css-tree')

const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)
const THEME_DIR = resolve(dirname, 'styles/themes')
const STYLE_ENTRY = resolve(dirname, 'style.css')

// ── Parsing ─────────────────────────────────────────────────────────────

/** Every custom property a theme file declares, name → value. */
function declaredTokens(source: string): Map<string, string> {
  const out = new Map<string, string>()
  const ast = css.parse(source)
  css.walk(ast, (node: unknown) => {
    if (!node || typeof node !== 'object') return
    const n = node as { type?: unknown; property?: unknown; value?: unknown }
    if (n.type !== 'Declaration') return
    if (typeof n.property !== 'string' || !n.property.startsWith('--')) return
    out.set(n.property, css.generate(n.value).trim())
  })
  return out
}

const themeFiles: string[] = readdirSync(THEME_DIR)
  .filter((f: string) => f.endsWith('.css'))
  .sort()

const themeIds = themeFiles.map((f) => f.replace(/\.css$/, ''))

const tokensById = new Map<string, Map<string, string>>(
  themeIds.map((id) => [id, declaredTokens(readFileSync(resolve(THEME_DIR, `${id}.css`), 'utf8'))]),
)

// ── Contrast ────────────────────────────────────────────────────────────

function hexToRGB(value: string): [number, number, number] | null {
  const m = /^#([0-9a-f]{6})$/i.exec(value.trim())
  if (m === null) return null
  const h = m[1]
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)]
}

/** Relative luminance, WCAG 2.x definition. */
function luminance([r, g, b]: [number, number, number]): number {
  const channel = (c: number): number => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)
}

function contrastRatio(fg: string, bg: string): number {
  const a = hexToRGB(fg)
  const b = hexToRGB(bg)
  if (a === null || b === null) return Number.NaN
  const la = luminance(a)
  const lb = luminance(b)
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05)
}

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

/** Text tokens — anything the app paints words with. */
const TEXT_TOKENS = ['--color-text', '--color-text-muted', '--color-text-dim']

/**
 * Background tokens text can land on. `--color-divider` is absent because it is
 * a hairline, never a fill; `--color-scrim` because it is translucent by design.
 */
const BACKGROUND_TOKENS = [
  '--color-canvas',
  '--color-chrome',
  '--color-chrome-rail',
  '--color-surface',
  '--color-surface-sunken',
  '--color-surface-raised',
  '--color-tab-active',
  '--color-surface-hover',
]

const AA_TEXT = 4.5
const AA_NON_TEXT = 3

// ── Tests ───────────────────────────────────────────────────────────────

describe('theme catalogue', () => {
  it('finds the default theme among the files on disk', () => {
    expect(themeIds).toContain(DEFAULT_THEME_ID)
    expect(themeIds.length).toBeGreaterThan(1)
  })

  it('registers every theme file in style.css, and imports nothing else', () => {
    const entry = readFileSync(STYLE_ENTRY, 'utf8')
    const imported = [...entry.matchAll(/@import\s+'\.\/styles\/themes\/([a-z0-9-]+)\.css'/g)].map(
      (m) => m[1],
    )
    expect(imported.slice().sort()).toEqual(themeIds)
  })

  it('registers every theme file in KNOWN_THEME_IDS', () => {
    expect([...KNOWN_THEME_IDS].sort()).toEqual(themeIds)
  })

  it.each(themeIds.filter((id) => id !== DEFAULT_THEME_ID))(
    '%s declares exactly the tokens the default theme declares',
    (id) => {
      const expected = [...tokensById.get(DEFAULT_THEME_ID)!.keys()].sort()
      const actual = [...tokensById.get(id)!.keys()].sort()
      expect(actual).toEqual(expected)
    },
  )

  it.each(themeIds)('%s keeps text at 4.5:1 on every background it can land on', (id) => {
    const t = tokensById.get(id)!
    const failures: string[] = []
    for (const fg of TEXT_TOKENS) {
      for (const bg of BACKGROUND_TOKENS) {
        const ratio = contrastRatio(t.get(fg)!, t.get(bg)!)
        // NaN means a token stopped being an opaque hex — the pair silently
        // stops being measured, which is the one failure this must not allow.
        expect(Number.isNaN(ratio), `${id}: ${fg} or ${bg} is not an opaque hex`).toBe(false)
        if (ratio < AA_TEXT) failures.push(`${fg} on ${bg} — ${ratio.toFixed(2)}:1`)
      }
    }
    expect(failures).toEqual([])
  })

  it.each(themeIds)('%s keeps the control border at 3:1 on the canvas', (id) => {
    const t = tokensById.get(id)!
    const canvas = t.get('--color-canvas')!
    expect(contrastRatio(t.get('--color-border')!, canvas)).toBeGreaterThanOrEqual(AA_NON_TEXT)
    expect(contrastRatio(t.get('--color-accent')!, canvas)).toBeGreaterThanOrEqual(AA_NON_TEXT)
  })

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

  it.each(themeIds)('%s carries a terminal palette distinct from its background', (id) => {
    // The palette itself is canon and ungated (see the file header). What is
    // asserted is that all 21 terminal tokens are present and that foreground
    // and background are not the same colour — the one way a ported palette can
    // be broken rather than merely low-contrast.
    const t = tokensById.get(id)!
    for (let i = 0; i <= 15; i++) expect(t.has(`--terminal-ansi-${i}`), `ansi-${i}`).toBe(true)
    for (const k of ['background', 'foreground', 'cursor', 'cursor-accent', 'selection']) {
      expect(t.has(`--terminal-${k}`), k).toBe(true)
    }
    expect(t.get('--terminal-foreground')).not.toBe(t.get('--terminal-background'))
  })
})
