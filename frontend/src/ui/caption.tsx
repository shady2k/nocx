/**
 * Caption — the kit's group-caption register (nocx-dgsp).
 *
 * The app's caption vocabulary for a label over a group of rows: uppercase,
 * letter-spaced, semibold, small, muted. It is the register the existing
 * captions already speak (sidebar.css `.sidebar-title`, floating-panel.css
 * `.ui-floating-panel__group`, connections.css `.cm-group-header`) — this is
 * the one owner of it, so a new surface composes the kit rather than writing
 * a fifth copy.
 *
 * Identity: `.ui-caption`. Surfaces place it (margin, width, position) and
 * never repaint it; the padding that positions a caption inside its rail is
 * the caller's wrapper, not this element.
 *
 * Size: the default pins the register's own size (`--font-size-2xs`) — a
 * standalone caption is fine print. `size="context"` makes the caption track
 * its column instead: the caption takes the surrounding type size, the way
 * the ghost Button's rows already do (`font: inherit`), so a caption inside
 * a rail column reads at the same size as the rows under it (the
 * floating-panel answer: a caption differs from its rows by case, weight,
 * spacing and colour, not by being smaller). The rail uses it because its
 * column is a deliberate size (md wide, sm narrow — a surface editorial)
 * that a pinned rem would undercut in one of the two layouts.
 */

import { splitProps, type JSX } from 'solid-js'

export interface CaptionProps {
  /**
   * Size register. Absent = the caption's own size (`--font-size-2xs`).
   * `'context'` = track the surrounding column (`font-size: inherit`).
   */
  size?: 'context'
  children: JSX.Element
}

type CaptionAttrs = CaptionProps & JSX.IntrinsicElements['span']

export function Caption(props: CaptionAttrs) {
  const [local, rest] = splitProps(props, ['size', 'children'])
  return (
    <span class="ui-caption" data-size={local.size} {...rest}>
      {local.children}
    </span>
  )
}
