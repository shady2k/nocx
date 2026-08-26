// The floating Mark button: a selection OFFERS to be marked; only pressing
// the button confirms the GrantBlock derived by ask-entry. The wrapper owns
// placement while the kit Button owns the control's paint.
//
// The wrapper renders at body level because a fixed element inside scrollback
// would ride the settle glide's transform instead of staying viewport-fixed.
import { createSignal } from 'solid-js'
import { render } from 'solid-js/web'
import type { GrantBlock } from './ask-entry'
import { Button } from './ui/button'
import {
  clampMenuPosition,
  EDGE_MARGIN_PX,
  type MenuSize,
  type ViewportSize,
} from './ui/menu-geometry'

/** The gap between the selection's edge and the button. */
export const SELECTION_GAP_PX = 4

/** A selection's last client rect, in viewport coordinates. */
export interface AnchorRect {
  readonly left: number
  readonly top: number
  readonly right: number
  readonly bottom: number
}

export interface MarkAffordance {
  /** The rendered kit Button — the control a person presses. */
  readonly button: HTMLButtonElement
  /** Whether the button is currently on screen. */
  readonly visible: boolean
  /** Position the button near the selection and show the offered grant. */
  show(anchor: AnchorRect, grant: GrantBlock, viewport?: ViewportSize): void
  /** Remove the button from the screen. */
  hide(): void
  /** Tear the button down and remove its wrapper. */
  dispose(): void
}

/** The button's on-screen position for a selection end, measured size and
 * viewport: below the selection's last client rect (right-aligned to its end),
 * flipped above when there is no room below, then clamped to the viewport with
 * EDGE_MARGIN_PX to spare. Exported so the geometry is testable — jsdom
 * measures nothing. */
export function markButtonPosition(
  anchor: AnchorRect,
  size: MenuSize,
  viewport: ViewportSize,
): { left: number; top: number } {
  let top = anchor.bottom + SELECTION_GAP_PX
  if (top + size.height + EDGE_MARGIN_PX > viewport.height) {
    top = anchor.top - SELECTION_GAP_PX - size.height
  }
  return clampMenuPosition({ x: anchor.right, y: top }, size, viewport)
}

function markLabel(grant: GrantBlock): string {
  if (grant.start === undefined || grant.count === undefined) return 'Mark block'
  return `Mark ${grant.count} line${grant.count === 1 ? '' : 's'}`
}

/** Create the affordance: the kit Button rendered into a fixed wrapper
 * appended to document.body. `onMark` receives the exact grant offered by
 * ask-entry; the wrapper's mousedown is prevented so the press never steals
 * the selection or collapses it. */
export function createMarkAffordance(onMark: (grant: GrantBlock) => void): MarkAffordance {
  const wrapper = document.createElement('div')
  wrapper.className = 'mark-affordance'
  // Fixed, inline: viewport coordinates from the selection's client rects map
  // directly, and the contract holds even where the stylesheet is not loaded.
  wrapper.style.position = 'fixed'
  wrapper.style.display = 'none'
  document.body.appendChild(wrapper)

  const [label, setLabel] = createSignal('Mark block')
  let offered: GrantBlock | null = null
  const dispose = render(
    () => (
      <Button
        variant="primary"
        size="sm"
        ariaLabel={`${label()} for the question`}
        onClick={() => {
          if (offered !== null) onMark(offered)
        }}
      >
        {label()}
      </Button>
    ),
    wrapper,
  )
  const button = wrapper.querySelector<HTMLButtonElement>('.ui-button')!
  wrapper.addEventListener('mousedown', (event) => {
    event.preventDefault()
    event.stopPropagation()
  })

  return {
    get button() {
      return button
    },
    get visible() {
      return wrapper.style.display !== 'none'
    },
    show(
      anchor: AnchorRect,
      grant: GrantBlock,
      viewport: ViewportSize = { width: window.innerWidth, height: window.innerHeight },
    ) {
      offered = grant
      setLabel(markLabel(grant))
      // Made visible before measuring: the clamp needs the laid-out size, and
      // the measured size decides the above/below flip before the next paint.
      wrapper.style.display = 'block'
      const rect = wrapper.getBoundingClientRect()
      const { left, top } = markButtonPosition(
        anchor,
        { width: rect.width, height: rect.height },
        viewport,
      )
      wrapper.style.left = `${left}px`
      wrapper.style.top = `${top}px`
    },
    hide() {
      offered = null
      wrapper.style.display = 'none'
    },
    dispose() {
      dispose()
      wrapper.remove()
    },
  }
}
