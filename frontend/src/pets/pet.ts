// What the pet is doing, and why (nocx-q4qeh.1).
//
// Three axes, deliberately separate:
//
//   locomotion — where the body is: idle, walking, running, falling.
//   mood       — how the last command went. Decays back to calm.
//   activity   — what it is busy with when not going anywhere.
//
// Folding them into one enum is the obvious move and it is wrong: it
// multiplies out into happy-walk, sad-walk, happy-climb and so on, and every
// new mood doubles the machine. Here a mood only shifts the WEIGHTS of the
// activities and the speed of the walk, so adding one costs a row in a table.
//
// Everything below is pure. Time arrives as `dt`, chance arrives as `rng`;
// there is no clock and no Math.random, so a test can run a thousand seconds
// of pet in a millisecond and get the same answer every time.

import type { Ledge } from './terrain'
import { ledgeById, ledgeCrossed } from './terrain'

export type Locomotion = 'idle' | 'walk' | 'run' | 'fall'
type Mood = 'calm' | 'pleased' | 'worried' | 'tired'
export type Activity = 'none' | 'sit' | 'groom' | 'stretch' | 'lie' | 'scratch' | 'meow' | 'sleep'

/** How a finished command reaches the pet. Mirrors CommandStatus without
 *  importing it: the pet must not depend on the ledger's module (AD-8), and
 *  'running' has no meaning here — a pet reacts to outcomes, not to starts. */
export type Outcome = 'success' | 'failure' | 'interrupted' | 'unknown'

export interface Pet {
  /** The ledge underfoot, by identity. Null while falling. */
  readonly ledgeId: string | null
  readonly x: number
  readonly y: number
  readonly dir: 1 | -1
  readonly locomotion: Locomotion
  readonly mood: Mood
  readonly activity: Activity
  /** Seconds left of the current occupation. */
  readonly hold: number
  /** Seconds left before the mood decays to calm. */
  readonly moodHold: number
  /** Seconds spent doing nothing in particular — the road to sleep. */
  readonly boredom: number
  /** Downward speed, px/s. Only meaningful while falling. */
  readonly vy: number
}

export interface PetTuning {
  readonly walkSpeed: number
  readonly runSpeed: number
  readonly gravity: number
  readonly maxFall: number
  /** Doing nothing for this long sends the pet to sleep. */
  readonly sleepAfter: number
  /** How long a reaction to a command holds the pet's attention. */
  readonly reactionHold: number
  /** How long a mood lasts before it decays back to calm. */
  readonly moodHold: number
}

export const DEFAULT_TUNING: PetTuning = {
  walkSpeed: 26,
  runSpeed: 78,
  gravity: 900,
  maxFall: 900,
  sleepAfter: 45,
  reactionHold: 1.4,
  moodHold: 12,
}

export function newPet(x: number, y: number): Pet {
  return {
    ledgeId: null,
    x,
    y,
    dir: 1,
    locomotion: 'fall',
    mood: 'calm',
    activity: 'none',
    hold: 0,
    moodHold: 0,
    boredom: 0,
    vy: 0,
  }
}

// ── Reacting to a command ──────────────────────────────────────────────────

const MOOD_OF: Record<Outcome, Mood> = {
  success: 'pleased',
  failure: 'worried',
  interrupted: 'worried',
  unknown: 'calm',
}

const REACTION_OF: Record<Outcome, Activity> = {
  success: 'meow',
  failure: 'scratch',
  interrupted: 'sit',
  unknown: 'sit',
}

/**
 * A command finished. The pet stops what it was doing and reacts.
 *
 * A falling pet is NOT interrupted: it has no say in the matter, and a cat
 * that grooms itself mid-air is the kind of detail that reads as a bug.
 */
