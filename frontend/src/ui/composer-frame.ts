// Passive kit card: context, open input row and keyboard hints.
// CommandEditor owns document state, focus and submission.
export interface ComposerFrameHandle {
  /** `.ui-composer-frame` — the outer inset card. */
  root: HTMLElement
  /** `.ui-composer-frame__field` — the open input row: the target switch,
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
  /** Record input focus for kit presentation. See the file header — never a
   *  read of CM6's own `cm-focused` class. */
  setFocused(focused: boolean): void
  /** Update the Enter hint when the input target changes. */
  setSubmitHint(text: string): void
  /** The target-switch chord's hint, e.g. `⌘↵ ask` — what the
   *  ⌘/Ctrl+Enter chord does from the target currently active. */
  setSwitchHint(text: string): void
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
  const hint = document.createElement('div')
  hint.className = 'ui-composer-frame__hint'
  const submitHint = document.createElement('span')
  paintHint(submitHint, '↵ run')
  const switchHint = document.createElement('span')
  switchHint.dataset.hint = 'switch'
  const newlineHint = document.createElement('span')
  paintHint(newlineHint, '⇧↵ newline')
  hint.append(submitHint, switchHint, newlineHint)
  root.append(chrome, field, hint)

  return {
    root,
    field,
    editor,
    submit,
    setFocused(focused: boolean): void {
      field.dataset.focused = focused ? 'true' : 'false'
    },
    setSubmitHint(text: string): void {
      paintHint(submitHint, text)
    },
    setSwitchHint(text: string): void {
      paintHint(switchHint, text)
      switchHint.hidden = text === ''
    },
    dispose(): void {
      root.remove()
    },
  }
}

/** Write a hint, holding every key symbol to one mono cell. The mono face has
 *  no ↵ ⇧ ⌘ of its own, so the browser borrows them from a symbol font whose
 *  advances differ, and `↵ run` stops lining up with `Ctrl↵ ask`. A symbol
 *  therefore sits in its own `__key` box one `ch` wide; the text stays
 *  `textContent`, so a reader hears the same words. */
function paintHint(host: HTMLElement, text: string): void {
  host.replaceChildren()
  let run = ''
  for (const ch of text) {
    if (/[\u2190-\u23ff]/.test(ch)) {
      if (run) host.append(run)
      run = ''
      const key = document.createElement('span')
      key.className = 'ui-composer-frame__key'
      key.textContent = ch
      host.append(key)
    } else {
      run += ch
    }
  }
  if (run) host.append(run)
}
