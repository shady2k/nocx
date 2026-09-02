// Disclosure — a native <details> the answer uses for everything a person
// may open and usually will not (nocx-s92so, nocx-hp8p2.13, ui/README table).
//
// Vanilla-emitted for the same reason ToolCallLine is: its only home is the
// scrollback's answer block, which is imperative DOM.
//
// TWO RULES DECIDE THE WHOLE SHAPE.
//
// It is NOT the answer. eino decodes `reasoning_content` off the wire into
// schema.Message.ReasoningContent and we used to drop it on the floor; the
// wrong fix is to append it to the answer text, which is the tool-result
// defect (nocx-bshm2) in another shape. So it is its own element, with its
// own body, and the answer never contains it. A tool's result is the same
// sentence a second time: what a call returned belongs beside the answer,
// never inside it.
//
// It is COLLAPSED. Thinking is usually many times longer than the answer,
// and a person opened the answer to read the answer; a tool result is longer
// still. The disclosure is a native <details>: it is the browser's own
// summary/expand control, keyboard operable and announced correctly without
// a line of our own script, and a hand-rolled toggle would be the kit rule's
// exact prohibition.
//
// ONE COMPONENT, TWO KINDS, because they are one thing. `kind` is a typed
// data attribute rather than a second module: the thinking note and a tool
// call's expansion are the same disclosure with different words on the
// summary, and the day they were two components is the day their padding,
// their marker and their colour started to drift. The attribute is what
// selects them apart where they genuinely differ — the expandReasoning
// setting applies to THINKING and must not reach for a tool result
// (reasoning-expanded.ts), and only the tool result's body is height-capped.
//
// A model that returns no reasoning gets NOTHING — no empty section, no
// placeholder. That is the caller's contract too: the thinking note is
// constructed at the first chunk, never up front.
//
// COLLAPSED IS THE DEFAULT, NOT A LAW (nocx-y9e88). A person who wants to
// watch the model think says so once, in Settings, and `expanded` is that
// answer arriving here — the caller reads the setting (reasoning-expanded.ts)
// and this disclosure obeys it. It stays a native <details> either way, so
// the person can close an opened one by clicking its summary: the setting
// decides what a note DOES until somebody says otherwise, which is the same
// shape `terminal.wrapOutput` has for a command block.

/** Which disclosure this is. A closed set, because the CSS and the settings
 *  applier both select on it. */
type DisclosureKind = 'reasoning' | 'tool-result'

/** What the caller may decide about a disclosure as it is built. */
export interface DisclosureSpec {
  kind: DisclosureKind
  /** The word on the summary — what a person is being offered. */
  summary: string
  /** Render it open. The setting's value at the moment it was built; false,
   *  and absent, both mean closed. */
  expanded?: boolean
}

export interface Disclosure {
  /** The disclosure itself. Typed as the element it is, so a caller that
   *  opens or closes one — the settings applier — needs no cast. */
  readonly el: HTMLDetailsElement
  /** Append one chunk. The body is `white-space: pre-wrap`, so the model's
   *  own line breaks survive and no chunk boundary shows. */
  append(text: string): void
  /** Replace the body — what a body that ARRIVES WHOLE needs, as a tool
   *  result read back from the ledger does. Distinct from `append` on
   *  purpose: a fetch that lands twice must not print itself twice. */
  set(text: string): void
}

export function createDisclosure(spec: DisclosureSpec): Disclosure {
  const root = document.createElement('details')
  root.className = 'ui-disclosure'
  root.dataset.kind = spec.kind
  root.open = spec.expanded === true

  const summary = document.createElement('summary')
  summary.className = 'ui-disclosure__summary'
  summary.textContent = spec.summary
  root.appendChild(summary)

  const body = document.createElement('div')
  body.className = 'ui-disclosure__body'
  root.appendChild(body)

  return {
    el: root,
    append(text: string): void {
      if (text === '') return
      body.textContent = (body.textContent ?? '') + text
    },
    set(text: string): void {
      body.textContent = text
    },
  }
}
