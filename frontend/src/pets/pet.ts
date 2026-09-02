// What the pet is doing, and why (nocx-q4qeh.1).
//
// Four axes, deliberately separate:
//
//   locomotion — where the body is: idle, walking, running, falling.
//   mood       — how the last command went. Decays back to calm.
//   activity   — what it is busy with when not going anywhere.
//   phase      — the short physical transition between settled behaviours:
//                bracing, takeoff, landing or turning.
//
// Folding them into one enum is the obvious move and it is wrong: it
// multiplies out into happy-walk, sad-walk, happy-climb and so on, and every
// new mood doubles the machine. A phase is similarly orthogonal: it changes
// how the body gets from one state to another without changing what the
// animal is doing, so collapsing it would multiply every locomotion and mood.
//
// Everything below is pure. Time arrives as `dt`, chance arrives as `rng`;
// there is no clock and no Math.random, so a test can run a thousand seconds
// of pet in a millisecond and get the same answer every time.

import type { Ledge } from './terrain'
import { ledgeAbove, ledgeById, ledgeCrossed } from './terrain'

type ClipPause = number | readonly [number, number]
export type ClipMode = 'loop' | 'once' | 'hold'

/** Timing data copied from the loaded drawing, not a pack dependency. */
interface ClipTiming {
  readonly mode: ClipMode
  readonly duration: number
  readonly pause: ClipPause
}
/**
 * The pure state machine receives this as data. Passing the logical state
 * maps keeps `pet.ts` independent of pack loading; passing a pack or a
 * clip-selector function would make this module own rendering policy.
 */
export interface PetTiming {
  readonly fps: number
  readonly locomotion: Readonly<Record<Locomotion, ClipTiming>>
  readonly activity: Readonly<Record<Exclude<Activity, 'none'>, ClipTiming>>
  readonly strides: Readonly<{ walk: number; run: number }>
}

export type Locomotion = 'idle' | 'walk' | 'run' | 'fall'
type MotionPhase = 'none' | 'anticipate' | 'takeoff' | 'land' | 'turn'
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
  /** Signed horizontal speed, px/s; direction changes brake through zero. */
  readonly vx: number
  readonly locomotion: Locomotion
  readonly mood: Mood
  /** The two most recent chosen occupations, newest first. */
  readonly recentBehaviors: readonly string[]
  /** Ages of noticeable autonomous events inside the rolling minute. */
  readonly noticeableAges: readonly number[]
  readonly activity: Activity
  /** Seconds left of the current occupation. */
  readonly hold: number
  /** The short transition layered over the ordinary behaviour axes. */
  readonly phase: MotionPhase
  /** Seconds left in the current motion phase. */
  readonly phaseHold: number
  /** Total duration of the current phase, for progress-based painting. */
  readonly phaseDuration: number
  /** Whether a turn has already crossed its midpoint. */
  readonly phaseTurned: boolean
  /** Seconds left before the mood decays to calm. */
  readonly moodHold: number
  /** Seconds spent doing nothing in particular — the road to sleep. */
  readonly boredom: number
  /** Downward speed, px/s. Only meaningful while falling. */
  readonly vy: number
  /** Whether the current activity is an ANSWER to a finished command rather
   * than something the animal chose. Only these are protected from being cut
   * short: an ordinary occupation may be interrupted freely. */
  readonly reacting: boolean
  /** Whose command is running right now, or null when nothing is.
   *
   * Not a fourth axis and not a mood: it does not colour the animal, it
   * narrows what the animal is willing to do. A pet that wandered off mid
   * build is a pet that is not watching, and watching is the whole reason it
   * lives in a terminal rather than on a desktop. */
  readonly attending: Author | null
}

