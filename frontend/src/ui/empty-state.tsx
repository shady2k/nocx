/**
 * EmptyState — centered placeholder for empty lists and idle panels.
 *
 * Replaced the ad-hoc markup that used to do this in connections.tsx — a
 * `div.cm-list-empty` and an inline-styled div — and both are gone, along with
 * the `.cm-list-empty` rule they left behind in style.css.
 *
 * Two visual modes:
 * - Standard: title + description, centered in the available space.
 * - With action: an additional action button below the description.
 *
 * No `class` prop (§3.6). Placement belongs to whatever contains this, not to a
 * class threaded through it: neither of the two call sites ever passed one.
 */

import type { JSX } from 'solid-js'
import { Show } from 'solid-js'

export interface EmptyStateProps {
  /** Optional glyph above the title. An empty page with nothing but two lines
   *  of centred text on it reads as a load that failed; a glyph says the
   *  emptiness is deliberate and is the message. */
  icon?: JSX.Element
  title: string
  description?: string
  /** Optional action button rendered below the description. */
  action?: JSX.Element
}

export function EmptyState(props: EmptyStateProps) {
  return (
    <div class="ui-empty-state">
      <Show when={props.icon !== undefined}>
        <div class="ui-empty-state__icon">{props.icon}</div>
      </Show>
      <p class="ui-empty-state__title">{props.title}</p>
      <Show when={props.description !== undefined}>
        <p class="ui-empty-state__desc">{props.description}</p>
      </Show>
      <Show when={props.action !== undefined}>
        <div class="ui-empty-state__action">{props.action}</div>
      </Show>
    </div>
  )
}
