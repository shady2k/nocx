/**
 * Badge — small inline label showing status, category or count.
 *
 * Replaces ad-hoc markup like .st-provenance, .st-section-nav-badge.
 *
 * Justified by callers:
 * - settings.ts: .st-provenance / .st-customized / .st-default (Customized/Default badge)
 * - settings.ts: .st-section-nav-badge (modified-only count)
 * - connections.ts: credential auth method chip
 */

export type BadgeVariant = 'default' | 'warning' | 'danger' | 'info'

export interface BadgeProps {
  class?: string
  children: string
  variant?: BadgeVariant
}

const VARIANT_CLASS: Record<BadgeVariant, string> = {
  default: '',
  warning: 'ui-badge-warning',
  danger: 'ui-badge-danger',
  info: 'ui-badge-info',
}

export function Badge(props: BadgeProps) {
  const variantClass = () => VARIANT_CLASS[props.variant ?? 'default']
  return <span class={`${variantClass()} ${props.class ?? ''}`.trim()}>{props.children}</span>
}
