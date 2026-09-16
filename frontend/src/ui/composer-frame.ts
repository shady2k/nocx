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
  setSubmitHint(label: string): void
  /** The target-switch chord's hint: its keys, one keycap each, and what it
   *  does from the target currently active — e.g. `['⌘', '↵'], 'ask'`. */
  setSwitchHint(keys: readonly string[], label: string): void
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
  paintHint(submitHint, ['↵'], 'run')
  const switchHint = document.createElement('span')
  switchHint.dataset.hint = 'switch'
  const newlineHint = document.createElement('span')
  paintHint(newlineHint, ['⇧', '↵'], 'newline')
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
    setSubmitHint(label: string): void {
      paintHint(submitHint, ['↵'], label)
    },
    setSwitchHint(keys: readonly string[], label: string): void {
      paintHint(switchHint, keys, label)
      switchHint.hidden = keys.length === 0
    },
    dispose(): void {
      root.remove()
    },
  }
}

/** Write one hint the way Warp does: each key in its own keycap, then what
 *  it does in plain words (owner review, 2026-09-15). The keycap boxes also
 *  absorb the mono face's missing ↵ ⇧ ⌘ glyphs, whose borrowed advances no
 *  longer have to line up with anything. */
function paintHint(host: HTMLElement, keys: readonly string[], label: string): void {
  host.replaceChildren()
  host.classList.add('ui-composer-frame__hint-item')
  for (const k of keys) {
    const cap = document.createElement('kbd')
    cap.className = 'ui-composer-frame__key'
    cap.textContent = k
    host.append(cap)
  }
  const text = document.createElement('span')
  text.className = 'ui-composer-frame__hint-label'
  text.textContent = label
  host.append(text)
}
