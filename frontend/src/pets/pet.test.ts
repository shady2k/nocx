// The pet's state machine (nocx-q4qeh.1). Pure: time is `dt`, chance is
// `rng`, so a thousand seconds of cat runs deterministically in a millisecond.
import { describe, expect, it } from 'vitest'
import {
  attend as rawAttend,
  DEFAULT_TUNING,
  FLOOR_ID,
  newPet,
  onFloor,
  react as rawReact,
  step,
  type Pet,
  type PetTiming,
  type StepEnv,
} from './pet'
import { CAT_PACK } from './pack'
import type { Ledge } from './terrain'

const FLOOR: Ledge = { id: FLOOR_ID, x0: 0, x1: 900, y: 580 }
const A: Ledge = { id: 'blk:1', x0: 100, x1: 300, y: 200 }
const B: Ledge = { id: 'blk:2', x0: 100, x1: 300, y: 400 }

const timing = (mode: 'loop' | 'once' | 'hold', duration: number) => ({
  mode,
  duration,
  pause: mode === 'loop' ? 0 : 0,
})

const TIMING: PetTiming = {
  fps: 10,
  locomotion: {
    idle: timing('loop', 1),
    walk: timing('loop', 0.8),
    run: timing('loop', 0.8),
    fall: timing('loop', 0.8),
  },
  activity: {
    sit: timing('hold', 0.1),
    groom: timing('once', 0.5),
    stretch: timing('once', 1.3),
    lie: timing('loop', 0.8),
    scratch: timing('once', 0.2),
    meow: timing('once', 0.4),
    sleep: timing('hold', 0.1),
  },
  strides: { walk: 32, run: 48 },
}

const ENV_DEFAULTS = { petScale: 1, timing: TIMING }

function attend(pet: Pet, author: 'shell' | 'agent' = 'shell'): Pet {
  return rawAttend(pet, author, TIMING)
}

function react(
  pet: Pet,
  outcome: 'success' | 'failure' | 'unknown',
  author: 'shell' | 'agent' = 'shell',
): Pet {
  return rawReact(pet, outcome, author, TIMING)
}

function env(terrain: readonly Ledge[] = [A, B]): StepEnv {
  return { terrain, floor: FLOOR, petHeight: 34, ...ENV_DEFAULTS }
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
  it('pauses at the edge before turning round', () => {
    let p = standing(A, { locomotion: 'walk', dir: 1, x: A.x1 - 1, hold: 100 })
    p = step(p, env(), 0.5, seq(0.5))
    expect(p.x).toBe(A.x1)
    expect(p.phase).toBe('turn')
    p = step(p, env(), 0.06, seq(0.5))
    expect(p.dir).toBe(-1)
    p = step(p, env(), 0.06, seq(0.5))
    expect(p.phase).toBe('none')
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
  it('ramps gait speed up and down instead of snapping it', () => {
    const target = TIMING.strides.walk / TIMING.locomotion.walk.duration
    let p = standing(A, { locomotion: 'walk', hold: 100 })
    p = step(p, env(), 0.09, seq(0.5))
    expect(p.vx).toBeGreaterThan(0)
    expect(p.vx).toBeLessThan(target)
    p = step(p, env(), 0.09, seq(0.5))
    expect(p.vx).toBeCloseTo(target, 5)
    p = step({ ...p, locomotion: 'idle' }, env(), 0.09, seq(0.5))
    expect(p.vx).toBeGreaterThan(0)
    p = step(p, env(), 0.09, seq(0.5))
    expect(p.vx).toBe(0)
  })

  it('derives gait speed from the same scale at small and large sizes', () => {
    const walk = (petScale: number) =>
      step(
        standing(A, { locomotion: 'walk', hold: 100 }),
        { ...env(), petScale },
        DEFAULT_TUNING.gaitRamp,
        seq(0.5),
      ).vx
    expect(walk(2)).toBeCloseTo(walk(0.5) * 4, 5)
  })

  it('keeps the contact foot within one source pixel per animation frame', () => {
    const ledge: Ledge = { id: 'stride', x0: 0, x1: 2000, y: 200 }
    for (const petScale of [0.5, 2]) {
      const startX = 800
      const sourceStride = TIMING.strides.walk / TIMING.locomotion.walk.duration / CAT_PACK.fps
      const target = (TIMING.strides.walk / TIMING.locomotion.walk.duration) * petScale
      const p = step(
        standing(ledge, { x: startX, locomotion: 'walk', vx: target, hold: 100 }),
        { ...env([ledge]), petScale },
        1 / CAT_PACK.fps,
        seq(0.5),
      )
      expect(Math.abs(p.x - startX - sourceStride * petScale)).toBeLessThan(1)
    }
  })

  it('uses per-frame run displacement derived from the full stride', () => {
    const startX = A.x0 + 50
    const sourceStride = TIMING.strides.run / TIMING.locomotion.run.duration / CAT_PACK.fps
    const target = TIMING.strides.run / TIMING.locomotion.run.duration
    const p = step(
      standing(A, { x: startX, locomotion: 'run', vx: target, hold: 100 }),
      env(),
      1 / CAT_PACK.fps,
      seq(0.5),
    )
    expect(Math.abs(p.x - startX - sourceStride)).toBeLessThan(1)
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
  })
  it('uses the reaction clip length rather than reactionHold', () => {
    const r = react(standing(A), 'success')
    expect(r.hold).toBe(4 / TIMING.fps)
    expect(r.hold).not.toBe(DEFAULT_TUNING.reactionHold)
  })

  it('keeps the caller hold for a hold-mode reaction', () => {
    const r = react(standing(A), 'unknown')
    expect(r.activity).toBe('sit')
    expect(r.hold).toBe(DEFAULT_TUNING.reactionHold)
  })

  it('keeps the caller hold for a loop-mode reaction', () => {
    const r = react(standing(A), 'failure', 'agent')
    expect(r.activity).toBe('lie')
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
      p = step(p, { terrain: wide, floor: FLOOR, petHeight: 34, ...ENV_DEFAULTS }, 1 / 60, rng)
      if (p.locomotion !== 'fall') visited.add(p.ledgeId)
    }
    expect(visited.size).toBeGreaterThan(1)
  })
})

