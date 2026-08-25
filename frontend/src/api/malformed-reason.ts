/**
 * A failed decoder, in a person's words.
 *
 * The wire carries a collection file's exact decode reason, verbatim —
 * `internal/apicoll/folder.go` stores `err.Error()` — because a developer
 * wants that text in a log. A person looking at the row under a file does
 * not: the owner's screen reads `json: unknown field "var"` and the person
 * reads nothing but that the file is wrong. Turning the machine's reason into
 * the person's sentence is this module's whole job, and it happens HERE, at
 * the surface, the way operation-row.tsx owns the outcome vocabulary for wire
 * phases — the wire keeps the precise reason and the renderer says what it
 * means.
 *
 * English only, no Solid, no i18n: the format, its decoder and its replies
 * share one language, and a mapping table would not know it had crossed one.
 * The sentences are written the way the rest of this directory is (plain
 * sentences, never "Error: ...").
 *
 * Two deliberate pass-throughs, both already sentences written for a person:
 * `not a regular file; symlinks are not followed` (folder.go's
 * `!d.Type().IsRegular()` branch) and `trailing content after the JSON
 * document` (version.go's decoder, which wrote it as a sentence rather than a
 * Go error). They come out unchanged. Rewriting a sentence that already reads
 * the way a person talks risks flattening the one meaning its words carry;
 * this module's job is the reasons that enumerate what the decoder did — the
 * inherited mechanics, not its own prose — and only those.
 */

/**
 * The fields a request file actually understands. DERIVED FROM
 * `frontend/src/generated/api.request.read.ts` — the wire contract, the only
 * list that decides what a file may hold. If that generated file learns a
 * field, this list must learn it too; there is deliberately no import,
 * because the generated module is a type file and this list is the honest
 * copy of a schema, kept next to the decoder it corrects for rather than
 * bound to the wrong end of the contract.
 *
 * The casing here is the format's canonical casing (`fileRef` is the only
 * mixed-case name), but the checks below read it case-insensitively, because
 * the decoder's own reading of a file is case-insensitive: Go accepts a key
 * that differs from the field only in case, so a case-deviant key reaching
 * the "unknown field" refusal means no field — in any case — lives at that
 * spot.
 */
const REAL_FIELDS: readonly string[] = [
  'id',
  'name',
  'method',
  'url',
  'headers',
  'query',
  'variables',
  'body',
  'auth',
  'value',
  'enabled',
  'kind',
  'text',
  'fileRef',
  'user',
  'token',
  'password',
]

/**
 * Levenshtein edit distance, cheap and deterministic. The matrix is squashed
 * to two rows because the suggestion loop is this module's hot path.
 */
function editDistance(a: string, b: string): number {
  const m = a.length
  const n = b.length
  const prev = Array.from({ length: n + 1 }, (_, i) => i)
  const curr = new Array<number>(n + 1)
  for (let i = 1; i <= m; i++) {
    curr[0] = i
    for (let j = 1; j <= n; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1
      curr[j] = Math.min(prev[j] + 1, curr[j - 1] + 1, prev[j - 1] + cost)
    }
    for (let j = 0; j <= n; j++) prev[j] = curr[j]
  }
  return prev[n]
}

/**
 * The floor on what a suggestion may "correct": a one- or two-character name
 * is too short to tell a shortening from a typo, and either search would
 * match any short fragment to some real field by luck. Both suggestion rules
 * skip anything at or under it — `va`, the ambiguous-prefix example, is both
 * a two-prefix AND under this floor; either rule alone silences it. The
 * exact-match prior question in malformedReason runs BEFORE this floor,
 * because it is not a guess at all.
 */
const SUGGEST_MAX_DISTANCE = 2
const SUGGEST_MIN_LENGTH = 3

/**
 * The real field a guessed name probably meant, or null when nothing is
 * close enough. The caller asks one question first — "is this even a guess?"
 * (the exact-match prior question in malformedReason) — and only calls this
 * when the answer is yes. Two answers are possible for a guess, and each
 * answers a different question:
 *
 * 1. "did you SHORTEN it" — the PREFIX rule. A field that is the whole
 *    beginning of exactly ONE real field (case-insensitively) is a shortening
 *    of that field: the owner's `var` is `variables` abbreviated — six edits
 *    from spelling it out, no edit-distance threshold could ever reach it,
 *    yet the letters line up exactly. It finds nothing when the field
 *    prefixes two or more real fields (`va` prefixes both `value` and
 *    `variables`): a shortening that could be either is not a shortening, so
 *    it is a FINAL, no-answer result. The typo rule (2) does not get to hear
 *    an ambiguous prefix, because the letters already answered a different
 *    question — whether this is an abbreviation — and answered it "can't
 *    tell". Having it silently suggest a typo'd field instead would be the
 *    coin-flip suggestion the brief forbids.
 *
 * 2. "did you mistype it" — the EDIT-DISTANCE rule. A field within
 *    SUGGEST_MAX_DISTANCE edits of one real field is a typo of it, and two
 *    edits is the line: a genuine typo off a real field is within two
 *    character changes, and beyond that the nearest real field ends up a
 *    coincidence of arithmetic more often than an intent. The length floor
 *    guards it again: a one- or two-letter name is too short to correct by
 *    distance, so a user who really named a key "a" or "id" gets no
 *    suggestion from THIS rule.
 *
 * The PREFIX rule is tried first. When it names a field it wins outright;
 * when it finds no match at all, the EDIT-DISTANCE rule runs in its place.
 * An AMBIGUOUS prefix is a "no" from rule one and returns before rule two —
 * the letters answered "can't tell", not "haven't a clue". The two are not
 * equally confident: an abbreviation's first letters match the field in
 * full, chosen by a person who knew the whole, while two edits also covers a
 * near-miss of a field the person never had in mind. The order is a
 * decision, not an accident of function shape.
 */
