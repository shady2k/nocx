// The request body, in the editor the rest of the product already uses.
//
// It was a `<textarea>`: no line numbers, no highlighting, no bracket
// matching, and a body of any size edited through a four-line box. A body is
// JSON far more often than it is anything else, and JSON in a textarea is the
// thing every API client stopped shipping a decade ago.
//
// The editor is CMHost's EditableHost — the same host the snippet body and
// the notes surface mount (cm-host.ts), and the language comes from the file
// viewer's registry, which is the one owner of "which CM6 language does this
// surface get". Nothing here constructs an EditorView or picks a theme.
//
// WHO OWNS THE TEXT, both ends named: from the moment the host mounts until
// the request being edited changes, the HOST is the truth and every change it
// reports goes up to the draft. When a different request is opened the store
// is the truth and `setDoc` puts it in the host. There is no third case, and
// that is deliberate: pushing the draft back into the host on every keystroke
// would move the caret to the end of the document on every character — the
// same defect as the one the tab panels had, one layer down.

import { createEffect, createMemo, on, onCleanup } from 'solid-js'
import { EditableHost } from '../cm-host'
import { jsonEditing, viewerHighlighting } from '../file-viewer/language-registry'
import { lineNumbers } from '@codemirror/view'

export interface BodyEditorProps {
  /** The body text, as the draft holds it. */
  text: string
  /**
   * What the text IS, which is the body's kind said in the editor's words.
   *
   * `json` brings the JSON grammar, its parser's opinion about where the
   * document stops being JSON, and the gutter that marks it. `text` brings
   * neither: a form body or a raw one is not JSON, and underlining it in red
   * for not being JSON would be the panel arguing with a person who already
   * told it what the body is.
   *
   * Changing it REBUILDS the view. CM6 takes its extensions at construction
   * and this surface holds no handle to reconfigure them, so the honest
   * answer is a fresh editor — which costs the undo history, and is what
   * changing the body's kind means anyway.
   */
  language: 'json' | 'text'
  /**
   * What is being edited. When this changes the host is re-filled from
   * `text`; while it stays the same the host is left alone. It is the
   * request's own id rather than a counter, so opening the same request
   * twice is not a reason to throw away what is in the editor.
   */
  docKey: string
  onChange: (text: string) => void
}

export function BodyEditor(props: BodyEditorProps) {
  let host: EditableHost | null = null
  let abort: AbortController | null = null
  // TRUE WHILE WE ARE THE ONES WRITING. CMHost reports every document
  // change, "including setDoc's" (cm-host.ts says so in as many words), so
  // filling the editor looked exactly like a person typing: the fill
  // reported a change, the change went into the draft, the draft re-rendered
  // the panel, the panel filled the editor again. That is an unbounded
  // render loop, and it took the tab down — `Maximum call stack size
  // exceeded`, from inside CM6's update listener.
  //
  // The flag is safe because setDoc dispatches SYNCHRONOUSLY: the listener
  // has run and returned before the assignment below it.
  let filling = false

  const fill = (text: string): void => {
    // Nothing to do, and doing it anyway would move the caret to the start
    // of the document for no reason. Cheap, and it makes every fill path
    // idempotent rather than only the ones that happen to be guarded.
    if (host === null || host.doc() === text) return
    filling = true
    try {
      host?.setDoc(text)
    } finally {
      filling = false
    }
  }

  /** The box the view lives in, kept so a language change can rebuild into
   *  the same element. */
  let parentEl: HTMLElement | null = null

  const mount = (parent: HTMLElement): void => {
    parentEl = parent
    abort?.abort()
    abort = new AbortController()
    // A REQUEST BODY IS CODE, not prose (cm-host.ts's EditableContent). It
    // wrapped, which is the default the snippet body and the notes surface
    // want and the wrong one here: one long value — a token, a base64 blob —
    // came back as a stack of rows whose continuations sat against the line
    // numbers, and the only thing that could be moved sideways was the pane
    // around the editor, which moved the whole surface with it. Unwrapped,
    // the long line is reached inside this box, and CM6's gutter is sticky
    // so the numbers stay put while the text moves (nocx-kdawd).
    const next = new EditableHost('code')
    host = next
    // The language is the KIND's, and the line numbers are unconditional:
    // an error a person is told about on line 14 is only useful next to a
    // line 14.
    const language = props.language === 'json' ? jsonEditing() : lineNumbers()
    // The host calls back on a keystroke, which is an event; `props.onChange`
    // is read at call time, not captured.
    // eslint-disable-next-line solid/reactivity -- event callback
    next.mount(parent, abort.signal, [language, viewerHighlighting], (text) => {
      if (filling) return
      props.onChange(text)
    })
    fill(props.text)
  }

  // ── The two things that may rebuild or refill the view ─────────────────
  //
  // MEMOS, NOT BARE ACCESSORS, and this is the whole of why the editor lost
  // the caret after every character. `on(deps, fn)` does not compare: it runs
  // `fn` whenever the computation re-runs, and that computation re-runs
  // whenever anything `deps()` READ has changed. Both of these read the
  // request — `props.language` is derived from the body's kind and
  // `props.docKey` from the request's id — so both were subscribed to the
  // draft, and the draft changes on every keystroke. The language effect
  // then called mount(), which destroys the view and builds a new one: type
  // one character, and the editor you were typing into no longer exists.
  //
  // A createMemo compares with === and propagates only on a real change, so
  // the effects below fire when the LANGUAGE changes and when the REQUEST
  // changes, which is what they were always meant to say.
  const language = createMemo(() => props.language)
  const docKey = createMemo(() => props.docKey)

  // A LANGUAGE CHANGE IS A NEW VIEW. Deferred, so the first build is
  // mount's; `mount` aborts the previous host, and CM6 removes its own DOM
  // when it is destroyed, so the box is empty before the new one arrives.
  createEffect(
    on(
      language,
      () => {
        if (parentEl) mount(parentEl)
      },
      { defer: true },
    ),
  )

  createEffect(
    on(
      docKey,
      () => fill(props.text),
      // Deferred: the first fill is mount's, above, and doing it twice would
      // reset a caret the person had already placed.
      { defer: true },
    ),
  )

  onCleanup(() => {
    abort?.abort()
    abort = null
    host = null
    parentEl = null
  })

  return <div class="api-body-editor" ref={(el) => mount(el)} />
}