describe('getting out of the pointer’s way', () => {
  const at = (x: number, y: number) => ({ ...env(), pointer: { x, y } })
  function overFrames(pet: Pet, points: readonly { x: number; y: number }[], dt = 0.1): Pet {
    let current = pet
    for (const pointer of points) current = step(current, { ...env(), pointer }, dt, seq(0.99))
    return current
  }

  it('turns to watch a cursor that approaches slowly instead of running', () => {
    const points = [{ x: 400, y: A.y - 17 }]
    for (let x = 390; x >= 270; x -= 10) points.push({ x, y: A.y - 17 })
    const p = overFrames(standing(A, { x: 200, activity: 'groom' }), points)
    expect(p.locomotion).not.toBe('run')
    expect(p.activity).toBe('sit')
  })

  it('runs from a fast approach within 150 milliseconds', () => {
    const p = overFrames(standing(A, { x: 200 }), [
      { x: 400, y: A.y - 17 },
      { x: 260, y: A.y - 17 },
    ])
    expect(p.locomotion).toBe('run')
  })

  it('sits after a nearby cursor stays still', () => {
    const points = Array.from({ length: 8 }, () => ({ x: 250, y: A.y - 17 }))
    const p = overFrames(standing(A, { x: 200, activity: 'groom' }), points)
    expect(p.locomotion).toBe('idle')
    expect(p.activity).toBe('sit')
  })

  it('walks aside when a still cursor is directly on its body', () => {
    const p = overFrames(standing(A, { x: 200, activity: 'groom' }), [{ x: 200, y: A.y - 17 }], 0.1)
    expect(p.locomotion).toBe('walk')
    expect(p.locomotion).not.toBe('run')
  })

  it('remembers calm cursor encounters only briefly', () => {
    let p = standing(A, { x: 200, activity: 'groom' })
    for (let encounter = 0; encounter < 3; encounter++) {
      p = overFrames(p, [
        { x: 250, y: A.y - 17 },
        { x: 250, y: A.y - 17 },
      ])
      p = overFrames(p, [{ x: 400, y: A.y - 17 }])
    }
    expect(p.pointerCalm).toBeGreaterThan(0)
    p = step(p, env(), 9, seq(0.99))
    expect(p.pointerCalm).toBe(0)
  })

  it('runs away from a cursor that comes close quickly', () => {
    const p = overFrames(standing(A, { x: 200 }), [
      { x: 400, y: A.y - 17 },
      { x: 190, y: A.y - 17 },
    ])
    expect(p.locomotion).toBe('run')
    expect(p.dir).toBe(1) // pointer is to its left, so it goes right
  })

  it('turns at the edge for a calm nearby cursor instead of falling', () => {
    const pointer = at(A.x1 - 40, A.y)
    let p = step(
      standing(A, { x: A.x1 - 5, locomotion: 'walk', dir: 1, vx: 40, hold: 9 }),
      pointer,
      0.1,
      seq(0.99),
    )
    p = step(p, pointer, 0.1, seq(0.99))
    expect(p.pointerAlarmed).toBe(false)
    expect(p.locomotion).not.toBe('fall')
    expect(p.phase).toBe('turn')
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
    // `y` is where it stands; 17px above that is the head of a 34px cat.
    const p = overFrames(standing(A, { x: 200 }), [
      { x: 400, y: A.y - 30 },
      { x: 200, y: A.y - 30 },
    ])
    expect(p.locomotion).toBe('run')
  })

  it('interrupts even a settled occupation', () => {
    const p = overFrames(standing(A, { x: 200, activity: 'sleep', hold: 99 }), [
      { x: 400, y: A.y },
      { x: 200, y: A.y },
    ])
    expect(p.locomotion).toBe('run')
    expect(p.activity).toBe('none')
  })
  it('runs the other way for a cursor on the other side', () => {
    const p = overFrames(standing(A, { x: 200 }), [
      { x: 400, y: A.y - 17 },
      { x: 215, y: A.y - 17 },
    ])
    expect(p.locomotion).toBe('run')
    expect(p.dir).toBe(-1)
  })

  it('steps off the ledge rather than turning back into a fast cursor', () => {
    // Cornered at the right end with a fast pointer on its left: turning
    // round would walk it into the thing it is running from.
    const far = { ...at(500, A.y) }
    const close = { ...far, pointer: { x: A.x1 - 40, y: A.y } }
    let p = step(
      standing(A, { x: A.x1 - 1, locomotion: 'run', dir: 1, hold: 9 }),
      far,
      0.01,
      seq(0.99),
    )
    p = step(p, close, 0.2, seq(0.99))
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
    // puts its own shoulder outside the threat.
    const start = { ...env(), petHeight: 96, petWidth: 180, pointer: { x: 400, y: A.y - 48 } }
    const close = { ...start, pointer: { x: 115, y: A.y - 48 } }
    let p = step(standing(A, { x: 200 }), start, 0.1, seq(0.9))
    p = step(p, close, 0.1, seq(0.9))
    expect(p.locomotion).toBe('run')
  })

  it('and a cursor well beyond the flank is not', () => {
    const wide = { ...env(), petHeight: 96, petWidth: 180, pointer: { x: 200 - 260, y: A.y - 48 } }
    const p = step(standing(A, { x: 200, activity: 'groom', hold: 5 }), wide, 1 / 60, seq(0.9))
    expect(p.activity).toBe('groom')
  })

  it('falls back to the height when nobody said how wide it is', () => {
    const start = { ...at(400, A.y - 17) }
    const close = { ...start, pointer: { x: 190, y: A.y - 17 } }
    let p = step(standing(A, { x: 200 }), start, 0.1, seq(0.9))
    p = step(p, close, 0.1, seq(0.9))
    expect(p.locomotion).toBe('run')
  })
})

