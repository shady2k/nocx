// ═══════════════════════════════════════════════════════════════════════════
// DocumentSurface — ONE canvas for a text document (nocx-qfdy7): shown,
// edited, searched, numbered, highlighted, or rendered.
//
// WHAT IT IS AND, MORE IMPORTANTLY, WHAT IT IS NOT. It owns a document's
// CANVAS and nothing else. It becomes the god-component the moment it owns
// file loading, bindings, reload, autosave, git state, binary refusals or
// terminal input — so it owns none of those, and the surfaces that have them
// keep them. What collapses into this is the canvas of the skill file (the
// first caller), and later the canvas — only the canvas — of the file
// viewer, the git diff, the snippet body, the note body and the API bodies.
// What never collapses in: `editor.ts`, which owns terminal focus,
// capture-phase keys, a block cursor and a height driven by the terminal
// grid; and `CodeBlock`/`FileReadout`, which are short machine output with
// arbitrary JSX children and three refusals to draw.
//
// THE SURVEY THAT SETTLED THE SHAPE. VS Code's universal component is Monaco
// — read-only is a FLAG, find is built in, the diff editor is a wrapper over
// two instances — and its markdown preview is NOT in it: that is a separate
// webview rendering markdown-it, and Monaco only highlights markdown SOURCE.
// termic, the closest sibling (a terminal on CM6), does the same with an
// Editor/Preview/Split toolbar. So rendering is a SIBLING of the editor, not
// a mode of it — and this component can still be universal at the CALL SITE,
// one import and one prop surface, by delegating internally.
//
// THE PROPS MAKE NONSENSE UNREPRESENTABLE, which is the other half of the
// design. A discriminated union forbids, at compile time: an `onChange` on a
// read-only document, a preview of a language that is not markdown, an
// editable preview with no source pane to edit in, line numbers or wrapping
// on rendered output that has no source lines, an editable `readout`, and a
// caller-supplied pixel height. Everything a caller can express is something
// this component can honestly do.
//
// HEIGHT IS A CHOICE FROM THREE, NEVER A NUMBER. `fill` means the parent has
// given a definite height and the document takes it; `field` is the kit's own
// editor-field height for a control inside somebody's form; `readout` is the
// kit's capped read-only height. A pixel prop would have put layout in the
// caller's hands, which is the thing `ui/README.md` forbids in the other
// direction too.
//
// THE SOURCE VIEW IS NOT UNMOUNTED WHEN IT IS HIDDEN. In `markdown` mode
// with `view: 'preview'` the EditorView stays mounted and hidden, because
// unmounting throws away the caret, the selection and the undo history —
// and CM caches geometry, so it is told to re-measure when it comes back
// (CMHost.requestMeasure). An editor mounted inside `display: none` measured
// a box of zero, and every click afterwards landed on the wrong line.
//
// A RENDERED DOCUMENT HAS NO SOURCE LINES. `revealLine` therefore switches a
// markdown surface to its source view first: a finding addressed at line 42
// is about the file, and pointing at a paragraph that came from four lines
// would be a claim nobody can check.
//
// CMHost lives in this kit directory because it is the engine behind this
// reusable canvas, not a surface dependency. Keeping it in `src/` would make
// the kit import outward and fail the layer rule; moving the existing host
// keeps all current callers on one implementation without migrating their
// surfaces.
// ═══════════════════════════════════════════════════════════════════════════

import { createEffect, createMemo, on, onCleanup, onMount, Show, type JSX } from 'solid-js'
import type { Extension } from '@codemirror/state'
import { EditableHost, ReadOnlyHost } from './cm-host'
import { languageForName, viewerHighlighting, type DocumentLanguage } from './document-language'
import { renderMarkdown } from './markdown-preview'
import { lineNumbers } from '@codemirror/view'

export type { DocumentLanguage }

/** Whether CM's find panel and its keymap are installed. A surface says
 *  this out loud because a keybinding nobody asked for is a keybinding that
 *  steals a chord from whatever the surface already bound. */
export type DocumentSearch = 'enabled' | 'disabled'

/** Whether a long line wraps or is reached by scrolling sideways. `none` is
 *  what a diff and a file viewer need — rows that stop aligning have stopped
 *  being a diff — and `soft` is what prose needs. */
export type DocumentWrap = 'soft' | 'none'

/** Which half of a markdown workbench is on screen. */
export type MarkdownView = 'source' | 'preview' | 'split'

export interface DocumentSourceOptions {
  wrap: DocumentWrap
  lineNumbers: boolean
  /** The caller's own decorations — diff colouring, an emphasised line, a
   *  scan mark. Appended AFTER the host's own facets, which is what keeps a
   *  caller extension from re-enabling input on a read-only document. */
  extensions?: readonly Extension[]
}

/** What a caller can ask of a mounted surface. Handed over through
 *  `onHandle`, and null again once the surface is gone — a caller holding a
 *  stale handle would be driving an editor that no longer exists. */
export interface DocumentSurfaceHandle {
  focus(): void
  /** 1-based, as a terminal, a compiler and a person all count lines. On a
   *  markdown surface this switches to the source view first: see the
   *  header. */
  revealLine(line: number): void
  openSearch(): void
  /** The document as it now stands — what an editable caller saves. */
  text(): string
}

interface DocumentCommon {
  text: string
  /**
   * Stable while this is the same logical document.
   *
   * A CHANGED key resets the surface: a different file, a different skill,
   * a different snippet — and with it the selection and the undo history,
   * because carrying a caret from one document into another is how a person
   * types into the wrong file. The SAME key with different text replaces the
   * contents in place, which is what a re-read of one file is.
   */
  documentKey: string
  ariaLabel: string
  search: DocumentSearch
  height: 'fill' | 'field' | 'readout'
  onHandle?: (handle: DocumentSurfaceHandle | null) => void
}