export function react(pet: Pet, outcome: Outcome, tuning: PetTuning = DEFAULT_TUNING): Pet {
  const mood = MOOD_OF[outcome]
  if (pet.locomotion === 'fall') {
    return { ...pet, mood, moodHold: tuning.moodHold }
  }
  return {
    ...pet,
    locomotion: 'idle',
    mood,
    moodHold: tuning.moodHold,
    activity: REACTION_OF[outcome],
    hold: tuning.reactionHold,
    boredom: 0,
  }
}

// ── Choosing what to do next ───────────────────────────────────────────────

interface Choice {
  readonly locomotion: Locomotion
  readonly activity: Activity
  readonly hold: number
  readonly weight: number
}

/** The menu, per mood. A pleased cat shows off; a worried one fidgets and
 *  keeps still; a tired one has already stopped caring. */
function menu(mood: Mood): readonly Choice[] {
  const walk = (w: number): Choice => ({
    locomotion: 'walk',
    activity: 'none',
    hold: 4,
    weight: w,
  })
  switch (mood) {
    case 'pleased':
      return [
        walk(34),
        { locomotion: 'run', activity: 'none', hold: 2, weight: 14 },
        { locomotion: 'idle', activity: 'stretch', hold: 2.4, weight: 20 },
        { locomotion: 'idle', activity: 'groom', hold: 3, weight: 16 },
        { locomotion: 'idle', activity: 'sit', hold: 3, weight: 16 },
      ]
    case 'worried':
      return [
        walk(18),
        { locomotion: 'idle', activity: 'sit', hold: 4, weight: 34 },
        { locomotion: 'idle', activity: 'scratch', hold: 2, weight: 22 },
        { locomotion: 'idle', activity: 'lie', hold: 4, weight: 26 },
      ]
    case 'tired':
      return [
        walk(8),
        { locomotion: 'idle', activity: 'lie', hold: 6, weight: 40 },
        { locomotion: 'idle', activity: 'sit', hold: 5, weight: 30 },
        { locomotion: 'idle', activity: 'groom', hold: 3, weight: 22 },
      ]
    default:
      return [
        walk(30),
        { locomotion: 'idle', activity: 'sit', hold: 4, weight: 24 },
        { locomotion: 'idle', activity: 'groom', hold: 3, weight: 20 },
        { locomotion: 'idle', activity: 'lie', hold: 5, weight: 14 },
        { locomotion: 'idle', activity: 'stretch', hold: 2.4, weight: 12 },
      ]
  }
}

function pick(choices: readonly Choice[], r: number): Choice {
  const total = choices.reduce((s, c) => s + c.weight, 0)
  let acc = 0
  const target = r * total
  for (const c of choices) {
    acc += c.weight
    if (target < acc) return c
  }
  return choices[choices.length - 1]
}

// ── The step ───────────────────────────────────────────────────────────────

/** The bottom of the pane is ground too, and the pet must be able to STAND
 *  there — otherwise a terminal with no frozen blocks has nowhere to put it.
 *  It is modelled as a ledge with a reserved id rather than as a special
 *  case, so every rule below has exactly one kind of ground to reason about.
 *  Block ledges are minted as `blk:<n>`, so the name cannot collide. */
export const FLOOR_ID = 'floor'

export interface StepEnv {
  readonly terrain: readonly Ledge[]
  /** The pane's own bottom edge, as ground. */
  readonly floor: Ledge
  readonly petHeight: number
}

/** Ground by identity, floor included. */
function groundOf(env: StepEnv, id: string | null): Ledge | null {
  if (id === FLOOR_ID) return env.floor
  return ledgeById(env.terrain, id)
}

/**
 * Advance the pet by `dt` seconds. `rng` returns [0,1).
 *
 * The order matters: ground is resolved BEFORE anything is decided, because
 * a pet whose ledge just vanished has no business choosing to groom itself.
 */
