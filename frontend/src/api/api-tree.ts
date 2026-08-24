// The workbench's tree, flattened.
//
// A collection is a FOLDER (design §6.1) and a request is one file inside it
// (§6.2), so `relPath` carries real directory structure — `users/create.json`
// is a request called "create" inside a folder called "users". TreeRow indents
// by a NUMBER rather than by nested DOM, so a flat row list is exactly what
// the kit wants.
//
// WHICH FOLDERS EXIST IS THE BACKEND'S ANSWER, and this file does not have a
// second one. The directory rows used to be derived here from the requests'
// paths, which agrees with `collection.folders` about every folder that holds
// a request and disagrees about every folder that does not — and "does not"
// is the state a folder spends its first minutes in, so a folder somebody had
// just made was invisible until they put something in it. The open schema
// says `folders` is the one answer in as many words; this walks it.
//
// A request is still placed by its path, because a path is where the file is.
// The two meet at `drawnUnder`: a request hangs off the nearest folder the
// backend listed. On a listing from this backend that is always its own
// directory — both halves come off one walk — and when it is not, the request
// is still drawn rather than silently dropped, which is the failure the
// derivation could not have.
//
// Malformed files are rows, not a footnote. Design §6.2's listing puts them
// ON the collection precisely so one bad file cannot hide every good one, and
// a row is where a person can see which file and what was wrong with it — a
// soft degrade that lives only in a log is a feature that does not exist.

import type { ApiCollection, ApiOpenCollection, ApiRequestRef } from './api-model'

type ApiTreeRowKind = 'collection' | 'dir' | 'request' | 'malformed' | 'empty'

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

/**
 * Narrow the open collections to what a filter matches, keeping the shape.
 *
 * FILTERING IS A NARROWING OF THE INPUT, not a second flattener. The rows a
 * person sees are still built by flattenCollections below — one owner of what
 * a tree looks like — and this decides only which refs go into it. A second
 * walk that knew about directories, depths and collapsed keys as well as
 * matching would be two answers to "what does the tree show", and they would
 * agree until the day one of them learnt about a row kind the other did not.
 *
 * What matches, and why each:
 *
 *  - A COLLECTION matches by its name or by the path it was opened at, and
 *    then it is kept WHOLE. Typing the name of a folder is how a person says
 *    "show me that one", and answering it with the subset of its requests
 *    that happen to contain the same letters would be the panel arguing.
 *  - A REQUEST matches by the name it shows and by its path within the
 *    collection, so `users/create` finds it by either half — the path is
 *    real structure (§6.2) and the only name a request with an empty `name`
 *    field has.
 *  - A FOLDER matches by its path, and it has to match on its own account: a
 *    folder that holds nothing has no request whose path could carry its
 *    name, so a filter that only narrowed requests would answer "nothing
 *    matches" about a folder that is on screen when the field is empty.
 *  - A MALFORMED file matches by its path, because that is all it has. It
 *    stays findable on purpose: a file that will not read is exactly what
 *    somebody goes looking for.
 *
 * A collection with nothing left is dropped rather than shown empty: an empty
 * folder in a filtered tree reads as "no matches in here", which is a sentence
 * the absence of the row already says, once, for all of them.
 */
export function filterCollections(
  collections: readonly ApiOpenCollection[],
  query: string,
): readonly ApiOpenCollection[] {
  const needle = query.trim().toLowerCase()
  if (needle === '') return collections

  const hit = (text: string): boolean => text.toLowerCase().includes(needle)
  const kept: ApiOpenCollection[] = []

  for (const open of collections) {
    if (hit(open.collection.name) || hit(open.path)) {
      kept.push(open)
      continue
    }
    const requests = open.collection.requests.filter(
      (ref) => hit(leafName(ref.name, ref.relPath)) || hit(ref.relPath),
    )
    const malformed = open.collection.malformed.filter((bad) => hit(bad.relPath))
    const folders = foldersFor(open.collection.folders, requests, hit)
    if (requests.length === 0 && malformed.length === 0 && folders.length === 0) continue
    kept.push({ ...open, collection: { ...open.collection, requests, folders, malformed } })
  }
  return kept
}

/**
 * Which of the collection's folders survive a filter: the ones that matched,
 * the ones a surviving request is inside, and every folder on the way to
 * either.
 *
 * The ancestors are not decoration. A row is drawn under its parent, so
 * keeping `v1/admin` while dropping `v1` would leave a folder hanging off the
 * collection at a depth that says otherwise — and dropping the directory a
 * kept request lives in would put the request there instead.
 *
 * It filters the ORIGINAL list rather than building one, which keeps two
 * properties for free: the backend's order (parents before their children),
 * and the promise that a folder on screen is a folder the backend named.
 */
function foldersFor(
  folders: readonly string[],
  requests: readonly { relPath: string }[],
  hit: (text: string) => boolean,
): string[] {
  const keep = new Set<string>()
  const keepWithAncestors = (dir: string): void => {
    let prefix = ''
    for (const segment of dir.split('/')) {
      prefix = prefix === '' ? segment : `${prefix}/${segment}`
      keep.add(prefix)
    }
  }
  for (const dir of folders) if (hit(dir)) keepWithAncestors(dir)
  for (const ref of requests) {
    const dir = directoryOf(ref.relPath)
    if (dir !== '') keepWithAncestors(dir)
  }
  return folders.filter((dir) => keep.has(dir))
}

/** The name a request row shows: the collection's `name` for it when there is
 *  one, and the file's own basename when there is not — a request file whose
 *  name field is empty must still be findable by the name it has on disk. */
function leafName(name: string, relPath: string): string {
  if (name !== '') return name
  const base = relPath.slice(relPath.lastIndexOf('/') + 1)
  return base !== '' ? base : relPath
}

