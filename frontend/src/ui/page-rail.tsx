/**
 * PageRail — optional navigation/filter column. The second named scroll
 * owner (§6.2): may scroll **only** as a bounded rail, and only when its
 * own content exceeds its height. At the narrow breakpoint (640px) the
 * rail stacks above the content and does not scroll independently, and its
 * chrome goes compact (base.css) — a stacked rail's footprint competes with
 * the content for the pane's height, so the rail trims itself rather than
 * asking the hosting surface to repaint it.
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
