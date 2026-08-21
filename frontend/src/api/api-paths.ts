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
