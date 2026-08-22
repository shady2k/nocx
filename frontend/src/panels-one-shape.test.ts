/**
 * Every activity-bar panel is ONE shape, and this is what says so.
 *
 * Four panels grew inside one shell — `sidebar.tsx` builds `SidebarView`
 * once, for every view — and each arranged itself inside it on its own. The
 * owner found the results one at a time over an evening: a filter that
 * scrolled away with the list it filters, a field 48px narrower than the
 * panel around it, a heading rendering at 21px after a change that asked for
 * 14 (nocx-708q.1-.2). Four reports, one cause.
 *
 * The rules below are the shell's, so they hold for a panel nobody has
 * written yet. They are asserted against the SHIPPED stylesheet rather than
 * a rendered box because jsdom computes no cascade — the pattern
 * `connections.scroll-chain.test.tsx` established for exactly this reason.
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, it, expect } from 'vitest'

const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)

/** base.css owns the shell; the surface files are where a panel would add
 *  its own inset on top of it. Files and operations are read but never
 *  edited here — they belong to another worker, and a rule that only holds
 *  for the panels one worker touched is not the kit's rule. */
const STYLESHEETS = [
  'styles/base.css',
  'styles/surfaces/git.css',
  'styles/surfaces/ports.css',
  'styles/surfaces/files.css',
  'styles/surfaces/operations.css',
  'styles/components/notes.css',
].map((p) => readFileSync(resolve(dirname, p), 'utf8'))

type Rule = { selectors: string[]; body: string }

/** Top-level rules only — a declaration that holds at one viewport width
 *  does not hold. Lifted from connections.scroll-chain.test.tsx. */
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

const RULES: Rule[] = STYLESHEETS.flatMap(topLevelRules)

/** The value the shipped cascade gives `property` for a bare class, later
 *  rules winning. Only a bare class selector counts: a compound or
 *  descendant selector is a rule about something else. */
function shipped(cls: string, property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    if (!rule.selectors.some((s) => s === `.${cls}`)) continue
    const m = rule.body.match(pattern)
    if (m) found = m[1].trim()
  }
  return found
}

/** The left/right halves of a `padding` or `margin` shorthand. CSS box
 *  shorthands are 1–4 values: all / (block, inline) / (top, inline, bottom)
 *  / (top, right, bottom, left). */
function inlineSides(shorthand: string | null): [string, string] | null {
  if (shorthand === null) return null
  const parts = shorthand.split(/\s+/).filter((p) => p !== '')
  switch (parts.length) {
    case 1:
      return [parts[0], parts[0]]
    case 2:
    case 3:
      return [parts[1], parts[1]]
    case 4:
      return [parts[3], parts[1]]
    default:
      return null
  }
}

/** What a class insets its content by horizontally, counting the padding
 *  and margin shorthands and their inline longhands. `null` where the class
 *  declares none — which is the answer every panel root must give. */
function horizontalInset(cls: string): string[] {
  const found: string[] = []
  for (const prop of ['padding', 'margin']) {
    const sides = inlineSides(shipped(cls, prop))
    if (sides) found.push(...sides)
  }
  for (const prop of [
    'padding-left',
    'padding-right',
    'padding-inline',
    'margin-left',
    'margin-right',
    'margin-inline',
  ]) {
    const v = shipped(cls, prop)
    if (v !== null) found.push(v)
  }
  return found.filter((v) => v !== '0' && v !== '0px' && v !== 'auto')
}

describe('the sidebar shell insets its three rows identically', () => {
  // Content that does not line up when you switch panels is the shape of
  // the defect, and the header/body pair was already held to this rule
  // (nocx-wzc4.11: "it is the kit's job to get that right once"). The
  // filter row was added later and was left flush against the panel edge,
  // so the one control a user aims at was the one thing out of column.
  const rows = ['ui-sidebar-view__header', 'ui-sidebar-view__filter', 'ui-sidebar-view__body']

  it('gives the header, the filter and the body the same horizontal inset', () => {
    const insets = rows.map((r) => inlineSides(shipped(r, 'padding')))
    for (const [i, inset] of insets.entries()) {
      expect(inset, `${rows[i]} declares no padding shorthand`).not.toBeNull()
    }
    const [header, filter, body] = insets as [string, string][][number][] as [
      [string, string],
      [string, string],
      [string, string],
    ]
    expect(filter).toEqual(body)
    expect(header).toEqual(body)
  })
})

describe('no panel adds horizontal inset on top of the shell body', () => {
  // Each of these is the ROOT a panel hands the shell — the direct child of
  // `.ui-sidebar-view__body`, or of the pinned filter row. The body already
  // insets by --space-2; a root that insets again doubles it, which in a
  // 250px rail is 13% of the panel spent on air, taken from the file names
  // (the Git panel's rows lost their names to exactly this).
  //
  // Ports is absent because it has no root of its own: its panel hands the
  // shell the kit's `Stack` directly, which is the answer this rule wants.
  const PANEL_ROOTS = ['git-panel', 'notes-panel', 'files-filter']

  for (const cls of PANEL_ROOTS) {
    it(`.${cls} leaves the horizontal inset to the shell`, () => {
      // A class no stylesheet declares would pass this vacuously, and a
      // renamed panel root is exactly how that would happen.
      expect(RULES.some((r) => r.selectors.includes(`.${cls}`))).toBe(true)
      expect(horizontalInset(cls)).toEqual([])
    })
  }
})
