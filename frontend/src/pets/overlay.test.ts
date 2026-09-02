// @vitest-environment jsdom
// The pet over a pane (nocx-q4qeh.1).
//
// This is the check that a PERSON gets a pet, not that the modules compose:
// blocks exist, the layer appears over them, the animal stands on a block's
// top edge, and finishing a command visibly changes what it is doing. jsdom
// computes no layout, so the rectangles are stated — the arithmetic they feed
// is what is under test, and the pixels are confirmed in the browser.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PetOverlay, timingFrom } from './overlay'
import { loadPack, type ImageSource, type PetPack } from './pack'

const CELL = 10

const PACK: PetPack = {
  id: 'test',
  cell: CELL,
  fps: 10,
  clips: {
    idle: { mode: 'loop', pause: 0, takes: [{ file: 'idle.png', frames: 2 }] },
    walk: { mode: 'loop', takes: [{ file: 'walk.png', frames: 2 }] },
    run: { mode: 'loop', takes: [{ file: 'run.png', frames: 2 }] },
    meow: { mode: 'once', takes: [{ file: 'meow.png', frames: 2 }] },
    itch: { mode: 'once', takes: [{ file: 'itch.png', frames: 2 }] },
    sitting: { mode: 'hold', takes: [{ file: 'sitting.png', frames: 1 }] },
  },
  locomotion: { idle: 'idle', walk: 'walk', run: 'run', fall: 'run' },
  activity: {
    sit: 'sitting',
    groom: 'idle',
    stretch: 'idle',
    lie: 'idle',
    scratch: 'itch',
    meow: 'meow',
    sleep: 'idle',
  },
  strideBodies: { walk: 1, run: 3 },
}

const AIR_PACK: PetPack = {
  ...PACK,
  airFrames: { rise: 1, apex: 0, fall: 1 },
}

const PAUSE_PACK: PetPack = {
  ...PACK,
  clips: {
    ...PACK.clips,
    idle: { ...PACK.clips.idle, pause: [0.4, 2.0] },
  },
}

/** Every clip paints a 4×4 animal at (3,5) of a 10×10 cell. */
const IMAGES: ImageSource = {
  load(url) {
    const frames = url.endsWith('sitting.png') ? 1 : 2
    const width = CELL * frames
    const alpha = new Uint8ClampedArray(width * CELL)
    for (let f = 0; f < frames; f++) {
      for (let y = 5; y < 9; y++)
        for (let x = 3; x < 7; x++) alpha[y * width + (f * CELL + x)] = 255
    }
    return Promise.resolve({ width, height: CELL, alpha })
  },
}

function rect(left: number, top: number, right: number, bottom: number): DOMRect {
  return {
    left,
    top,
    right,
    bottom,
    width: right - left,
    height: bottom - top,
    x: left,
    y: top,
    toJSON: () => ({}),
  }
}

interface Stand {
  host: HTMLElement
  blocks: HTMLElement
  frames: ((t: number) => void)[]
  pump(seconds: number): void
}

function addBlock(s: Stand, top: number): HTMLElement {
  const b = document.createElement('div')
  b.className = 'cmd-block'
  b.getBoundingClientRect = () => rect(50, top, 550, top + 40)
  s.blocks.appendChild(b)
  return b
}

function stand(blockTops: number[]): Stand {
  const host = document.createElement('div')
  const blocks = document.createElement('div')
  // The real terrain is taken from the ACTIVE pane, so the stand wears the
  // class the selector names rather than the test naming its own — otherwise
  // the default ground would go untested and only a browser would notice.
  blocks.className = 'pane active'
  host.appendChild(blocks)
  document.body.appendChild(host)
  host.getBoundingClientRect = () => rect(0, 0, 600, 400)
  for (const top of blockTops) {
    const b = document.createElement('div')
    b.className = 'cmd-block'
    b.getBoundingClientRect = () => rect(50, top, 550, top + 40)
    blocks.appendChild(b)
  }
  const frames: ((t: number) => void)[] = []
  let t = 0
  return {
    host,
    blocks,
    frames,
    pump(seconds: number) {
      const steps = Math.round(seconds * 60)
      for (let i = 0; i < steps; i++) {
        const cb = frames.pop()
        if (!cb) return
        t += 1000 / 60
        cb(t)
      }
    },
  }
}

/** A settings stub the test can flip. */
function settingsStub(enabled = true, height = 34, base = '/p/') {
  const listeners = new Set<() => void>()
  return {
    enabled: () => enabled,
    height: () => height,
    base: () => base,
    onChange: (cb: () => void) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    },
    set(next: { enabled?: boolean; height?: number; base?: string }) {
      if (next.enabled !== undefined) enabled = next.enabled
      if (next.height !== undefined) height = next.height
      if (next.base !== undefined) base = next.base
      for (const l of [...listeners]) l()
    },
  }
}

