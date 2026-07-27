/**
 * PageHeader — title, optional description, and optional actions bar for a
 * page. Renders `.ui-page__header`.
 */

import { Show } from 'solid-js'
import type { JSX } from 'solid-js'

export interface PageHeaderProps {
  title: string
  description?: string
  actions?: JSX.Element
}

export function PageHeader(props: PageHeaderProps) {
  return (
    <div class="ui-page__header">
      <h1>{props.title}</h1>
      <Show when={props.description}>
        <p>{props.description}</p>
      </Show>
      <Show when={props.actions}>
        <div class="ui-page__header-actions">{props.actions}</div>
      </Show>
    </div>
  )
}