describe('watching a command that is running', () => {
  it('settles down when one starts, and lies for yours', () => {
    const p = attend(standing(A, { locomotion: 'walk', activity: 'none' }), 'shell')
    expect(p.attending).toBe('shell')
    expect(p.locomotion).toBe('idle')
    expect(p.activity).toBe('lie')
  })

  it('sits up for the assistant’s, rather than lying down', () => {
    expect(attend(standing(A), 'agent').activity).toBe('sit')
  })

  it('does not wander off, descend or run while it is watching', () => {
    // Every rng value, so this is the whole menu rather than a sample of it.
    for (let i = 0; i <= 20; i++) {
      let p = attend(standing(A, { hold: 0.01 }), 'shell')
      p = step(p, env(), 0.02, seq(i / 20))
      expect(p.locomotion).not.toBe('run')
      expect(p.locomotion).not.toBe('fall')
      expect(p.ledgeId).toBe(A.id)
    }
  })

  it('never falls asleep on the job', () => {
    // A pet that dozed off during your build would report the opposite of
    // what is happening.
    let p = attend(standing(A), 'shell')
    for (let i = 0; i < 60 * (DEFAULT_TUNING.sleepAfter + 5); i++) {
      p = step(p, env(), 1 / 60, seq(0.99))
    }
    expect(p.activity).not.toBe('sleep')
    expect(p.mood).not.toBe('tired')
  })

  it('stops watching the moment the command finishes', () => {
    const watching = attend(standing(A), 'shell')
    expect(react(watching, 'success', 'shell').attending).toBeNull()
    expect(react(watching, 'failure', 'agent').attending).toBeNull()
  })

  it('still gets out of the pointer’s way while watching', () => {
    // Watching narrows what it chooses to do; it does not glue it down.
    const first = { ...env(), pointer: { x: 400, y: A.y - 17 } }
    const close = { ...first, pointer: { x: 190, y: A.y - 17 } }
    let p = step(attend(standing(A, { x: 200 }), 'shell'), first, 0.1, seq(0.9))
    p = step(p, close, 0.1, seq(0.9))
    expect(p.locomotion).toBe('run')
  })

  it('takes the news of a start even mid-air, without interrupting the fall', () => {
    const falling = { ...newPet(200, 100), locomotion: 'fall' as const }
    const p = attend(falling, 'agent')
    expect(p.attending).toBe('agent')
    expect(p.locomotion).toBe('fall')
  })

  it('changes pose during a long command and returns to watching', () => {
    let p = attend(standing(A), 'shell')
    const activities = new Set<string>()
    for (let i = 0; i < 300; i++) {
      p = step(p, env(), 0.25, seq(0.99))
      activities.add(p.activity)
    }
    expect([...activities].some((activity) => ['stretch', 'scratch'].includes(activity))).toBe(true)
    expect(p.attending).toBe('shell')
    expect(p.ledgeId).toBe(A.id)
  })

  it('does not repeat the long-command thresholds', () => {
    let p = attend(standing(A), 'shell')
    let specialTransitions = 0
    let previous = p.activity
    for (let i = 0; i < 600; i++) {
      p = step(p, env(), 0.25, seq(0.99))
      if (
        ['stretch', 'scratch'].includes(p.activity) &&
        !['stretch', 'scratch'].includes(previous)
      ) {
        specialTransitions++
      }
      previous = p.activity
    }
    expect(specialTransitions).toBe(1)
  })

  it('uses a neutral pose for the agent vigil change', () => {
    let p = attend(standing(A), 'agent')
    for (let i = 0; i < 239; i++) p = step(p, env(), 0.25, seq(0.99))
    p = step(p, env(), 0.25, seq(0.99))
    expect(p.activity).toBe('groom')
  })

  it('freezes in a quiet pose after the final vigil gesture', () => {
    let p = attend(standing(A), 'agent')
    for (let i = 0; i < 300; i++) p = step(p, env(), 0.25, seq(0.99))
    expect(p.attending).toBe('agent')
    expect(p.locomotion).toBe('idle')
    expect(p.activity).toBe('sit')
    expect(p.hold).toBe(0)
  })
})

