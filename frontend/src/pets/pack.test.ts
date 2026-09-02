// The pack loader (nocx-q4qeh.1). The image source is injected, so the trim
// rule is testable without a canvas.
import { describe, expect, it } from 'vitest'
import { CAT_PACK, clipFor, loadPack, type ImageSource, type PetPack } from './pack'

/** A strip of `frames` cells, painting one rectangle per cell at the offsets
 *  given. Anything not listed stays transparent. */
function strip(
  cell: number,
  frames: number,
  boxes: readonly { x: number; y: number; w: number; h: number }[],
) {
  const width = cell * frames
  const alpha = new Uint8ClampedArray(width * cell)
  boxes.forEach((b, i) => {
    for (let y = b.y; y < b.y + b.h; y++) {
      for (let x = b.x; x < b.x + b.w; x++) {
        alpha[y * width + (i * cell + x)] = 255
      }
    }
  })
  return { width, height: cell, alpha }
}

const TINY: PetPack = {
  id: 'tiny',
  cell: 10,
  fps: 8,
  clips: {
    idle: { mode: 'loop', pause: 0, takes: [{ file: 'idle.png', frames: 2 }] },
    walk: { mode: 'loop', takes: [{ file: 'walk.png', frames: 2 }] },
  },
  locomotion: { idle: 'idle', walk: 'walk', run: 'walk', fall: 'walk' },
  activity: {
    sit: 'idle',
    groom: 'idle',
    stretch: 'idle',
    lie: 'idle',
    scratch: 'idle',
    meow: 'idle',
    sleep: 'idle',
  },
  strideBodies: { walk: 1, run: 3 },
}

function sourceOf(sheets: Record<string, ReturnType<typeof strip>>): ImageSource {
  return {
    load(url) {
      const key = url.slice(url.lastIndexOf('/') + 1)
      const s = sheets[key]
      if (!s) return Promise.reject(new Error(`no fixture for ${url}`))
      return Promise.resolve(s)
    },
  }
}

describe('loadPack', () => {
  it('crops the padding the artist left around the animal', async () => {
    const loaded = await loadPack(
      TINY,
      '/p/',
      sourceOf({
        'idle.png': strip(10, 2, [
          { x: 3, y: 5, w: 4, h: 4 },
          { x: 3, y: 5, w: 4, h: 4 },
        ]),
        'walk.png': strip(10, 2, [
          { x: 3, y: 5, w: 4, h: 4 },
          { x: 3, y: 5, w: 4, h: 4 },
        ]),
      }),
    )
    expect(loaded.trim).toEqual({ x0: 3, y0: 5, x1: 7, y1: 9 })
  })

  it('takes the UNION over every frame, so motion inside a clip survives', async () => {
    // The walk bobs by two pixels. Cropping per frame would flatten it; the
    // union keeps both extremes inside one box.
    const loaded = await loadPack(
      TINY,
      '/p/',
      sourceOf({
        'idle.png': strip(10, 2, [
          { x: 3, y: 5, w: 4, h: 4 },
          { x: 3, y: 5, w: 4, h: 4 },
        ]),
        'walk.png': strip(10, 2, [
          { x: 2, y: 3, w: 4, h: 4 },
          { x: 4, y: 5, w: 4, h: 4 },
        ]),
      }),
    )
    expect(loaded.trim).toEqual({ x0: 2, y0: 3, x1: 8, y1: 9 })
  })

  it('falls back to the whole cell when a pack paints nothing', async () => {
    const blank = strip(10, 2, [])
    const loaded = await loadPack(TINY, '/p/', sourceOf({ 'idle.png': blank, 'walk.png': blank }))
    expect(loaded.trim).toEqual({ x0: 0, y0: 0, x1: 10, y1: 10 })
  })

  it('reports the sheet geometry each clip was loaded with', async () => {
    const loaded = await loadPack(
      TINY,
      '/p/',
      sourceOf({
        'idle.png': strip(10, 2, [{ x: 1, y: 1, w: 2, h: 2 }]),
        'walk.png': strip(10, 2, [{ x: 1, y: 1, w: 2, h: 2 }]),
      }),
    )
    expect(loaded.clips.idle.takes[0]).toMatchObject({
      url: '/p/idle.png',
      frames: 2,
      sheetWidth: 20,
    })
  })

  it('keeps a clip’s other takes when one of its sheets is missing', async () => {
    // A behaviour drawn twice, with one drawing absent: the animal keeps
    // doing it, from the take that loaded.
    const twice: PetPack = {
      ...TINY,
      clips: {
        idle: {
          mode: 'loop',
          takes: [
            { file: 'idle.png', frames: 2 },
            { file: 'idle2.png', frames: 2 },
          ],
        },
        walk: { mode: 'loop', takes: [{ file: 'walk.png', frames: 2 }] },
      },
    }
    const loaded = await loadPack(
      twice,
      '/p/',
      sourceOf({
        'idle.png': strip(10, 2, [{ x: 3, y: 5, w: 4, h: 4 }]),
        'walk.png': strip(10, 2, [{ x: 3, y: 5, w: 4, h: 4 }]),
      }),
    )
    expect(loaded.clips.idle.takes).toHaveLength(1)
    expect(loaded.clips.idle.takes[0].url).toBe('/p/idle.png')
  })

  it('keeps the rest of the pack when one sheet is missing', async () => {
    // Not every colour draws every clip — Cat-3 has no scratch — and losing
    // the whole animal over one absent behaviour is the wrong trade.
    const loaded = await loadPack(
      TINY,
      '/p/',
      sourceOf({ 'idle.png': strip(10, 2, [{ x: 3, y: 5, w: 4, h: 4 }]) }),
    )
    expect(Object.keys(loaded.clips)).toEqual(['idle'])
    expect(loaded.trim).toEqual({ x0: 3, y0: 5, x1: 7, y1: 9 })
  })

  it('refuses a pack with no usable sprite at all', async () => {
    await expect(loadPack(TINY, '/p/', sourceOf({}))).rejects.toThrow(/no usable sprites/)
  })
})