export function step(
  pet: Pet,
  env: StepEnv,
  dt: number,
  rng: () => number,
  tuning: PetTuning = DEFAULT_TUNING,
): Pet {
  let next = pet

  // 1. Is there still ground? A block removed under the pet drops it — it is
  //    never quietly moved to another ledge, which would read as a teleport.
  const standing = groundOf(env, next.ledgeId)
  if (next.locomotion !== 'fall' && standing === null) {
    next = { ...next, locomotion: 'fall', ledgeId: null, vy: 0, activity: 'none', hold: 0 }
  }

  if (next.locomotion === 'fall') return fall(next, env, dt, tuning)

  const ledge = standing as Ledge
  // A ledge that moved (the view scrolled) carries the pet with it.
  const y = ledge.y
  let x = Math.min(Math.max(next.x, ledge.x0), ledge.x1)
  let dir = next.dir
  let hold: number = next.hold
  let activity: Activity = next.activity
  let locomotion: Locomotion = next.locomotion
  let boredom: number = next.boredom

  // 2. Mood decays. It is a colour on the behaviour, not a state to be stuck in.
  let { mood, moodHold } = next
  moodHold = Math.max(0, moodHold - dt)
  if (moodHold === 0 && mood !== 'tired') mood = 'calm'

  // 3. Move, and turn round at the end of the world rather than falling off it.
  if (locomotion === 'walk' || locomotion === 'run') {
    const speed = locomotion === 'run' ? tuning.runSpeed : tuning.walkSpeed
    x += dir * speed * dt
    if (x <= ledge.x0) {
      x = ledge.x0
      dir = 1
    } else if (x >= ledge.x1) {
      x = ledge.x1
      dir = -1
    }
    boredom = 0
  } else {
    boredom += dt
  }

  // 4. Long enough with nothing to do and the cat goes to sleep. This is the
  //    one activity that is not chosen from the menu — it is arrived at.
  if (boredom >= tuning.sleepAfter && activity !== 'sleep') {
    return {
      ...next,
      x,
      y,
      dir,
      mood: 'tired',
      moodHold: 0,
      locomotion: 'idle',
      activity: 'sleep',
      hold: 0,
      boredom,
      ledgeId: ledge.id,
    }
  }
  if (activity === 'sleep') {
    return { ...next, x, y, dir, mood: 'tired', moodHold: 0, boredom, ledgeId: ledge.id }
  }

  // 5. Finish the current occupation before starting another one. Deciding
  //    afresh every frame is what makes a pet twitch instead of live.
  hold -= dt
  if (hold <= 0) {
    const c = pick(menu(mood), rng())
    locomotion = c.locomotion
    activity = c.activity
    hold = c.hold
    if (c.locomotion !== 'idle' && rng() < 0.5) dir = (dir * -1) as 1 | -1
  }

  return {
    ...next,
    x,
    y,
    dir,
    locomotion,
    activity,
    hold,
    mood,
    moodHold,
    boredom,
    ledgeId: ledge.id,
  }
}

function fall(pet: Pet, env: StepEnv, dt: number, tuning: PetTuning): Pet {
  const vy = Math.min(tuning.maxFall, pet.vy + tuning.gravity * dt)
  const from = pet.y
  const to = from + vy * dt
  const landed = ledgeCrossed(env.terrain, pet.x, from, to)
  if (landed) {
    return {
      ...pet,
      ledgeId: landed.id,
      x: Math.min(Math.max(pet.x, landed.x0), landed.x1),
      y: landed.y,
      vy: 0,
      locomotion: 'idle',
      activity: 'sit',
      hold: 1.2,
      boredom: 0,
    }
  }
  if (to >= env.floor.y) {
    return {
      ...pet,
      ledgeId: FLOOR_ID,
      x: Math.min(Math.max(pet.x, env.floor.x0), env.floor.x1),
      y: env.floor.y,
      vy: 0,
      locomotion: 'idle',
      activity: 'sit',
      hold: 1.2,
      boredom: 0,
    }
  }
  return { ...pet, y: to, vy }
}

/** True while the pet is standing on the pane's own bottom edge rather than
 *  on a command block. */
export function onFloor(pet: Pet): boolean {
  return pet.ledgeId === FLOOR_ID
}
