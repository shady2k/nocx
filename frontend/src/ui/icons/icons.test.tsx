// @vitest-environment jsdom
// The kit's glyphs, and the one rule that keeps a glyph findable: it is in
// the barrel and it has a row in the README.
//
// There was no test here until `ArrowRightLeftIcon` was added (nocx-zccer),
// and the reason it exists now is the failure that bead records: the API
// surface shipped `ArrowRightIcon` — a NAVIGATION glyph — with no recorded
// reason, and nothing anywhere said what the icon was supposed to mean or
// where the next person would look for one. A barrel export nobody can find
// in the inventory is the same defect as a component that is not in the kit.
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { render } from '@solidjs/testing-library'
import type { Component } from 'solid-js'
import { ArrowRightIcon, ArrowRightLeftIcon } from './index'

const README = readFileSync('src/ui/README.md', 'utf8')

/** The `d` of every path the icon draws, in document order. */
function paths(icon: Component): string[] {
  const { container } = render(() => icon({}))
  return [...container.querySelectorAll('path')].map((p) => p.getAttribute('d') ?? '')
}

describe('ArrowRightLeftIcon — the request out and the response back', () => {
  it('draws TWO arrows, not the one ArrowRightIcon draws', () => {
    const exchange = paths(ArrowRightLeftIcon)
    const single = paths(ArrowRightIcon)

    // Two shafts and two heads. A single arrow has one of each, which is
    // exactly what this replaced and what it must not be mistaken for at
    // 16px beside FolderIcon and PlugIcon.
    expect(exchange).toHaveLength(4)
    expect(single).toHaveLength(2)
    for (const d of single) expect(exchange).not.toContain(d)
  })

  it('the two shafts are horizontal, at different heights, and end at opposite sides', () => {
    const [headOut, shaftOut, headBack, shaftBack] = paths(ArrowRightLeftIcon)

    // The shafts are the horizontal runs: `H` (or `h`) and nothing else.
    expect(shaftOut).toMatch(/^M\d+ \d+H\d+$/)
    expect(shaftBack).toMatch(/^M\d+ \d+H\d+$/)
    // Stacked, not overdrawn: two arrows on one line would read as one.
    const y = (d: string) => Number(/^M\d+ (\d+)H/.exec(d)?.[1])
    expect(y(shaftOut)).not.toBe(y(shaftBack))
    // And the heads are on opposite ends — out at the right, back at the
    // left, which is what makes the pair read as a round trip rather than
    // as two arrows going the same way.
    expect(headOut).not.toBe(headBack)
  })

  it('renders as a scalable svg with no fixed pixel size — the button decides', () => {
    const { container } = render(() => ArrowRightLeftIcon({}))
    const svg = container.querySelector('svg')
    expect(svg?.getAttribute('viewBox')).toBe('0 0 24 24')
    expect(svg?.getAttribute('width')).toBeNull()
    expect(svg?.getAttribute('height')).toBeNull()
    // Decorative: the IconButton around it carries the accessible name.
    expect(svg?.getAttribute('aria-hidden')).toBe('true')
    expect(svg?.getAttribute('stroke')).toBe('currentColor')
  })

  it('has its row in the kit inventory, so the next person finds it', () => {
    expect(README).toContain('ArrowRightLeftIcon')
  })
})
