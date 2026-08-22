// Where a new file goes inside a collection, spelled once.
//
// A collection is a folder and a request is a file in it (§6.2), so making
// either means naming a file. Two callers need that: the store, which gives a
// saved request a file uniquified against the folder, and the environments
// page. One rule, one module — the same name gives the same file wherever it
// is typed, which is what makes a Playground committed from one machine and
// pulled onto another a folder rather than a diff.
//
// AN OFFER, NOT A DERIVATION, at every call site. The field a person sees is
// the truth and the backend is what refuses it (§13.1 — a name that is not a
// single path component is refused rather than sanitised); this only fills
// the field in while nothing has been typed into it.

/** A name reduced to a file-safe stem: lower case, runs of anything else
 *  collapsed to one dash, no dash at either end. '' when nothing survives,
 *  which the callers treat as "no offer to make" rather than as a name. */
export function slugify(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

/** The file an environment called `name` goes into. Under `environments/`,
 *  which is where the reader looks for it and nowhere else (§6.2). */
export function environmentPath(name: string): string {
  const slug = slugify(name)
  return slug === '' ? '' : `environments/${slug}.json`
}

/**
 * The destination an import PROPOSES for a chosen export:
 * `<defaultRoot>/<the file's name without any of its extensions>`.
 *
 * `acme.postman_collection.json` proposes `acme`, not
 * `acme.postman_collection` — a Postman export is named with two suffixes
 * and a folder called after the first one is a folder named after our
 * import machinery rather than after the collection inside it.
 *
 * '' when there is nothing to propose, which is three states and one
 * answer: no default location on this build (the backend said ""), no file
 * chosen yet, or a name with no stem before its first dot — every hidden
 * file, `.postman_collection.json` included. The caller leaves the field
 * alone in all three, because an offer nobody can act on is worse than an
 * empty field a person is already typing into.
 *
 * AN OFFER, NOT A DERIVATION — this module's own rule, stated at the top.
 * The field the person sees is the truth, the backend refuses what it must
 * (a destination that exists is refused rather than merged), and this only
 * fills the field in while nothing has been typed into it.
 */
export function proposedDestination(defaultRoot: string, exportPath: string): string {
  if (defaultRoot === '') return ''
  // The basename by hand rather than through a path library: this runs in a
  // renderer that has none, and the two separators are what any path a
  // person can choose here uses. Trailing separators are not a case — the
  // picker answers a file.
  const base = exportPath.split(/[\\/]/).pop() ?? ''
  // Everything before the FIRST dot, so both suffixes go — and a name that
  // BEGINS with one therefore has no stem at all. That is the wanted
  // answer rather than a case to work around: stripping the leading dot
  // from `.postman_collection.json` would propose a folder called
  // `postman_collection`, named after our import machinery and after
  // nothing the person recognises.
  const stem = base.split('.')[0].trim()
  if (stem === '') return ''
  const root = defaultRoot.replace(/[\\/]+$/, '')
  return `${root}/${stem}`
}
