// createTerminalInput — the pane's input element, emitted without Solid.
//
// WHY THE KIT OWNS IT. It is a real focusable textarea, and the rule that a
// control belongs to the kit does not stop applying because the control is
// invisible (ADR-0014; the lint refuses a raw textarea anywhere but here). Its
// identity class, its stylesheet and its accessible name are the kit's, exactly
// as createButton's are Button's.
//
// WHAT IT IS FOR (nocx-zg3k3.3.1). The pane paints its screen into cells, so
// there is no editable text under the caret and no caret for a browser to
// measure — and an IME draws its candidate window against a focused element's
// own caret. xterm carried one for the same reason; this is that element for
// the pane, minus the terminal. A person never reads it: it is transparent,
// one pixel wide and pointer-inert, and the surface places it where the caret
// is (painter's onCaretPlaced). What it carries is the KEYBOARD: every key, IME
// commit and focus change is read from it as intent.

export function createTerminalInput(): HTMLTextAreaElement {
  const el = document.createElement('textarea')
  // The identity class of frontend/src/styles/components/terminal-input.css.
  el.className = 'ui-terminal-input'
  // An IME anchor and not a buffer: the browser's own text services must not
  // help. Autocorrect would rewrite what the person typed before it was ever
  // committed, and a spellchecker would decorate a caret nobody sees.
  el.setAttribute('autocomplete', 'off')
  el.setAttribute('autocorrect', 'off')
  el.setAttribute('autocapitalize', 'off')
  // ── NOT A SURFACE ANYBODY READS, AND IT SAYS SO (nocx-7jd59) ─────────────
  //
  // This element was written carrying aria-label="Terminal input" and
  // role="textbox", by analogy with xterm's helper textarea — and that made a
  // pane answer a screen reader with TWO textboxes of one name, with nothing
  // to say which of them the person's typing reaches. The analogy is what was
  // wrong: xterm's helper textarea IS xterm's accessibility surface (its
  // screen-reader mode reads and writes it), while this one carries nothing
  // anybody can read — it never keeps a value, it is one pixel wide and
  // transparent, and it is pointer-inert, so no gesture of the person's ever
  // reaches it. It is focused by the pane itself, on the pane's own path.
  //
  // THE READING REJECTED: give the anchor a name of its own and leave both
  // exposed (say 'IME composition anchor'). It was rejected because it answers
  // a reading-order question with a control nobody can use: a second textbox
  // in the pane is still a second place the person's typing can appear to go,
  // and a different name only tells them which of the two is not for them —
  // while a reader navigating by controls has no way at all to prefer the grid.
  // Measured 2026-10-06, before this change: both engines' aria snapshots and
  // chromium's OWN accessibility tree (Accessibility.getFullAXTree) each held
  // two textboxes named 'Terminal input' for one pane.
  //
  // WHAT IS LEFT FOR A READER is what carried the pane before this element
  // existed: the painted grid, role="grid" with aria-label 'Terminal output',
  // whose rows are the terminal's content. Nothing a person could read is
  // taken away here, because nothing was ever read from this element.
  //
  // THE LIMIT, stated rather than implied, because it is the whole reason this
  // is a decision and not one attribute: an IME measures the caret rect of a
  // FOCUSED editable, so this element MUST stay focusable, and aria-hidden is
  // not supposed to cover a focusable element. Measured with
  // Accessibility.getPartialAXTree, one node at a time: chromium honours the
  // attribute on this element while it is UNFOCUSED (ignored: true), and
  // exposes it the moment it holds focus (role textbox, ignored: false), as it
  // does for any focused element — a control carrying the same attribute but
  // no focusability is ignored throughout. So what this change delivers is ONE
  // NAMED INPUT PER PANE; the entry chromium keeps for the focused anchor
  // carries no name to offer, and no attribute can remove it while the IME
  // needs the caret.
  el.setAttribute('aria-hidden', 'true')
  // NOT IN THE TAB ORDER either: Tab must not stop on a caret nobody can see.
  // tabindex="-1" rather than `disabled`, which would take the caret the IME
  // measures — and `focus()` from the pane's own path still reaches this.
  el.tabIndex = -1
  el.spellcheck = false
  el.rows = 1
  return el
}
