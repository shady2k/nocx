// ═══════════════════════════════════════════════════════════════════════════
// FileViewerContent — the read-only file viewer tab (fm-w7).
//
// A snapshot plus an offer (D7): the file is read once when the binding is
// live, rendered in a read-only CodeMirror 6 host, and NEVER re-read
// automatically. A file that changed mid-read is announced with a Reload
// affordance; the affordance is present but disabled when the binding is
// gone, with the reason legible in the banner text.
//
// The states that are not "the file" are rendered states, not toasts:
// binary / truncated / lossy / changed come from the read result, and
// "source unavailable" is a live transition — the content STAYS on screen,
// and no further files.* call is issued against a dead binding. The
// liveness seam is injected (never imported): the caller owns the binding
// registry and tells this content whether the binding may still be called.
// ═══════════════════════════════════════════════════════════════════════════

import { ReadOnlyHost } from '../cm-host'
import { render } from 'solid-js/web'
import { Button } from '../ui'
import type { FilesReadResult } from '../generated/files.read'
import { BasePaneContent, type ActiveOrigin, type PaneHost } from '../pane-content'
import { languageForPath, viewerHighlighting } from './language-registry'
import { highlightActiveLine } from '@codemirror/view'

// ── The seam (injected at registration; never imported) ────────────────────

export interface FileViewerTarget {
  /** Backend-issued binding id the read is addressed to. */
  readonly bindingId: string
  /** Backend-attested endpoint id; null for a local binding. */
  readonly endpointId: string | null
  /** Lexical path as listed — what the tab label shows, not necessarily
   *  what was read. */
  readonly path: string
  /** Provider-canonical identity of the file — what the tab deduplicates on. */
  readonly canonical: string
  /** Host label for remote files; null for local. */
  readonly displayHost: string | null
  /** Basename, for the title. */
  readonly name: string
  /** The machine this file came from — the origin the Files panel was
   *  scoped to when the row was clicked (design §5.4). The viewer answers
   *  `activeOrigin()` with it, so the origin-following panel treats a
   *  viewer tab as the same machine and keeps its binding — and the
   *  viewer's read — alive. `paneId` is deliberately absent: the viewer
   *  does not know its tab; PaneManager adds it. */
  readonly origin: Omit<ActiveOrigin, 'paneId'> | null
  /**
   * The 1-based line to open at, when the opener knew one — a terminal link
   * written `docs/architecture.md:101` (terminal-links/) is the only thing
   * that does today.
   *
   * Absent is not "line 1": a viewer opened from the Files panel has no line
   * to go to, and it must not be scrolled or have a line emphasised as
   * though it did. The distinction is what keeps the active-line decoration
   * off every file that was opened by name.
   */
  readonly line?: number
}

/**
 * The viewer's narrow window onto the app: how to read a file and whether the
 * binding may still be called. The caller (composition root) owns the binding
 * registry and any D6 endpoint-match policy; this interface only asks for the
 * verdicts the viewer needs.
 *
 * `live: false` is terminal for calls: the viewer issues no further
 * `readFile` once the binding has reported dead — whether it is gone or has
 * been replaced by a different endpoint is the registry's distinction, not
 * this viewer's.
 */
export interface FileViewerDeps {
  /** Issue files.read. Rejects when the binding is gone or the read fails. */
  readFile(params: { bindingId: string; path: string }): Promise<FilesReadResult>
  /**
   * Subscribe to a binding's liveness. `cb` is invoked synchronously with the
   * current state, then on every transition. Returns an unsubscribe.
   */
  onBindingLiveness(bindingId: string, cb: (live: boolean) => void): () => void
}

// ── Rendered states ─────────────────────────────────────────────────────────

type ViewerState =
  | { kind: 'loading' }
  | { kind: 'content'; result: FilesReadResult }
  | { kind: 'error'; message: string }
  | { kind: 'unavailable' }

/** One line of the notice bar; data-state is what tests and CSS key on. */
interface NoticeLine {
  readonly state: 'binary' | 'truncated' | 'lossy' | 'changed' | 'error' | 'unavailable' | 'loading'
  readonly text: string
  readonly tone?: 'warning' | 'danger'
}

/** "binary file, 42 bytes" — raw count, as the brief names it. */
function binaryLine(size: number): string {
  return `binary file, ${size} bytes`
}

/** "file is larger than 2 MiB (1.5 MiB shown)" — the ceiling is a constant
 *  the wire already enforces; this line says so with the size. */
function truncatedLine(size: number): string {
  return `file is larger than 2 MiB (${size} bytes); showing a truncated preview`
}

const LOSSY_LINE =
  'some byte sequences were not valid UTF-8 and were replaced — this view is not byte-identical to the file'

const UNAVAILABLE_LINE =
  'Source unavailable — the terminal or connection that provided this file is gone'

