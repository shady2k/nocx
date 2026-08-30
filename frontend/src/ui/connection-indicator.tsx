/**
 * ConnectionIndicator — the corner mark that says a pane's connection is in
 * trouble, and nothing at all when it is not.
 *
 * It exists because the reachability axis had no persistent surface. A host
 * that stopped answering was announced with a Toast, which is the wrong shape
 * for it and the product already said so in its own words: "a dropped
 * connection is a persistent condition, so it is not a toast (a toast fades
 * whether or not the condition has come back)" (connection-notice.tsx). A
 * condition needs a mark that lasts exactly as long as the condition.
 *
 * WHY IT OVERLAYS THE TERMINAL, when the integration notice deliberately does
 * not. That notice is a CARD carrying sentences and buttons, and it covered
 * the first prompt line — a card that hides what it describes is worse than
 * the toast it replaced. This is a glyph in a corner, and it is absent in the
 * healthy state, which is almost always. It costs a person nothing until the
 * alternative is not knowing their host has gone quiet; a game's netcode
 * indicator earns its place the same way and appears on the same terms.
 *
 * WHAT IT MAY NOT DO: pretend to be live. The measurement behind it is one
 * keepalive probe every thirty seconds, so there is no animated gauge and no
 * moving bars — a scale that redraws faster than it is measured is a lie about
 * its own resolution. The numbers a person can act on ride the tooltip.
 *
 * WHAT IT DOES NOT OWN: whether the SESSION has ended. That is terminal, has
 * an action attached, and belongs to a StatusCard in the flow where the action
 * can be pressed. This axis is about reaching a host that has not ended.
 */

import { Show } from 'solid-js'
import { AlertTriangleIcon, PlugIcon, ServerIcon } from './icons'

/**
 * What the indicator can say, in severity order.
 *
 * `reachable` is the absent state rather than a green light: a mark that is
 * always on is a mark nobody reads, and "your connection is fine" is not news
 * a terminal needs to repeat.
 */
export type ConnectionCondition = 'reachable' | 'slow' | 'unreachable' | 'lost'

export interface ConnectionIndicatorProps {
  condition: ConnectionCondition
  /**
   * The round trip of the last probe, in milliseconds, or null when nothing
   * measured one. Shown in the tooltip and never as a number on screen — a
   * figure that changes every thirty seconds in the corner of the eye is
   * noise, and the person who wants it is already hovering.
   */
  roundTripMs?: number | null
  /** How long ago that probe was, as words the caller composed. */
  observedAgo?: string
}

/** The sentence for a condition — the product's words, one owner.
 *
 *  It takes the VALUES rather than the props object, so every prop read
 *  happens in the JSX below where Solid can track it. A helper handed `props`
 *  reads them outside a tracked scope and the mark would keep its first
 *  sentence for the life of the pane. */
function sentence(condition: ConnectionCondition): string {
  switch (condition) {
    case 'slow':
      return 'This host is answering slowly'
    case 'unreachable':
      return 'This host has stopped answering — the session may still be running on it'
    case 'lost':
      return 'The connection to this host is gone'
    case 'reachable':
      return ''
  }
}

/** The tooltip: the sentence, then the evidence behind it. */
function detail(
  condition: ConnectionCondition,
  roundTripMs: number | null | undefined,
  observedAgo: string | undefined,
): string {
  const parts = [sentence(condition)]
  if (roundTripMs != null && roundTripMs > 0) {
    parts.push(`last probe ${roundTripMs} ms`)
  }
  if (observedAgo) {
    parts.push(observedAgo)
  }
  return parts.join(' · ')
}

function Glyph(props: { condition: ConnectionCondition }) {
  return (
    <Show when={props.condition === 'slow'} fallback={<PlugIcon />}>
      <ServerIcon />
    </Show>
  )
}

export function ConnectionIndicator(props: ConnectionIndicatorProps) {
  return (
    <Show when={props.condition !== 'reachable'}>
      <span
        class="ui-connection-indicator"
        data-condition={props.condition}
        role="status"
        aria-label={detail(props.condition, props.roundTripMs, props.observedAgo)}
        title={detail(props.condition, props.roundTripMs, props.observedAgo)}
      >
        <Glyph condition={props.condition} />
        <Show when={props.condition === 'lost'}>
          <span class="ui-connection-indicator__overlay" aria-hidden="true">
            <AlertTriangleIcon />
          </span>
        </Show>
      </span>
    </Show>
  )
}
