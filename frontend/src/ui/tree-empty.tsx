/**
 * TreeEmpty — what is inside an open folder that holds nothing.
 *
 * It is the ABSENCE of a row, not a row. A tree cannot say "this folder is
 * empty" by drawing nothing, because the rows below an open folder are its
 * SIBLINGS: they sit at its depth with their icons in its icon's column, so
 * an empty folder reads as their parent and its emptiness reads as "these are
 * what is in it". The owner hit exactly that — a folder made a minute earlier
 * appeared to have swallowed every request in the collection.
 *
 * Indentation cannot answer it and neither can an indent guide. Both draw a
 * level, and the whole problem is that there is nothing at the level. The only
 * honest signal is the absence, stated, one step in.
 *
 * WHY IT IS NOT A `TreeRow`. There is no entry here to be a row about: nothing
 * to open, select, act on or expand. A `treeitem` that answers none of the
 * questions a treeitem is asked is worse than no treeitem, so this is
 * `aria-hidden` and carries no role — a reader walking the tree hears
 * `aria-level`, which was never ambiguous. The confusion this fixes is a
 * SIGHTED one.
 *
 * Indentation is driven by the depth number and the step is TreeRow's own
 * custom property, so the two cannot drift apart.
 */

/** Where the missing contents would have been: the folder's depth plus one. */
export interface TreeEmptyProps {
  depth: number
  /** What to call it. The default is the word for the ordinary case; a
   *  surface with a truer one (a filtered listing, a permission) passes it. */
  label?: string
}

export function TreeEmpty(props: TreeEmptyProps) {
  return (
    <p
      class="ui-tree-empty"
      data-depth={props.depth}
      style={{ '--tree-row-depth': String(props.depth) }}
      aria-hidden="true"
    >
      {props.label ?? 'Empty'}
    </p>
  )
}
