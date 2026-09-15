// ComposerFrame (ui/README table) — the composer's outer inset card and
// inner bordered field (decision record .internal/specs/2026-09-15-terminal-
// screen-mockup-decision.md §4, task C). A passive, vanilla kit component:
// it owns the CARD's own box (margin, padding, border, radius, ground) and
// the FIELD's grid (a leading target-switch column, CM6's mount parent, and
// the submit control's host) plus the field's own border. It never owns
// document state, submission, or keyboard dispatch — CommandEditor
// (editor.ts) fills every slot and drives `setFocused` from its own focus
// notifications.
//
// THE FIELD'S BORDER LIVES ON THIS WRAPPER, an ANCESTOR of CM6's own root,
// never on `.cm-editor` itself and never on both at once (spec §4: "the
// field wrapper, not CM6 and the wrapper simultaneously, owns the input
// border"). CM6 rewrites `.cm-editor`'s class attribute from its
// `editorAttributes` facet on every update whose derived string changes —
// in particular the first focus, which flips in `cm-focused` via a blind
// `setAttribute('class', …)` — so a class added to `view.dom` by hand
// (`view.dom.classList.add(...)`) is wiped the moment CM6 next recomputes
// it (round 3 regression: present unfocused, gone the instant the field
// focused). Moving the bordered element one level UP, to an element CM6
// never touches, sidesteps the rewrite entirely instead of routing around
// it with `editorAttributes` concatenation. `setFocused` is written from
// CommandEditor's EXISTING focusin/focusout listeners (root, not CM6), so
// the typed `data-focused` attribute here never depends on CM6's own class
// at all.
//
// THE CHROME ROW (prompt + controls) IS THE CALLER'S ELEMENT, not this
// module's — `createComposerFrame` takes it as a parameter rather than
// building it. Two reasons, not one: it is genuinely surface content
// (editor.ts's PromptContext and chip row, unchanged since before this
// component existed), and the kit-identity scanner
// (lint-fixtures/scan-kit-identities.mjs) derives "which classes a surface
// may not repaint" from every static `.className` assignment inside `ui/`
// — so building `nocx-editor-chrome` HERE would silently move its existing,
// deliberate appearance rules in composer.css (the fixed meta-row height
// nocx-i4h04/nocx-6c546 exist to hold, terminal-content.test.ts's own read
// of that exact file) into violations of the very rule this file is
// written to respect. Accepting it as a parameter keeps that ownership
// exactly where it already was.

export interface ComposerFrameHandle {
  /** `.ui-composer-frame` — the outer inset card. */
  root: HTMLElement
  /** `.ui-composer-frame__field` — the bordered box: the target switch,
   *  the CM6 host and the submit control share this ONE row (spec §6: "the
   *  CM6 editor and the mode switch sit inside one box"). */
  field: HTMLElement
  /** `.ui-composer-frame__editor` — CM6's mount parent (`parent:` on the
   *  EditorView config). The target switch is inserted as this element's
   *  preceding sibling (ask-entry.ts's `TargetIndicator.mount`), which is
   *  what puts it in the grid's leading column by construction — no
   *  separate "mode" slot is needed for that. */
  editor: HTMLElement
  /** `.ui-composer-frame__submit` — the submit control's host. */
  submit: HTMLElement
  /** Project focus onto the field's border. See the file header — never a
   *  read of CM6's own `cm-focused` class. */
  setFocused(focused: boolean): void
  /** Remove the frame from its parent. CommandEditor's own `dispose()`
   *  still owns the CM6 view, the chrome row and every listener it
   *  installed; this is only the frame's own DOM teardown. */
  dispose(): void
}

/** `chrome` is the caller's already-built prompt/control row (editor.ts's
 *  `.nocx-editor-chrome`) — placed inside the card, above the field, never
 *  repainted or reparented beyond that one move. See the file header for
 *  why this module does not build it itself. */
export function createComposerFrame(chrome: HTMLElement): ComposerFrameHandle {
  const root = document.createElement('div')
  root.className = 'ui-composer-frame'

  const field = document.createElement('div')
  field.className = 'ui-composer-frame__field'
  field.dataset.focused = 'false'

  const editor = document.createElement('div')
  editor.className = 'ui-composer-frame__editor'

  const submit = document.createElement('div')
  submit.className = 'ui-composer-frame__submit'

  field.append(editor, submit)
  root.append(chrome, field)

  return {
    root,
    field,
    editor,
    submit,
    setFocused(focused: boolean): void {
      field.dataset.focused = focused ? 'true' : 'false'
    },
    dispose(): void {
      root.remove()
    },
  }
}