export interface PetTuning {
  readonly walkSpeed: number
  readonly runSpeed: number
  readonly gravity: number
  readonly maxFall: number
  /** Doing nothing for this long sends the pet to sleep. */
  readonly sleepAfter: number
  /** Minimum attention interval between choices while a command runs.
   *  This is not a clip length; reactions use their drawing's duration. */
  readonly reactionHold: number
  /** Seconds to linearly accelerate or brake a gait. */
  readonly gaitRamp: number
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
  gaitRamp: 0.18,
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
    vx: 0,
    locomotion: 'fall',
    mood: 'calm',
    recentBehaviors: [],
    noticeableAges: [],
    activity: 'none',
    hold: 0,
    phase: 'none',
    phaseHold: 0,
    phaseDuration: 0,
    phaseTurned: false,
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

function timingFor(timing: PetTiming, locomotion: Locomotion, activity: Activity): ClipTiming {
  return locomotion === 'idle' && activity !== 'none'
    ? timing.activity[activity]
    : timing.locomotion[locomotion]
}

/** The drawing sets a lifetime only for a once clip; other modes keep caller hold. */
function chosenHold(
  choice: Pick<Choice, 'locomotion' | 'activity' | 'hold'>,
  timing: PetTiming,
): number {
  const clip = timingFor(timing, choice.locomotion, choice.activity)
  return clip.mode === 'once' ? clip.duration : choice.hold
}

function approach(
  current: number,
  target: number,
  dt: number,
  ramp: number,
  reference = 0,
): number {
  if (current === target) return target
  const change =
    (Math.max(Math.abs(current), Math.abs(target), reference) / Math.max(0.1, ramp)) * dt
  if (Math.abs(target - current) <= change) return target
  return current + Math.sign(target - current) * change
}

/** How fast this gait travels, in display pixels per second.
 *
 *  Exported because the PAINTER needs the same number: the clip's playback
 *  rate is the ratio of actual speed to this one, so that a cat still
 *  getting up to speed draws its walk cycle correspondingly slower. Written
 *  out a second time in overlay.ts it was the same expression twice, which
 *  is how the two drift and the feet start sliding again in exactly the
 *  state — accelerating — this task existed to fix. */
export function gaitSpeed(locomotion: Locomotion, timing: PetTiming, scale: number): number {
  if (locomotion === 'walk') return (timing.strides.walk / timing.locomotion.walk.duration) * scale
  if (locomotion === 'run') return (timing.strides.run / timing.locomotion.run.duration) * scale
  return 0
}
// These short transitions are state, not elapsed wall time: callers advance
// their remaining durations with dt, while the painter derives expression
// progress from the same values.
const ANTICIPATE_DURATION = 0.15
const TAKEOFF_DURATION = 0.085
const TURN_DURATION = 0.11
const LAND_MIN_DURATION = 0.15
const LAND_MAX_DURATION = 0.22

function advancePausedPhase(pet: Pet, dt: number): Pet {
  const phaseHold = Math.max(0, pet.phaseHold - dt)
  let phaseTurned = pet.phaseTurned
  let dir = pet.dir
  if (pet.phase === 'turn' && !phaseTurned && phaseHold <= pet.phaseDuration / 2) {
    dir = -dir as 1 | -1
    phaseTurned = true
  }
  if (phaseHold === 0) {
    if (pet.phase === 'anticipate') {
      return {
        ...pet,
        dir,
        phase: 'takeoff',
        phaseHold: TAKEOFF_DURATION,
        phaseDuration: TAKEOFF_DURATION,
        phaseTurned: false,
        ledgeId: null,
        locomotion: 'fall',
      }
    }
    return { ...pet, dir, phase: 'none', phaseHold: 0, phaseDuration: 0, phaseTurned: false }
  }
  return { ...pet, dir, phaseHold, phaseTurned }
}

function landDuration(impactSpeed: number, maxFall: number): number {
  const impact = Math.min(1, Math.max(0, impactSpeed / Math.max(1, maxFall)))
  return LAND_MIN_DURATION + (LAND_MAX_DURATION - LAND_MIN_DURATION) * impact
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
  author: Author,
  timing: PetTiming,
  tuning: PetTuning = DEFAULT_TUNING,
): Pet {
  const mood = MOOD_OF[outcome]
  // Whatever it was watching is over, whether or not it may react.
  const done = { ...pet, attending: null }
  if (pet.locomotion === 'fall') {
    return { ...done, mood, moodHold: tuning.moodHold }
  }
  const activity = REACTION_OF[author][outcome]
  return {
    ...done,
    locomotion: 'idle',
    mood,
    moodHold: tuning.moodHold,
    activity,
    // Once reactions finish when their drawing finishes. Loop and hold
    // reactions keep the caller's reaction cadence instead.
    hold: chosenHold({ locomotion: 'idle', activity, hold: tuning.reactionHold }, timing),
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
export function attend(
  pet: Pet,
  author: Author,
  timing: PetTiming,
  tuning: PetTuning = DEFAULT_TUNING,
): Pet {
  if (pet.locomotion === 'fall') return { ...pet, attending: author }
  // A reaction still playing is left to finish. The previous command's
  // verdict routinely lands AFTER the next command has started — the freeze
  // waits on a render fence and a start does not — so cutting it off here
  // would mean the answer to what you just ran is swallowed by the thing you
  // ran next. The attending menu takes over at the next choice either way.
  if (pet.reacting && pet.hold > 0) return { ...pet, attending: author, boredom: 0 }
  const activity = author === 'agent' ? 'sit' : 'lie'
  return {
    ...pet,
    attending: author,
    locomotion: 'idle',
    // reactionHold is an attention cadence, not a clip length. Once drawings
    // may override it; loop and hold drawings leave it unchanged.
    activity,
    hold: chosenHold({ locomotion: 'idle', activity, hold: tuning.reactionHold }, timing),
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
   *  trying not to be. Descending is deliberately outside the noticeable
   *  budget: it is a quiet escape down, unlike the expressive jump upward,
   *  and excluding it keeps a full budget from trapping the animal on one
   *  ledge.
   */
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

const NOTICEABLE_WINDOW = 60
const NOTICEABLE_LIMIT = 2
const REPETITION_EXEMPT: Record<string, true> = { sit: true, lie: true, sleep: true }

function choiceKey(choice: Choice): string {
  if (choice.ascend === true) return 'jump'
  if (choice.descend === true) return 'descend'
  return choice.locomotion === 'idle' ? choice.activity : choice.locomotion
}

function isNoticeableChoice(choice: Choice): boolean {
  return choice.ascend === true || choice.locomotion === 'run'
}

function weightedChoices(
  choices: readonly Choice[],
  recent: readonly string[],
  attending: Author | null,
  canNotice: boolean,
): readonly Choice[] {
  if (attending !== null) return choices
  return choices.map((choice) => {
    const key = choiceKey(choice)
    const recentAt = recent.indexOf(key)
    const repetitionScale =
      REPETITION_EXEMPT[key] === true ? 1 : recentAt === 0 ? 0 : recentAt === 1 ? 0.25 : 1
    // The budget never forces a more visible outcome than the walk it limits:
    // it reduces future walks while leaving an already-reached edge to turn.
    const edgeScale =
      choice.locomotion === 'walk' &&
      choice.ascend !== true &&
      choice.descend !== true &&
      !canNotice
        ? 0.25
        : 1
    const noticeScale = isNoticeableChoice(choice) && !canNotice ? 0 : 1
    return { ...choice, weight: choice.weight * repetitionScale * noticeScale * edgeScale }
  })
}

function rememberChoice(pet: Pet, choice: Choice): readonly string[] {
  if (pet.attending !== null) return pet.recentBehaviors
  return [choiceKey(choice), ...pet.recentBehaviors].slice(0, 2)
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
  /** Source-to-display scale, computed once by the painter from the trim. */
  readonly petScale: number
  /** Clip modes and durations, supplied as data so this module stays pure. */
  readonly timing: PetTiming
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
  let next: Pet = {
    ...pet,
    noticeableAges: pet.noticeableAges
      .map((age) => age + dt)
      .filter((age) => age < NOTICEABLE_WINDOW),
  }

  // 1. Resolve the world before phases. A block can move or disappear while
  //    a visual transition is paused; the pet must ride it or fall at once.
  const standing = groundOf(env, next.ledgeId)
  if (next.locomotion !== 'fall' && standing === null) {
    next = {
      ...next,
      locomotion: 'fall',
      ledgeId: null,
      vy: 0,
      vx: 0,
      activity: 'none',
      hold: 0,
      phase: 'none',
      phaseHold: 0,
      phaseDuration: 0,
      phaseTurned: false,
    }
  }

  if (next.phase === 'takeoff') {
    const phaseHold = next.phaseHold - dt
    next =
      phaseHold > 0
        ? { ...next, phaseHold }
        : { ...next, phase: 'none', phaseHold: 0, phaseDuration: 0, phaseTurned: false }
    return fall(next, env, dt, tuning)
  }
  if (next.locomotion === 'fall') return fall(next, env, dt, tuning)

  const ledge = standing as Ledge
  // A ledge that moved (the view scrolled) carries the pet with it.
  const y = ledge.y
  let x = Math.min(Math.max(next.x, ledge.x0), ledge.x1)
  // A motion phase intentionally completes without checking the pointer or
  // choosing a new behaviour; its world position still follows the ledge.
  if (next.phase === 'anticipate' || next.phase === 'land' || next.phase === 'turn') {
    return advancePausedPhase({ ...next, x, y, vx: 0 }, dt)
  }
  let dir = next.dir
  let vx = next.vx
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
  //    and moves away — except for a reaction whose once clip still has a
  //    frame to show. A verdict is the one activity nothing may cut short.
  const threat = nearPointer(env, x, y, tuning)
  if (threat !== 0 && !(next.reacting && next.hold > 0)) {
    locomotion = 'run'
    activity = 'none'
    reacting = false
    hold = Math.max(hold, 0.5)
    dir = threat
    boredom = 0
  }

  // 4. Move with a signed velocity. The displayed scale and pack stride are
  //    the sole source of the gait target; braking is explicit so a reversal
  //    cannot snap the feet to the other side of the body.
  if (locomotion === 'walk' || locomotion === 'run') {
    const target = dir * gaitSpeed(locomotion, env.timing, env.petScale)
    vx = approach(vx, target, dt, tuning.gaitRamp)
    x += vx * dt
    const past = vx < 0 && x <= ledge.x0 ? -1 : vx > 0 && x >= ledge.x1 ? 1 : 0
    if (past !== 0) {
      // The budget never forces a more visible outcome than the walk it
      // limits: once the edge is reached, turning is unavoidable. A turn
      // reached with a full window is therefore not charged as discretionary.
      const chargeTurn = next.attending === null && next.noticeableAges.length < NOTICEABLE_LIMIT
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
          vx: 0,
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
      vx = 0
      return {
        ...next,
        noticeableAges: chargeTurn ? [...next.noticeableAges, 0] : next.noticeableAges,
        x,
        y,
        dir,
        vx,
        locomotion,
        activity,
        hold,
        mood,
        moodHold,
        boredom,
        phase: 'turn',
        phaseHold: TURN_DURATION,
        phaseDuration: TURN_DURATION,
        phaseTurned: false,
        ledgeId: ledge.id,
      }
    }
    boredom = 0
  } else {
    vx = approach(
      vx,
      0,
      dt,
      tuning.gaitRamp,
      Math.max(
        gaitSpeed('walk', env.timing, env.petScale),
        gaitSpeed('run', env.timing, env.petScale),
      ),
    )
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
      vx,
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
    return { ...next, x, y, dir, vx, mood: 'tired', moodHold: 0, boredom, ledgeId: ledge.id }
  }
  // 6. Finish the current occupation before starting another one. Deciding
  //    afresh every frame is what makes a pet twitch instead of live.
  hold -= dt
  if (hold <= 0) {
    const up = next.attending === null ? ledgeAbove(env.terrain, x, y, tuning.jumpReach) : null
    const offered =
      up === null ? menu(mood, next.attending) : [...menu(mood, next.attending), ASCEND]
    const choices = weightedChoices(
      offered,
      next.recentBehaviors,
      next.attending,
      next.noticeableAges.length < NOTICEABLE_LIMIT,
    )
    const c = pick(choices, rng())
    if (c.ascend === true && up !== null) {
      return {
        ...next,
        recentBehaviors: rememberChoice(next, c),
        noticeableAges: [...next.noticeableAges, 0],
        x,
        y,
        dir,
        vx: 0,
        ledgeId: ledge.id,
        locomotion: 'idle',
        activity: 'none',
        hold: 0,
        reacting: false,
        phase: 'anticipate',
        phaseHold: ANTICIPATE_DURATION,
        phaseDuration: ANTICIPATE_DURATION,
        phaseTurned: false,
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
        recentBehaviors: rememberChoice(next, c),
        x,
        y,
        dir,
        vx: 0,
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
    hold = chosenHold(c, env.timing)
    reacting = false
    next = { ...next, recentBehaviors: rememberChoice(next, c) }
    if (c.locomotion === 'run' && next.attending === null) {
      next = { ...next, noticeableAges: [...next.noticeableAges, 0] }
    }
    if (c.locomotion !== 'idle' && rng() < 0.5) dir = (dir * -1) as 1 | -1
  }

  return {
    ...next,
    x,
    y,
    vx,
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
    const phaseDuration = landDuration(Math.abs(vy), tuning.maxFall)
    return {
      ...pet,
      ledgeId: landed.id,
      x: Math.min(Math.max(pet.x, landed.x0), landed.x1),
      y: landed.y,
      vy: 0,
      locomotion: 'idle',
      activity: 'sit',
      hold: 1.2,
      phase: 'land',
      phaseHold: phaseDuration,
      phaseDuration,
      phaseTurned: false,
      boredom: 0,
    }
  }
  if (to >= env.floor.y) {
    const phaseDuration = landDuration(Math.abs(vy), tuning.maxFall)
    return {
      ...pet,
      ledgeId: FLOOR_ID,
      x: Math.min(Math.max(pet.x, env.floor.x0), env.floor.x1),
      y: env.floor.y,
      vy: 0,
      locomotion: 'idle',
      activity: 'sit',
      hold: 1.2,
      phase: 'land',
      phaseHold: phaseDuration,
      phaseDuration,
      phaseTurned: false,
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