/** The folder a path is in — '' at the collection's root. Exported
 *  because the surface asks the same question when it decides whether a
 *  drop is a no-op (a request dragged onto the folder it is already in):
 *  one owner of "where does this path live", per AD-8. */
export function directoryOf(relPath: string): string {
  const cut = relPath.lastIndexOf('/')
  return cut === -1 ? '' : relPath.slice(0, cut)
}

/** How far in a row is drawn: one step per path segment, with the collection
 *  itself at nought. Read off the PATH rather than counted while walking, so
 *  a row's depth is a fact about where the thing is and not about the order
 *  the rows happened to be emitted in. */
function depthOf(relPath: string): number {
  return relPath.split('/').length
}

/** The folder a row hangs under: its own, or the nearest one above it the
 *  backend actually listed. See the header — on a listing from this backend
 *  the first case is the only one. */
function drawnUnder(dir: string, listed: ReadonlySet<string>): string {
  let at = dir
  while (at !== '' && !listed.has(at)) at = directoryOf(at)
  return at
}

function pushInto<T>(index: Map<string, T[]>, key: string, value: T): void {
  const held = index.get(key)
  if (held === undefined) index.set(key, [value])
  else held.push(value)
}

/**
 * Everything a collection holds, indexed by the folder it hangs under.
 *
 * Both indexes are built against the SAME set of listed folders, so a folder
 * and the requests beside it can never be drawn under different parents.
 * ONE OWNER of "what is in there" (AD-8): the tree walks the whole index and
 * the folder page reads a single entry of it, so a folder cannot show one
 * thing in the column and another in the page.
 */
function indexByFolder(collection: ApiCollection): {
  children: Map<string, string[]>
  requests: Map<string, ApiRequestRef[]>
} {
  const listed = new Set(collection.folders)
  const children = new Map<string, string[]>()
  for (const dir of collection.folders) {
    pushInto(children, drawnUnder(directoryOf(dir), listed), dir)
  }
  const requests = new Map<string, ApiRequestRef[]>()
  for (const ref of collection.requests) {
    pushInto(requests, drawnUnder(directoryOf(ref.relPath), listed), ref)
  }
  return { children, requests }
}

/** What is DIRECTLY inside one folder — its subfolders and the requests
 *  beside them, in the order the tree draws them. `dir` is '' for the
 *  collection's own root. */
export function contentsOf(
  collection: ApiCollection,
  dir: string,
): { folders: readonly string[]; requests: readonly ApiRequestRef[] } {
  const { children, requests } = indexByFolder(collection)
  return { folders: children.get(dir) ?? [], requests: requests.get(dir) ?? [] }
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
  /**
   * Whether what arrives here has been NARROWED by a filter.
   *
   * It changes one thing: an empty folder stops saying "Empty". A narrowed
   * tree is not a tree of what exists — a folder kept because its own name
   * matched has no children in it, and calling that empty answers a question
   * nobody asked and contradicts what the same folder says when the box is
   * cleared. The caller is the one that knows, so it is the one that says.
   */
  narrowed = false,
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

    const { children, requests } = indexByFolder(open.collection)

    /**
     * AN EXPANDED FOLDER WITH NOTHING IN IT SAYS SO.
     *
     * Without this the rows below it are the only thing under an open folder,
     * and they are its SIBLINGS — drawn at its depth, with their icons in the
     * column its icon is in — so the folder reads as their parent and its
     * emptiness reads as "these are what is in it". The owner hit exactly
     * that: a folder made a minute earlier appeared to have swallowed every
     * request in the collection.
     *
     * Indentation cannot answer it and neither can an indent guide: there is
     * no deeper level to draw, because there is nothing at it. The only
     * honest signal is the absence itself, stated.
     */
    const nothingIn = (dir: string): boolean =>
      (children.get(dir) ?? []).length === 0 && (requests.get(dir) ?? []).length === 0

    const sayEmpty = (dir: string, depth: number): void => {
      if (narrowed) return
      rows.push({
        // Not a path: no file may be called this, so the key cannot collide
        // with a row that exists.
        key: `${open.handle}:${dir}/\u0000empty`,
        kind: 'empty',
        depth,
        name: 'Empty',
        handle: open.handle,
        relPath: dir,
        method: '',
        reason: '',
        expandable: false,
        expanded: false,
      })
    }

    const walk = (dir: string): void => {
      for (const child of children.get(dir) ?? []) {
        const key = `${open.handle}:${child}`
        const expanded = !collapsed.has(key)
        rows.push({
          key,
          kind: 'dir',
          depth: depthOf(child),
          name: leafName('', child),
          handle: open.handle,
          relPath: child,
          method: '',
          reason: '',
          // Expandable whether or not anything is in it, exactly as the
          // collection row is: a folder a person has just made is empty and
          // is still a folder they can fold away.
          expandable: true,
          expanded,
        })
        if (expanded) {
          if (nothingIn(child)) sayEmpty(child, depthOf(child) + 1)
          else walk(child)
        }
      }
      for (const ref of requests.get(dir) ?? []) {
        rows.push({
          key: `${open.handle}:${ref.relPath}`,
          kind: 'request',
          depth: depthOf(ref.relPath),
          name: leafName(ref.name, ref.relPath),
          handle: open.handle,
          relPath: ref.relPath,
          method: ref.method,
          reason: '',
          expandable: false,
          expanded: false,
        })
      }
    }
    walk('')

    // The collection's own root, by the same rule and for the same reason:
    // an open collection with nothing in it stands directly above the next
    // collection's rows. Malformed files count as something in it — they are
    // listed below, and a root that said "Empty" above them would be wrong.
    if (nothingIn('') && open.collection.malformed.length === 0) sayEmpty('', 1)

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
