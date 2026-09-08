// ═══════════════════════════════════════════════════════════════════════════
// GitDiffContent — the read-only unified-diff tab (git-manager §5.4).
//
// A snapshot plus an offer (filesystem D7): the diff is read once when the
// binding is live, rendered in the read-only CM6 host, and NEVER re-read
// automatically. The panel polls; when its poll sees the status of this path
// on this side move, the seam says so and the tab offers Reload. The offer
// is the only re-read — a log you are reading never scrolls out from under
// you.
//
// The five wire states render as themselves, not as toasts: `binary` says so
// instead of showing an empty editor, `tooLarge` shows the retained prefix
// and that it is a prefix, `empty` means the file changed back (or the poll
// raced the click) and is not an error, `gone` means the path no longer
// exists on that side. "Source unavailable" is a live transition — the
// content STAYS on screen, and no further git.* call is issued against a
// dead binding. The liveness and staleness seams are injected (never
// imported): the caller owns the binding registry and the poll.
// ═══════════════════════════════════════════════════════════════════════════

import { render } from 'solid-js/web'
import { Button } from '../../ui'
import type { GitDiffResult } from '../../generated/git.diff'
import type { GitDiffSide } from '../git-client'
import type { ActiveOrigin, PaneHost } from '../../pane-content'
import { BasePaneContent } from '../../pane-content'
import { ReadOnlyHost } from '../../ui/cm-host'
import { gitDiffDecoration } from './diff-decoration'
import type { GitDiffTarget } from './open-git-diff'
import './git-diff.css'

/**
 * The byte bound this surface sets for git.diff. The backend requires a
 * positive bound — internal/git/local/diff.go errors on maxBytes <= 0 — and
 * the caller is the policy owner, so the surface names its own. 1 MiB is the
 * bound the repo's own wire-conformance test exercises (ws_contract_test.go).
 * At the bound git is cut and the result is tooLarge with text as a prefix.
 */
const DIFF_MAX_BYTES = 1 << 20

/** The same bound as a human label for the tooLarge line. */
const DIFF_MAX_LABEL = '1 MiB'

// ── The seam (injected at registration; never imported) ────────────────────

/**
 * The diff surface's narrow window onto the app: how to read a diff and
 * whether the binding may still be called, plus when the panel's poll has
 * seen this row's status move. The caller (composition root) owns the
 * binding registry and the poll; this interface only asks for the verdicts
 * the surface needs.
 *
 * `live: false` is terminal for calls: the surface issues no further `diff`
 * once the binding has reported dead. `onDiffStale` is the D7 signal — the
 * snapshot is stale and Reload becomes a valid offer; nothing is re-read
 * automatically.
 */
export interface GitDiffDeps {
  /** Issue git.diff. Rejects when the binding is gone or the call fails. */
  diff(params: {
    bindingId: string
    path: string
    side: GitDiffSide
    maxBytes: number
  }): Promise<GitDiffResult>

  /** Subscribe to a binding's liveness. `cb` is invoked synchronously with
   *  the current state, then on every transition. Returns an unsubscribe. */
  onBindingLiveness(bindingId: string, cb: (live: boolean) => void): () => void

  /** Subscribe to "the status of this path on this side changed". The
   *  store's poll observed the row move (reverted, re-modified, staged
   *  away) since the diff was read — the snapshot is stale and the tab
   *  offers Reload (filesystem D7). `cb` fires at most once per change;
   *  the unsubscribe is returned. */
  onDiffStale(bindingId: string, path: string, side: GitDiffSide, cb: () => void): () => void
}

// ── Rendered states ─────────────────────────────────────────────────────────

type DiffState =
  | { kind: 'loading' }
  | { kind: 'content'; result: GitDiffResult }
  | { kind: 'error'; message: string }
  | { kind: 'unavailable' }

/** One line of the notice bar; data-state is what tests and CSS key on. */
interface NoticeLine {
  readonly state:
    'binary' | 'tooLarge' | 'empty' | 'gone' | 'changed' | 'error' | 'unavailable' | 'loading'
  readonly text: string
  readonly tone?: 'warning' | 'danger'
}