function suggestRealField(field: string): string | null {
  if (field.length < SUGGEST_MIN_LENGTH) return null

  // PREFIX rule, first. Counting rather than returning at the first match:
  // a second match is what turns a confident abbreviation into a coin flip.
  const lowered = field.toLowerCase()
  let prefixMatch: string | null = null
  let prefixed = 0
  for (const candidate of REAL_FIELDS) {
    // Case-insensitive on BOTH sides: the candidate carries the format's
    // casing (`fileRef`), so the candidate must be lowercased too — the input
    // already is. The ORIGINAL candidate is returned, so a lower-case
    // `fileref` still reads as the field's real name.
    if (!candidate.toLowerCase().startsWith(lowered)) continue
    prefixed++
    if (prefixed > 1) return null // two or more — a shortening of neither
    prefixMatch = candidate
  }
  if (prefixMatch !== null) return prefixMatch

  // EDIT-DISTANCE rule, second: the prefix question found no match, so a
  // typo may.
  let best: string | null = null
  let bestDistance = Number.POSITIVE_INFINITY
  for (const candidate of REAL_FIELDS) {
    const distance = editDistance(field, candidate)
    if (distance < bestDistance) {
      bestDistance = distance
      best = candidate
    }
  }
  return best !== null && bestDistance <= SUGGEST_MAX_DISTANCE ? best : null
}

/**
 * What the Go type name means in a sentence a reader sees while looking at
 * a value they typed. Only the scalars the type errors most often carry are
 * translated; an array or an object comes out as the shape it is rather than
 * a guess at the value.
 */
function expectedShape(goType: string): string {
  switch (goType) {
    case 'bool':
      return 'a true or false value'
    case 'string':
      return 'text'
    case 'int':
    case 'int32':
    case 'int64':
    case 'uint':
    case 'uint32':
    case 'uint64':
    case 'float32':
    case 'float64':
      return 'a number'
    case '[]string':
      return 'a list of text'
    default:
      return 'a value of a different shape'
  }
}

/** The field a decoder names as part of a struct path — `Request.auth.enabled`
 *  names `enabled`. An index segment (an array element) is dropped, so
 *  `Request.headers.0.name` still names `name`. */
function fieldIn(path: string): string {
  const segments = path.split('.')
  for (let i = segments.length - 1; i >= 0; i--) {
    const segment = segments[i]
    if (!/^\d+$/.test(segment)) return segment
  }
  return segments[segments.length - 1] ?? ''
}

/**
 * Turn one decoder reason into the sentence a person reads under a failed
 * request file. Unknown reasons get a neutral sentence rather than the raw
 * text — a raw Go error into the renderer is the exact defect this module
 * exists to stop.
 */
export function malformedReason(reason: string): string {
  const unknown = /^json: unknown field "([^"]*)"$/.exec(reason)
  if (unknown) {
    const field = unknown[1]
    // EXACT-MATCH prior question, asked BEFORE either suggestion rule and
    // before the length floor: "is this even a guess?" A field that is a
    // real field, case-blind — `name`, `NAME`, `fileref` are all spellings
    // of a name the format already has — was not guessed at: the person
    // wrote a real name correctly and put it where the format does not allow
    // it. decodeStrict refuses an unknown field at ANY depth and its message
    // never says which object the field was in, so the sentence names the
    // field and the refusal's shape without claiming to know the place.
    //
    // It is not a THIRD rule but a PRIOR question: "is this even a guess"
    // comes before "which real field was guessed at", so an exact match
    // short-circuits the suggestion machinery entirely — including the
    // length floor, which is why `id` answers here, not there.
    //
    // Case-insensitive because the prefix rule it precedes is
    // case-insensitive: `NAME` would otherwise sail past an exact-by-casing
    // check, self-suggest `name` through the prefix rule, and the defect
    // would merely have moved to uppercase. A case-deviant key is therefore
    // also "spelled correctly" — which supersedes the earlier expectation
    // that `FILEREF` suggests `fileRef`: under the decoder's own
    // case-insensitive reading, `FILEREF` IS `fileRef`, so it gets the
    // place-is-wrong sentence, not a suggestion to itself.
    const lowered = field.toLowerCase()
    if (REAL_FIELDS.some((candidate) => candidate.toLowerCase() === lowered)) {
      return `The field "${field}" exists in the request format, but not at this point in the file.`
    }
    const suggestion = suggestRealField(field)
    return suggestion === null
      ? `The file uses a field the request format does not know: "${field}".`
      : `The file uses a field the request format does not know: "${field}" — did you mean "${suggestion}"?`
  }

  if (reason === 'unexpected end of JSON input') {
    return 'The file ends mid-way — it is cut off or empty.'
  }

  const type = /^json: cannot unmarshal .+ into Go struct field ([^ ]+) of type (.+)$/.exec(reason)
  if (type) {
    return `The field "${fieldIn(type[1])}" expects ${expectedShape(type[2])} here.`
  }

  if (
    reason === 'not a regular file; symlinks are not followed' ||
    reason === 'trailing content after the JSON document'
  ) {
    // Already a person's sentence; see the header. Pass both through unchanged.
    return reason
  }

  return 'This file could not be read as a request.'
}