/** The lines a read result earns. Changed is a separate axis from the shape
 *  states (a binary file can change mid-read too), so it is a distinct line. */
function linesForResult(result: FilesReadResult): NoticeLine[] {
  const lines: NoticeLine[] = []
  if (result.binary) lines.push({ state: 'binary', text: binaryLine(result.size) })
  if (result.truncated) lines.push({ state: 'truncated', text: truncatedLine(result.size) })
  if (result.lossy) lines.push({ state: 'lossy', text: LOSSY_LINE, tone: 'warning' })
  if (result.changed) {
    lines.push({
      state: 'changed',
      text: 'File changed while reading — the content below is an unknowable mixture',
      tone: 'warning',
    })
  }
  return lines
}

// ── Content ────────────────────────────────────────────────────────────────

export class FileViewerContent extends BasePaneContent {
  private root: HTMLElement | null = null
  private noticeEl: HTMLElement | null = null
  private readonly host = new ReadOnlyHost()
  private noticeDispose: (() => void) | null = null
  private unsubscribeLiveness: (() => void) | null = null

  private state: ViewerState = { kind: 'loading' }
  /** Liveness verdict from the seam. While false, NO readFile may be issued. */
  private dead = false
  /** Generation of the in-flight read; any resolution with a stale generation
   *  is dropped (a later read, a liveness death, or disposal superseded it). */
  private inflight = 0
  private everRead = false
  private disposed = false

  constructor(
    private readonly target: FileViewerTarget,
    private readonly deps: FileViewerDeps,
  ) {
    super()
  }

  // ── PaneContent ──────────────────────────────────────────────────────────

  mount(target: HTMLElement, _host: PaneHost, signal: AbortSignal): Promise<void> {
    if (this.disposed) return Promise.resolve()

    this.root = document.createElement('div')
    this.root.className = 'file-viewer'
    this.noticeEl = document.createElement('div')
    this.noticeEl.className = 'file-viewer__notice'
    const editorHost = document.createElement('div')
    editorHost.className = 'file-viewer__editor'
    this.root.append(this.noticeEl, editorHost)
    target.append(this.root)

    // The active-line decoration is added ONLY when a line was asked for:
    // it is how the line the user clicked stays findable after the scroll,
    // and on a file opened by name it would just be a stripe over line 1.
    this.host.mount(editorHost, signal, [
      viewerHighlighting,
      languageForPath(this.target.path),
      ...(this.target.line === undefined ? [] : [highlightActiveLine()]),
    ])

    signal.addEventListener('abort', () => this.dispose(), { once: true })

    // Synchronous first call: current liveness, then transitions. A dead
    // binding at mount renders "source unavailable" and never reads.
    this.unsubscribeLiveness = this.deps.onBindingLiveness(this.target.bindingId, (live) => {
      this.onLiveness(live)
    })
    return Promise.resolve()
  }
  /** The machine this viewer speaks for (design §5.4): the origin the
   *  panel was scoped to when the file was opened. Answering it makes the
   *  origin-following panel treat a viewer tab as the same machine — it
   *  keeps its tree AND its binding, so the viewer's own read is never
   *  closed out from under it by the tab activation it caused. Null only
   *  when the opener had no origin to hand over (nothing should open a
   *  viewer without one). */
  activeOrigin(): Omit<ActiveOrigin, 'paneId'> | null {
    return this.target.origin
  }
  viewportChanged(): void {
    // The CM6 view measures itself; there is nothing to do here.
  }

  /**
   * Scroll to `line` and mark it — the same jump the target's own `line`
   * performs at mount, exposed because a SECOND click on the same file at a
   * different line is deduplicated onto this very tab (openFileViewer's
   * singletonKey is the canonical path, deliberately not the path plus a
   * line: one file is one tab). Without this, `x.ts:10` then `x.ts:200`
   * would activate the tab and leave the user staring at line 10, which
   * reads as the click having done nothing at all.
   *
   * Inert before the document arrives: `applyDoc` performs the jump for the
   * mount case, and a reveal against an empty doc would clamp to line 1 and
   * then be overwritten anyway.
   */
  revealLine(line: number): void {
    if (this.state.kind !== 'content') return
    this.host.revealLine(line)
  }

  focus(): void {
    this.host.focus()
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    // Invalidate any in-flight read: its resolution must not paint a recycled
    // or disposed tab (B.6).
    this.inflight++
    this.unsubscribeLiveness?.()
    this.unsubscribeLiveness = null
    this.noticeDispose?.()
    this.noticeDispose = null
    this.host.dispose()
    this.root?.remove()
    this.root = null
    this.noticeEl = null
  }

  // ── Liveness (the only transition source besides the read itself) ────────