function overlayOn(
  s: Stand,
  rng = () => 0.99,
  settings: ReturnType<typeof settingsStub> = settingsStub(),
  pack: PetPack = PACK,
): PetOverlay {
  return new PetOverlay({
    settings,
    host: s.host,
    blocks: s.blocks,
    pack,
    imageSource: IMAGES,
    rng,
    raf: (cb) => {
      s.frames.push(cb)
      return s.frames.length
    },
    caf: () => {},
  })
}

describe('a pet over a pane', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('puts a layer over the pane and nothing inside the blocks', async () => {
    const s = stand([200])
    overlayOn(s)
    await vi.waitFor(() => expect(s.host.querySelector('.pet-layer')).not.toBeNull())
    expect(s.blocks.querySelector('.pet-layer')).toBeNull()
    expect(s.blocks.querySelectorAll('.cmd-block')).toHaveLength(1)
  })

  it('separates world placement from expressive transform and reports phase', async () => {
    const s = stand([200])
    overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)

    const layer = s.host.querySelector<HTMLElement>('.pet-layer')!
    const world = s.host.querySelector<HTMLElement>('.pet-world')!
    const sprite = s.host.querySelector<HTMLElement>('.pet-sprite')!
    expect(layer.dataset.phase).toBe('none')
    expect(world.parentElement).toBe(layer)
    expect(sprite.parentElement).toBe(world)
    expect(world.style.transform).toMatch(/^translate\(.*\) scaleX\([-\d]+\)$/)
    expect(sprite.style.transform).toBe('rotate(0deg) scale(1, 1)')
  })

  it('falls in and comes to rest ON a command block', async () => {
    const s = stand([200])
    overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)
    const world = s.host.querySelector<HTMLElement>('.pet-world')!
    // 4 painted rows of a 10px cell scaled to 34px tall → the transform's y
    // puts the animal's feet on the block's top edge.
    const m = /translate\(([-\d.]+)px, ([-\d.]+)px\)/.exec(world.style.transform)
    expect(m).not.toBeNull()
    expect(Number(m![2]) + 34).toBeCloseTo(200, 0)
  })
  it('holds one airborne frame and tilts with downward speed', async () => {
    const s = stand([200])
    overlayOn(s, () => 0.99, settingsStub(), AIR_PACK)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(1 / 60)
    const sprite = s.host.querySelector<HTMLElement>('.pet-sprite')!
    const first = sprite.style.backgroundPosition
    expect(first).toContain('px')
    expect(sprite.style.transform).toMatch(/^rotate\([0-9.]+deg\) scale\(1, 1\)$/)
    s.pump(0.1)
    expect(sprite.style.backgroundPosition).toBe(first)
  })

  it('keeps the one-frame fallback for packs without airborne poses', async () => {
    const s = stand([200])
    overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(1 / 60)
    const sprite = s.host.querySelector<HTMLElement>('.pet-sprite')!
    const first = sprite.style.backgroundPosition
    s.pump(0.2)
    expect(sprite.style.backgroundPosition).toBe(first)
  })

  it('carries a pause range into drawing timing', () => {
    expect(timingFrom(PAUSE_PACK).locomotion.idle.pause).toEqual([0.4, 2.0])
  })

  it('resolves one pause per loop cycle without growing the accumulator', async () => {
    const draws = vi.fn(() => 0)
    const s = stand([200])
    const pet = overlayOn(s, draws, settingsStub(), PAUSE_PACK)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)
    pet.attendTo('shell')
    const before = draws.mock.calls.length
    s.pump(30)
    expect(draws.mock.calls.length - before).toBeLessThan(200)
  })
  it('drops when the block under it is removed, rather than jumping to another', async () => {
    const s = stand([150, 300])
    overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)
    const world = s.host.querySelector<HTMLElement>('.pet-world')!
    const yOf = () => Number(/translate\([-\d.]+px, ([-\d.]+)px\)/.exec(world.style.transform)![1])
    const landed = yOf()
    s.blocks.querySelector('.cmd-block')!.remove()
    // Let the mutation observer deliver — the overlay learns the layout moved
    // from an observer, never by re-measuring inside the frame.
    await new Promise((r) => setTimeout(r, 0))
    // A tenth of a second of falling: far enough to see it move, nowhere near
    // the next block. The assertion IS the no-teleport rule.
    s.pump(0.1)
    expect(yOf()).toBeGreaterThan(landed)
    expect(yOf()).toBeLessThan(300)
  })

  it('a finished command changes what the pet is doing', async () => {
    const s = stand([200])
    const pet = overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)
    pet.reactTo('success')
    s.pump(1 / 60)
    expect(pet.playing).toBe('meow')
    pet.reactTo('failure')
    s.pump(1 / 60)
    expect(pet.playing).toBe('itch')
  })

  it('keeps a once clip through its final frame before changing activity', async () => {
    const s = stand([200])
    const pet = overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)
    pet.reactTo('success')
    pet.attendTo('shell')
    s.pump(0.15)
    expect(pet.playing).toBe('meow')
    s.pump(0.1)
    expect(pet.playing).not.toBe('meow')
  })

  it('restarts a repeated reaction even when it uses the same clip', async () => {
    const s = stand([200])
    const pet = overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)
    pet.reactTo('success')
    s.pump(0.19)
    const lateFrame = s.host.querySelector<HTMLElement>('.pet-sprite')!.style.backgroundPosition
    pet.reactTo('success')
    s.pump(0.05)
    const restarted = s.host.querySelector<HTMLElement>('.pet-sprite')!.style.backgroundPosition
    expect(restarted).not.toBe(lateFrame)
  })

  it('derives pixel strides from the loaded trim and body-length factors', async () => {
    const loaded = await loadPack(PACK, '/', IMAGES)
    const timing = timingFrom(PACK, loaded)
    expect(loaded.trim).toEqual({ x0: 3, y0: 5, x1: 7, y1: 9 })
    expect(timing.strides).toEqual({ walk: 4, run: 12 })
    expect(timing.strides.run / timing.strides.walk).toBe(3)
  })

  it('leaves the terminal untouched when the pack will not load', async () => {
    const s = stand([200])
    new PetOverlay({
      host: s.host,
      blocks: s.blocks,
      pack: PACK,
      settings: settingsStub(),
      imageSource: { load: () => Promise.reject(new Error('gone')) },
      raf: (cb) => {
        s.frames.push(cb)
        return s.frames.length
      },
      caf: () => {},
    })
    await vi.waitFor(() => expect(s.host.querySelector('.pet-layer')).toBeNull())
    expect(s.blocks.querySelectorAll('.cmd-block')).toHaveLength(1)
  })

  it('takes its layer with it when disposed', async () => {
    const s = stand([200])
    const pet = overlayOn(s)
    await vi.waitFor(() => expect(s.host.querySelector('.pet-layer')).not.toBeNull())
    pet.dispose()
    expect(s.host.querySelector('.pet-layer')).toBeNull()
  })
})

