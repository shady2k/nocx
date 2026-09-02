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

  it('sometimes steps off the end instead of turning, and lands below', () => {
    // The behaviour that makes the terminal a landscape rather than a set of
    // separate shelves. `seq(0)` is inside the step-off chance; the turn test
    // above uses 0.5, which is outside it.
    let p = standing(A, { locomotion: 'walk', dir: 1, x: A.x1 - 1, hold: 100 })
    p = step(p, env(), 0.5, seq(0))
    expect(p.locomotion).toBe('fall')
    expect(p.ledgeId).toBeNull()
    expect(p.x).toBe(A.x1)

    // It left at A's own edge, which is inside B's span, so B catches it —
    // the ledges below are the same width, and leaving a pixel past the end
    // would have missed every one of them.
    for (let i = 0; i < 120 && p.locomotion === 'fall'; i++) p = step(p, env(), 1 / 60, seq(0.5))
    expect(p.ledgeId).toBe('blk:2')
  })

  it('steps off the LEFT end facing left, so it does not walk backwards off it', () => {
    const p = step(
      standing(A, { locomotion: 'walk', dir: -1, x: A.x0 + 1, hold: 100 }),
      env(),
      0.5,
      seq(0),
    )
    expect(p.locomotion).toBe('fall')
    expect(p.dir).toBe(-1)
    expect(p.x).toBe(A.x0)
  })

  it('never leaves the ledge it stands on', () => {
    // rng pinned above the step-off chance, so this is the turning path.
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

describe('leaving a ledge from the middle', () => {
  it('the menu offers dropping off where it stands', () => {
    // A command block is the full width of the pane; at a walking pace the
    // animal needs the better part of a minute to reach an edge. Without
    // this it lives on whichever ledge it first landed on.
    const p = standing(A, { hold: 0.01, x: 200 })
    // The calm menu weighs walk 30, descend 12, then the still ones — total
    // 112, so the descend band is [30,42)/112. 0.3 is inside it.
    const after = step(p, env(), 0.02, seq(0.3))
    expect(after.locomotion).toBe('fall')
    expect(after.ledgeId).toBeNull()
    expect(after.x).toBe(200)
  })

  it('a walking pet reaches a DIFFERENT ledge within a minute of terminal time', () => {
    // The claim the feature actually makes: over a minute, on real geometry
    // (blocks as wide as the pane), the animal is seen in more than one
    // place. Deterministic — the rng cycles, it is not sampled.
    const wide: Ledge[] = [
      { id: 'blk:1', x0: 8, x1: 1300, y: 120 },
      { id: 'blk:2', x0: 8, x1: 1300, y: 260 },
      { id: 'blk:3', x0: 8, x1: 1300, y: 400 },
    ]
    let p = standing(wide[0])
    const visited = new Set<string | null>()
    const rng = seq(0.05, 0.6, 0.3, 0.9, 0.12, 0.44)
    for (let i = 0; i < 60 * 60; i++) {
      p = step(p, { terrain: wide, floor: FLOOR, petHeight: 34 }, 1 / 60, rng)
      if (p.locomotion !== 'fall') visited.add(p.ledgeId)
    }
    expect(visited.size).toBeGreaterThan(1)
  })
})

describe('getting out of the pointer’s way', () => {
  const at = (x: number, y: number) => ({ ...env(), pointer: { x, y } })

  it('runs away from a cursor that comes close', () => {
    const p = step(standing(A, { x: 200 }), at(190, A.y - 17), 1 / 60, seq(0.9))
    expect(p.locomotion).toBe('run')
    expect(p.dir).toBe(1) // pointer is to its left, so it goes right
  })

  it('runs the other way for a cursor on the other side', () => {
    const p = step(standing(A, { x: 200 }), at(215, A.y - 17), 1 / 60, seq(0.9))
    expect(p.dir).toBe(-1)
  })

  it('ignores a cursor that is not near it', () => {
    const p = step(
      standing(A, { x: 200, activity: 'groom', hold: 5 }),
      at(295, A.y),
      1 / 60,
      seq(0.9),
    )
    expect(p.locomotion).toBe('idle')
    expect(p.activity).toBe('groom')
  })

  it('measures from the animal’s middle, so a cursor on its head counts', () => {
    // `y` is where it stands; a cursor level with the head of a 34px cat is
    // 17px above that, and must still be a threat.
    const p = step(standing(A, { x: 200 }), at(200, A.y - 30), 1 / 60, seq(0.9))
    expect(p.locomotion).toBe('run')
  })

  it('interrupts even a settled occupation', () => {
    const p = step(
      standing(A, { x: 200, activity: 'sleep', hold: 99 }),
      at(200, A.y),
      1 / 60,
      seq(0.9),
    )
    expect(p.locomotion).toBe('run')
    expect(p.activity).toBe('none')
  })

  it('steps off the ledge rather than turning back into the cursor', () => {
    // Cornered at the right end with the pointer on its left: turning round
    // would walk it into the thing it is running from.
    const p = step(
      standing(A, { x: A.x1 - 1, locomotion: 'run', dir: 1, hold: 9 }),
      at(A.x1 - 40, A.y),
      0.2,
      seq(0.99), // above the step-off chance: only the cornering can do this
    )
    expect(p.locomotion).toBe('fall')
    expect(p.ledgeId).toBeNull()
  })

  it('does nothing at all when the pointer is elsewhere', () => {
    const p = step(standing(A, { x: 200, activity: 'groom', hold: 5 }), env(), 1 / 60, seq(0.9))
    expect(p.activity).toBe('groom')
  })
})

describe('the flee test uses the animal’s box, not a radius', () => {
  const at = (x: number, y: number) => ({ ...env(), pointer: { x, y } })

  it('a cursor on a WIDE pet’s flank is still a threat', () => {
    // At 96px tall the cat is about 180px wide. A radius around its position
    // puts its own shoulder outside the threat, and it sits there while the
    // cursor rests on it — which is what happened in the browser.
    const wide = { ...env(), petHeight: 96, petWidth: 180, pointer: { x: 200 - 85, y: A.y - 48 } }
    expect(step(standing(A, { x: 200 }), wide, 1 / 60, seq(0.9)).locomotion).toBe('run')
  })

  it('and a cursor well beyond the flank is not', () => {
    const wide = { ...env(), petHeight: 96, petWidth: 180, pointer: { x: 200 - 260, y: A.y - 48 } }
    const p = step(standing(A, { x: 200, activity: 'groom', hold: 5 }), wide, 1 / 60, seq(0.9))
    expect(p.activity).toBe('groom')
  })

  it('falls back to the height when nobody said how wide it is', () => {
    const p = step(standing(A, { x: 200 }), { ...at(190, A.y - 17) }, 1 / 60, seq(0.9))
    expect(p.locomotion).toBe('run')
  })
})