  private onLiveness(live: boolean): void {
    if (this.disposed) return
    if (live) {
      this.dead = false
      if (!this.everRead) {
        // Mounted while dead earlier (or the first call): the binding is back
        // and nothing has ever been shown — read now, never automatically
        // again.
        this.issueRead()
      } else {
        // Content (or an error) is on screen; Reload becomes a valid offer.
        // D7: enabling the button is all that happens — no silent re-read.
        this.renderNotice()
      }
      return
    }
    // Terminal for calls: abort in-flight reads, keep the content on screen,
    // render the unavailable state. No further readFile is issued from here —
    // reload() refuses while dead, and issueRead() is only reached from
    // reload() or a live transition.
    this.dead = true
    this.inflight++
    this.state = { kind: 'unavailable' }
    this.renderNotice()
  }

  // ── Read ────────────────────────────────────────────────────────────────

  /** User-invoked re-read (Reload). Refuses while the binding is dead. */
  private reload(): void {
    if (this.disposed || this.dead) return
    this.issueRead()
  }

  private issueRead(): void {
    if (this.disposed || this.dead) return
    const gen = ++this.inflight
    this.everRead = true
    this.state = { kind: 'loading' }
    this.renderNotice()
    this.deps.readFile({ bindingId: this.target.bindingId, path: this.target.path }).then(
      (result) => {
        if (this.disposed || gen !== this.inflight || this.dead) return
        this.state = { kind: 'content', result }
        this.applyDoc(result.text)
        this.renderNotice()
      },
      (err: unknown) => {
        if (this.disposed || gen !== this.inflight || this.dead) return
        const message = err instanceof Error ? err.message : String(err)
        this.state = { kind: 'error', message }
        this.renderNotice()
      },
    )
  }

  /** Replace the whole document. Read-only is an input gate; the host may
   *  still paint content (the wire's bytes are the only writer). */
  private applyDoc(text: string): void {
    this.host.setDoc(text)
    // After the document, never before: `revealLine` clamps to the doc's
    // length, and an empty doc clamps every line to 1.
    if (this.target.line !== undefined) this.host.revealLine(this.target.line)
  }

  // ── Notice bar ──────────────────────────────────────────────────────────

  private renderNotice(): void {
    const notice = this.noticeEl
    if (!notice) return
    this.noticeDispose?.()
    this.noticeDispose = null
    notice.textContent = ''

    const { lines, reload } = this.noticeContent()
    notice.hidden = lines.length === 0 && !reload
    for (const line of lines) {
      const el = document.createElement('span')
      el.className = 'file-viewer__line'
      el.dataset.state = line.state
      if (line.tone) el.dataset.tone = line.tone
      el.textContent = line.text
      notice.append(el)
    }
    if (reload) {
      this.mountReloadButton(reload.disabled, reload.label, reload.reason)
    }
  }

  /** What the notice bar should say in the current state. Reload is offered
   *  exactly where a re-read is meaningful: changed (D7), a failed read on a
   *  live binding, and the unavailable state — where it is present and
   *  disabled while dead, so the offer survives the drop. */
  private noticeContent(): {
    lines: NoticeLine[]
    reload: { disabled: boolean; label: string; reason: string } | null
  } {
    switch (this.state.kind) {
      case 'loading':
        return { lines: [{ state: 'loading', text: 'Reading…' }], reload: null }
      case 'content': {
        const lines = linesForResult(this.state.result)
        if (this.state.result.changed) {
          return {
            lines,
            reload: { disabled: this.dead, label: 'Reload', reason: 'Re-read the file from disk' },
          }
        }
        return { lines, reload: null }
      }
      case 'error':
        return {
          lines: [
            { state: 'error', text: `Could not read file: ${this.state.message}`, tone: 'danger' },
          ],
          reload: { disabled: this.dead, label: 'Reload', reason: 'Try reading the file again' },
        }
      case 'unavailable':
        return {
          lines: [{ state: 'unavailable', text: UNAVAILABLE_LINE }],
          reload: {
            disabled: this.dead,
            label: 'Reload',
            reason: this.dead
              ? 'The terminal or connection that provided this file is gone'
              : 'The source is back — re-read the file',
          },
        }
    }
  }

  /** Mount the kit Button (Solid) into the notice bar. One Solid root per
   *  render; disposed on the next render or on content disposal. */
  private mountReloadButton(disabled: boolean, label: string, reason: string): void {
    const wrap = document.createElement('span')
    wrap.className = 'file-viewer__reload'
    this.noticeEl?.append(wrap)
    this.noticeDispose = render(
      () => (
        <Button
          variant="default"
          size="sm"
          disabled={disabled}
          title={reason}
          ariaLabel={`${label}: ${reason}`}
          onClick={() => this.reload()}
        >
          {label}
        </Button>
      ),
      wrap,
    )
  }
}