describe('the setting governs it', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('fetches nothing at all while pets are off', async () => {
    const s = stand([200])
    const load = vi.fn((url: string) => IMAGES.load(url))
    new PetOverlay({
      host: s.host,
      blocks: s.blocks,
      pack: PACK,
      imageSource: { load },
      settings: settingsStub(false),
      raf: (cb) => {
        s.frames.push(cb)
        return s.frames.length
      },
      caf: () => {},
    })
    await new Promise((r) => setTimeout(r, 0))
    // A decoration somebody declined must cost them no bytes.
    expect(load).not.toHaveBeenCalled()
    expect(s.host.querySelector('.pet-layer')).toBeNull()
  })

  it('arrives when the setting is switched on, and leaves when switched off', async () => {
    const s = stand([200])
    const set = settingsStub(false)
    overlayOn(s, () => 0.99, set)
    expect(s.host.querySelector('.pet-layer')).toBeNull()

    set.set({ enabled: true })
    await vi.waitFor(() => expect(s.host.querySelector('.pet-layer')).not.toBeNull())

    set.set({ enabled: false })
    expect(s.host.querySelector('.pet-layer')).toBeNull()
  })

  it('redraws at the new size, and re-measures the ground against it', async () => {
    // A ledge 40px from the top is ground for a 34px cat and a ceiling for a
    // 60px one. Changing the size must re-derive the terrain, not just scale
    // the sprite.
    const s = stand([40, 300])
    const set = settingsStub(true, 34)
    overlayOn(s, () => 0.99, set)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)
    const world = s.host.querySelector<HTMLElement>('.pet-world')!
    expect(world.style.height).toBe('34px')
    const yOf = () => Number(/translate\([-\d.]+px, ([-\d.]+)px\)/.exec(world.style.transform)![1])
    expect(yOf() + 34).toBeCloseTo(40, 0)

    set.set({ height: 60 })
    s.pump(3)
    expect(world.style.height).toBe('60px')
    // The 40px ledge no longer clears a 60px cat, so it fell to the next one.
    expect(yOf() + 60).toBeCloseTo(300, 0)
  })
})

