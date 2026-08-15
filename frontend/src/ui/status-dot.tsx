/**
 * StatusDot — a coloured dot that says out loud what its colour means.
 *
 * The dot alone is decoration: a screen-reader user is told "System keychain"
 * with no hint that it is the store which is not answering. So the visible dot
 * is `aria-hidden` and the meaning travels in a visually-hidden span beside it,
 * which is why the two cannot be separated and why this is a component rather
 * than a CSS class each caller applies for itself.
 *
 * Lived inside Tabs as `.ui-tabs__status` until the Vault store list needed the
 * same thing outside a tab. Copying it into the surface would have put the
 * `ui-visually-hidden` class in markup — which the no-inline-markup rule
 * forbids, and forbids precisely so that the accessible half does not get
 * dropped by the next person to copy the dot.
 *
 * Renders a fragment, not a wrapper: the dot has to be a flex child of whatever
 * row it sits in, and a wrapper would take that place instead.
 *
 * It takes the label it marks as its children rather than sitting beside it,
 * which is what fixes the reading order. The dot must come first visually and
 * the state must come last audibly — "Test Store, not responding", not "Not
 * responding, Test Store" — and only something that holds both ends can put
 * the hidden name on the far side of the label.
 */

import type { JSX } from 'solid-js'

export type StatusDotTone = 'ok' | 'warning' | 'error' | 'neutral'

export interface StatusDotProps {
  tone: StatusDotTone
  /** What the colour means, in words. Read by assistive technology. */
  accessibleName: string
  /** The label the dot marks. */
  children: JSX.Element
}

export function StatusDot(props: StatusDotProps) {
  return (
    <>
      <span class="ui-status-dot" data-tone={props.tone} aria-hidden="true" />
      {props.children}
      <span class="ui-visually-hidden">{props.accessibleName}</span>
    </>
  )
}