type ReadOnlyDocument = { readOnly: true; onChange?: never }
type EditableDocument = {
  readOnly: false
  /** An editable document is never a `readout` — that height is the kit's
   *  cap for something nobody types into. */
  height: 'fill' | 'field'
  onChange: (text: string) => void
}

type SourcePresentation = {
  language: DocumentLanguage
  presentation: { kind: 'source'; source: DocumentSourceOptions }
}
type PreviewPresentation = {
  language: 'markdown'
  presentation: { kind: 'preview' }
}
type MarkdownPresentation = {
  language: 'markdown'
  presentation: {
    kind: 'markdown'
    view: MarkdownView
    onViewChange: (view: MarkdownView) => void
    source: DocumentSourceOptions
  }
}

export type DocumentSurfaceProps =
  | (DocumentCommon & ReadOnlyDocument & SourcePresentation)
  | (DocumentCommon & EditableDocument & SourcePresentation)
  | (DocumentCommon & ReadOnlyDocument & PreviewPresentation)
  | (DocumentCommon & ReadOnlyDocument & MarkdownPresentation)
  | (DocumentCommon & EditableDocument & MarkdownPresentation)

export function DocumentSurface(props: DocumentSurfaceProps): JSX.Element {
  let sourceEl: HTMLDivElement | undefined
  let previewEl: HTMLDivElement | undefined
  let host: ReadOnlyHost | EditableHost | null = null
  let controller: AbortController | null = null

  const sourceOptions = createMemo<DocumentSourceOptions | null>(() => {
    const presentation = props.presentation
    return presentation.kind === 'preview' ? null : presentation.source
  })
  const view = createMemo<MarkdownView>(() => {
    const presentation = props.presentation
    return presentation.kind === 'markdown'
      ? presentation.view
      : presentation.kind === 'preview'
        ? 'preview'
        : 'source'
  })
  const onViewChange = createMemo<((view: MarkdownView) => void) | null>(() => {
    const presentation = props.presentation
    return presentation.kind === 'markdown' ? presentation.onViewChange : null
  })
  const showsSource = (): boolean => view() !== 'preview'
  const showsPreview = (): boolean => view() !== 'source'

  /** The source pane exists at all only when this surface HAS one. A
   *  preview-only document is not an editor with its editor hidden. */
  const hasSource = (): boolean => sourceOptions() !== null

  const buildHost = (): void => {
    const options = sourceOptions()
    if (!options || !sourceEl) return
    controller = new AbortController()
    const readOnly = props.readOnly
    const searchable = props.search === 'enabled'
    const content = options.wrap === 'soft' ? 'prose' : 'code'
    const onChange = readOnly ? undefined : props.onChange
    host = readOnly ? new ReadOnlyHost(content, searchable) : new EditableHost(content, searchable)
    host.mount(
      sourceEl,
      controller.signal,
      [
        languageForName(props.language),
        viewerHighlighting,
        ...(options.lineNumbers ? [lineNumbers()] : []),
        ...(options.extensions ?? []),
      ],
      onChange,
    )
    host.setDoc(props.text)
  }

  const tearDownHost = (): void => {
    controller?.abort()
    controller = null
    host = null
  }

  onMount(() => {
    buildHost()
    props.onHandle?.({
      focus: () => (showsSource() ? host?.focus() : previewEl?.focus()),
      revealLine: (line) => {
        // A rendered document has no line to point at, so the surface goes
        // to the source first. `split` already shows it.
        if (view() === 'preview') onViewChange()?.('source')
        host?.revealLine(line)
      },
      openSearch: () => host?.openSearch(),
      text: () => host?.doc() ?? props.text,
    })
  })

  onCleanup(() => {
    props.onHandle?.(null)
    tearDownHost()
  })

  /** A different document is a different surface: the host is rebuilt, which
   *  is what drops the caret and the undo history with it. */
  createEffect(
    on(
      () => props.documentKey,
      (_key, previous) => {
        if (previous === undefined) return
        tearDownHost()
        if (sourceEl) sourceEl.replaceChildren()
        buildHost()
      },
    ),
  )

  /** Same document, new bytes — a re-read. Replaced in place, so a person
   *  who had scrolled stays roughly where they were. */
  createEffect(
    on(
      () => props.text,
      (text, previous) => {
        if (previous === undefined) return
        if (host?.doc() !== text) host?.setDoc(text)
      },
    ),
  )

  /** CM caches geometry, and a view that was mounted or updated while hidden
   *  measured a box of zero. Re-measure whenever the source comes back. */
  createEffect(
    on(view, () => {
      if (showsSource()) host?.requestMeasure()
    }),
  )

  createEffect(() => {
    if (!showsPreview() || !previewEl) return
    renderMarkdown(previewEl, props.text)
  })

  return (
    <div
      class="ui-document-surface"
      data-height={props.height}
      data-view={view()}
      data-editable={props.readOnly ? undefined : 'true'}
      aria-label={props.ariaLabel}
    >
      <Show when={hasSource()}>
        {/* Hidden, never unmounted — see the header. `hidden` rather than a
            style, so `[hidden]` in the stylesheet is the one rule that
            decides it. */}
        <div
          class="ui-document-surface__source"
          hidden={!showsSource()}
          ref={(el) => {
            sourceEl = el
          }}
        />
      </Show>
      <Show when={showsPreview()}>
        <div
          class="ui-document-surface__preview"
          tabIndex={0}
          ref={(el) => {
            previewEl = el
          }}
        />
      </Show>
    </div>
  )
}
