// ReasoningNote — the model's thinking, beside the answer and never inside
// it (nocx-s92so, ui/README table).
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
// own body, and the answer never contains it.
//
// It is COLLAPSED. Thinking is usually many times longer than the answer,
// and a person opened the answer to read the answer. The disclosure is a
// native <details>: it is the browser's own summary/expand control, keyboard
// operable and announced correctly without a line of our own script, and a
// hand-rolled toggle would be the kit rule's exact prohibition.
//
// A model that returns no reasoning gets NOTHING — no empty section, no
// placeholder. That is the caller's contract too: this is constructed at the
// first chunk, never up front.
//
// COLLAPSED IS THE DEFAULT, NOT A LAW (nocx-y9e88). A person who wants to
// watch the model think says so once, in Settings, and `expanded` is that
// answer arriving here — the caller reads the setting (reasoning-expanded.ts)
// and this note obeys it. It stays a native <details> either way, so the
// person can close an opened note by clicking its summary: the setting
// decides what a note DOES until somebody says otherwise, which is the same
// shape `terminal.wrapOutput` has for a command block.

/** What the caller may decide about a note as it is built. */
export interface ReasoningNoteSpec {
  /** Render the note open. The setting's value at the moment the note was
   *  built; false, and absent, both mean closed. */
  expanded?: boolean
}

export interface ReasoningNote {
  /** The disclosure itself. Typed as the element it is, so a caller that
   *  opens or closes one — the settings applier — needs no cast. */
  readonly el: HTMLDetailsElement
  /** Append one chunk. The body is `white-space: pre-wrap`, so the model's
   *  own line breaks survive and no chunk boundary shows. */
  append(text: string): void
}

export function createReasoningNote(spec: ReasoningNoteSpec = {}): ReasoningNote {
  const root = document.createElement('details')
  root.className = 'ui-reasoning'
  root.open = spec.expanded === true

  const summary = document.createElement('summary')
  summary.className = 'ui-reasoning__summary'
  summary.textContent = 'Thinking'
  root.appendChild(summary)

  const body = document.createElement('div')
  body.className = 'ui-reasoning__body'
  root.appendChild(body)

  return {
    el: root,
    append(text: string): void {
      if (text === '') return
      body.textContent = (body.textContent ?? '') + text
    },
  }
}
