/**
 * EmptyState — centered placeholder for empty lists and idle panels.
 *
 * Replaces ad-hoc markup:
 * - connections.ts: div.cm-list-empty (text: "No connections yet…")
 * - connections.ts: inline-styled div (text: "Select a connection to edit…")
 *
 * Two visual modes:
 * - Standard: title + description, centered in the available space.
 * - With action: an additional action button below the description.
 */

import type { JSX } from 'solid-js'
import { Show } from 'solid-js'

export interface EmptyStateProps {
  class?: string
  title: string
  description?: string
  /** Optional action button rendered below the description. */
  action?: JSX.Element
}

export function EmptyState(props: EmptyStateProps) {
  return (
    <div class={`ui-empty-state ${props.class ?? ''}`.trim()}>
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
