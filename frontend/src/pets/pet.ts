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
import { ledgeAbove, ledgeById, ledgeCrossed } from './terrain'

export type Locomotion = 'idle' | 'walk' | 'run' | 'fall'
type Mood = 'calm' | 'pleased' | 'worried' | 'tired'
export type Activity = 'none' | 'sit' | 'groom' | 'stretch' | 'lie' | 'scratch' | 'meow' | 'sleep'

/** How a finished command reaches the pet.
 *
 *  The block's own three settled states, no more: 'entered' (an environment
 *  opened) and an abandoned attempt both arrive as 'unknown', because neither
 *  is a verdict on the command. A fourth value for "interrupted" was written
 *  first and removed — no freeze path produces one, and a branch nothing can
 *  reach is a claim the tests cannot check. */
export type Outcome = 'success' | 'failure' | 'unknown'

/** Who ran the command.
 *
 *  Mirrors CommandAuthor without importing it, for the reason Outcome gives:
 *  the pet must not depend on the ledger's module (AD-8). The author is
 *  minted at submit and never derived afterwards, so the animal is reacting
 *  to a fact rather than to a guess about whose work it watched. */
export type Author = 'shell' | 'agent'

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
  /** Whether the current activity is an ANSWER to a finished command rather
   *  than something the animal chose. Only these are protected from being cut
   *  short: an ordinary occupation may be interrupted freely. */
  readonly reacting: boolean
  /** Whose command is running right now, or null when nothing is.
   *
   *  Not a fourth axis and not a mood: it does not colour the animal, it
   *  narrows what the animal is willing to do. A pet that wandered off mid
   *  build is a pet that is not watching, and watching is the whole reason it
   *  lives in a terminal rather than on a desktop. */
  readonly attending: Author | null
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
  /** How far up the animal can reach, in pixels.
   *
   *  A DISTANCE rather than a launch speed, because the jump is aimed: the
   *  speed is computed from the ledge it is jumping to, so a shelf just
   *  overhead gets a hop and one two blocks up gets a leap. A single launch
   *  speed makes every jump the same height, which is both uglier and
   *  either too weak to leave the floor or too strong for a chip. */
  readonly jumpReach: number
  /** How close the pointer may come before the animal moves away, in pixels.
   *
   *  A cat that lets you put the cursor through it is a sticker. This is the
   *  one way a person touches the pet at all — the layer takes no clicks by
   *  design — so it is also what makes it feel present rather than painted
   *  on. */
  readonly fleeRadius: number
  /** Chance, at the end of a ledge, of stepping off it instead of turning
   *  round. This is what makes the terminal a landscape rather than a set of
   *  separate shelves: without it the animal walks one edge for ever, and a
   *  pet that never explores reads as a decoration stuck to the screen. */
  readonly stepOff: number
}

export const DEFAULT_TUNING: PetTuning = {
  walkSpeed: 26,
  runSpeed: 78,
  gravity: 900,
  maxFall: 900,
  sleepAfter: 45,
  reactionHold: 1.4,
  moodHold: 12,
  stepOff: 0.4,
  fleeRadius: 70,
  jumpReach: 240,
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
    reacting: false,
    attending: null,
  }
}

// ── Reacting to a command ──────────────────────────────────────────────────

const MOOD_OF: Record<Outcome, Mood> = {
  success: 'pleased',
  failure: 'worried',
  unknown: 'calm',
}

/** What the animal does about an outcome, per author.
 *
 *  The two lanes read differently on purpose. Your command is addressed to
 *  the cat as much as to the shell — it meows back. The assistant's is work
 *  it merely watched, so the answer is quieter: a stretch for a good one, and
 *  lying down rather than fretting for a bad one, because an agent failing is
 *  not the person failing and a pet that scolded them for it would be wrong
 *  about who did what. */
const REACTION_OF: Record<Author, Record<Outcome, Activity>> = {
  shell: { success: 'meow', failure: 'scratch', unknown: 'sit' },
  agent: { success: 'stretch', failure: 'lie', unknown: 'sit' },
}