describe('whose command it was', () => {
  it('answers your command as though it were addressed to the cat', () => {
    expect(react(standing(A), 'success', 'shell').activity).toBe('meow')
    expect(react(standing(A), 'failure', 'shell').activity).toBe('scratch')
  })

  it('answers the assistant’s more quietly', () => {
    // An agent failing is not the person failing, and a pet that scolded them
    // for it would be wrong about who did what.
    expect(react(standing(A), 'success', 'agent').activity).toBe('stretch')
    expect(react(standing(A), 'failure', 'agent').activity).toBe('lie')
  })

  it('carries the same mood either way — the verdict is about the command', () => {
    expect(react(standing(A), 'failure', 'agent').mood).toBe('worried')
    expect(react(standing(A), 'failure', 'shell').mood).toBe('worried')
  })
})

describe('a verdict arriving after the next command has started', () => {
  // The freeze waits on a render fence and a start does not, so this ordering
  // is the NORMAL one, not an edge case: press Enter twice in a row and the
  // answer to the first command lands after the second has begun.
  it('lets the reaction finish rather than swallowing it', () => {
    const answered = react(standing(A), 'failure', 'shell')
    expect(answered.activity).toBe('scratch')
    const then = attend(answered, 'shell')
    expect(then.activity).toBe('scratch') // still answering you
    expect(then.attending).toBe('shell') // and already watching the next one
  })

  it('and settles to watching once the answer has played out', () => {
    let p = attend(react(standing(A), 'failure', 'shell'), 'shell')
    expect(p.hold).toBe(TIMING.activity.scratch.duration)
    for (let i = 0; i < 60 * 2; i++) {
      p = step(p, env(), 1 / 60, seq(0))
    }
    // First entry of the watching menu for your lane is lying down.
    expect(p.attending).toBe('shell')
    expect(p.activity).toBe('lie')
  })

  it('an ordinary occupation is interrupted, only an answer is protected', () => {
    const busy = standing(A, { locomotion: 'walk', activity: 'none', hold: 10 })
    expect(attend(busy, 'shell').activity).toBe('lie')
  })
})

