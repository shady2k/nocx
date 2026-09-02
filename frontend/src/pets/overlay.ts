// The pet's one contact with the DOM (nocx-q4qeh.1).
//
// Everything about WHAT the pet does lives in `pet.ts` and `terrain.ts`,
// which know nothing about elements. This module does three things and
// nothing else: it reads geometry, it runs the clock, and it paints.
//
// Geometry is read into a SNAPSHOT, refreshed only when something says the
// layout moved — a resize, a scroll, a block arriving or leaving. Asking the
// DOM for rectangles inside the animation frame is the obvious shortcut and
// it turns every scroll of the terminal into a layout-thrashing benchmark:
// each getBoundingClientRect forces the style and layout the scroll just
// invalidated, sixty times a second, for as long as the pet exists.
//
// Painting is one sprite inside a world transform, not one element per
// frame — the sprite never enters the document as new nodes. Keeping placement
// outside expression is what lets compression leave the feet on the ledge.

import {
  attend,
  DEFAULT_TUNING,
  humanActivity,
  gaitSpeed,
  newPet,
  react,
  FLOOR_ID,
  onFloor,
  step,
  type Author,
  type Outcome,
  type Pet,
  type PetTiming,
  type PetTuning,
} from './pet'
import {
  CAT_PACK,
  airFrame,
  clipFor,
  loadPack,
  packBase,
  type ImageSource,
  type LoadedPack,
  type PetPack,
} from './pack'
import { DEFAULT_TERRAIN, deriveTerrain, type Ledge, type LedgeCandidate } from './terrain'
import { onPetsSettingsChanged, petHeight, petPack, petsEnabled } from './setting'

/** One kind of ground: what to find, and which of its edges can be stood on. */
export interface LedgeSource {
  readonly selector: string
  /** `top` for something you stand ON, `bottom` for something you stand
   *  UNDER the near side of — the tab strip's underside is a shelf. */
  readonly edge: 'top' | 'bottom'
}

/** What counts as ground, by default.
 *
 *  Not only the command block's own top edge. The chips a block wears — the
 *  directory, the duration, the exit badge — are painted, raised and about
 *  sixty pixels wide, which is what the old screenmates actually treated as
 *  terrain: a Neko walked title bars and window edges, not the desktop. A cat
 *  standing on the `ok` badge of the command it just watched finish is the
 *  whole idea of the feature in one frame.
 *
 *  The tab strip is here because the animal belongs to the WINDOW: its
 *  underside is the one shelf that does not move when you change tabs, which
 *  is exactly where a screenmate should be able to sit and wait.
 *
 *  Blocks are taken from the ACTIVE pane only. A pet standing on a block in a
 *  pane you cannot see would be a pet you cannot see.
 */
/** The full terrain sweep reads every block and chip of the active pane, so
 *  it is rate-limited. Between sweeps the animal still follows the ledge it
 *  is standing on, one rectangle per frame. */

const SWEEP_INTERVAL_MS = 100

/** The contact shadow appears only shortly before a ledge landing. */
const SHADOW_WINDOW = 0.2

const DEFAULT_LEDGES: readonly LedgeSource[] = [
  { selector: '.tabbar', edge: 'bottom' },
  { selector: '.pane.active .cmd-block', edge: 'top' },
  { selector: '.pane.active .nocx-chip', edge: 'top' },
]