const UNAVAILABLE_LINE =
  'Source unavailable — the terminal or connection that provided this diff is gone'

// ── Content ────────────────────────────────────────────────────────────────

export class GitDiffContent extends BasePaneContent {
  private root: HTMLElement | null = null
  private noticeEl: HTMLElement | null = null
  private readonly host = new ReadOnlyHost()
  private noticeDispose: (() => void) | null = null
  private unsubscribeLiveness: (() => void) | null = null
  private unsubscribeStale: (() => void) | null = null

  private state: DiffState = { kind: 'loading' }
  /** Liveness verdict from the seam. While false, NO diff may be issued. */
  private dead = false
  /** The panel's poll saw this row move (D7). Reset by a user-invoked
   *  reload; a staleness that lands while a read is in flight marks the
   *  landing snapshot stale too. */
  private stale = false
  /** Generation of the in-flight read; any resolution with a stale
   *  generation is dropped (a later read, a liveness death, or disposal
   *  superseded it). */
  private inflight = 0
  private everRead = false
  private disposed = false

  constructor(
    private readonly target: GitDiffTarget,
    private readonly deps: GitDiffDeps,
  ) {
    super()
  }

  // ── PaneContent ──────────────────────────────────────────────────────────

  mount(target: HTMLElement, _host: PaneHost, signal: AbortSignal): Promise<void> {
    if (this.disposed) return Promise.resolve()

    this.root = document.createElement('div')
    this.root.className = 'git-diff'
    this.noticeEl = document.createElement('div')
    this.noticeEl.className = 'git-diff__notice'
    const editorHost = document.createElement('div')
    editorHost.className = 'git-diff__editor'
    this.root.append(this.noticeEl, editorHost)
    target.append(this.root)

    this.host.mount(editorHost, signal, [gitDiffDecoration])

    signal.addEventListener('abort', () => this.dispose(), { once: true })

    // Synchronous first call: current liveness, then transitions. A dead
    // binding at mount renders "source unavailable" and never reads.
    this.unsubscribeLiveness = this.deps.onBindingLiveness(this.target.bindingId, (live) => {
      this.onLiveness(live)
    })
    this.unsubscribeStale = this.deps.onDiffStale(
      this.target.bindingId,
      this.target.path,
      this.target.side,
      () => {
        this.onStale()
      },
    )
    return Promise.resolve()
  }

  /** The machine this diff speaks for (design §5.4): the origin the panel
   *  was scoped to when the row was clicked, FROZEN. Answering it makes the
   *  origin-following panels treat a diff tab as the same machine — they
   *  keep their bindings, including the one this tab reads through, so the
   *  tab's own read is never closed out from under it by the activation it
   *  caused. `cwdFollow: false` is the whole point: a frozen cwd is a
   *  snapshot, never a claim about where we are now, so activating the tab
   *  never moves the panel. Null only when the opener had no origin. */
  activeOrigin(): Omit<ActiveOrigin, 'paneId'> | null {
    return this.target.origin
  }

  viewportChanged(): void {
    // The CM6 view measures itself; there is nothing to do here.
  }

  focus(): void {
    this.host.focus()
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    // Invalidate any in-flight read: its resolution must not paint a
    // recycled or disposed tab (B.6).
    this.inflight++
    this.unsubscribeLiveness?.()
    this.unsubscribeLiveness = null
    this.unsubscribeStale?.()
    this.unsubscribeStale = null
    this.noticeDispose?.()
    this.noticeDispose = null
    this.host.dispose()
    this.root?.remove()
    this.root = null
    this.noticeEl = null
  }

  // ── Seam callbacks ───────────────────────────────────────────────────────

