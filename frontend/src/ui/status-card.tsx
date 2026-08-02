/**
 * StatusCard — a state, and the one thing to do about it.
 *
 * For the case where a surface's headline fact is not a control but a
 * condition: the vault is locked, the connection is refusing, the update is
 * ready. Rendering that as a paragraph with a button after it — which is what
 * the Vault page did — makes the most important sentence on the page the least
 * prominent thing on it, because a paragraph is what body copy looks like.
 *
 * Not EmptyState. EmptyState says "there is nothing here" and centres itself in
 * the space that would have held the list; a StatusCard sits at the top of a
 * page that has plenty else on it and stays out of the way of the rest.
 *
 * The action is a slot rather than a label + handler: which button variant a
 * state deserves is the caller's decision (primary to unlock, danger to
 * disconnect), and threading `variant` through would just re-export Button's
 * API one level up.
 */

import { Show, type JSX } from 'solid-js'

export type StatusCardTone = 'neutral' | 'ok' | 'warning' | 'danger'

export interface StatusCardProps {
  /** Optional glyph. Omitted entirely when absent — see the icon Show below. */
  icon?: JSX.Element
  title: string
  description?: string
  /** The single action for this state. Usually a kit Button. */
  action?: JSX.Element
  tone?: StatusCardTone
}

export function StatusCard(props: StatusCardProps) {
  return (
    <div class="ui-status-card" data-tone={props.tone ?? 'neutral'}>
      {/* The slot is absent, not empty, when there is no icon: an empty flex
          child still takes its gap, and the text would then sit indented from
          a column that holds nothing. */}
      <Show when={props.icon !== undefined}>
        <div class="ui-status-card__icon">{props.icon}</div>
      </Show>
      <div class="ui-status-card__body">
        <p class="ui-status-card__title">{props.title}</p>
        <Show when={props.description !== undefined}>
          <p class="ui-status-card__desc">{props.description}</p>
        </Show>
      </div>
      <Show when={props.action !== undefined}>
        <div class="ui-status-card__action">{props.action}</div>
      </Show>
    </div>
  )
}