export function timingFrom(pack: PetPack, loaded: LoadedPack | null = null): PetTiming {
  const clipTiming = (name: string) => {
    const definition = pack.clips[name]
    const takes = loaded?.clips[name]?.takes ?? definition.takes
    // Unequal takes share the longest duration; a shorter sheet remains on
    // its final frame instead of ending the behaviour early.
    const frames = Math.max(...takes.map((take) => take.frames))
    return {
      mode: definition.mode,
      duration: frames / pack.fps,
      pause: definition.pause ?? 0,
    }
  }
  // Built mutable and handed over as readonly. `PetTiming` is readonly
  // because the state machine must not be able to edit its own rules; the
  // one place allowed to write these tables is the one assembling them.
  const locomotion: Record<string, ReturnType<typeof clipTiming>> = {}
  for (const [key, clip] of Object.entries(pack.locomotion)) locomotion[key] = clipTiming(clip)
  const activity: Record<string, ReturnType<typeof clipTiming>> = {}
  for (const [key, clip] of Object.entries(pack.activity)) activity[key] = clipTiming(clip)
  // Before the image arrives, use the cell as a temporary body width. The
  // loaded trim replaces it so the actual drawing owns the source-pixel scale.
  const bodyWidth = loaded === null ? pack.cell : loaded.trim.x1 - loaded.trim.x0
  const strides = {
    walk: bodyWidth * pack.strideBodies.walk,
    run: bodyWidth * pack.strideBodies.run,
  }
  return {
    fps: pack.fps,
    locomotion: locomotion as PetTiming['locomotion'],
    activity: activity as PetTiming['activity'],
    strides,
  }
}

export interface PetOverlayOpts {
  /** The element the pet is drawn over. Must be a positioned ancestor of
   *  nothing in particular — the layer is appended to it and sized to it. */
  readonly host: HTMLElement
  /** Where the command blocks live. Their top edges are the terrain. */
  readonly blocks: HTMLElement
  /** Directory the pack's files sit in, trailing slash included. */
  readonly base?: string
  readonly pack?: PetPack
  readonly tuning?: PetTuning
  /** Injected in tests. Defaults to the browser's. */
  readonly raf?: (cb: (t: number) => void) => number
  readonly caf?: (h: number) => void
  readonly rng?: () => number
  /** Injected in tests; defaults to the browser's decoder. */
  readonly imageSource?: ImageSource
  /** What counts as ground. Defaults to DEFAULT_LEDGES; the settings preview
   *  passes its own so the mock scrollback is walkable without borrowing the
   *  real scrollback's classes and its stylesheet with them. */
  readonly ledges?: readonly LedgeSource[]
  /** Injected in tests. Defaults to the declared settings module. */
  readonly settings?: {
    enabled(): boolean
    height(): number
    /** Which pack directory to load, as a base path with a trailing slash. */
    base(): string
    onChange(cb: () => void): () => void
  }
}

/**
 * A pet living over one pane.
 *
 * Construction is synchronous and painting starts only once the pack has
 * loaded; a pack that fails to load leaves the terminal exactly as it was,
 * because a decoration must never be able to break the thing it decorates.
 */
export class PetOverlay {
  private readonly _host: HTMLElement
  private readonly _blocks: HTMLElement
  private readonly _layer: HTMLElement
  private readonly _world: HTMLElement
  private readonly _shadow: HTMLElement
  private readonly _sprite: HTMLElement
  private readonly _raf: (cb: (t: number) => void) => number
  private readonly _caf: (h: number) => void
  private readonly _rng: () => number
  private readonly _tuning: PetTuning
  private _timing: PetTiming