describe('clipFor', () => {
  it('an activity wins while the pet stands still', () => {
    expect(clipFor(CAT_PACK, 'idle', 'stretch')).toBe('stretching')
    expect(clipFor(CAT_PACK, 'idle', 'sleep')).toBe('sleeping')
  })

  it('locomotion wins while the pet is moving', () => {
    expect(clipFor(CAT_PACK, 'walk', 'sleep')).toBe('walk')
    expect(clipFor(CAT_PACK, 'run', 'sit')).toBe('run')
  })

  it('degrades to idle for a behaviour this colour did not draw', () => {
    // The honest reading of a missing sheet: the animal simply does not do
    // that thing, rather than showing a blank frame while it tries.
    const have = { idle: 1, walk: 1 }
    expect(clipFor(CAT_PACK, 'idle', 'scratch', have)).toBe('idle')
    expect(clipFor(CAT_PACK, 'walk', 'none', have)).toBe('walk')
  })

  it('every activity and locomotion names a clip the pack actually has', () => {
    const names = new Set(Object.keys(CAT_PACK.clips))
    for (const c of Object.values(CAT_PACK.locomotion)) expect(names).toContain(c)
    for (const c of Object.values(CAT_PACK.activity)) expect(names).toContain(c)
  })
  it('declares clip playback modes, idle pause, and measured gait strides', () => {
    for (const name of ['idle', 'walk', 'run', 'laying']) {
      expect(CAT_PACK.clips[name]).toMatchObject({ mode: 'loop' })
    }
    for (const name of ['meow', 'stretching', 'itch', 'licking']) {
      expect(CAT_PACK.clips[name]).toMatchObject({ mode: 'once' })
    }
    for (const name of ['sitting', 'sleeping']) {
      expect(CAT_PACK.clips[name]).toMatchObject({ mode: 'hold' })
    }
    expect(CAT_PACK.clips.idle).toMatchObject({ pause: 0.8 })
    // Strides are body-length coefficients; timingFrom applies the loaded
    // trim width, so the run/walk ratio remains three.
    expect(CAT_PACK.strideBodies).toEqual({ walk: 1, run: 3 })
  })
})
