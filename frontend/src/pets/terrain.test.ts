// Terrain derivation (nocx-q4qeh.1). No DOM: these are the rules, not the
// plumbing that feeds them.
import { describe, expect, it } from 'vitest'
import {
  deriveTerrain,
  ledgeById,
  ledgeCrossed,
  type Ledge,
  type LedgeCandidate,
  type TerrainOpts,
} from './terrain'

const OPTS: TerrainOpts = {
  petHeight: 34,
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
