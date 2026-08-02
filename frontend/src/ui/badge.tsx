/**
 * Badge — small inline label showing status, category or count.
 *
 * Replaces ad-hoc markup like .st-provenance, .st-section-nav-badge.
 *
 * Justified by callers:
 * - settings.ts: .st-provenance / .st-customized / .st-default (Customized/Default badge)
 * - settings.ts: .st-section-nav-badge (modified-only count)
 *
 * Per §3.1: class="ui-badge" always, variance on data-tone.
 */

export type BadgeTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'

export interface BadgeProps {
  children: string
  tone?: BadgeTone
}

export function Badge(props: BadgeProps) {
  return (
    <span class="ui-badge" data-tone={props.tone ?? 'neutral'}>
      {props.children}
    </span>
  )
}