  private _loaded: LoadedPack | null = null
  private _loadedBase = ''
  /** Which load is the current one. Clicking through six colours starts six
   *  fetches, and only the last one may land. */
  private _loadToken = 0
  private _pet: Pet
  private _terrain: Ledge[] = []
  private _floor: Ledge = { id: 'floor', x0: 0, x1: 0, y: 0 }
  private _clip = ''
  private _doing = ''
  private _behaviorGeneration = 0
  private _paintedGeneration = -1
  private _clipT = 0
  private _clipPause = 0
  /** Which drawing of the current behaviour is playing. */
  private _take = 0
  private _last = 0
  private _handle: number | null = null
  /** Pointer position in host coordinates, or null while it is elsewhere.
   *  Written from a passive listener and read once per frame — never
   *  measured, so it costs no layout. */
  private _pointer: { x: number; y: number } | null = null
  /** The drawn width of the animal, from the pack's trim. Kept because the
   *  flee test needs the box and only the painter knows it. */
  private _width = 0
  private _scale = 1
  private _nextLedgeId = 0
  private _lastSweep = -1e9
  private _hostLeft = 0
  private _hostTop = 0
  /** The element the animal is standing on, so its rectangle can be refreshed
   *  on its own each frame. One getBoundingClientRect is nothing; sweeping
   *  every block and chip of a long scrollback sixty times a second is the
   *  layout thrash this module is arranged to avoid. */
  private _standingOn: { id: string; el: HTMLElement; source: LedgeSource } | null = null
  private _stale = true
  private _disposed = false
  private _running = false
  /** Whether an animal is currently living in this layer. Distinct from
   *  `_running`: the loop can be restarted around a pet that never left. */
  private _alive = false
  private _height = 0
  private readonly _observers: { disconnect(): void }[] = []
  private readonly _settings: NonNullable<PetOverlayOpts['settings']>
  private readonly _opts: PetOverlayOpts

  constructor(opts: PetOverlayOpts) {
    this._host = opts.host
    this._blocks = opts.blocks
    this._raf = opts.raf ?? ((cb) => requestAnimationFrame(cb))
    this._caf = opts.caf ?? ((h) => cancelAnimationFrame(h))
    this._rng = opts.rng ?? Math.random
    this._tuning = opts.tuning ?? DEFAULT_TUNING

    this._layer = document.createElement('div')
    this._layer.className = 'pet-layer'
    this._world = document.createElement('div')
    this._world.className = 'pet-world'
    this._shadow = document.createElement('div')
    this._shadow.className = 'pet-shadow'
    this._sprite = document.createElement('div')
    this._sprite.className = 'pet-sprite'
    this._world.appendChild(this._shadow)
    this._world.appendChild(this._sprite)
    this._layer.appendChild(this._world)

    this._pet = newPet(0, 0)
    this._opts = opts
    this._timing = timingFrom(opts.pack ?? CAT_PACK)
    this._settings = opts.settings ?? {
      enabled: petsEnabled,
      height: petHeight,
      base: () => packBase(petPack()),
      onChange: onPetsSettingsChanged,
    }
    this._height = this._settings.height()
    this._observers.push({ disconnect: this._settings.onChange(() => this._sync()) })
    this._sync()
  }

  /** Bring the layer into line with the setting.
   *
   *  Switched off, the pack is never fetched: a decoration somebody declined
   *  should cost them no bytes, and "loaded but hidden" is how a disabled
   *  feature quietly keeps running. */
  private _sync(): void {
    if (this._disposed) return
    this._height = this._settings.height()
    if (!this._settings.enabled()) {
      this._stop()
      return
    }
    const base = this._opts.base ?? this._settings.base()
    const sameArt = this._loaded !== null && this._loadedBase === base
    if (this._running && sameArt) {
      // Only the size changed. The ledges were filtered against the old
      // height, so they have to be derived again before the next frame.
      this.invalidate()
      return
    }
    this._running = true
    if (this._layer.parentNode === null) this._host.appendChild(this._layer)
    if (sameArt) {
      this._begin()
      return
    }
    // A different animal: fetch it, and swap only once it is in hand. A
    // colour that fails to load must not cost the person the pet they had.
    const token = ++this._loadToken
    void loadPack(this._opts.pack ?? CAT_PACK, base, this._opts.imageSource)
      .then((loaded) => {
        if (this._disposed || !this._running || token !== this._loadToken) return
        this._loaded = loaded
        this._loadedBase = base
        this._timing = timingFrom(loaded.pack, loaded)
        this._clip = ''
        this._begin()
      })
      .catch(() => {
        if (token !== this._loadToken) return
        // A missing sprite is not worth a broken terminal — but if an animal
        // is already on screen, keep it rather than taking it away.
        if (this._loaded === null) this._stop()
      })
  }

