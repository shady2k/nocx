/**
 * Stack — vertical rhythm for kit controls.
 *
 * A flex column whose `gap` comes from the space scale. Every child is evenly
 * spaced; surfaces must not add their own margins between kit components
 * (enforced by the surface-spacing-kit lint rule).
 *
 * Gap vocabulary (fewer is better — two named steps):
 * - `default` → var(--space-4, 16px) — standard control-to-control rhythm.
 * - `loose`   → var(--space-6, 24px) — between independent groups.
 *
 * No `class` prop — appearance is locked to the kit (§3.6). Layout and
 * placement belong to a parent wrapper or a typed prop.
 */
import type { JSX } from 'solid-js'

export type StackGap = 'default' | 'loose'

export interface StackProps {
  class?: never
  className?: never
  children: JSX.Element
  id?: string
  /**
   * Vertical gap between children. Defaults to `default` (var(--space-4)).
   */
  gap?: StackGap
  /**
   * When true, draws a hairline and adds vertical padding between children.
   * The separator uses `> :not(.st-vis-hidden) ~ :not(.st-vis-hidden)` so it
   * stays correct when search filtering hides rows.
   */
  divided?: boolean
}

export function Stack(props: StackProps) {
  return (
    <div
      id={props.id}
      class="ui-stack"
      data-gap={props.gap ?? 'default'}
      data-divided={props.divided ? 'true' : undefined}
    >
      {props.children}
    </div>
  )
}
