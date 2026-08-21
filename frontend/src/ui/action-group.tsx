/**
 * ActionGroup — a set of related actions presented as one answer to one
 * question.
 *
 * Built for the approval prompt (nocx-gycwo), which stopped having one yes and
 * one no. A policy question is answered with a direction AND a width — allow
 * or deny, once, in this session or always — and six equal buttons in a single
 * row is a wall a hurried person reads left to right. Grouped, it is two
 * short answers to two obvious questions, and the group a person has already
 * chosen is the only one they have to read across.
 *
 * It is not a Toolbar. A toolbar is a view's action bar and takes the
 * `toolbar` role, which promises arrow-key navigation and ONE tab stop for
 * everything in it; these are peer decisions and every one of them must be its
 * own tab stop. `role="group"` with a name says the truthful thing instead:
 * these belong together, and each is reached the ordinary way.
 *
 * The group names itself for assistive technology and draws no label. A
 * visible heading beside buttons that already read "Allow once" / "Allow
 * always" says "Allow" twice; a screen reader that announces the group on
 * entry says it once, where it is actually missing.
 *
 * Layout only — the buttons inside carry their own kit identity and their own
 * variants. `class` is absent for the usual reason (§3.6): appearance is
 * locked to the kit.
 */
import type { JSX } from 'solid-js'

export interface ActionGroupProps {
  class?: never
  className?: never
  /**
   * Names the group in the accessibility tree. Required, not optional: an
   * unnamed group is announced as a boundary with nothing on the other side
   * of it, which is worse than no group at all.
   */
  ariaLabel: string
  children: JSX.Element
}

export function ActionGroup(props: ActionGroupProps) {
  return (
    <div role="group" class="ui-action-group" aria-label={props.ariaLabel}>
      {props.children}
    </div>
  )
}
