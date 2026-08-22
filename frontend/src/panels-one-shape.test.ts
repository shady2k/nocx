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

describe("the sidebar body is a panel's one scroll owner", () => {
  // The Page family has had this rule since §6.2 — "only PageScroller and
  // PageRail may scroll, no third scroll container" — and the sidebar has
  // exactly the same shape one size down: `.ui-sidebar-view__body` is the
  // scroller, the header and the filter row are pinned outside it. Nothing
  // said so, so panels declared their own: the notes list carried
  // `overflow-y: auto` inside the body's own, which is inert while nothing
  // bounds its height and becomes a list that traps the wheel the day
  // something does.
  //
  // Asserted over the shipped rules rather than a rendered box for the
  // reason at the top of this file: jsdom computes no layout, so a nested
  // scroller is invisible to a render test and plain to read here.

  /** The class prefixes a sidebar PANEL's own elements carry. The notes TAB
   *  (`note-tab*`) and the terminal panes are not panels and keep their own
   *  scrollers; naming the prefixes is what keeps this rule about the thing
   *  it is about. */
  const PANEL_PREFIXES = ['git-', 'ports-', 'notes-panel', 'ops-', 'files-']

  /** Every scroller the panel elements declare, by bare class — a compound
   *  or descendant selector is a rule about something inside a panel, not
   *  about the panel's own frame. */
  const declared: string[] = []
  for (const rule of RULES) {
    if (!/(?:^|;)\s*overflow(?:-y)?\s*:\s*(?:auto|scroll)/.test(rule.body)) continue
    for (const sel of rule.selectors) {
      const m = /^\.([\w-]+)$/.exec(sel)
      if (m && PANEL_PREFIXES.some((p) => m[1].startsWith(p))) declared.push(m[1])
    }
  }

  /** The exceptions that are on record rather than quietly excluded.
   *
   *  EMPTY, and that is the state to keep it in. It held `ops-list` for one
   *  merge — the operations panel's two lists each declared their own
   *  scroller, and they were in another worker's branch when this test was
   *  written, so they were NAMED rather than silently excluded. They were
   *  removed as soon as that branch landed, because an exception that
   *  outlives its reason stops being a record and becomes a hole. Anything
   *  added here carries the line that says why, and the line says when it
   *  goes. */
  const KNOWN_EXCEPTIONS: string[] = []

  it('leaves the scrolling to the shell, bar the exceptions on record', () => {
    // The shell really is the scroller — otherwise this rule would be
    // satisfied by nothing scrolling at all.
    expect(shipped('ui-sidebar-view__body', 'overflow-y')).toBe('auto')
    expect(declared.filter((c) => !KNOWN_EXCEPTIONS.includes(c))).toEqual([])
  })
})

describe("the room under a panel's name is the shell's", () => {
  /** The top/bottom halves of a `padding` or `margin` shorthand — the
   *  block-axis twin of `inlineSides` above. */
  function blockSides(shorthand: string | null): [string, string] | null {
    if (shorthand === null) return null
    const parts = shorthand.split(/\s+/).filter((p) => p !== '')
    switch (parts.length) {
      case 1:
        return [parts[0], parts[0]]
      case 2:
        return [parts[0], parts[0]]
      case 3:
      case 4:
        return [parts[0], parts[2]]
      default:
        return null
    }
  }

  /** `shipped`, for a selector that is not a bare class. */
  function shippedFor(selector: string, property: string): string | null {
    let found: string | null = null
    const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
    for (const rule of RULES) {
      if (!rule.selectors.some((s) => s === selector)) continue
      const m = rule.body.match(pattern)
      if (m) found = m[1].trim()
    }
    return found
  }

  // The header carries no room below it on purpose, and the row under it
  // supplies it — which the filter row does, and the body did not. So the
  // one panel with no filter row (Operations) had NOTHING between its name
  // and its first line of content, while the other four had the filter's
  // own 8px. The owner reported it as "the padding is wrong on the
  // operations screen"; it is the shell's rule that was wrong, for any
  // panel that does not happen to want a filter (nocx-708q.4).
  it('leaves no room below the header itself, so the room has one owner', () => {
    const header = blockSides(shipped('ui-sidebar-view__header', 'padding'))
    expect(header, 'the header declares no padding shorthand').not.toBeNull()
    expect((header as [string, string])[1]).toBe('0')
  })

  it('gives a filterless panel the same room under the name as a filtered one', () => {
    const filterTop = blockSides(shipped('ui-sidebar-view__filter', 'padding'))
    expect(filterTop, 'the filter row declares no padding shorthand').not.toBeNull()
    const bodyTop = shippedFor('.ui-sidebar-view__header + .ui-sidebar-view__body', 'padding-top')
    expect(bodyTop).toBe((filterTop as [string, string])[0])
  })
})
