// A response body, painted rather than dumped.
//
// It was a <pre> (the kit's CodeBlock): no line numbers, no highlighting, and
// its own scroll box stacked above a second one holding the headers. A JSON
// payload — which is most of them — arrived as a wall of monospace, and
// twenty kilobytes of HTML arrived as twenty kilobytes of monospace.
//
// The viewer is CMHost's READ-ONLY mode — the same host the file viewer and
// the git diff mount, and the same one the request body's editor uses one
// panel to the left. Read-only is enforced by the host's own facets, which no
// caller extension can re-enable (cm-host.ts): a response is a fact that
// arrived, and a caret that could change it would be a lie about what is
// being shown.
//
// WHAT DECIDES THE LANGUAGE is the CONTENT TYPE the server sent, never a
// guess at the bytes. A server that says `application/json` and sends
// something else is a server whose answer is worth seeing as it is; parsing
// the body to decide how to paint it would make the panel argue with the
// header, and the raw view is one tab away for exactly that case.

import { createEffect, createMemo, on, onCleanup } from 'solid-js'
import { ReadOnlyHost } from '../cm-host'
import { jsonEditing, viewerHighlighting } from '../file-viewer/language-registry'
import { lineNumbers } from '@codemirror/view'

export interface ResponseBodyProps {
  text: string
  /** `json` brings the grammar and its parser's opinion; `text` brings line
   *  numbers and nothing else. */
  language: 'json' | 'text'
  ariaLabel: string
}

export function ResponseBody(props: ResponseBodyProps) {
  let host: ReadOnlyHost | null = null
  let abort: AbortController | null = null
  let parentEl: HTMLElement | null = null

  // MEMOS, not bare accessors. `on(deps, fn)` re-runs whenever anything
  // `deps()` read has changed, and both of these are derived from a run that
  // is replaced whenever the list changes — so without the memo a new run
  // arriving would rebuild every OTHER run's viewer as well. The body editor
  // paid for this lesson with a caret (body-editor.tsx).
  const language = createMemo(() => props.language)
  const text = createMemo(() => props.text)

  const mount = (parent: HTMLElement): void => {
    parentEl = parent
    abort?.abort()
    abort = new AbortController()
    const next = new ReadOnlyHost()
    host = next
    next.mount(parent, abort.signal, [
      props.language === 'json' ? jsonEditing() : lineNumbers(),
      viewerHighlighting,
    ])
    next.setDoc(props.text)
  }

  createEffect(
    on(
      language,
      () => {
        if (parentEl) mount(parentEl)
      },
      { defer: true },
    ),
  )

  // The body of ONE run never changes — it is what came back — but the
  // component is reused across a list that grows at the head, so the props
  // can arrive pointing at a different exchange.
  createEffect(on(text, (next) => host?.setDoc(next), { defer: true }))

  onCleanup(() => {
    abort?.abort()
    abort = null
    host = null
    parentEl = null
  })

  return <div class="api-response-body" aria-label={props.ariaLabel} ref={(el) => mount(el)} />
}
