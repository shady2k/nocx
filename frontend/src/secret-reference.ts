// The reference grammar, one scan shared by every consumer: the chip
// decorates the spans, planSubmit decides whether a line needs resolving,
// and the offer must never treat a reference's NAME as a secret.
//
// {{secret:NAME}} — NAME is the vault inventory name (ADR-0016). The
// grammar is deliberately open (spaces are legal — internal/secrets tests
// `echo {{secret:with space in name}}`); only `}` is structural.
export const REFERENCE_RE = /\{\{secret:([^}]*)\}\}/g

/** One {{secret:NAME}} reference span in `input`, in first-occurrence
 *  order. Offsets are UTF-16 code-unit positions — what CM6 uses. */
export interface ReferenceSpan {
  from: number
  to: number
  name: string
}

/** Find every well-formed reference in `input`. A malformed span (a `}`
 *  inside the name) matches nothing — the chip must never decorate it. */
export function findReferences(input: string): ReferenceSpan[] {
  const out: ReferenceSpan[] = []
  for (const m of input.matchAll(REFERENCE_RE)) {
    out.push({ from: m.index, to: m.index + m[0].length, name: m[1] })
  }
  return out
}

/** Build the reference for a vault inventory name — the one writer of the
 *  grammar, beside its one reader above (nocx-fk32). The name is not
 *  escaped and must not be: the grammar has exactly one structural
 *  character, `}`, and a vault name containing one cannot be referenced at
 *  all, which findReferences already states by refusing to match it. */
export function secretReference(name: string): string {
  return `{{secret:${name}}}`
}

/** The handle a field is BOUND to, or `undefined` when it holds a literal.
 *
 *  A field is bound when its WHOLE value is one reference and nothing else:
 *  `{{secret:secrow:…}}` alone means "this value comes from the vault", while
 *  `Bearer {{secret:x}}` is a literal that happens to interpolate one. The
 *  distinction is what the segmented "type a new one / use existing secret"
 *  control used to ask a person to declare before they had typed anything
 *  (nocx-3o0ed.4); it is now read off the value, so the two-way choice is
 *  made by doing rather than by declaring.
 *
 *  This is the ONE reader of that rule. Three surfaces write a bound value to
 *  three different wire shapes — the endpoint's `credential`, a header row's
 *  `secret`, a connection's `options.passwordSecret` — and each of them asks
 *  here rather than re-deriving it, because a second derivation is a second
 *  answer waiting to disagree (AGENTS.md: one owner per behaviour). */
export function boundSecret(value: string): string | undefined {
  const spans = findReferences(value)
  const only = spans.length === 1 ? spans[0] : undefined
  return only !== undefined && only.from === 0 && only.to === value.length ? only.name : undefined
}
