// ═══════════════════════════════════════════════════════════════════════════
// CMHost — the reusable CodeMirror 6 host (git-manager §5.4), in its two
// modes: ReadOnlyHost for a surface that only paints (the file viewer, the
// git diff) and EditableHost for one a person types into (the snippet body,
// design §10.4).
//
// One owner for everything such a surface needs from CM6: EditorView
// construction, the editability facets, and the base theme. A caller brings
// its own extensions — language selection, highlighting, decorations — and
// the host appends them AFTER its own. The host's facets come first in the
// extension array, and CM6 resolves facets by precedence with the first
// value winning, so a caller extension can never re-enable editing on the
// read-only mode.
//
// The two modes are one module rather than two files because they differ in
// exactly two facets and one theme rule. A sibling built beside this one
// would have been a second construction of the same view — free to drift on
// the theme, the disposal contract and the facet order, which is the drift
// the read-only guarantee is made of.
//
// The host renders no chrome: the caller creates the parent element and owns
// everything around it (notices, banners, diff decoration). The file viewer
// and the git diff surface share this module; neither constructs an
// EditorView itself.
//
// Lifecycle: mount() constructs and attaches the view and arms the given
// AbortSignal; dispose() destroys the view and is idempotent. Abort-driven
// disposal is the host's job, independent of whatever the caller does on its
// own dispose path — a surface that forgets to tear down cannot leak a live
// view when its tab dies.
// ═══════════════════════════════════════════════════════════════════════════

import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { EditorState, type Extension } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'

/** What an EDITABLE field needs before it is a field at all: the standard
 *  editing bindings and an undo history. The read-only modes take none of
 *  it — nothing they bind could change a document that cannot change.
 *
 *  Enter is the one that bites. With no binding it is not handled here, so
 *  it reaches the surface around the editor: a Return inside a dialog then
 *  submits the form instead of breaking the line the person was typing. The
 *  editor's own module learned the same lesson about history() — the
 *  package was a dependency and the extension was installed nowhere, so
 *  Ctrl+Z did nothing in a field that looked like every other one. */
/*  Wrapping belongs here and not to a caller, because it follows from the
 *  mode. What a person types into these hosts is PROSE — a snippet body, a
 *  note — and CM6 does not wrap unless it is told to, so a pasted paragraph
 *  came out as one line running off the right edge of the dialog, clipped at
 *  the frame with a horizontal scrollbar under it (nocx-dn33v).
 *
 *  The read-only modes deliberately do not take it. Their surfaces are the file
 *  viewer and the git diff, where a line is a line: wrapping one puts what the
 *  reader sees out of step with what the file says, and a diff whose rows stop
 *  aligning row-for-row has stopped being a diff. That is the same rule the
 *  terminal's frozen output is held to (nocx-juau) — long content is reached by
 *  scrolling sideways.
 *
 *  The command editor (src/editor.ts) states this for itself. It builds its own
 *  EditorView rather than going through this host, so there is nothing here for
 *  it to inherit; it is named so the next reader knows the two are not one
 *  setting with two spellings. */
const editingExtensions: Extension[] = [
  history(),
  keymap.of([...defaultKeymap, ...historyKeymap]),
  EditorView.lineWrapping,
]

/** CM6 look: colours only, resolved through the app's --color-* tokens so a
 *  theme switch recolours every host surface (ADR-0013). Layout lives in
 *  CSS. The diff surface inherits this by construction — it is the host's
 *  base theme, not a per-surface choice. */