describe('the answer stops being an answer once it is over', () => {
  // The protection is for a reaction that is still playing, not for whatever
  // the animal drifted into afterwards. Without a test the flag stayed true
  // for the rest of the pet's life and only the expiring hold hid it — the
  // lint noticed the dead assignment before any assertion did.
  it('an occupation chosen after a reaction is interruptible again', () => {
    let p = react(standing(A), 'failure', 'shell')
    expect(p.reacting).toBe(true)
    // Let the answer play out; the next choice is the animal's own.
    for (let i = 0; i < 60 * 3; i++) p = step(p, env(), 1 / 60, seq(0.99))
    expect(p.reacting).toBe(false)
    expect(attend(p, 'shell').activity).toBe('lie')
  })

  it('does not let the pointer cut a reaction clip short', () => {
    const answering = step(
      react(standing(A, { x: 200 }), 'failure', 'shell'),
      { ...env(), pointer: { x: 190, y: A.y - 17 } },
      1 / 60,
      seq(0.9),
    )
    expect(answering.reacting).toBe(true)
    expect(answering.activity).toBe('scratch')
  })
})

describe('going back up', () => {
  // Without this the animal could only ever go down. Stepping off an edge and
  // descending from the middle move it through the terrain in one direction
  // only, so over a few minutes every pet ended on the floor and stayed
  // there — the state that looks most like a sticker.
  const stack: Ledge[] = [
    { id: 'blk:1', x0: 100, x1: 300, y: 200 },
    { id: 'blk:2', x0: 100, x1: 300, y: 260 },
  ]
  const stacked: StepEnv = { terrain: stack, floor: FLOOR, petHeight: 34, ...ENV_DEFAULTS }

  it('jumps to the ledge above and is caught by it coming down', () => {
    let p = standing(stack[1], { hold: 0.01, x: 200 })
    // The ascend choice is appended last, so the top of the range picks it.
    p = step(p, stacked, 0.02, seq(0.999))
    expect(p.phase).toBe('anticipate')
    p = step(p, stacked, 0.15, seq(0.5))
    expect(p.locomotion).toBe('fall')
    expect(p.phase).toBe('takeoff')
    expect(p.vy).toBeLessThan(0) // rising
    for (let i = 0; i < 240 && p.locomotion === 'fall'; i++) p = step(p, stacked, 1 / 60, seq(0.5))
    expect(p.ledgeId).toBe('blk:1')
  })
  it('rises past the target before it can be caught, so the arc reads as a jump', () => {
    let p = step(standing(stack[1], { hold: 0.01, x: 200 }), stacked, 0.02, seq(0.999))
    p = step(p, stacked, 0.15, seq(0.5))
    let highest = p.y
    for (let i = 0; i < 240 && p.locomotion === 'fall'; i++) {
      p = step(p, stacked, 1 / 60, seq(0.5))
      highest = Math.min(highest, p.y)
    }
    expect(highest).toBeLessThan(stack[0].y)
  })

  it('is not offered when there is nothing overhead', () => {
    // Only the ascend choice can put an idle pet into a rising fall.
    let p = standing(A, { hold: 0.01, x: 200 })
    p = step(p, env([A]), 0.02, seq(0.999))
    expect(p.vy).toBeGreaterThanOrEqual(0)
  })

  it('is not offered while it is watching a command', () => {
    // Watching keeps the animal where the work is.
    const p = step(
      attend(standing(stack[1], { hold: 0.01, x: 200 }), 'shell'),
      stacked,
      0.02,
      seq(0.999),
    )
    expect(p.vy).toBeGreaterThanOrEqual(0)
  })
})

