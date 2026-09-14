// ── Element shape, for emitter parity tests ─────────────────────────────────
//
// A kit component emitted twice — once by Solid, once by a vanilla function for
// imperative code that may not use Solid (ADR-0012) — is two implementations of
// one DOM contract. This reduces an element to what that contract is made of:
// the tag, every attribute and value, and the same for each child, so that two
// emitters can be compared for exact agreement. Event listeners are not part of
// it (Solid delegates them; nothing in the DOM says so).

export interface ElementShape {
  tag: string
  attrs: Array<[string, string]>
  children: Array<ElementShape | string>
}

/** The shape of one node: an element's tag, sorted attributes and children,
 *  or `#text:<content>` for a text node. Comment nodes (Solid leaves markers
 *  for some control flow) are dropped. */
export function elementShape(node: Node): ElementShape | string {
  if (node.nodeType === Node.TEXT_NODE) return `#text:${node.textContent ?? ''}`
  const el = node as Element
  const attrs = Array.from(el.attributes)
    .map((a): [string, string] => [a.name, a.value])
    .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0))
  const children = Array.from(el.childNodes)
    .filter((c) => c.nodeType === Node.ELEMENT_NODE || c.nodeType === Node.TEXT_NODE)
    .map(elementShape)
  return { tag: el.tagName.toLowerCase(), attrs, children }
}

/** Throws with both shapes when the two elements are not the same contract. */
export function assertSameShape(actual: Element, expected: Element): void {
  const a = JSON.stringify(elementShape(actual))
  const b = JSON.stringify(elementShape(expected))
  if (a !== b) {
    throw new Error(`emitters disagree:\n  vanilla: ${a}\n  solid:   ${b}`)
  }
}
