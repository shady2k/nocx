/**
 * Page — the base layout that every application surface is built on.
 *
 * Fills its `.surface-host`, establishes the flex/min-height chain from
 * §6.1 of the shell-kit design spec, and composes the header, optional
 * leading rail, and the content scroller.
 *
 * API: `leading` prop for the rail rather than child composition, because
 * the rail is structurally a sibling of the body (not content inside it)
 * and the narrow-breakpoint layout needs to control the rail's position
 * explicitly. A child-detection approach would require filtering and would
 * break if children were wrapped in a fragment.
 */

import { Show } from 'solid-js'
import type { JSX } from 'solid-js'
import { PageHeader } from './page-header'
import { PageBody } from './page-body'
import { PageRail } from './page-rail'
import { PageScroller, type PageScrollerHandle } from './page-scroller'

export type { PageScrollerHandle }

export interface PageProps {
  title: string
  description?: string
  actions?: JSX.Element
  /** Optional rail content — placed in `.ui-page__rail`. Pass this as
   *  plain JSX; Page wraps it in the rail container internally. */
  leading?: JSX.Element
  /** Exposes the PageScroller handle for `scrollToElement()` calls. */
  scrollerRef?: PageScrollerHandle | ((h: PageScrollerHandle) => void)
  children: JSX.Element
}

export function Page(props: PageProps) {
  return (
    <div class="ui-page">
      <PageHeader title={props.title} description={props.description} actions={props.actions} />
      <PageBody>
        <Show when={props.leading}>
          <PageRail>{props.leading}</PageRail>
        </Show>
        <PageScroller handle={props.scrollerRef}>{props.children}</PageScroller>
      </PageBody>
    </div>
  )
}