const themeFor = (editable: boolean) =>
  EditorView.theme({
    '&': {
      // A read-only host IS the surface it fills — the file viewer and the
      // diff are the page. An editable one is a FIELD inside somebody's
      // layout, and a field that paints itself the canvas colour reads as a
      // hole cut in the dialog it sits in (owner review). Same token the
      // kit's text field uses, so the two fields of one form match.
      backgroundColor: editable ? 'var(--color-surface-raised)' : 'var(--color-canvas)',
      color: 'var(--color-text)',
    },
    '&.cm-focused': { outline: 'none' },
    // A caret is a promise that typing does something. The read-only modes
    // hide it; the editable one must not, or the field looks inert at the
    // moment it is focused.
    '.cm-content': { caretColor: editable ? 'var(--color-text)' : 'transparent' },
    '.cm-gutters': {
      backgroundColor: editable ? 'var(--color-surface-raised)' : 'var(--color-canvas)',
      color: 'var(--color-text-dim)',
      border: 'none',
    },
    '.cm-activeLine': { backgroundColor: 'var(--color-surface-hover)' },
    '.cm-activeLineGutter': { backgroundColor: 'var(--color-surface-hover)' },
    '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': {
      backgroundColor: 'var(--color-surface-active)',
    },
  })

/** What a document change reports back — the seam an editable surface's
 *  draft follows. Called for every change, including setDoc's. */
type DocChangeCallback = (text: string) => void

class CMHost {
  private view: EditorView | null = null
  private disposed = false
  private abortSignal: AbortSignal | null = null
  /** Bound once so dispose() can detach it; a late abort is then a no-op. */
  private readonly onAbort = (): void => this.dispose()

  protected constructor(private readonly editable: boolean) {}

  /**
   * Construct and mount the view into `parent`, with the caller's extensions
   * appended after the host's own (read-only enforcement + base theme). The
   * host never inspects the caller's extensions.
   *
   * The view is destroyed when `signal` aborts; a signal that was ALREADY
   * aborted before this call mounts nothing. Call at most once per host —
   * a second mount throws.
   */
  mount(
    parent: HTMLElement,
    signal: AbortSignal,
    extensions: Extension[] = [],
    onDocChange?: DocChangeCallback,
  ): void {
    if (this.disposed) return
    if (this.view) {
      throw new Error('nocx: ReadOnlyHost.mount called twice on one host')
    }
    if (signal.aborted) {
      this.disposed = true
      return
    }
    this.abortSignal = signal
    this.view = new EditorView({
      state: EditorState.create({
        doc: '',
        extensions: [
          EditorState.readOnly.of(!this.editable),
          EditorView.editable.of(this.editable),
          themeFor(this.editable),
          ...(this.editable ? editingExtensions : []),
          ...(onDocChange
            ? [
                EditorView.updateListener.of((u) => {
                  if (u.docChanged) onDocChange(u.state.doc.toString())
                }),
              ]
            : []),
          ...extensions,
        ],
      }),
      parent,
    })
    signal.addEventListener('abort', this.onAbort, { once: true })
  }

  /** Replace the whole document. Read-only is an input gate; the host may
   *  still paint content — the wire's bytes are the only writer. */
  setDoc(text: string): void {
    const view = this.view
    if (!view) return
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } })
  }

  /** The current document — '' before mount and after dispose, never a
   *  stale one: a surface that saved from a disposed host would write the
   *  contents of a field the person has already closed. */
  doc(): string {
    return this.view?.state.doc.toString() ?? ''
  }

  /** Focus the content. Inert before mount or after dispose. */
  focus(): void {
    this.view?.focus()
  }

  /** Destroy the view and detach from the abort signal. Idempotent; safe to
   *  call after the signal already fired, and vice versa. */
  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    this.abortSignal?.removeEventListener('abort', this.onAbort)
    this.abortSignal = null
    this.view?.destroy()
    this.view = null
  }
}

/** The mode for a surface that only paints: no keystroke can reach the
 *  document, and no caller extension can re-enable editing. */
export class ReadOnlyHost extends CMHost {
  constructor() {
    super(false)
  }
}

/** The mode for a surface a person types into (the snippet body editor).
 *  The caller reads `doc()` when it saves and follows `onDocChange` while
 *  the person types. */
export class EditableHost extends CMHost {
  constructor() {
    super(true)
  }
}
