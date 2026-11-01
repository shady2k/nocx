// ONE ACTIVE BODY, ONE BOX, NO PADDING AT THE JOINT (nocx-zg3k3.14.26,
// ADR-3kqrem7v).
//
// A running command's rows are painted in two boxes and both are its body: the
// block paints the rows the durable record has already taken (the ones that
// left the screen) into its own `.cmd-output`, and the live region below
// paints the screen the command is still writing. ADR-0091 removed the two
// pads that met at that joint; ADR-3kqrem7v removes the body padding
// altogether while a command runs, in BOTH presentations, so the active rows
// are one edge-to-edge box and a buffer switch cannot change the measured
// grid. The frozen body keeps its own pads, and that contrast is the point:
// a freeze restores the transcript's spacing, a mode switch does not touch
// it.
//
// The layout half of that is e2e/running-block-body-seam.spec.ts's, which reads
// the real boxes. This is the other half and the reason it is worth pinning
// here: jsdom computes no layout, so a rule that quietly comes back is invisible
// to every unit test until the stand shows it again. Same treatment as
// live-clip.test.ts — read the shipped stylesheet and assert the contract.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

type Rule = { selectors: string[]; body: string }

/** A selector list split on its own separators: a comma inside
 *  `:is()`/`:not()` belongs to that argument list, and splitting on it
 *  invents selectors out of the arguments (nocx-3an1s.1.4). */
function selectorList(selector: string): string[] {
  const out: string[] = []
  let depth = 0
  let current = ''
  for (const ch of selector) {
    if (ch === '(' || ch === '[') depth++
    else if (ch === ')' || ch === ']') depth--
    if (ch === ',' && depth === 0) {
      out.push(current.trim())
      current = ''
      continue
    }
    current += ch
  }
  out.push(current.trim())
  return out
}

/** Top-level rules only, comments stripped; an at-rule block is skipped whole.
 *  Lifted from live-clip.test.ts, which lifted it from cmd-output-wrap.test.ts. */
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
          rules.push({ selectors: selectorList(selector), body })
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

const RULES = [
  ...topLevelRules(readFileSync(resolve(import.meta.dirname ?? '.', '..', 'style.css'), 'utf8')),
  // The running header's fixed height is kit geometry (ADR-3kqrem7v), and the
  // frozen block's trailing space is the surface's — both imported by
  // style.css, neither written in it.
  ...topLevelRules(
    readFileSync(
      resolve(import.meta.dirname ?? '.', '..', 'styles/components/command-block-frame.css'),
      'utf8',
    ),
  ),
  ...topLevelRules(
    readFileSync(
      resolve(import.meta.dirname ?? '.', '..', 'styles/surfaces/command-block.css'),
      'utf8',
    ),
  ),
]

/** What the shipped cascade resolves `property` to for one exact selector,
 *  whitespace folded so a declaration wrapped across lines compares equal.
 *  `property` is a literal name (the queries below pass `padding-bottom` and
 *  its kin), so it is matched as written. */
function declared(selector: string, property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    if (!rule.selectors.includes(selector)) continue
    const m = rule.body.match(pattern)
    if (m) found = m[1].replace(/\s+/g, ' ').trim()
  }
  return found
}

describe('a running block whose body outgrew its screen (nocx-zg3k3.14.26, ADR-3kqrem7v)', () => {
  it('ends the active body at its last own row instead of paying the body bottom there', () => {
    // Both tiers of the active body are edge-to-edge with no padding: the
    // committed rows are not the body's end while the live region continues it,
    // and neither box pays anything at the joint.
    expect(declared('.cmd-block.cmd-block-running > .cmd-output', 'padding')).toBe('0')
    expect(declared('.terminal-live-container.live-running', 'padding')).toBe('0')
  })

  it('reserves one fixed header instead of body padding in either presentation', () => {
    // What the pane pays while a command runs is the shared 32px header and
    // nothing else: a body pad or a trailing space in either presentation is
    // what let a buffer switch change the measured grid (ADR-3kqrem7v).
    expect(declared(".ui-command-block-frame[data-variant='running']", 'height')).toBe(
      'var(--control-height-md)',
    )
    expect(declared('.cmd-block.cmd-block-running', 'padding-bottom')).toBe('0')
  })

  it('restores the frozen body pads, so a freeze is the same rows in the transcript box', () => {
    // The frozen body is where the transcript's spacing lives — the contrast
    // that makes "no active padding" a decision rather than an omission.
    expect(declared('.cmd-output', 'padding')).toBe(
      'var(--cmd-output-pad-top) 0 var(--cmd-output-pad-bottom)',
    )
    expect(declared('.cmd-block', 'padding-bottom')).toBe('var(--terminal-block-trailing-space)')
  })

  it('gives the whole active body one edge-to-edge box, with no inline inset of its own', () => {
    // The gutter is the transcript's (ADR-3kqrem7v keeps it on frozen rows
    // only). Any rule reaching the nested live region — or the running block
    // and its committed rows — must state an inline inset of 0; a second
    // nonzero inset inside the block would shift every column of the grid off
    // the width the PTY was told.
    for (const rule of RULES) {
      for (const selector of rule.selectors) {
        const active =
          /\.cmd-block[^ ,>]*\.cmd-block-running\b/.test(selector) ||
          /\.cmd-block[^ ,>]*\s*>\s*\.terminal-live-container/.test(selector)
        if (!active) continue
        for (const m of rule.body.matchAll(
          /(?:^|;)\s*(padding(?:-inline)?(?:-left|-right)?)\s*:\s*([^;]+)/g,
        ))
          expect(m[2].replace(/\s+/g, ' ').trim(), `${selector} ${m[1]}`).toBe('0')
      }
    }
    expect(declared('.scrollback-inner > :not(.cell-metric-probe)', 'padding-inline')).toBe(
      'var(--pane-inline-padding)',
    )
  })
})
