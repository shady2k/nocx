import { describe, expect, it } from 'vitest'
import { devicePxToCssPx } from './committed-metric'

// The one conversion the painter's committed-px consumers share, tested at
// the two ratios that decide its behaviour (review round 1, nocx-zg3k3.2.9).
describe('committed device px → CSS px', () => {
  it('is the identity at dpr 1', () => {
    expect(devicePxToCssPx(8, 1)).toBe(8)
    expect(devicePxToCssPx(20, 1)).toBe(20)
  })

  it('returns the exact fractional CSS cell at dpr 2', () => {
    // xterm's device cell 17x34 is a CSS cell of 8.5x17 — the division is
    // the whole conversion, and no rounding may meet it.
    expect(devicePxToCssPx(17, 2)).toBe(8.5)
    expect(devicePxToCssPx(34, 2)).toBe(17)
  })
})
