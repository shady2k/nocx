// The sprite pack, and what it costs to load one (nocx-q4qeh.1).
//
// A pack is a set of horizontal strips: one PNG per clip, N cells of the same
// size laid out left to right. That is the shape the CC0 pack in
// `public/pets/cat/` already has, and it is the shape a user-supplied pack
// will have to declare, so the description lives here rather than being
// inferred from the files.
//
// The one non-obvious part is the TRIM. The cat occupies about 30×16 of a
// 50×50 cell; drawn cell-aligned it floats a third of a cell above whatever
// it is standing on. So the loader scans the alpha channel and crops — but it
// crops by the UNION over every frame of every clip, never per frame.
// Per-frame cropping re-centres the animal on itself, which flattens the
// walk cycle's bob and deletes the crouch that makes the stretch read as a
// stretch. One trim for the whole pack keeps the frames in the relationship
// the artist drew them in.

import type { Activity, Locomotion } from './pet'

interface Clip {
  /** File name inside the pack directory. */
  readonly file: string
  readonly frames: number
}

export interface PetPack {
  readonly id: string
  /** Square cell edge, in source pixels. */
  readonly cell: number
  readonly fps: number
  readonly clips: Readonly<Record<string, Clip>>
  /** Which clip plays for a given locomotion. */
  readonly locomotion: Readonly<Record<Locomotion, string>>
  /** Which clip plays for a given activity, when standing still. */
  readonly activity: Readonly<Record<Exclude<Activity, 'none'>, string>>
}

/** The pack that ships in the box: luizmelo's Pet Cats, CC0.
 *  See `public/pets/cat/SOURCE.md` for provenance. */
export const CAT_PACK: PetPack = {
  id: 'cat',
  cell: 50,
  fps: 10,
  clips: {
    idle: { file: 'idle.png', frames: 10 },
    walk: { file: 'walk.png', frames: 8 },
    run: { file: 'run.png', frames: 8 },
    meow: { file: 'meow.png', frames: 4 },
    laying: { file: 'laying.png', frames: 8 },
    itch: { file: 'itch.png', frames: 2 },
    licking: { file: 'licking1.png', frames: 5 },
    sitting: { file: 'sitting.png', frames: 1 },
    sleeping: { file: 'sleeping1.png', frames: 1 },
    stretching: { file: 'stretching.png', frames: 13 },
  },
  locomotion: {
    idle: 'idle',
    walk: 'walk',
    run: 'run',
    // No falling cat was drawn. The run cycle reads as scrabbling, which is
    // closer than any still pose.
    fall: 'run',
  },
  activity: {
    sit: 'sitting',
    groom: 'licking',
    stretch: 'stretching',
    lie: 'laying',
    scratch: 'itch',
    meow: 'meow',
    sleep: 'sleeping',
  },
}

/** The rectangle inside a cell that any frame of the pack actually paints. */
interface Trim {
  readonly x0: number
  readonly y0: number
  readonly x1: number
  readonly y1: number
}

interface LoadedClip {
  readonly url: string
  readonly frames: number
  readonly sheetWidth: number
  readonly sheetHeight: number
}

export interface LoadedPack {
  readonly pack: PetPack
  readonly clips: Readonly<Record<string, LoadedClip>>
  readonly trim: Trim
}

/** Injected so the loader can be exercised without a browser. */
export interface ImageSource {
  load(url: string): Promise<{ width: number; height: number; alpha: Uint8ClampedArray }>
}

/** The browser's decoder, reading the alpha plane through a canvas. */
const domImageSource: ImageSource = {
  async load(url) {
    const img = new Image()
    await new Promise<void>((resolve, reject) => {
      img.onload = () => resolve()
      img.onerror = () => reject(new Error(`pet sprite failed to load: ${url}`))
      img.src = url
    })
    const canvas = document.createElement('canvas')
    canvas.width = img.naturalWidth
    canvas.height = img.naturalHeight
    const ctx = canvas.getContext('2d', { willReadFrequently: true })
    if (!ctx) throw new Error('pet sprite: no 2d context')
    ctx.drawImage(img, 0, 0)
    const data = ctx.getImageData(0, 0, canvas.width, canvas.height).data
    const alpha = new Uint8ClampedArray(canvas.width * canvas.height)
    for (let i = 0; i < alpha.length; i++) alpha[i] = data[i * 4 + 3]
    return { width: canvas.width, height: canvas.height, alpha }
  },
}

const OPAQUE_ENOUGH = 8

/**
 * Load every clip of a pack and compute the one trim they share.
 *
 * `base` is the directory the pack's files sit in, with a trailing slash.
 */
export async function loadPack(
  pack: PetPack,
  base: string,
  source: ImageSource | undefined = domImageSource,
): Promise<LoadedPack> {
  const clips: Record<string, LoadedClip> = {}
  let x0 = Infinity
  let y0 = Infinity
  let x1 = -Infinity
  let y1 = -Infinity

  for (const [name, clip] of Object.entries(pack.clips)) {
    const url = base + clip.file
    const img = await (source ?? domImageSource).load(url)
    clips[name] = {
      url,
      frames: clip.frames,
      sheetWidth: img.width,
      sheetHeight: img.height,
    }
    for (let y = 0; y < img.height; y++) {
      for (let x = 0; x < img.width; x++) {
        if (img.alpha[y * img.width + x] < OPAQUE_ENOUGH) continue
        const cx = x % pack.cell
        if (cx < x0) x0 = cx
        if (cx + 1 > x1) x1 = cx + 1
        if (y < y0) y0 = y
        if (y + 1 > y1) y1 = y + 1
      }
    }
  }

  const trim: Trim =
    x1 > x0 && y1 > y0 ? { x0, y0, x1, y1 } : { x0: 0, y0: 0, x1: pack.cell, y1: pack.cell }
  return { pack, clips, trim }
}

/** Which clip a pet in this state should be playing. */
export function clipFor(pack: PetPack, locomotion: Locomotion, activity: Activity): string {
  if (locomotion === 'idle' && activity !== 'none') return pack.activity[activity]
  return pack.locomotion[locomotion]
}
