// The pet's state machine (nocx-q4qeh.1). Pure: time is `dt`, chance is
// `rng`, so a thousand seconds of cat runs deterministically in a millisecond.
import { describe, expect, it } from 'vitest'
import {
  DEFAULT_TUNING,
  FLOOR_ID,
  newPet,
  onFloor,
  react,
  step,
  type Pet,
  type StepEnv,
} from './pet'
import type { Ledge } from './terrain'

const FLOOR: Ledge = { id: FLOOR_ID, x0: 0, x1: 900, y: 580 }
const A: Ledge = { id: 'blk:1', x0: 100, x1: 300, y: 200 }
const B: Ledge = { id: 'blk:2', x0: 100, x1: 300, y: 400 }

function env(terrain: readonly Ledge[] = [A, B]): StepEnv {
  return { terrain, floor: FLOOR, petHeight: 34 }
}

/** Deterministic rng cycling through the given values. */
function seq(...values: number[]): () => number {
  let i = 0
  return () => values[i++ % values.length]
}

function standing(on: Ledge, over: Partial<Pet> = {}): Pet {
  return {
    ...newPet(on.x0 + 50, on.y),
    ledgeId: on.id,
    locomotion: 'idle',
    activity: 'sit',
    hold: 10,
    ...over,
  }
}

describe('falling and landing', () => {
  it('a new pet falls until a ledge catches it', () => {
    let p = newPet(200, 0)
    for (let i = 0; i < 60 && p.locomotion === 'fall'; i++) p = step(p, env(), 1 / 60, seq(0.5))
    expect(p.ledgeId).toBe('blk:1')
    expect(p.y).toBe(200)
    expect(p.locomotion).toBe('idle')
  })

  it('lands on the pane floor when nothing else is under it', () => {
    let p = newPet(700, 0) // beyond every ledge's right edge
    for (let i = 0; i < 120 && p.locomotion === 'fall'; i++) p = step(p, env(), 1 / 60, seq(0.5))
    expect(onFloor(p)).toBe(true)
    expect(p.y).toBe(FLOOR.y)
  })

  it('does not fall THROUGH a ledge on a single fast frame', () => {
    // One tenth of a second under gravity covers more ground than the gap
    // between A and B. Sampling would miss A entirely.
    const p = step(newPet(200, 150), env(), 0.4, seq(0.5))
    expect(p.ledgeId).toBe('blk:1')
  })
})

describe('losing the ground', () => {
  it('drops the pet when its block is removed, never moves it elsewhere', () => {
    const p = standing(A)
    const after = step(p, env([B]), 1 / 60, seq(0.5))
    expect(after.locomotion).toBe('fall')
    expect(after.ledgeId).toBeNull()
    // The point of the rule: it did NOT become B.
    expect(after.y).toBeCloseTo(A.y, 0)
  })

  it('carries the pet when its block merely moves, as on a scroll', () => {
    const p = standing(A)
    const moved: Ledge = { ...A, y: 120 }
    const after = step(p, env([moved, B]), 1 / 60, seq(0.5))
    expect(after.ledgeId).toBe('blk:1')
    expect(after.y).toBe(120)
    expect(after.locomotion).not.toBe('fall')
  })
})

describe('walking', () => {
  it('turns round at the end of the ledge instead of walking off it', () => {
    let p = standing(A, { locomotion: 'walk', dir: 1, x: A.x1 - 1, hold: 100 })
    p = step(p, env(), 0.5, seq(0.5))
    expect(p.x).toBe(A.x1)
    expect(p.dir).toBe(-1)
    p = step(p, env(), 0.5, seq(0.5))
    expect(p.x).toBeLessThan(A.x1)
  })

  it('never leaves the ledge it stands on', () => {
    let p = standing(A, { locomotion: 'run', dir: 1, hold: 1e6 })
    for (let i = 0; i < 2000; i++) {
      p = step(p, env(), 1 / 60, seq(0.5))
      expect(p.x).toBeGreaterThanOrEqual(A.x0)
      expect(p.x).toBeLessThanOrEqual(A.x1)
    }
  })
})

describe('reacting to a command', () => {
  it('meows on success and scratches on failure', () => {
    expect(react(standing(A), 'success').activity).toBe('meow')
    expect(react(standing(A), 'success').mood).toBe('pleased')
    expect(react(standing(A), 'failure').activity).toBe('scratch')
    expect(react(standing(A), 'failure').mood).toBe('worried')
  })

  it('interrupts whatever the pet was doing', () => {
    const busy = standing(A, { locomotion: 'walk', activity: 'none', hold: 99 })
    const r = react(busy, 'success')
    expect(r.locomotion).toBe('idle')
    expect(r.hold).toBe(DEFAULT_TUNING.reactionHold)
  })

  it('does not interrupt a fall — a cat mid-air has no say', () => {
    const falling = { ...newPet(200, 100), locomotion: 'fall' as const }
    const r = react(falling, 'success')
    expect(r.locomotion).toBe('fall')
    expect(r.activity).toBe('none')
    expect(r.mood).toBe('pleased') // the mood still lands
  })

  it('lets the mood decay back to calm on its own', () => {
    let p = react(standing(A), 'failure')
    expect(p.mood).toBe('worried')
    for (let i = 0; i < 60 * (DEFAULT_TUNING.moodHold + 1); i++) {
      p = step(p, env(), 1 / 60, seq(0.5))
    }
    expect(p.mood).toBe('calm')
  })
})

describe('boredom', () => {
  it('falls asleep after long enough with nothing to do', () => {
    // rng pinned to the sit/lie end of the menu so it never walks.
    let p = standing(A, { hold: 0.1 })
    for (let i = 0; i < 60 * (DEFAULT_TUNING.sleepAfter + 2); i++) {
      p = step(p, env(), 1 / 60, seq(0.99))
    }
    expect(p.activity).toBe('sleep')
    expect(p.mood).toBe('tired')
  })

  it('a walk resets the road to sleep', () => {
    let p = standing(A, { locomotion: 'walk', activity: 'none', hold: 1e6 })
    for (let i = 0; i < 60 * (DEFAULT_TUNING.sleepAfter + 2); i++) {
      p = step(p, env(), 1 / 60, seq(0.5))
    }
    expect(p.activity).not.toBe('sleep')
  })

  it('wakes when a command finishes', () => {
    const asleep = standing(A, { activity: 'sleep', mood: 'tired', boredom: 999 })
    expect(react(asleep, 'success').activity).toBe('meow')
  })
})

describe('choosing what to do', () => {
  it('finishes an occupation before picking another', () => {
    const p = standing(A, { activity: 'groom', hold: 2 })
    const after = step(p, env(), 1, seq(0))
    expect(after.activity).toBe('groom')
    expect(after.hold).toBeCloseTo(1, 5)
  })

  it('picks from the menu once the hold runs out', () => {
    const p = standing(A, { activity: 'groom', hold: 0.01 })
    // First entry of every menu is the walk.
    const after = step(p, env(), 0.02, seq(0))
    expect(after.locomotion).toBe('walk')
  })
})