  private _begin(): void {
    this._watch()
    this._measure()
    // A pet already living here KEEPS living. Minting one unconditionally is
    // what made dragging the size slider rain cats: every value the drag
    // passed through arrived as a settings change, and each one dropped a
    // fresh animal in from above while the last was still walking. An
    // arrival is for the first one and for a pet that was switched off and
    // back on — not for a property of the one already on screen.
    if (!this._alive) {
      // Dropped in from above the first ledge, so the arrival is a fall
      // rather than a pop: the pet is seen to take its place.
      //
      // What it has already been TOLD survives the mint. Sprites are fetched
      // asynchronously and the terminal does not wait for them, so a command
      // that starts while the pack is still loading reaches a pet that is
      // about to be replaced — and the animal arrived knowing nothing about
      // the build it was supposed to be watching.
      const heard = this._pet
      this._pet = {
        ...newPet((this._floor.x0 + this._floor.x1) / 2, -this._height),
        mood: heard.mood,
        moodHold: heard.moodHold,
        attending: heard.attending,
      }
      this._alive = true
    }
    this._last = 0
    this._start()
  }

  private _stop(): void {
    this._running = false
    this._alive = false
    if (this._handle !== null) this._caf(this._handle)
    this._handle = null
    this._layer.remove()
  }

  /** What the pet is playing right now. The one window a test needs: it is
   *  what a person actually sees. */
  get playing(): string {
    return this._clip
  }

  /** A command started: the animal settles down to watch it. */
  attendTo(author: Author): void {
    if (this._disposed) return
    const reactionInProgress = this._pet.reacting && this._pet.hold > 0
    this._pet = attend(this._pet, author, this._timing, this._tuning)
    if (!reactionInProgress) this._behaviorGeneration++
  }

  /** Feed the raw human activity signal into the pure pet state machine. */
  onUserActivity(): void {
    if (this._disposed) return
    const waking = this._pet.humanAway && this._pet.locomotion !== 'fall'
    this._pet = humanActivity(this._pet, this._timing)
    if (waking) this._behaviorGeneration++
  }

  /** A command finished. */
  reactTo(outcome: Outcome, author: Author = 'shell'): void {
    if (this._disposed) return
    this._behaviorGeneration++
    this._pet = react(this._pet, outcome, author, this._timing, this._tuning)
  }

  /** The layout moved; re-read it before the next frame. */
  invalidate(): void {
    this._stale = true
  }

  dispose(): void {
    if (this._disposed) return
    this._disposed = true
    this._stop()
    for (const o of this._observers) o.disconnect()
    this._observers.length = 0
  }

  // ── geometry ─────────────────────────────────────────────────────────────

  private _watched = false

  private _watch(): void {
    if (this._watched) return
    this._watched = true
    const mark = () => this.invalidate()
    if (typeof ResizeObserver !== 'undefined') {
      const ro = new ResizeObserver(mark)
      ro.observe(this._host)
      this._observers.push(ro)
    }
    if (typeof MutationObserver !== 'undefined') {
      const mo = new MutationObserver(mark)
      // `class` as well as childList: which pane is active is a CLASS, so
      // without it switching tabs left the animal walking on the geometry of
      // a pane nobody can see. The filter is what keeps this affordable —
      // the editor rewrites other attributes on every keystroke.
      mo.observe(this._blocks, {
        childList: true,
        subtree: true,
        attributes: true,
        attributeFilter: ['class'],
      })
      this._observers.push(mo)
    }
    // On the DOCUMENT, in the capture phase. A scroll event does not bubble,
    // and the animal lives over the whole window rather than inside the
    // scroller it is watching, so a listener on its own host heard nothing:
    // every ordinary scroll of the scrollback left the terrain snapshot
    // describing where the blocks USED to be.
    document.addEventListener('scroll', mark, { capture: true, passive: true })
    this._observers.push({
      disconnect: () => document.removeEventListener('scroll', mark, { capture: true }),
    })

    // The pointer, so the animal can get out of its way. Listened for on the
    // HOST rather than the layer: the layer takes no pointer events at all
    // (that is what makes the pet unclickable), so it would never hear one.
    // The position is stored raw and converted per frame against the host
    // rect the terrain snapshot already holds — no measuring on the event.
    const onMove = (e: Event) => {
      const ev = e as PointerEvent
      // Against the rect the terrain snapshot already holds, NOT a fresh
      // measurement: pointermove fires faster than frames do, and a
      // getBoundingClientRect on each one is the layout thrash this whole
      // module is arranged to avoid. Resize and scroll both invalidate the
      // snapshot, so the rect is never stale for longer than a frame.
      this._pointer = { x: ev.clientX - this._hostLeft, y: ev.clientY - this._hostTop }
    }
    const onLeave = () => {
      this._pointer = null
    }
    this._host.addEventListener('pointermove', onMove, { passive: true })
    this._host.addEventListener('pointerleave', onLeave, { passive: true })
    this._observers.push({
      disconnect: () => {
        this._host.removeEventListener('pointermove', onMove)
        this._host.removeEventListener('pointerleave', onLeave)
      },
    })
  }

