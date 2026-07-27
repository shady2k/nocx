/**
 * PageRail — optional navigation/filter column. The second named scroll
 * owner (§6.2): may scroll **only** as a bounded rail, and only when its
 * own content exceeds its height. At the narrow breakpoint (640px) the
 * rail stacks above the content and does not scroll independently.
 *
 * Renders `.ui-page__rail`.
 */

import type { JSX } from 'solid-js'

export interface PageRailProps {
  children: JSX.Element
}

export function PageRail(props: PageRailProps) {
  return <div class="ui-page__rail">{props.children}</div>
}
