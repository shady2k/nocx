// The workbench's tree, flattened.
//
// A collection is a FOLDER (design §6.1) and a request is one file inside it
// (§6.2), so `relPath` carries real directory structure — `users/create.json`
// is a request called "create" inside a folder called "users". The wire sends
// a flat list of refs; the shape is in their paths, and this is the one place
// that reads it. TreeRow indents by a NUMBER rather than by nested DOM, so a
// flat row list is exactly what the kit wants.
//
// Malformed files are rows, not a footnote. Design §6.2's listing puts them
// ON the collection precisely so one bad file cannot hide every good one, and
// a row is where a person can see which file and what was wrong with it — a
// soft degrade that lives only in a log is a feature that does not exist.

import type { ApiOpenCollection } from './api-model'

type ApiTreeRowKind = 'collection' | 'dir' | 'request' | 'malformed'

export interface ApiTreeRow {
  /** Stable identity for the row: the handle plus the path within it. Also
   *  what the collapsed set is keyed by, so collapsing "users" in one
   *  collection does not collapse "users" in another. */
  readonly key: string
  readonly kind: ApiTreeRowKind
  readonly depth: number
  readonly name: string
  readonly handle: string
  /** The path WITHIN the collection: a request's file, a directory's prefix,
   *  and '' for the collection row itself. Never a path the renderer chose. */
  readonly relPath: string
  /** A request's verb, for the row's badge. '' on every other kind. */
  readonly method: string
  /** Why a malformed file could not be read. '' on every other kind. */
  readonly reason: string
  readonly expandable: boolean
  readonly expanded: boolean
}

/** The name a request row shows: the collection's `name` for it when there is
 *  one, and the file's own basename when there is not — a request file whose
 *  name field is empty must still be findable by the name it has on disk. */
function leafName(name: string, relPath: string): string {
  if (name !== '') return name
  const base = relPath.slice(relPath.lastIndexOf('/') + 1)
  return base !== '' ? base : relPath
}

/**
 * Flatten every open collection into the rows the tree renders, in the order
 * they appear: directories before the requests beside them, and each
 * collection's malformed files at its foot.
 *
 * `collapsed` holds the keys the user has folded away — collapsed rather than
 * expanded, so a collection that arrives with new directories in it shows
 * them, which is what "the folder is re-read on every call" is for.
 */
export function flattenCollections(
  collections: readonly ApiOpenCollection[],
  collapsed: ReadonlySet<string>,
): ApiTreeRow[] {
  const rows: ApiTreeRow[] = []
  for (const open of collections) {
    const rootKey = `${open.handle}:`
    const rootExpanded = !collapsed.has(rootKey)
    rows.push({
      key: rootKey,
      kind: 'collection',
      depth: 0,
      name: open.collection.name !== '' ? open.collection.name : open.path,
      handle: open.handle,
      relPath: '',
      method: '',
      reason: '',
      expandable: true,
      expanded: rootExpanded,
    })
    if (!rootExpanded) continue

    // Directories are derived from the refs' paths; a directory is emitted
    // once, the first time a request under it is reached, so the order the
    // backend listed the requests in is the order the tree shows.
    const emitted = new Set<string>()
    const hidden = (dirs: string[]): boolean => {
      let prefix = ''
      for (const d of dirs) {
        prefix = prefix === '' ? d : `${prefix}/${d}`
        if (collapsed.has(`${open.handle}:${prefix}`)) return true
      }
      return false
    }
    for (const ref of open.collection.requests) {
      const segments = ref.relPath.split('/')
      const dirs = segments.slice(0, -1)
      let prefix = ''
      let cut = false
      for (const [i, dir] of dirs.entries()) {
        prefix = prefix === '' ? dir : `${prefix}/${dir}`
        const key = `${open.handle}:${prefix}`
        if (!emitted.has(key) && !hidden(dirs.slice(0, i))) {
          emitted.add(key)
          rows.push({
            key,
            kind: 'dir',
            depth: i + 1,
            name: dir,
            handle: open.handle,
            relPath: prefix,
            method: '',
            reason: '',
            expandable: true,
            expanded: !collapsed.has(key),
          })
        }
        if (collapsed.has(key)) {
          cut = true
          break
        }
      }
      if (cut) continue
      rows.push({
        key: `${open.handle}:${ref.relPath}`,
        kind: 'request',
        depth: dirs.length + 1,
        name: leafName(ref.name, ref.relPath),
        handle: open.handle,
        relPath: ref.relPath,
        method: ref.method,
        reason: '',
        expandable: false,
        expanded: false,
      })
    }
    for (const bad of open.collection.malformed) {
      rows.push({
        key: `${open.handle}:!${bad.relPath}`,
        kind: 'malformed',
        depth: 1,
        name: leafName('', bad.relPath),
        handle: open.handle,
        relPath: bad.relPath,
        method: '',
        reason: bad.reason,
        expandable: false,
        expanded: false,
      })
    }
  }
  return rows
}
