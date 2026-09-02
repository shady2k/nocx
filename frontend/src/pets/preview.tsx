/**
 * PetPreview — the animal, on the settings page, before you commit to it
 * (nocx-q4qeh.1).
 *
 * It runs the REAL overlay over a mock scrollback rather than showing a
 * picture of one frame. That is the whole point: a preview drawn separately
 * would be a second implementation of how the pet looks, agreeing with the
 * terminal until the day one of them changed. Here the size, the colour, the
 * gait, the head clearance and the fall are the same code answering the same
 * settings, so the preview cannot flatter the thing it previews.
 *
 * The mock ledges carry their own class instead of borrowing `.cmd-block`.
 * Borrowing it would pull the scrollback's stylesheet into the settings page
 * for the sake of two rectangles, and would make this surface a second owner
 * of what a command block looks like.
 */
import { onCleanup, onMount } from 'solid-js'
import { PetOverlay } from './overlay'

// The chips are ground here for the same reason they are ground in the
// terminal: they are what the animal is seen to climb onto.
const LEDGE = '.pet-preview__ledge, .pet-preview__chip'

export function PetPreview() {
  let stage!: HTMLDivElement
  let ground!: HTMLDivElement

  onMount(() => {
    const overlay = new PetOverlay({
      host: stage,
      blocks: ground,
      ledgeSelector: LEDGE,
    })
    // A pet that never reacted here would be a preview of half the feature —
    // the mood is the reason the animal is in a terminal rather than on a
    // desktop. Success and failure alternate slowly enough to read.
    let ok = true
    const beat = setInterval(() => {
      overlay.reactTo(ok ? 'success' : 'failure')
      ok = !ok
    }, 6000)
    onCleanup(() => {
      clearInterval(beat)
      overlay.dispose()
    })
  })

  return (
    <div class="pet-preview" ref={stage}>
      <div class="pet-preview__scroll" ref={ground}>
        <div class="pet-preview__ledge">
          <span class="pet-preview__chip">~/nocx</span>
          <span class="pet-preview__cmd">go build ./...</span>
          <span class="pet-preview__chip pet-preview__chip--ok">ok</span>
        </div>
        <div class="pet-preview__ledge">
          <span class="pet-preview__chip">~/nocx</span>
          <span class="pet-preview__cmd">go test ./internal/...</span>
          <span class="pet-preview__chip pet-preview__chip--bad">exit 1</span>
        </div>
      </div>
    </div>
  )
}
