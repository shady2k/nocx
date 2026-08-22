// The Files panel's name filter (nocx-708q.2) — one predicate and one
// narrowing, both here, both with their own tests.
//
// ## This is the third panel to want a filter and the second to ship without
//
// Ports had one, Git shipped without one and got it back as nocx-52by, and
// the file-manager design put a name filter in scope for this panel and it
// never arrived. So this is deliberately built the way Git's was — the same
// predicate semantics (`git/git-filter.ts`), the kit's `SearchField`, and
// Escape to clear — rather than as a third invention. What differs is
// forced by the shape of the thing being filtered and is written down
// below; nothing else does.
//
// ## The predicate: name, not path
//
// Git filters on the repository-relative PATH because its rows are a flat
// list where the directory is part of what identifies a row. A tree already
// draws the directory as the row above, so matching the path would make
// every descendant of a matching folder match — type "src" and the whole of
// src/ answers, which is not narrowing, it is the tree you already had. The
// design's word for this control is a NAME filter, and the name is what a
// person is looking at on the row.
//
// Otherwise the semantics are Git's, for Git's reasons: case-insensitive
// SUBSTRING, never a subsequence (a fuzzy match on scattered characters is
// how "notes.txt" matches "nst", which is useless for navigating a list);
// the query is TRIMMED, because a leading space is never a deliberate
// character of a name filter; and characters are LITERAL, so a file with a
// "(" in its name matches a filter with a "(" rather than exploding.
//
// ## The narrowing: a filter, not a new listing
//
// Two rules, and they are the difference between narrowing a tree and
// rebuilding one:
//
// 1. **Expansion is not touched.** Nothing here writes to the store. The
//    filter is a function over the rows the tree already produced, so
//    clearing it restores exactly the view the person had — the same
//    folders open, the same pages loaded, the same row selected. A filter
//    that collapsed the tree and re-expanded the matches would be a
//    different tree wearing the same panel, and the person's own work of
//    opening four levels would be gone.
// 2. **A match brings its ancestors with it.** A row three levels down is
//    meaningless without the folders it is in — both because the person
//    cannot tell two `index.ts` apart, and because the tree's indentation
//    is a lie the moment a level is missing. Ancestors appear because a
//    descendant matched, not because they matched, and they are emitted in
//    tree order so the shape is the shape.
//
// A matching FOLDER does not drag its children in. It is one row that
// matched, like any other; its children are still each asked. That keeps
// the rule single ("a row is shown when it matches, or when something below
// it does") instead of two rules that have to agree about a folder.
//
// ## What the filter cannot see, and how it says so
//
// The tree is lazy: a folder nobody expanded has no children loaded, and a
// paginated one has only its first page. The filter is renderer-side over
// what is loaded — typing never issues a request, which is what keeps it
// from churning the watch set or racing the reveal walk — so it genuinely
// cannot match a file nocx has not listed.
//
// The structural rows are what keep that honest, and it is why they are
// retained rather than filtered away: a "Show next 47" under a folder that
// is on screen says there are 47 entries this filter has not seen, and a
// too-large or errored folder goes on saying so. They are statements about
// a LISTING and not entries with names, so there is nothing in them for a
// name filter to match; they follow their directory instead. The root's own
// structural rows are always kept, because the root is always shown.

import type { FilesFlatRow } from './files-store'

/**
 * Does this entry's name match the filter? Case-insensitive substring over
 * the name as the row draws it. An empty (or all-whitespace) filter matches
 * everything, which is what makes "no filter" and "a filter of spaces" the
 * same state.
 */
export function matchesNameFilter(name: string, filter: string): boolean {
  const q = filter.trim().toLowerCase()
  if (q === '') return true
  return name.toLowerCase().includes(q)
}

/** Is a filter actually narrowing anything? The one owner of the question,
 *  so the panel's "did this match nothing" state and the narrowing below
 *  cannot disagree about a query of three spaces. */
export function filterIsActive(filter: string): boolean {
  return filter.trim() !== ''
}

/**
 * The rows the panel should draw, given the rows the tree produced and the
 * filter the person typed.
 *
 * With no filter this returns the SAME array — identity, not a copy — so
 * clearing the box costs nothing and cannot perturb anything downstream.
 *
 * It works on the flat rows rather than on the tree because the flat rows
 * are already in depth-first order with each row's depth on it, which is
 * all "who are my ancestors" needs: the ancestor of a row at depth d is the
 * most recent entry row at depth d-1. Reaching into the tree would make
 * this a second reader of the store's internals for no more information.
 */
export function narrowFilesRows(rows: FilesFlatRow[], filter: string): FilesFlatRow[] {
  if (!filterIsActive(filter)) return rows
  const out: FilesFlatRow[] = []
  /** The chain of entry rows from the root down to where the walk is now,
   *  each with whether it has already been emitted. Index i holds the
   *  ancestor at depth i. */
  const chain: { row: FilesFlatRow; emitted: boolean }[] = []
  /** Emit every ancestor not yet on screen, root-first, so a match's
   *  folders appear above it and in tree order. */
  const flush = (): void => {
    for (const link of chain) {
      if (link.emitted) continue
      link.emitted = true
      out.push(link.row)
    }
  }
  for (const row of rows) {
    // Leave the chain holding exactly this row's ancestors: depths 0..d-1.
    while (chain.length > row.depth) chain.pop()
    if (row.kind === 'entry') {
      chain.push({ row, emitted: false })
      if (matchesNameFilter(row.node.name, filter)) flush()
      continue
    }
    // A structural row ('loading', 'more', 'state') belongs to the
    // directory one level up — the root when it is at depth 0. It is kept
    // iff that directory is on screen, so it never drags a folder into
    // view and never disappears from one that is there.
    const owner = row.depth === 0 ? null : chain[row.depth - 1]
    if (owner === null || owner.emitted) out.push(row)
  }
  return out
}
