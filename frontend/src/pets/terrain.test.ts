// Terrain derivation (nocx-q4qeh.1). No DOM: these are the rules, not the
// plumbing that feeds them.
import { describe, expect, it } from 'vitest'
import {
  deriveTerrain,
  ledgeAbove,
  ledgeById,
  ledgeCrossed,
  type Ledge,
  type LedgeCandidate,
  type TerrainOpts,
} from './terrain'

const OPTS: TerrainOpts = {
  petHeight: 34,
  petWidth: 0,
  minWidth: 56,
  inset: 8,
  viewport: { width: 900, height: 600 },
}

function block(id: string, top: number, left = 100, right = 800): LedgeCandidate {
  return { id, top, left, right }
}

describe('deriveTerrain', () => {
  it('turns a block top edge into a ledge inset from both ends', () => {
    expect(deriveTerrain([block('a', 200)], OPTS)).toEqual<Ledge[]>([
      { id: 'a', x0: 108, x1: 792, y: 200 },
    ])
  })

  it('refuses a ledge with no head clearance above it', () => {
    // The body occupies the space ABOVE the edge it stands on, so an edge
    // closer to the top than the animal is tall puts the animal off-screen.
    expect(deriveTerrain([block('high', 33)], OPTS)).toEqual([])
    expect(deriveTerrain([block('just', 34)], OPTS)).toHaveLength(1)
  })

  it('refuses a ledge below the floor', () => {
    expect(deriveTerrain([block('under', 601)], OPTS)).toEqual([])
  })

  it('refuses a ledge too narrow to walk on', () => {
    // 100..170 insets to 108..162 — 54px, under the 56 minimum.
    expect(deriveTerrain([block('sliver', 200, 100, 170)], OPTS)).toEqual([])
  })

  it('keeps block geometry independent of pet width', () => {
    expect(deriveTerrain([block('wide', 200, 100, 300)], OPTS)).toEqual<Ledge[]>([
      { id: 'wide', x0: 108, x1: 292, y: 200 },
    ])
  })
  it('clips a ledge at the pane edges by half the body width', () => {
    const opts = { ...OPTS, petWidth: 94 }
    expect(deriveTerrain([block('edge', 200, 0, 900)], opts)).toEqual<Ledge[]>([
      { id: 'edge', x0: 47, x1: 853, y: 200 },
    ])
  })

  it('keeps a chip as ground when its visible body overhangs', () => {
    expect(deriveTerrain([block('chip', 200, 100, 177)], { ...OPTS, petWidth: 94 })).toEqual<
      Ledge[]
    >([{ id: 'chip', x0: 108, x1: 169, y: 200 }])
  })

  it('keeps the measured candidate count at both pet sizes', () => {
    const widths = [1332, 84, 52, 77, 1319, 44, 53, 64, 155]
    const candidates = widths.map((width, i) => {
      const left = width >= 500 ? 0 : 300
      return block(String(i), 100 + i * 40, left, left + width)
    })
    const standard = deriveTerrain(candidates, { ...OPTS, petWidth: 94 })
    const maximum = deriveTerrain(candidates, { ...OPTS, petWidth: 180 })
    expect(standard).toHaveLength(5)
    expect(maximum).toHaveLength(5)
    expect(standard.map((l) => l.id)).toEqual(['0', '1', '3', '4', '8'])
    expect(maximum.map((l) => l.id)).toEqual(['0', '1', '3', '4', '8'])
  })

  it('returns ledges top to bottom whatever order they arrived in', () => {
    const t = deriveTerrain([block('c', 400), block('a', 100), block('b', 250)], OPTS)
    expect(t.map((l) => l.id)).toEqual(['a', 'b', 'c'])
  })
})

describe('ledgeById', () => {
  const terrain = deriveTerrain([block('a', 200), block('b', 300)], OPTS)

  it('finds the ledge the pet is standing on', () => {
    expect(ledgeById(terrain, 'b')?.y).toBe(300)
  })

  it('reports nothing once the block is gone', () => {
    // The pet holds an id precisely so that this can be detected: a block
    // removed under it must drop it, never silently move it elsewhere.
    expect(ledgeById(terrain, 'gone')).toBeNull()
    expect(ledgeById(terrain, null)).toBeNull()
  })
})

describe('ledgeCrossed', () => {
  const terrain = deriveTerrain([block('a', 200), block('b', 300)], OPTS)

  it('catches a ledge the step jumped clean over', () => {
    // One frame of a fast fall covers more than the ledge is thick. Sampling
    // "am I inside it now" finds nothing; sweeping the segment finds it.
    expect(ledgeCrossed(terrain, 400, 150, 260)?.id).toBe('a')
  })

  it('returns the FIRST ledge under a step that crosses two', () => {
    expect(ledgeCrossed(terrain, 400, 150, 500)?.id).toBe('a')
  })

  it('ignores a ledge the pet is not above horizontally', () => {
    expect(ledgeCrossed(terrain, 50, 150, 500)).toBeNull()
  })

  it('ignores a ledge already passed and one not yet reached', () => {
    expect(ledgeCrossed(terrain, 400, 200, 250)).toBeNull()
    expect(ledgeCrossed(terrain, 400, 100, 199)).toBeNull()
  })

  it('finds nothing when the step does not descend', () => {
    expect(ledgeCrossed(terrain, 400, 260, 260)).toBeNull()
    expect(ledgeCrossed(terrain, 400, 500, 100)).toBeNull()
  })
})

describe('ledgeAbove', () => {
  const terrain = deriveTerrain([block('high', 100), block('mid', 250), block('low', 400)], OPTS)

  it('finds the nearest ledge overhead that is within reach', () => {
    expect(ledgeAbove(terrain, 400, 400, 200)?.id).toBe('mid')
  })

  it('ignores one too far up to jump to', () => {
    expect(ledgeAbove(terrain, 400, 400, 100)).toBeNull()
  })

  it('ignores one the animal is not under', () => {
    expect(ledgeAbove(terrain, 50, 400, 400)).toBeNull()
  })

  it('ignores the ledge it is already standing on, and anything below', () => {
    expect(ledgeAbove(terrain, 400, 250, 500)?.id).toBe('high')
  })

  it('will not pick a shelf that is barely overhead', () => {
    // Under the clearance it is not a place to jump to, it is where you are.
    const close = deriveTerrain([block('a', 300), block('b', 295)], OPTS)
    expect(ledgeAbove(close, 400, 300, 500)).toBeNull()
  })
})
