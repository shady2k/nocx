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
// Painting is one element with a background-position, not one element per
// frame — the sprite never enters the document as new nodes.

import { DEFAULT_TUNING, newPet, react, step, type Outcome, type Pet, type PetTuning } from './pet'
import {
  CAT_PACK,
  clipFor,
  loadPack,
  packBase,
  type ImageSource,
  type LoadedPack,
  type PetPack,
} from './pack'
import { DEFAULT_TERRAIN, deriveTerrain, type Ledge, type LedgeCandidate } from './terrain'
import { onPetsSettingsChanged, petHeight, petPack, petsEnabled } from './setting'

/** What counts as ground, by default.
 *
 *  Not only the command block's own top edge. The chips a block wears — the
 *  directory, the duration, the exit badge — are painted, raised and about
 *  sixty pixels wide, which is what the old screenmates actually treated as
 *  terrain: a Neko walked title bars and window edges, not the desktop. A cat
 *  standing on the `ok` badge of the command it just watched finish is the
 *  whole idea of the feature in one frame.
 */
const LEDGE_SELECTOR = '.cmd-block, .nocx-chip'

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
  /** What counts as ground. Defaults to LEDGE_SELECTOR; the settings preview
   *  passes its own so the mock scrollback is walkable without borrowing the
   *  real scrollback's classes and its stylesheet with them. */
  readonly ledgeSelector?: string
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
  private readonly _sprite: HTMLElement
  private readonly _raf: (cb: (t: number) => void) => number
  private readonly _caf: (h: number) => void
  private readonly _rng: () => number
  private readonly _tuning: PetTuning

  private _loaded: LoadedPack | null = null
  private _loadedBase = ''
  /** Which load is the current one. Clicking through six colours starts six
   *  fetches, and only the last one may land. */
  private _loadToken = 0
  private _pet: Pet
  private _terrain: Ledge[] = []
  private _floor: Ledge = { id: 'floor', x0: 0, x1: 0, y: 0 }
  private _clip = ''
  private _clipT = 0
  private _last = 0
  private _handle: number | null = null
  /** Pointer position in host coordinates, or null while it is elsewhere.
   *  Written from a passive listener and read once per frame — never
   *  measured, so it costs no layout. */
  private _pointer: { x: number; y: number } | null = null
  /** The drawn width of the animal, from the pack's trim. Kept because the
   *  flee test needs the box and only the painter knows it. */
  private _width = 0
  private _hostLeft = 0
  private _hostTop = 0
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
    this._sprite = document.createElement('div')
    this._sprite.className = 'pet-sprite'
    this._layer.appendChild(this._sprite)

    this._pet = newPet(0, 0)
    this._opts = opts
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
      this._pet = newPet((this._floor.x0 + this._floor.x1) / 2, -this._height)
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

  /** A command finished. */
  reactTo(outcome: Outcome): void {
    if (this._disposed) return
    this._pet = react(this._pet, outcome, this._tuning)
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
      mo.observe(this._blocks, { childList: true, subtree: true })
      this._observers.push(mo)
    }
    const scroller = this._blocks.parentElement ?? this._host
    scroller.addEventListener('scroll', mark, { passive: true })
    this._observers.push({ disconnect: () => scroller.removeEventListener('scroll', mark) })

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
    let n = 0
    const selector = this._opts.ledgeSelector ?? LEDGE_SELECTOR
    for (const el of this._blocks.querySelectorAll<HTMLElement>(selector)) {
      const r = el.getBoundingClientRect()
      if (r.width === 0 && r.height === 0) continue
      candidates.push({
        // Minted here rather than read off the element: the pet needs an
        // identity that is stable across a re-measure and unique within one
        // pane, and blocks carry neither.
        id: `blk:${(el.dataset.petLedge ??= String(++n))}`,
        left: r.left - host.left,
        right: r.right - host.left,
        top: r.top - host.top,
      })
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
      if (this._stale) this._measure()
      this._pet = step(
        this._pet,
        {
          terrain: this._terrain,
          floor: this._floor,
          petHeight: this._height,
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

  // ── paint ────────────────────────────────────────────────────────────────

  private _paint(dt: number): void {
    const loaded = this._loaded
    if (!loaded) return
    const name = clipFor(loaded.pack, this._pet.locomotion, this._pet.activity, loaded.clips)
    const clip = loaded.clips[name]
    if (!clip) return
    if (name !== this._clip) {
      this._clip = name
      this._clipT = 0
    }
    this._clipT += dt * loaded.pack.fps
    const frame = Math.floor(this._clipT) % clip.frames

    const { x0, y0, x1, y1 } = loaded.trim
    const scale = this._height / (y1 - y0)
    const width = (x1 - x0) * scale
    this._width = width
    const s = this._sprite.style
    s.width = `${width}px`
    s.height = `${this._height}px`
    s.backgroundImage = `url(${clip.url})`
    s.backgroundSize = `${clip.sheetWidth * scale}px ${clip.sheetHeight * scale}px`
    s.backgroundPosition = `${-(frame * loaded.pack.cell + x0) * scale}px ${-y0 * scale}px`
    // translate, not left/top: the pet moves on the compositor and never
    // asks the pane for a new layout.
    s.transform =
      `translate(${this._pet.x - width / 2}px, ${this._pet.y - this._height}px) ` +
      `scaleX(${this._pet.dir > 0 ? 1 : -1})`
  }
}
