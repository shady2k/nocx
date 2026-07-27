/**
 * Section — titled grouping of controls or content in a view.
 *
 * Justified by callers:
 * - settings.ts: div.st-section > h2.st-section-heading + rows
 * - connections.ts: div.cm-form-section > h2 + fields
 * - export-section.ts: div.st-export wrapper with heading + description
 */

import type { JSX } from 'solid-js'
export interface SectionProps {
  class?: string
  id?: string
  title: string
  children: JSX.Element
}

export function Section(props: SectionProps) {
  return (
    <section id={props.id} class={props.class ?? ''}>
      <h2>{props.title}</h2>
      {props.children}
    </section>
  )
}