  private _measure(): void {
    const host = this._host.getBoundingClientRect()
    this._hostLeft = host.left
    this._hostTop = host.top
    const candidates: LedgeCandidate[] = []
    this._standingOn = null
    for (const source of this._opts.ledges ?? DEFAULT_LEDGES) {
      for (const el of this._blocks.querySelectorAll<HTMLElement>(source.selector)) {
        const r = el.getBoundingClientRect()
        if (r.width === 0 && r.height === 0) continue
        // Minted here rather than read off the element: the pet needs an
        // identity that is stable across a re-measure and unique within the
        // window, and neither blocks nor tabs carry one.
        // The counter lives on the overlay, not on the measurement. Reset
        // per sweep it did not advance for elements that were already
        // marked — `??=` skips its right-hand side — so the next new block
        // was handed an id another element already held, and the pet
        // teleported onto it instead of falling. An identity that is reused
        // is worse than no identity at all.
        const id = `l:${(el.dataset.petLedge ??= String(++this._nextLedgeId))}`
        if (id === this._pet.ledgeId) this._standingOn = { id, el, source }
        candidates.push({
          id,
          left: r.left - host.left,
          right: r.right - host.left,
          top: (source.edge === 'top' ? r.top : r.bottom) - host.top,
        })
      }
    }
    const viewport = { width: host.width, height: host.height }
    this._terrain = deriveTerrain(candidates, {
      ...DEFAULT_TERRAIN,
      petHeight: this._height,
      viewport,
    })
    this._floor = { id: 'floor', x0: 8, x1: Math.max(8, host.width - 8), y: host.height }
    this._stale = false
  }

  /** Keep the ledge underfoot current without re-deriving the world. */
  private _follow(): void {
    const held = this._standingOn
    if (held === null) return
    if (!held.el.isConnected) {
      this._stale = true
      return
    }
    const r = held.el.getBoundingClientRect()
    const y = (held.source.edge === 'top' ? r.top : r.bottom) - this._hostTop
    const x0 = r.left - this._hostLeft + DEFAULT_TERRAIN.inset
    const x1 = r.right - this._hostLeft - DEFAULT_TERRAIN.inset
    this._terrain = this._terrain.map((l) => (l.id === held.id ? { id: l.id, x0, x1, y } : l))
  }

  // ── the clock ────────────────────────────────────────────────────────────

