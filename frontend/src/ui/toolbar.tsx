/**
 * Toolbar — horizontal action bar at the top of a view.
 *
 * Justified by callers:
 * - connections.ts: div.cm-header > h1 + action buttons (header toolbar)
 * - settings-content.ts: nav.st-rail (section nav + search + filter toolbar)
 */

import type { JSX } from 'solid-js'

export interface ToolbarProps {
  class?: string
  children: JSX.Element
  ariaLabel?: string
}

export function Toolbar(props: ToolbarProps) {
  return (
    <div role="toolbar" class={props.class ?? ''} aria-label={props.ariaLabel ?? undefined}>
      {props.children}
    </div>
  )
}