describe('changing which animal', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('fetches the new pack from the new directory', async () => {
    const s = stand([200])
    const set = settingsStub(true, 34, '/ginger/')
    const asked: string[] = []
    new PetOverlay({
      host: s.host,
      blocks: s.blocks,
      pack: PACK,
      settings: set,
      imageSource: {
        load: (url) => {
          asked.push(url)
          return IMAGES.load(url)
        },
      },
      raf: (cb) => {
        s.frames.push(cb)
        return s.frames.length
      },
      caf: () => {},
    })
    await vi.waitFor(() => expect(asked.some((u) => u.startsWith('/ginger/'))).toBe(true))
    set.set({ base: '/black/' })
    await vi.waitFor(() => expect(asked.some((u) => u.startsWith('/black/'))).toBe(true))
  })

  it('keeps the animal it has when the new one will not load', async () => {
    // Losing the pet you had because a colour is missing is a worse outcome
    // than staying on the old one.
    const s = stand([200])
    const set = settingsStub(true, 34, '/ginger/')
    new PetOverlay({
      host: s.host,
      blocks: s.blocks,
      pack: PACK,
      settings: set,
      imageSource: {
        load: (url) =>
          url.startsWith('/ginger/') ? IMAGES.load(url) : Promise.reject(new Error('missing')),
      },
      raf: (cb) => {
        s.frames.push(cb)
        return s.frames.length
      },
      caf: () => {},
    })
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(2)
    set.set({ base: '/nope/' })
    await new Promise((r) => setTimeout(r, 0))
    s.pump(1)
    expect(s.host.querySelector('.pet-layer')).not.toBeNull()
    expect(s.host.querySelector<HTMLElement>('.pet-sprite')!.style.backgroundImage).toContain(
      '/ginger/',
    )
  })
})

describe('news that arrives before the sprites do', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('is not lost when the animal is minted', async () => {
    // Sprites are fetched asynchronously and the terminal does not wait for
    // them. A command that starts in that window reached a pet about to be
    // replaced, and the replacement arrived knowing nothing about the build
    // it was supposed to be watching.
    const s = stand([200])
    let release!: (v: { width: number; height: number; alpha: Uint8ClampedArray }) => void
    const gate = new Promise<{ width: number; height: number; alpha: Uint8ClampedArray }>((r) => {
      release = r
    })
    const pet = new PetOverlay({
      host: s.host,
      blocks: s.blocks,
      pack: PACK,
      settings: settingsStub(),
      imageSource: { load: () => gate },
      // Pinned to the first entry of the watching menu, which for the
      // assistant's lane is sitting up.
      rng: () => 0,
      raf: (cb) => {
        s.frames.push(cb)
        return s.frames.length
      },
      caf: () => {},
    })
    pet.attendTo('agent')
    const width = CELL * 2
    const alpha = new Uint8ClampedArray(width * CELL)
    for (let f = 0; f < 2; f++)
      for (let y = 5; y < 9; y++)
        for (let x = 3; x < 7; x++) alpha[y * width + (f * CELL + x)] = 255
    release({ width, height: CELL, alpha })
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(3)
    // Still watching: the animal that landed knows a command is in flight,
    // so it settles rather than wandering off.
    expect(pet.playing).toBe('sitting')
  })
})

describe('ledges keep their names', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('a block that arrives later never inherits another ledge’s identity', async () => {
    // The counter used to be reset per sweep and advanced only for unmarked
    // elements, so the next new block was handed an id another element
    // already held. The pet then TELEPORTED onto it instead of falling —
    // which is the one thing the identity exists to prevent.
    const s = stand([150, 300])
    overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(2)

    addBlock(s, 450)
    addBlock(s, 500)
    await new Promise((r) => setTimeout(r, 0))
    s.pump(0.3)

    const ids = [...s.blocks.querySelectorAll<HTMLElement>('.cmd-block')].map(
      (e) => e.dataset.petLedge,
    )
    expect(new Set(ids).size).toBe(ids.length)
    expect(ids.every((v) => v !== undefined)).toBe(true)
  })

  it('an element keeps the name it was given across sweeps', async () => {
    const s = stand([150])
    overlayOn(s)
    await vi.waitFor(() => expect(s.frames.length).toBeGreaterThan(0))
    s.pump(2)
    const first = s.blocks.querySelector<HTMLElement>('.cmd-block')!.dataset.petLedge
    addBlock(s, 400)
    await new Promise((r) => setTimeout(r, 0))
    s.pump(0.3)
    expect(s.blocks.querySelector<HTMLElement>('.cmd-block')!.dataset.petLedge).toBe(first)
  })
})