describe('the jump is aimed, not a fixed leap', () => {
  it('reaches a shelf far above as readily as one just overhead', () => {
    // A single launch speed is either too weak to leave the floor or too
    // strong for a chip. Measured on the real gap: the pane floor to the
    // lowest command block is well over a hundred pixels.
    const far: Ledge[] = [
      { id: 'blk:1', x0: 100, x1: 900, y: 620 },
      { id: 'floor', x0: 8, x1: 900, y: 860 },
    ]
    const envFar: StepEnv = { terrain: far, floor: far[1], petHeight: 34, ...ENV_DEFAULTS }
    let p = standing(far[1], { hold: 0.01, x: 400 })
    p = step(p, envFar, 0.02, seq(0.999))
    expect(p.phase).toBe('anticipate')
    p = step(p, envFar, 0.15, seq(0.5))
    expect(p.locomotion).toBe('fall')
    for (let i = 0; i < 400 && p.locomotion === 'fall'; i++) p = step(p, envFar, 1 / 60, seq(0.5))
    expect(p.ledgeId).toBe('blk:1')
  })

  it('does not launch harder than the shelf it is aiming at', () => {
    const near: Ledge[] = [
      { id: 'blk:1', x0: 100, x1: 900, y: 300 },
      { id: 'blk:2', x0: 100, x1: 900, y: 340 },
    ]
    const envNear: StepEnv = { terrain: near, floor: FLOOR, petHeight: 34, ...ENV_DEFAULTS }
    const far: Ledge[] = [
      { id: 'blk:1', x0: 100, x1: 900, y: 140 },
      { id: 'blk:2', x0: 100, x1: 900, y: 340 },
    ]
    const envFar: StepEnv = { terrain: far, floor: FLOOR, petHeight: 34, ...ENV_DEFAULTS }
    const hop = step(standing(near[1], { hold: 0.01, x: 400 }), envNear, 0.02, seq(0.999))
    const leap = step(standing(far[1], { hold: 0.01, x: 400 }), envFar, 0.02, seq(0.999))
    expect(Math.abs(leap.vy)).toBeGreaterThan(Math.abs(hop.vy) * 1.5)
  })

  it('refuses a shelf beyond its reach rather than jumping short', () => {
    const unreachable: Ledge[] = [
      { id: 'blk:1', x0: 100, x1: 900, y: 100 },
      { id: 'floor', x0: 8, x1: 900, y: 860 },
    ]
    const e: StepEnv = {
      terrain: unreachable,
      floor: unreachable[1],
      petHeight: 34,
      ...ENV_DEFAULTS,
    }
    const p = step(standing(unreachable[1], { hold: 0.01, x: 400 }), e, 0.02, seq(0.999))
    expect(p.vy).toBeGreaterThanOrEqual(0)
  })
})

