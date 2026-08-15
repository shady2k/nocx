// Frozen frame minting (spec §2.2, bead nocx-3j9b).
//
// A frozen block has NO xterm cells left — they are cleared after the visual
// freeze — and its text has already been transformed by the serializer
// (wrapped lines joined, leading/trailing blanks dropped). So a frozen frame
// is TEXT, and its provenance records source=frozen and the SERIALIZER
// VERSION. The same gesture, the same frame shape, a different recorded
// source — the two are never silently substituted for one another.
//
// AD-8: this module defines the SEAM and satisfies it from what already
// exists. blockOutputText (scrollback/blocks.ts) owns "block output as text
// with line breaks restored"; SERIALIZER_VERSION (scrollback/serializer.ts)
// owns the transform version. Nothing here re-derives VT output.

import { blockOutputText } from '../scrollback/blocks'
import { SERIALIZER_VERSION } from '../scrollback/serializer'
import type { CapturedFrame } from './types'

/** The frozen source seam: what a frozen frame can be minted from. */
export interface FrozenFrameSource {
  /** The block's output with line breaks restored — `.term-line` spans
   *  joined by '\n' (the blockOutputText contract). */
  text(): string
  /** The serializer version that produced the block's HTML. */
  serializerVersion: number
}

/** Satisfy the seam from a frozen block's DOM element — the block itself,
 *  which is what the user actually sees. blockOutputText is asked of the
 *  block (it resolves the output element, the fact the kind owns);
 *  null yields an empty frame. */
export function frozenFrameSourceFromBlock(blockEl: HTMLElement | null): FrozenFrameSource {
  return {
    text: () => blockOutputText(blockEl),
    serializerVersion: SERIALIZER_VERSION,
  }
}

/** Mint a frozen frame: text rows, no cursor, provenance recording
 *  source=frozen and the serializer version. The identity is closed — a
 *  frozen block's generation never advances again, so it can never go
 *  stale. */
export function mintFrozenFrame(source: FrozenFrameSource): CapturedFrame {
  // '' means NO output (an empty block element) — zero rows. A text that
  // genuinely holds a blank line keeps it: split('\n') preserves it.
  const text = source.text()
  return {
    rows: text === '' ? [] : text.split('\n').map((row) => ({ kind: 'text', text: row })),
    cursor: null,
    provenance: {
      source: 'frozen',
      serializerVersion: source.serializerVersion,
      transforms: 'wrapped lines joined; leading/trailing blanks dropped',
      closed: true,
    },
  }
}