/**
 * A command finished. The pet stops what it was doing and reacts.
 *
 * A falling pet is NOT interrupted: it has no say in the matter, and a cat
 * that grooms itself mid-air is the kind of detail that reads as a bug.
 */
export function react(
  pet: Pet,
  outcome: Outcome,
  author: Author = 'shell',
  tuning: PetTuning = DEFAULT_TUNING,
): Pet {
  const mood = MOOD_OF[outcome]
  // Whatever it was watching is over, whether or not it may react.
  const done = { ...pet, attending: null }
  if (pet.locomotion === 'fall') {
    return { ...done, mood, moodHold: tuning.moodHold }
  }
  return {
    ...done,
    locomotion: 'idle',
    mood,
    moodHold: tuning.moodHold,
    activity: REACTION_OF[author][outcome],
    hold: tuning.reactionHold,
    reacting: true,
    boredom: 0,
  }
}

/**
 * A command STARTED. The animal settles down to watch it.
 *
 * Separate from `react` because it is not a verdict: nothing has happened
 * yet. Until now the pet learned only about endings, so during the minute a
 * build takes — the minute you are actually looking at the terminal — it
 * wandered about as though nothing were going on.
 */
export function attend(pet: Pet, author: Author, tuning: PetTuning = DEFAULT_TUNING): Pet {
  if (pet.locomotion === 'fall') return { ...pet, attending: author }
  // A reaction still playing is left to finish. The previous command's
  // verdict routinely lands AFTER the next command has started — the freeze
  // waits on a render fence, the start does not — so cutting it off here
  // would mean the answer to what you just ran is swallowed by the thing you
  // ran next. The attending menu takes over at the next choice either way.
  if (pet.reacting && pet.hold > 0) return { ...pet, attending: author, boredom: 0 }
  return {
    ...pet,
    attending: author,
    locomotion: 'idle',
    // Your command it lies down for; the assistant's it sits up and watches.
    activity: author === 'agent' ? 'sit' : 'lie',
    hold: tuning.reactionHold,
    reacting: false,
    boredom: 0,
  }
}

// ── Choosing what to do next ───────────────────────────────────────────────

interface Choice {
  readonly locomotion: Locomotion
  readonly activity: Activity
  readonly hold: number
  readonly weight: number
  /** Jump to the ledge above, when there is one within reach. */
  readonly ascend?: boolean
  /** Leave this ledge where it stands and drop to whatever is under it.
   *
   *  Stepping off the END is not enough on its own: a command block is the
   *  full width of the pane, and at a walking pace an animal needs the better
   *  part of a minute to reach an edge. Without a way to leave from the
   *  middle it stays on the ledge it landed on for as long as anyone watches,
   *  which is exactly the "decoration stuck to the screen" this feature is
   *  trying not to be. */
  readonly descend?: boolean
}

/** The menu, per mood. A pleased cat shows off; a worried one fidgets and
 *  keeps still; a tired one has already stopped caring.
 *
 *  Order matters to the tests and so it is stated: the moving choices come
 *  first and the still ones last, so "pin the rng high" means "keep still"
 *  in every mood rather than whichever entry happens to be at the bottom. */
/** The one choice that is offered only when the terrain allows it. Weighted
 *  above descending, because a pet that sinks faster than it climbs ends on
 *  the floor whatever the numbers say. */
const ASCEND: Choice = {
  locomotion: 'walk',
  activity: 'none',
  hold: 1,
  weight: 26,
  ascend: true,
}

