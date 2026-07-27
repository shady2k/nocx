/**
 * PageSection — a titled section within a Page, with an optional anchor id
 * for deep linking. Uses `<section>` for semantics.
 *
 * Overlaps with the existing `Section` component (same h2+children pattern)
 * but differs in:
 * - Uses `<section>` not `<div>`
 * - Has `id` for anchor-based scroll targeting
 * - Gets page-specific spacing from surface.css
 *
 * They are deliberately not merged; the coordinator decides.
 */

import type { JSX } from 'solid-js'

export interface PageSectionProps {
  id?: string
  title: string
  class?: string
  children: JSX.Element
}

export function PageSection(props: PageSectionProps) {
  return (
    <section id={props.id} class={props.class ?? ''}>
      <h2>{props.title}</h2>
      {props.children}
    </section>
  )
}
