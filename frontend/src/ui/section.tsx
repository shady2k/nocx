/**
 * Section — titled grouping of controls or content in a view.
 *
 * Justified by callers:
 * - settings.ts: div.st-section > h2.st-section-heading + rows
 * - connections.ts: div.cm-form-section > h2 + fields
 *
 * Children are spaced by the Stack primitive (one source of truth for vertical
 * rhythm). No `class` passthrough — see page-section.tsx for why the structural
 * containers stopped accepting one.
 */
import type { JSX } from 'solid-js'
import { Stack } from './stack'

export interface SectionProps {
  id?: string
  title: string
  /** When true, forwards to the inner Stack to draw separators between children. */
  divided?: boolean
  children: JSX.Element
}

export function Section(props: SectionProps) {
  return (
    <section id={props.id} class="ui-section">
      <h2>{props.title}</h2>
      <Stack gap="default" divided={props.divided}>
        {props.children}
      </Stack>
    </section>
  )
}
