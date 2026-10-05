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
  el.setAttribute('aria-label', 'Terminal input')
  el.setAttribute('role', 'textbox')
  el.spellcheck = false
  el.rows = 1
  return el
}