  private onLiveness(live: boolean): void {
    if (this.disposed) return
    if (live) {
      this.dead = false
      if (!this.everRead) {
        // Mounted while dead earlier (or the first call): the binding is
        // back and nothing has ever been shown — read now, never
        // automatically again.
        this.issueDiff()
      } else {
        // Content (or an error) is on screen; Reload becomes a valid
        // offer. D7: enabling the button is all that happens — no silent
        // re-read.
        this.renderNotice()
      }
      return
    }
    // Terminal for calls: abort in-flight reads, keep the content on
    // screen, render the unavailable state. No further diff is issued from
    // here — reload() refuses while dead, and issueDiff() is only reached
    // from reload() or a live transition.
    this.dead = true
    this.inflight++
    this.state = { kind: 'unavailable' }
    this.renderNotice()
  }

  /** The panel's poll saw this row move (D7). The offer is the only
   *  re-read; nothing is issued from here. */
  private onStale(): void {
    if (this.disposed || this.dead) return
    this.stale = true
    if (this.state.kind === 'content') this.renderNotice()
  }

  // ── Read ────────────────────────────────────────────────────────────────

  /** User-invoked re-read (Reload). Refuses while the binding is dead. */
  private reload(): void {
    if (this.disposed || this.dead) return
    this.stale = false
    this.issueDiff()
  }

  private issueDiff(): void {
    if (this.disposed || this.dead) return
    const gen = ++this.inflight
    this.everRead = true
    this.state = { kind: 'loading' }
    this.renderNotice()
    this.deps
      .diff({
        bindingId: this.target.bindingId,
        path: this.target.path,
        side: this.target.side,
        maxBytes: DIFF_MAX_BYTES,
      })
      .then(
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
      el.className = 'git-diff__line'
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
   *  exactly where a re-read is meaningful: a stale snapshot (D7), a failed
   *  read on a live binding, and the unavailable state — where it is present
   *  and disabled while dead, so the offer survives the drop. */
  private noticeContent(): {
    lines: NoticeLine[]
    reload: { disabled: boolean; label: string; reason: string } | null
  } {
    switch (this.state.kind) {
      case 'loading':
        return { lines: [{ state: 'loading', text: 'Loading diff…' }], reload: null }
      case 'content': {
        const lines: NoticeLine[] = []
        switch (this.state.result.state) {
          case 'binary':
            lines.push({ state: 'binary', text: 'binary file — nothing to show' })
            break
          case 'tooLarge':
            lines.push({
              state: 'tooLarge',
              text: `diff is larger than ${DIFF_MAX_LABEL} (${this.state.result.text.length} bytes shown); showing a prefix`,
            })
            break
          case 'empty':
            lines.push({
              state: 'empty',
              text: 'no differences — the file changed back, or the poll raced the click',
            })
            break
          case 'gone':
            lines.push({ state: 'gone', text: 'the file no longer exists on this side' })
            break
          case 'ok':
            break
        }
        if (this.stale) {
          lines.push({
            state: 'changed',
            text: 'the diff changed since it was opened — it may no longer match the panel',
            tone: 'warning',
          })
          return {
            lines,
            reload: {
              disabled: this.dead,
              label: 'Reload',
              reason: 'Re-read the diff from the repository',
            },
          }
        }
        return { lines, reload: null }
      }
      case 'error':
        return {
          lines: [
            { state: 'error', text: `Could not load diff: ${this.state.message}`, tone: 'danger' },
          ],
          reload: { disabled: this.dead, label: 'Reload', reason: 'Try loading the diff again' },
        }
      case 'unavailable':
        return {
          lines: [{ state: 'unavailable', text: UNAVAILABLE_LINE }],
          reload: {
            disabled: this.dead,
            label: 'Reload',
            reason: this.dead
              ? 'The terminal or connection that provided this diff is gone'
              : 'The source is back — re-read the diff',
          },
        }
    }
  }

  /** Mount the kit Button (Solid) into the notice bar. One Solid root per
   *  render; disposed on the next render or on content disposal. */
  private mountReloadButton(disabled: boolean, label: string, reason: string): void {
    const wrap = document.createElement('span')
    wrap.className = 'git-diff__reload'
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
