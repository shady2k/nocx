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

/**
 * What a pasted string IS, decided HERE and nowhere else.
 *
 * Two derivations of this question is the defect AGENTS.md names about `ssh`
 * without a trailing space: they agree on every case anybody tries and
 * disagree on the one that matters. The ask, the destination offer and the
 * client call all read this one answer.
 *
 * `unusable` is a real answer rather than an error: a person who pasted a
 * curl line gets a sentence from the ask, and no round trip is spent to
 * learn what the form already knew. Curl is not this ask's question — it has
 * its own door in the request editor.
 */
export type PastedSource =
  { kind: 'url'; url: string } | { kind: 'document'; document: string } | { kind: 'unusable' }

export function classifyPastedSource(text: string): PastedSource {
  const trimmed = text.trim()
  if (trimmed === '') return { kind: 'unusable' }
  if (/^https?:\/\//i.test(trimmed)) return { kind: 'url', url: trimmed }
  if (trimmed.startsWith('{') || trimmed.startsWith('['))
    return { kind: 'document', document: trimmed }
  return { kind: 'unusable' }
}

/**
 * The destination a PASTED EXPORT proposes: `<defaultRoot>/<slug of
 * info.name>`.
 *
 * A syntactic offer and not a parse of the format — the module's own rule at
 * the top of this file. It reads one field, validates nothing, refuses
 * nothing, and answers '' for every failure, because the backend is the only
 * reader of hostile input and this is a suggestion in a field the person can
 * overwrite.
 */
export function proposedDestinationFromDocument(defaultRoot: string, document: string): string {
  if (defaultRoot === '') return ''
  let name = ''
  try {
    const parsed: unknown = JSON.parse(document)
    const info = (parsed as { info?: { name?: unknown } } | null)?.info
    if (typeof info?.name === 'string') name = info.name
  } catch {
    return ''
  }
  const slug = slugify(name)
  if (slug === '') return ''
  return `${defaultRoot.replace(/[\\/]+$/, '')}/${slug}`
}

/**
 * The destination a URL proposes: the last path segment, without any of its
 * suffixes, exactly as proposedDestination treats a file name.
 *
 * '' when the URL has no last segment — a share link ending in a slash — and
 * the ask then opens the destination as an empty required field rather than
 * proposing a folder named after nothing.
 */
export function proposedDestinationFromURL(defaultRoot: string, url: string): string {
  if (defaultRoot === '') return ''
  let path = ''
  try {
    path = new URL(url).pathname
  } catch {
    return ''
  }
  const last =
    path
      .split('/')
      .filter((s) => s !== '')
      .pop() ?? ''
  const stem = decodeURIComponent(last).split('.')[0].trim()
  if (stem === '') return ''
  return `${defaultRoot.replace(/[\\/]+$/, '')}/${stem}`
}

/**
 * What a request CALLS ITSELF while nobody has named it: the method and the
 * last path segment of the address, which is the pair the crumbs above the
 * form already read — `POST http://127.0.0.1:8080/v1/broker-access` is
 * `POST broker-access`.
 *
 * ONE DERIVATION, HERE. The name is offered as the URL is typed and it is
 * read back in the tree row, the crumb trail and the file the allocator
 * names; a second place that worked out "what is this request called" would
 * be the two-owners defect with a person's own name for their request as the
 * thing the two disagreed about.
 *
 * AN OFFER, NOT A DERIVATION — this module's rule, stated at the top. The
 * moment somebody names the request themselves the offer stops for good; the
 * store owns that interval, because it owns the draft and knows when the
 * name last changed and who changed it (api-store.ts).
 *
 * The segments are taken SYNTACTICALLY rather than through `new URL`,
 * because the address most requests carry is not a URL any parser accepts:
 * `{{baseUrl}}/users` is what a Postman export holds and what a person
 * writing against an environment types. The authority is dropped where there
 * is one — `POST 127.0.0.1:8080` names the machine and not the call — and a
 * segment that is nothing but a `{{reference}}` is passed over, because
 * `DELETE {{id}}` is a row nobody can tell from the next one.
 *
 * '' is the answer for every address with nothing to take, and the caller
 * leaves the name exactly as it is: an offer that is absent is a row still
 * called what it was, and an offer that is wrong is a row named after our
 * machinery.
 */
export function proposedRequestName(method: string, url: string): string {
  const trimmed = url.trim()
  if (trimmed === '') return ''
  const address = trimmed.split('#')[0].split('?')[0]
  let path = address
  const scheme = /^[a-z][a-z0-9+.-]*:\/\//i
  if (scheme.test(address)) {
    const rest = address.replace(scheme, '')
    const slash = rest.indexOf('/')
    path = slash === -1 ? '' : rest.slice(slash)
  }
  const segment = path
    .split('/')
    .map((s) => s.trim())
    .filter((s) => s !== '' && !/^\{\{[^}]*\}\}$/.test(s))
    .pop()
  if (segment === undefined) return ''
  const verb = method.trim()
  return verb === '' ? segment : `${verb} ${segment}`
}
