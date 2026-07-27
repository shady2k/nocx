/**
 * SidebarView — panel view layout for sidebar views (Explorer, Git, …).
 *
 * Same idea as Page but simpler: header with title and actions, optional
 * filter row, scrolling body. Designed for `nocx-708q`; must not assume
 * anything specific to tabs.
 *
 * CSS lives in surface.css because every `styles/` file in this wave
 * is owned by nocx-82l9.2 / nocx-imkb.1.
 */

import { Show } from 'solid-js'
import type { JSX } from 'solid-js'

export interface SidebarViewProps {
  title: string
  actions?: JSX.Element
  /** Optional filter/search row rendered between the header and body. */
  filter?: JSX.Element
  children: JSX.Element
}

export function SidebarView(props: SidebarViewProps) {
  return (
    <div class="ui-sidebar-view">
      <div class="ui-sidebar-view__header">
        <h2>{props.title}</h2>
        <Show when={props.actions}>
          <div class="ui-sidebar-view__actions">{props.actions}</div>
        </Show>
      </div>
      <Show when={props.filter}>
        <div class="ui-sidebar-view__filter">{props.filter}</div>
      </Show>
      <div class="ui-sidebar-view__body">{props.children}</div>
    </div>
  )
}
