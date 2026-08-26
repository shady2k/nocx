// ONE owner for "make this string safe to assign to innerHTML".
//
// It lives in ui/ because the kit may not import outward (the lint rule says
// so, and it is right): the kit's own markdown painter needs it, so the
// function belongs here and the highlighter outside reaches in.
//
// There were two before this: a private one in shell-highlight.ts and an
// inline chain in the block serializer. A third would have been the shape
// AGENTS.md names — a predicate implemented twice, agreeing everywhere
// anybody looked. So the highlighter now imports this, and so does the
// answer's markdown painter; both write model- or shell-authored bytes into
// innerHTML, which is exactly the pair that must never disagree.
//
// `&`, `<`, `>` and nothing else, because every caller writes TEXT CONTENT
// and never an attribute value. The moment a caller wants to interpolate a
// quoted attribute, this is not the function it needs — it needs one that
// escapes quotes too, and putting them here would let the wrong caller feel
// safe. Nothing in the renderer interpolates model text into an attribute,
// and nothing should.

const ESCAPE: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;' }

/** Escape `text` for use as HTML text content. */
export function escapeHtml(text: string): string {
  return text.replace(/[&<>]/g, (ch) => ESCAPE[ch])
}