describe('motion phases', () => {
  it('braces before a jump, then releases into takeoff', () => {
    const stack: Ledge[] = [
      { id: 'lower', x0: 100, x1: 300, y: 200 },
      { id: 'upper', x0: 100, x1: 300, y: 260 },
    ]
    const e: StepEnv = { terrain: stack, floor: FLOOR, petHeight: 34, ...ENV_DEFAULTS }
    let p = standing(stack[1], { x: 200, hold: 0.01 })

    p = step(p, e, 0.02, seq(0.999))
    expect(p.phase).toBe('anticipate')
    expect(p.phaseHold).toBeGreaterThanOrEqual(0.12)
    expect(p.phaseHold).toBeLessThanOrEqual(0.18)
    expect(p.locomotion).toBe('idle')
    expect(p.vx).toBe(0)
    expect(p.x).toBe(200)

    p = step(p, e, 0.15, seq(0.5))
    expect(p.phase).toBe('takeoff')
    expect(p.phaseHold).toBeGreaterThanOrEqual(0.07)
    expect(p.phaseHold).toBeLessThanOrEqual(0.1)
    expect(p.locomotion).toBe('fall')
    expect(p.ledgeId).toBeNull()
    expect(p.vy).toBeLessThan(0)
  })

  it('holds the pet in anticipation instead of moving it horizontally', () => {
    const stack: Ledge[] = [
      { id: 'lower', x0: 100, x1: 300, y: 200 },
      { id: 'upper', x0: 100, x1: 300, y: 260 },
    ]
    const e: StepEnv = { terrain: stack, floor: FLOOR, petHeight: 34, ...ENV_DEFAULTS }
    const p = step(
      step(standing(stack[1], { x: 200, vx: 32, hold: 0.01 }), e, 0.02, seq(0.999)),
      e,
      0.05,
      seq(0.5),
    )
    expect(p.phase).toBe('anticipate')
    expect(p.x).toBe(200)
    expect(p.vx).toBe(0)
  })

  it('pauses at an edge and turns halfway through the pause', () => {
    let p = standing(A, { locomotion: 'walk', dir: 1, x: A.x1 - 1, hold: 100 })
    p = step(p, env(), 0.5, seq(0.5))
    expect(p.phase).toBe('turn')
    expect(p.dir).toBe(1)
    expect(p.phaseHold).toBeGreaterThan(0.05)

    p = step(p, env(), 0.06, seq(0.5))
    expect(p.phase).toBe('turn')
    expect(p.dir).toBe(-1)

    p = step(p, env(), 0.06, seq(0.5))
    expect(p.phase).toBe('none')
    expect(p.dir).toBe(-1)
  })

  it('lands through a scaled compression and bounce phase', () => {
    const falling: Pet = {
      ...newPet(200, 100),
      locomotion: 'fall',
      vy: 500,
    }
    const p = step(falling, env(), 0.2, seq(0.5))
    expect(p.phase).toBe('land')
    expect(p.phaseHold).toBeGreaterThanOrEqual(0.15)
    expect(p.phaseHold).toBeLessThanOrEqual(0.22)
    expect(p.locomotion).toBe('idle')
    expect(p.ledgeId).toBe(A.id)
  })

  it('follows a ledge that moves during landing', () => {
    const landed = step({ ...newPet(200, 100), locomotion: 'fall', vy: 500 }, env(), 0.2, seq(0.5))
    const moved: Ledge = { ...A, y: A.y - 40 }
    const after = step(landed, env([moved]), 1 / 60, seq(0.5))
    expect(after.phase).toBe('land')
    expect(after.y).toBe(moved.y)
  })

  it('falls immediately when the ledge vanishes during a turn', () => {
    const turning = step(
      standing(A, { locomotion: 'walk', x: A.x1 - 1, dir: 1, hold: 100 }),
      env(),
      0.5,
      seq(0.5),
    )
    const after = step(turning, env([B]), 1 / 60, seq(0.5))
    expect(after.phase).toBe('none')
    expect(after.locomotion).toBe('fall')
    expect(after.y).toBeGreaterThan(A.y)
  })

  it('never gives a motion phase more than 220 milliseconds', () => {
    let p = standing(A, { locomotion: 'walk', dir: 1, x: A.x1 - 1, hold: 100 })
    p = step(p, env(), 0.5, seq(0.5))
    expect(p.phaseHold).toBeLessThanOrEqual(0.22)
    const landed = step({ ...newPet(200, 100), locomotion: 'fall', vy: 900 }, env(), 0.2, seq(0.5))
    expect(landed.phaseHold).toBeLessThanOrEqual(0.22)
  })
})