function menu(mood: Mood, attending: Author | null): readonly Choice[] {
  // Watching narrows the menu rather than replacing the mood. It keeps the
  // animal in place and near the work: no descending to another block, no
  // running off, no falling asleep on the job. It is still the same creature
  // — a worried cat watching a build still fidgets — which is why this is a
  // filter over the mood's menu and not a fifth menu of its own.
  if (attending !== null) {
    return attending === 'agent'
      ? [
          { locomotion: 'idle', activity: 'sit', hold: 4, weight: 50 },
          { locomotion: 'idle', activity: 'groom', hold: 3, weight: 18 },
          { locomotion: 'walk', activity: 'none', hold: 1.6, weight: 18 },
          { locomotion: 'idle', activity: 'lie', hold: 4, weight: 14 },
        ]
      : [
          { locomotion: 'idle', activity: 'lie', hold: 5, weight: 46 },
          { locomotion: 'idle', activity: 'sit', hold: 4, weight: 22 },
          { locomotion: 'idle', activity: 'groom', hold: 3, weight: 18 },
          { locomotion: 'walk', activity: 'none', hold: 1.6, weight: 14 },
        ]
  }
  return moodMenu(mood)
}

function moodMenu(mood: Mood): readonly Choice[] {
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
        { locomotion: 'walk', activity: 'none', hold: 1, weight: 14, descend: true },
        { locomotion: 'run', activity: 'none', hold: 2, weight: 14 },
        { locomotion: 'idle', activity: 'stretch', hold: 2.4, weight: 20 },
        { locomotion: 'idle', activity: 'groom', hold: 3, weight: 16 },
        { locomotion: 'idle', activity: 'sit', hold: 3, weight: 16 },
      ]
    case 'worried':
      return [
        walk(18),
        { locomotion: 'walk', activity: 'none', hold: 1, weight: 6, descend: true },
        { locomotion: 'idle', activity: 'sit', hold: 4, weight: 34 },
        { locomotion: 'idle', activity: 'scratch', hold: 2, weight: 22 },
        { locomotion: 'idle', activity: 'lie', hold: 4, weight: 26 },
      ]
    case 'tired':
      return [
        walk(8),
        { locomotion: 'walk', activity: 'none', hold: 1, weight: 4, descend: true },
        { locomotion: 'idle', activity: 'lie', hold: 6, weight: 40 },
        { locomotion: 'idle', activity: 'sit', hold: 5, weight: 30 },
        { locomotion: 'idle', activity: 'groom', hold: 3, weight: 22 },
      ]
    default:
      return [
        walk(30),
        { locomotion: 'walk', activity: 'none', hold: 1, weight: 12, descend: true },
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
  /** How wide the animal is drawn. The flee test is against its BOX plus a
   *  margin, not against a radius round its position: at 96px tall the cat is
   *  180px wide, so a cursor visibly on top of it is ninety pixels from the
   *  point a radius would measure — and it would sit there unbothered. */
  readonly petWidth?: number
  /** Where the pointer is, in the same coordinates as the terrain, or null
   *  when it is not over this pane at all. */
  readonly pointer?: { readonly x: number; readonly y: number } | null
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
  let reacting: boolean = next.reacting

  // 2. Mood decays. It is a colour on the behaviour, not a state to be stuck in.
  let { mood, moodHold } = next
  moodHold = Math.max(0, moodHold - dt)
  if (moodHold === 0 && mood !== 'tired') mood = 'calm'

  // 3. The pointer. Close enough and the animal stops whatever it was doing
  //    and moves away — the only interaction the pet has, since the layer
  //    deliberately takes no clicks.
  const threat = nearPointer(env, x, y, tuning)
  if (threat !== 0) {
    locomotion = 'run'
    activity = 'none'
    reacting = false
    hold = Math.max(hold, 0.5)
    dir = threat
    boredom = 0
  }

  // 4. Move, and turn round at the end of the world rather than falling off it.
  if (locomotion === 'walk' || locomotion === 'run') {
    const speed = locomotion === 'run' ? tuning.runSpeed : tuning.walkSpeed
    x += dir * speed * dt
    const past = x <= ledge.x0 ? -1 : x >= ledge.x1 ? 1 : 0
    if (past !== 0) {
      // Cornered by the pointer: leave, rather than turn round into it.
      if (threat === past || rng() < tuning.stepOff) {
        // Off the end, and whatever is below catches it. The landing is the
        // ordinary swept one, so the animal may drop past several ledges or
        // all the way to the floor — which is the point: this is how it gets
        // from the chip it was on to the block two commands down.
        //
        // It leaves AT the edge, not one pixel past it. Command blocks are
        // all the same width, so a step that overshot by a pixel would miss
        // every block below by the same pixel and land on the floor every
        // single time — the animal would live on the floor and visit the
        // blocks only on the way down.
        return {
          ...next,
          x: past > 0 ? ledge.x1 : ledge.x0,
          y,
          dir: past > 0 ? 1 : -1,
          ledgeId: null,
          locomotion: 'fall',
          activity: 'none',
          hold: 0,
          vy: 0,
          mood,
          moodHold,
          boredom: 0,
        }
      }
      x = past > 0 ? ledge.x1 : ledge.x0
      dir = past > 0 ? -1 : 1
    }
    boredom = 0
  } else {
    boredom += dt
  }

  // 5. Long enough with nothing to do and the cat goes to sleep. This is the
  //    one activity that is not chosen from the menu — it is arrived at.
  // Watching keeps it awake: a pet that dozed off during your build would be
  // reporting the opposite of what is happening.
  if (next.attending === null && boredom >= tuning.sleepAfter && activity !== 'sleep') {
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

  // 6. Finish the current occupation before starting another one. Deciding
  //    afresh every frame is what makes a pet twitch instead of live.
  hold -= dt
  if (hold <= 0) {
    const up = next.attending === null ? ledgeAbove(env.terrain, x, y, tuning.jumpReach) : null
    const choices =
      up === null ? menu(mood, next.attending) : [...menu(mood, next.attending), ASCEND]
    const c = pick(choices, rng())
    if (c.ascend === true && up !== null) {
      return {
        ...next,
        x,
        y,
        dir,
        ledgeId: null,
        locomotion: 'fall',
        activity: 'none',
        hold: 0,
        reacting: false,
        // Aimed at the shelf, with a little to spare so it clears the edge
        // and is caught on the way down rather than grazing it.
        vy: -Math.sqrt(2 * tuning.gravity * (y - up.y + JUMP_MARGIN)),
        mood,
        moodHold,
        boredom: 0,
      }
    }
    if (c.descend === true) {
      return {
        ...next,
        x,
        y,
        dir,
        ledgeId: null,
        locomotion: 'fall',
        activity: 'none',
        hold: 0,
        vy: 0,
        mood,
        moodHold,
        boredom: 0,
      }
    }
    locomotion = c.locomotion
    activity = c.activity
    hold = c.hold
    reacting = false
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
    reacting,
    mood,
    moodHold,
    boredom,
    ledgeId: ledge.id,
  }
}

/**
 * Which way to run from the pointer, or 0 when it is not a threat.
 *
 * The animal's BOX plus a margin, not a radius round its position. Two
 * reasons, both learned by watching a cat ignore a cursor sitting on it:
 * `y` is where it STANDS, so its body is the `petHeight` above that; and a
 * 96px cat is 180px wide, so its own edges are further from its centre than
 * any sensible radius.
 */
function nearPointer(env: StepEnv, x: number, y: number, tuning: PetTuning): 0 | 1 | -1 {
  const p = env.pointer
  if (!p) return 0
  const halfW = (env.petWidth ?? env.petHeight) / 2
  const dx = x - p.x
  const dy = y - env.petHeight / 2 - p.y
  if (Math.abs(dx) > halfW + tuning.fleeRadius) return 0
  if (Math.abs(dy) > env.petHeight / 2 + tuning.fleeRadius) return 0
  // Directly on top of it: pick a side rather than freezing.
  return dx >= 0 ? 1 : -1
}

/** Extra rise above the target, so the arc peaks over the shelf and the
 *  animal is caught coming down instead of grazing the edge. */
const JUMP_MARGIN = 16

function fall(pet: Pet, env: StepEnv, dt: number, tuning: PetTuning): Pet {
  // Negative vy is a jump on its way up. Nothing is caught while rising —
  // `ledgeCrossed` only considers a descending segment — so the animal sails
  // past the shelf it is aiming for and is caught by it coming back down,
  // which is also what makes the arc read as a jump rather than a lift.
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