  private _start(): void {
    // Exactly one loop. Without this a second `_begin` — a pack that finished
    // loading while another was in flight — leaves both running, and the pet
    // moves at twice its speed for the rest of the session.
    if (this._handle !== null) this._caf(this._handle)
    const frame = (t: number) => {
      if (this._disposed) return
      const dt = this._last === 0 ? 1 / 60 : Math.min(0.1, (t - this._last) / 1000)
      this._last = t
      // The ledge underfoot is refreshed EVERY frame, from one rectangle, so
      // the animal rides a scroll smoothly. The full sweep is what costs — it
      // reads every block and chip of the pane — so it runs only when
      // something said the layout moved, and at most this often.
      this._follow()
      if (this._stale && t - this._lastSweep >= SWEEP_INTERVAL_MS) {
        this._lastSweep = t
        this._measure()
      }
      this._updateMetrics()
      this._pet = step(
        this._pet,
        {
          terrain: this._terrain,
          floor: this._floor,
          petHeight: this._height,
          petScale: this._scale,
          timing: this._timing,
          petWidth: this._width,
          pointer: this._pointer,
        },
        dt,
        this._rng,
        this._tuning,
      )
      this._paint(dt)
      this._handle = this._raf(frame)
    }
    this._handle = this._raf(frame)
  }
  private _updateMetrics(): void {
    const loaded = this._loaded
    if (!loaded) return
    const { x0, y0, x1, y1 } = loaded.trim
    this._scale = this._height / (y1 - y0)
    this._width = (x1 - x0) * this._scale
  }

  // ── paint ────────────────────────────────────────────────────────────────