describe('airborne and ordinary-choice policy', () => {
  const wide: Ledge = { id: 'wide', x0: -1e6, x1: 1e6, y: 200 }
  const wideEnv = env([wide])

  it('descends without the jump anticipation phase', () => {
    const after = step(standing(A, { x: 200, hold: 0.01 }), env(), 0.02, seq(0.3))
    expect(after.locomotion).toBe('fall')
    expect(after.phase).toBe('none')
  })

  it('turns at an exhausted budget instead of falling off the ledge', () => {
    const edge: Ledge = { id: 'edge', x0: 100, x1: 300, y: 200 }
    const p = standing(edge, {
      locomotion: 'walk',
      dir: 1,
      x: edge.x1 - 1,
      hold: 100,
      noticeableAges: [0, 1],
    })
    const after = step(p, env([edge]), 0.5, seq(0.99))
    expect(after.locomotion).toBe('walk')
    expect(after.phase).toBe('turn')
  })

  it('never repeats a non-exempt occupation across many seeded choices', () => {
    const exempt = new Set(['sit', 'lie', 'sleep'])
    let seed = 0x12345678
    const rng = () => {
      seed = (1664525 * seed + 1013904223) >>> 0
      return seed / 0x100000000
    }
    let p = standing(wide, { hold: 0 })
    for (let i = 0; i < 5000; i++) {
      p = step(p, wideEnv, 0.01, rng)
      const [latest, previous] = p.recentBehaviors
      if (latest !== undefined && latest === previous && !exempt.has(latest)) {
        throw new Error(`repeated occupation: ${latest}`)
      }
      p = {
        ...p,
        x: 0,
        y: wide.y,
        ledgeId: wide.id,
        locomotion: 'idle',
        activity: 'sit',
        hold: 0,
        vx: 0,
        vy: 0,
        phase: 'none',
        phaseHold: 0,
        phaseDuration: 0,
        phaseTurned: false,
      }
    }
  })

  it('keeps noticeable non-watching events within two per rolling minute', () => {
    let seed = 0xabcdef01
    const rng = () => {
      seed = (1103515245 * seed + 12345) >>> 0
      return seed / 0x100000000
    }
    let p = standing(wide, { hold: 0, mood: 'pleased', moodHold: 1000 })
    const samples: number[] = []
    for (let i = 0; i < 7200; i++) {
      p = step(p, wideEnv, 0.1, rng)
      samples.push(p.noticeableAges.length)
      expect(p.noticeableAges.length).toBeLessThanOrEqual(2)
      p = {
        ...p,
        x: 0,
        y: wide.y,
        ledgeId: wide.id,
        locomotion: 'idle',
        activity: 'sit',
        hold: 0,
        vx: 0,
        vy: 0,
        phase: 'none',
        phaseHold: 0,
        phaseDuration: 0,
        phaseTurned: false,
      }
    }
    const ordered = [...samples].sort((a, b) => a - b)
    expect(ordered[Math.floor(ordered.length * 0.95)]).toBeLessThanOrEqual(2)
    expect(Math.max(...samples)).toBeGreaterThan(0)
  })

  it('preserves mood signatures while damping remembered choices', () => {
    const draw = (mood: 'calm' | 'worried', recentBehaviors: readonly string[]) => {
      let seed = 17
      const counts = new Map<string, number>()
      for (let i = 0; i < 800; i++) {
        seed = (1664525 * seed + 1013904223) >>> 0
        const r = () => seed / 0x100000000
        const p = step(
          standing(wide, {
            hold: 0,
            mood,
            moodHold: 1000,
            recentBehaviors,
          }),
          wideEnv,
          0.01,
          r,
        )
        const key = p.recentBehaviors[0] ?? 'none'
        counts.set(key, (counts.get(key) ?? 0) + 1)
      }
      return counts
    }
    const calm = draw('calm', [])
    const worried = draw('worried', [])
    const dampedWorried = draw('worried', ['walk', 'scratch'])
    const share = (counts: Map<string, number>, keys: readonly string[]) =>
      keys.reduce((sum, key) => sum + (counts.get(key) ?? 0), 0) / 800
    const worriedSignature = share(worried, ['sit', 'lie', 'scratch'])
    const dampedSignature = share(dampedWorried, ['sit', 'lie', 'scratch'])
    expect(dampedSignature).toBeGreaterThan(worriedSignature * 0.8)
    expect(dampedSignature).toBeGreaterThan(share(calm, ['sit', 'lie', 'groom', 'stretch']))
  })

  it('does not spend memory or budget on the observing menu', () => {
    let p = attend(standing(wide, { hold: 0 }), 'shell')
    for (let i = 0; i < 30; i++) {
      p = step(p, wideEnv, 1, seq(0))
      p = { ...p, hold: 0 }
    }
    expect(p.recentBehaviors).toEqual([])
    expect(p.noticeableAges).toEqual([])
  })
})
