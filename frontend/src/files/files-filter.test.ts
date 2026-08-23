// The Files panel's name filter: the predicate, and the narrowing that
// makes it a filter rather than a new listing.
//
// The narrowing's tests are built on hand-made flat rows rather than on a
// live store, because what is being asserted is a function of (rows, query)
// and threading a real tree through would make the interesting cases —
// a match four levels down, a structural row under a folder nobody matched
// — expensive to reach and hard to read. That the panel actually calls this
// over the store's rows, and that expansion survives it, is asserted in
// files-view.test.tsx against the real thing.
import { describe, expect, it } from 'vitest'

import { filterIsActive, matchesNameFilter, narrowFilesRows } from './files-filter'
import type { FilesFlatRow, FilesNode } from './files-store'

/** The parts of a node the filter and the narrowing read. The rest of
 *  FilesNode is listing bookkeeping neither touches. */
function node(name: string, path = `/${name}`): FilesNode {
  return {
    name,
    path,
    kind: 'regular',
    size: 0,
    modTime: '',
    mode: 0o644,
    expanded: false,
    cyclic: false,
    children: [],
    busy: false,
    state: 'ok',
    tooLargeLimit: null,
    observedCount: null,
    timeout: null,
    error: null,
    canonical: null,
    rev: '',
    total: 0,
    hasMore: false,
    nextOffset: 0,
    appliedGeneration: 0,
  }
}

const entry = (name: string, depth: number, path?: string): FilesFlatRow => ({
  kind: 'entry',
  node: node(name, path),
  depth,
})

const more = (depth: number): FilesFlatRow => ({
  kind: 'more',
  dir: node('x'),
  depth,
})

const names = (rows: FilesFlatRow[]): string[] =>
  rows.map((r) => (r.kind === 'entry' ? r.node.name : `<${r.kind}@${r.depth}>`))

describe('the predicate', () => {
  it('matches a case-insensitive substring of the name', () => {
    expect(matchesNameFilter('README.md', 'read')).toBe(true)
    expect(matchesNameFilter('readme.md', 'ME.')).toBe(true)
    expect(matchesNameFilter('readme.md', 'zzz')).toBe(false)
  })

  it('is not fuzzy — scattered characters do not match', () => {
    // A subsequence match is how "notes.txt" answers "nst", which is
    // useless for navigating a list and surprising to whoever typed it.
    expect(matchesNameFilter('notes.txt', 'nst')).toBe(false)
  })

  it('treats characters literally, never as a pattern', () => {
    expect(matchesNameFilter('report(final).pdf', '(final)')).toBe(true)
    // A regex-minded implementation would either explode here or match
    // everything; a substring matches neither.
    expect(matchesNameFilter('report.pdf', '.*')).toBe(false)
  })

  it('trims the query, so a filter of spaces is no filter at all', () => {
    expect(matchesNameFilter('a.txt', '  a  ')).toBe(true)
    expect(matchesNameFilter('a.txt', '   ')).toBe(true)
    expect(filterIsActive('   ')).toBe(false)
    expect(filterIsActive('')).toBe(false)
    expect(filterIsActive(' a ')).toBe(true)
  })
})

describe('narrowing the tree', () => {
  it('with no filter it hands back the same array, untouched', () => {
    // Identity, not a copy: clearing the box must cost nothing and must
    // not perturb anything downstream.
    const rows = [entry('a', 0), entry('b', 0)]
    expect(narrowFilesRows(rows, '')).toBe(rows)
    expect(narrowFilesRows(rows, '   ')).toBe(rows)
  })

  it('keeps the rows whose NAME matches and drops the rest', () => {
    const rows = [entry('notes.md', 0), entry('image.png', 0), entry('other-notes.md', 0)]
    expect(names(narrowFilesRows(rows, 'notes'))).toEqual(['notes.md', 'other-notes.md'])
  })

  it('brings a match`s ancestors with it, in tree order', () => {
    // A row three levels down is meaningless without the folders it is in:
    // the person cannot tell two index.ts apart, and the indentation is a
    // lie the moment a level is missing.
    const rows = [
      entry('src', 0),
      entry('ui', 1),
      entry('button.tsx', 2),
      entry('docs', 0),
      entry('readme.md', 1),
    ]
    expect(names(narrowFilesRows(rows, 'button'))).toEqual(['src', 'ui', 'button.tsx'])
  })

  it('emits an ancestor once, however many of its descendants match', () => {
    const rows = [entry('src', 0), entry('a.ts', 1), entry('b.ts', 1)]
    expect(names(narrowFilesRows(rows, '.ts'))).toEqual(['src', 'a.ts', 'b.ts'])
  })

  it('does not drag a matching folder`s children in with it', () => {
    // A folder that matched is one row that matched, like any other. The
    // alternative is two rules that have to agree about a folder, and
    // "type src and get the whole of src/" is not narrowing.
    const rows = [entry('src', 0), entry('a.ts', 1), entry('b.ts', 1)]
    expect(names(narrowFilesRows(rows, 'src'))).toEqual(['src'])
  })

  it('matches nothing when nothing matches, rather than falling back to everything', () => {
    const rows = [entry('src', 0), entry('a.ts', 1)]
    expect(narrowFilesRows(rows, 'zzz')).toEqual([])
  })

  it('keeps the ROOT`s structural rows always — they say what the filter cannot see', () => {
    // "Show next 47" under the root is the honest answer to "did you search
    // everything": no, 47 entries have not been listed.
    const rows = [entry('a.ts', 0), more(0)]
    expect(names(narrowFilesRows(rows, 'zzz'))).toEqual(['<more@0>'])
  })

  it('keeps a folder`s structural row only while that folder is on screen', () => {
    const rows = [entry('src', 0), entry('a.ts', 1), more(1), entry('docs', 0), more(1)]
    // 'a.ts' matches, so src is on screen and its "show next" belongs there.
    expect(names(narrowFilesRows(rows, 'a.ts'))).toEqual(['src', 'a.ts', '<more@1>'])
    // Nothing under docs matched and docs did not either, so neither docs
    // nor its "show next" appear — a structural row must not drag a folder
    // into view.
    expect(names(narrowFilesRows(rows, 'a.ts'))).not.toContain('docs')
  })

  it('preserves depth-first order across siblings at several depths', () => {
    const rows = [
      entry('src', 0),
      entry('deep.ts', 1),
      entry('nested', 1),
      entry('deeper.ts', 2),
      entry('top.ts', 0),
    ]
    expect(names(narrowFilesRows(rows, 'deep'))).toEqual(['src', 'deep.ts', 'nested', 'deeper.ts'])
  })
})