  private _paint(dt: number): void {
    const loaded = this._loaded
    if (!loaded) return
    const preContact =
      this._pet.locomotion === 'fall' &&
      this._pet.landingTarget !== null &&
      this._pet.landingTarget !== FLOOR_ID &&
      this._pet.landingIn !== null &&
      this._pet.landingIn <= SHADOW_WINDOW
    const grounded = this._pet.locomotion !== 'fall' && !onFloor(this._pet)
    this._shadow.hidden = !(grounded || preContact)
    const growth = preContact
      ? 1 - Math.min(1, Math.max(0, this._pet.landingIn! / SHADOW_WINDOW))
      : 0
    this._shadow.style.width = `${36 + 6 * growth}%`
    const name = clipFor(loaded.pack, this._pet.locomotion, this._pet.activity, loaded.clips)
    const clip = loaded.clips[name]
    const definition = loaded.pack.clips[name]
    if (!clip || !definition) return
    const doing = `${this._pet.locomotion}/${this._pet.activity}`
    if (
      this._behaviorGeneration !== this._paintedGeneration ||
      doing !== this._doing ||
      name !== this._clip
    ) {
      this._paintedGeneration = this._behaviorGeneration
      this._doing = doing
      this._clip = name
      this._clipT = 0
      // A fresh take and pause are chosen only when the behaviour starts.
      // Resolving a range here, rather than per frame, keeps the final pose
      // stable for the whole pause while still giving repeated cycles variety.
      this._take = Math.floor(this._rng() * clip.takes.length) % clip.takes.length
      const pause = definition.pause ?? 0
      this._clipPause =
        definition.mode === 'loop'
          ? typeof pause === 'number'
            ? pause
            : pause[0] + this._rng() * (pause[1] - pause[0])
          : 0
    }
    const take = clip.takes[Math.min(this._take, clip.takes.length - 1)]
    // The clip plays at the ratio of actual speed to the gait's own speed, so
    // a cat still accelerating draws its walk correspondingly slower and the
    // contact foot stays put. Airborne poses are discrete, so their clip time
    // does not advance at all.
    const moving = this._pet.locomotion === 'walk' || this._pet.locomotion === 'run'
    const baseSpeed = gaitSpeed(this._pet.locomotion, this._timing, this._scale)
    const rate = moving && baseSpeed > 0 ? Math.abs(this._pet.vx) / baseSpeed : 1
    if (this._pet.locomotion !== 'fall') this._clipT += dt * rate
    const cycle = take.frames / loaded.pack.fps
    if (definition.mode === 'loop') {
      while (this._clipT >= cycle + this._clipPause) {
        this._clipT -= cycle + this._clipPause
        const pause = definition.pause ?? 0
        this._clipPause =
          typeof pause === 'number' ? pause : pause[0] + this._rng() * (pause[1] - pause[0])
      }
    }
    const phase = this._clipT
    const airborne = this._pet.locomotion === 'fall'
    const fixedAirFrame = airborne
      ? airFrame(loaded.pack, this._pet.vy, this._tuning.maxFall)
      : null
    const frame =
      fixedAirFrame !== null
        ? Math.min(take.frames - 1, Math.max(0, fixedAirFrame))
        : airborne
          ? 0
          : definition.mode === 'loop'
            ? Math.min(
                take.frames - 1,
                Math.floor(Math.min(phase, cycle - Number.EPSILON) * loaded.pack.fps),
              )
            : Math.min(take.frames - 1, Math.floor(phase * loaded.pack.fps))

    const { x0, y0 } = loaded.trim
    const width = this._width
    // What the animal is doing, on the element, in the app's own vocabulary.
    //
    // The alternative is a test reading which PNG the sprite happens to be
    // showing, which pins the pack rather than the behaviour: rename a sheet
    // or give a clip a second take and every assertion about the pet breaks
    // without anything being wrong. This is the same idiom the rest of the
    // app already offers a suite — `data-activity` on a tab, `data-view` on
    // the activity bar — and it is the only window onto a module that
    // deliberately takes no clicks and holds no text.
    this._layer.dataset.doing = `${this._pet.locomotion}/${this._pet.activity}`
    this._layer.dataset.mood = this._pet.mood
    this._layer.dataset.watching = this._pet.attending ?? 'nothing'
    this._layer.dataset.phase = this._pet.phase
    let expressionScaleX = 1
    let expressionScaleY = 1
    const progress =
      this._pet.phaseDuration > 0
        ? Math.min(1, Math.max(0, 1 - this._pet.phaseHold / this._pet.phaseDuration))
        : 1
    if (this._pet.phase === 'anticipate') {
      expressionScaleX = 1.04
      expressionScaleY = 0.94
    } else if (this._pet.phase === 'takeoff') {
      expressionScaleX = 1.04 - 0.04 * progress
      expressionScaleY = 0.94 + 0.06 * progress
    } else if (this._pet.phase === 'land') {
      if (progress <= 0.5) {
        const compression = progress * 2
        expressionScaleX = 1 + 0.04 * compression
        expressionScaleY = 1 - 0.06 * compression
      } else {
        const rebound = (progress - 0.5) * 2
        const bump = rebound <= 0.75 ? rebound / 0.75 : (1 - rebound) / 0.25
        expressionScaleX = 1.04 - 0.04 * rebound
        // The little bump settles to scaleY 1.0 exactly at phase end.
        expressionScaleY = 0.94 + 0.06 * rebound + 0.04 * bump
      }
    }
    const tilt =
      this._pet.locomotion === 'fall'
        ? Math.max(-1, Math.min(1, this._pet.vy / Math.max(1, this._tuning.maxFall))) * 4
        : 0
    const world = this._world.style
    world.width = `${width}px`
    world.height = `${this._height}px`
    // World placement and facing are separate from the expressive transform:
    // independent scaleY/rotation must not fight the pet's translated position.
    world.transform =
      `translate(${this._pet.x - width / 2}px, ${this._pet.y - this._height}px) ` +
      `scaleX(${this._pet.dir > 0 ? 1 : -1})`
    const s = this._sprite.style
    s.width = `${width}px`
    s.height = `${this._height}px`
    s.backgroundImage = `url(${take.url})`
    s.backgroundSize = `${take.sheetWidth * this._scale}px ${take.sheetHeight * this._scale}px`
    s.backgroundPosition = `${-(frame * loaded.pack.cell + x0) * this._scale}px ${-y0 * this._scale}px`
    s.transform = `rotate(${tilt}deg) scale(${expressionScaleX}, ${expressionScaleY})`
  }
}
