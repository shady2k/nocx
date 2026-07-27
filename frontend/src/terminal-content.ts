// ═══════════════════════════════════════════════════════════════════════════
// TerminalContent — all terminal machinery behind the TabContent seam.
// Extracted from Tab so the chrome layer never touches a session or renderer.
// ═══════════════════════════════════════════════════════════════════════════

import { XtermRenderer } from './renderers/xterm'
import type { TerminalRenderer, MarkerAdapter } from './renderers/types'
import { InputStateController } from './input-state'
import { CommandEditor } from './editor'
import { ShellInputTarget } from './input-target'
import { submitCommand } from './submit'
import { shouldShowEditor, NATIVE_RESTORE } from './native-mode'
import { shouldCopy, type ClipboardAccess, type ClipboardGate } from './clipboard'
import type { ClipboardBanner } from './banner'
import { CommandLedger } from './command-ledger'
import { ScrollbackController } from './scrollback/controller'
import { log } from './log'
import type { WSClient, SessionHandle } from './ipc'
import { showConfirm } from './ui/dialog'
import { BaseTabContent, type TabHost, type ContentViewport } from './tab-content'

// How long the grid must hold still before the PTY is told about it.
const RESIZE_SETTLE_MS = 80

// Shown only until the session reports where it started.
const FALLBACK_TITLE = 'Terminal'

/**
 * Names a tab after its directory, the way every other terminal does.
 * Keeps the tail — the CSS ellipsis cuts from the right.
 */
function directoryLabel(cwd: string): string {
  const path = cwd.trim().replace(/\/+$/, '')
  if (!path) return FALLBACK_TITLE
  const parts = path.split('/').filter(Boolean)
  if (path === '~' || parts.length === 0) return path || FALLBACK_TITLE
  return parts.slice(-2).join('/')
}

/**
 * Tooltip for a cwd. When the value comes from session open (no OSC 7 yet)
 * the tooltip surfaces that fact (AD-5 fallback visibility).
 */
function cwdTooltip(cwd: string, fromOSC7: boolean): string {
  if (!cwd) return ''
  return fromOSC7 ? cwd : `${cwd} (initial cwd)`
}

/**
 * TerminalContent owns the renderer, session, editor, scrollback, command
 * ledger, input-state machine, and PTY resize policy. It receives geometry
 * through viewportChanged() — it NEVER interprets container geometry itself.
 */
export class TerminalContent extends BaseTabContent {
  private renderer: TerminalRenderer | null = null
  private session: SessionHandle | null = null
  private editor: CommandEditor | null = null
  private shellTarget: ShellInputTarget | null = null
  private scrollback: ScrollbackController | null = null
  private ledger: CommandLedger | null = null
  private inputState = new InputStateController()
  private _markers = new Map<number, MarkerAdapter>()
  private _pendingCommand = ''
  private _globalKeydown: ((e: KeyboardEvent) => void) | null = null
  private _cwd = '~'
  private _host = ''
  private _lastExitCode: number | null = null
  private _bufferType: 'normal' | 'alternate' = 'normal'
  private nativeMode = false
  private _disposed = false
  private mountAbortController: AbortController | null = null
  private resizeTimer: number | undefined
  private host: TabHost | null = null

  // ── Title composition ────────────────────────────────────────────────
  // Title = programTitle || cwdTitle || 'Terminal'
  // Computed here so the host receives the final string.
  private programTitle = ''
  private cwdTitle = ''

  // Grid dimensions computed by the renderer from the last authoritative
  // viewport. Owned here so PTY resize policy lives with the content.
  cols = 0
  rows = 0

  // _readyPromise resolves true when the renderer mounts and the PTY session
  // opens; resolves false when mount() throws. Never rejects.
  private readonly _readyPromise: Promise<boolean>
  private _readyResolve!: (value: boolean) => void

  constructor(
    private readonly client: WSClient,
    private readonly clipboard: ClipboardAccess,
    private readonly gate: ClipboardGate,
    private readonly banner: ClipboardBanner,
    private readonly onTooltipChange: (tooltip: string) => void,
    private readonly onAltScreenChange?: (inAltScreen: boolean) => void,
    private readonly sshOpts?: {
      profileId: string
      host: string
      user?: string
    },
  ) {
    super()
    this._readyPromise = new Promise<boolean>((resolve) => {
      this._readyResolve = resolve
    })
  }

  /**
   * Resolves true when the renderer mounts and the PTY session opens;
   * resolves false when mount() throws. Never rejects. The initial-tab
   * health signal reads this — NOT a generic "first tab mounted" signal.
   */
  get ready(): Promise<boolean> {
    return this._readyPromise
  }

  /** Push the composed title to the host: program title > cwd title > 'Terminal'. */
  private pushTitle(): void {
    if (!this.host) return
    const title = this.programTitle || this.cwdTitle || 'Terminal'
    this.host.setTitle(title)
  }

  // ── TabContent ──────────────────────────────────────────────────────────

  async mount(target: HTMLElement, host: TabHost, signal: AbortSignal): Promise<void> {
    if (this._disposed) return
    this.host = host

    // Wire the signal: if the tab is disposed during mount, abort.
    if (signal.aborted) {
      this._readyResolve(false)
      return
    }
    this.mountAbortController = new AbortController()
    const onAbort = () => this.mountAbortController!.abort()
    signal.addEventListener('abort', onAbort, { once: true })

    try {
      // Wait for pane to become visible and have proper dimensions.
      await new Promise((resolve) => requestAnimationFrame(resolve))

      if (signal.aborted) {
        this._readyResolve(false)
        return
      }

      log.info('nocx: creating renderer')
      const renderer = new XtermRenderer()

      // ── DOM scrollback controller ───────────────────────────────────────
      this.scrollback = new ScrollbackController({
        pane: target,
        renderer,
        now: () => performance.now(),
      })

      log.info('nocx: mounting renderer')
      await renderer.mount(this.scrollback.mountTarget)

      if (signal.aborted) {
        renderer.dispose()
        this.scrollback.dispose()
        this._readyResolve(false)
        return
      }

      log.info('nocx: renderer mounted', { cols: renderer.cols, rows: renderer.rows })
      this.cols = renderer.cols
      this.rows = renderer.rows

      // ── Command ledger (ADR-0008) ────────────────────────────────────────
      this.ledger = new CommandLedger({ now: () => performance.now() })

      // ── Wire input ownership BEFORE opening the session ─────────────────
      this.shellTarget = new ShellInputTarget(
        (text: string) => renderer.paste(text),
        (data: string) => this.session!.send(data),
      )
      this.editor = new CommandEditor({
        submit: (doc: string) => {
          this._pendingCommand = doc
          if (this.ledger) {
            let markerLine: () => number | undefined = () => undefined
            const rec = this.ledger.open(doc, this._cwd, this._host, () => markerLine())
            const m = renderer.registerMarker()
            if (m) {
              markerLine = () => m.line()
              this._markers.set(rec.id, m)
              m.onDispose(() => {
                this.ledger?.dispose(rec.id)
                this._markers.delete(rec.id)
              })
            }
          }
          this.scrollback?.maybeClear(doc)
          submitCommand(doc, {
            dispatchSubmit: () => this.inputState.dispatch({ type: 'submit' }),
            focusGrid: () => renderer.focus(),
            sendDoc: (d) => void this.shellTarget!.submit(d),
          })
        },
        cancel: () => this.session?.send('\x03'),
      })
      this.editor.mount(target)

      if (signal.aborted) {
        this.editor.dispose()
        renderer.dispose()
        this.scrollback.dispose()
        this._readyResolve(false)
        return
      }

      renderer.onCommandMarker((marker) => {
        this.inputState.dispatch({ type: 'marker', kind: marker.kind })
        if (marker.kind === 'D' && marker.exitCode !== undefined) {
          this._lastExitCode = marker.exitCode
        }
        this.ledger?.onMarker(marker.kind, marker.exitCode)
        if (marker.kind === 'C') {
          this.scrollback?.onCommandStart(this._pendingCommand, this._cwd, marker.line)
        } else if (marker.kind === 'D') {
          const getLine = (y: number) => renderer.getBufferLine(y)
          this.scrollback?.onCommandEnd(getLine, marker.line, marker.exitCode ?? null)
          renderer.clearViewport()
        }
      })

      renderer.onBufferChange((type) => {
        this._bufferType = type
        this.inputState.dispatch({ type: 'buffer', buffer: type })
        if (type === 'alternate') {
          this.scrollback?.enterFullscreen()
        } else {
          this.scrollback?.exitFullscreen()
        }
        this.onAltScreenChange?.(type === 'alternate')
      })

      this.inputState.onChange((m) => {
        console.debug('nocx: input-state', m.state, 'trusted=', m.trusted, 'owned=', m.owned)
        if (shouldShowEditor(m.owned, this.nativeMode)) {
          this.editor!.setTime(new Date())
          this.editor!.show()
          renderer.setReadOnly(true)
          this.scrollback?.setIdle()
        } else if (m.state === 'RUNNING_RAW') {
          this.editor!.hide()
          renderer.setReadOnly(false)
          renderer.focus()
          this.scrollback?.setRunning()
        } else {
          this.editor!.hide()
          renderer.setReadOnly(false)
          renderer.focus()
          this.scrollback?.setIdle()
        }
      })

      // ── Focus bounce (P0-4) ────────────────────────────────────────────
      target.addEventListener('focusin', () => {
        if (!this.editor?.isVisible) return
        const active = document.activeElement
        if (
          active &&
          (this.editor.rootContains(active) || this.scrollback?.xtermLiveContainer.contains(active))
        )
          return
        this.editor.focus()
      })

      this.editor.root.addEventListener('click', () => {
        this.editor?.focus()
      })

      this._globalKeydown = (e: KeyboardEvent) => {
        // Read the flag the chrome set, not the class it rendered (nocx-fttm).
        if (!target.isConnected || !this._active) return
        if (this.scrollback && this.scrollback.selectedBlockId !== null) {
          if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
            e.preventDefault()
            this.scrollback.deselectBlocks()
            if (this.editor?.isVisible) {
              this.editor.focus()
              this.editor.insertText(e.key)
            }
            return
          }
          if (e.key === 'Escape') {
            this.scrollback.deselectBlocks()
            e.preventDefault()
            return
          }
        }
        if (!this.editor?.isVisible) return
        if (e.key.length !== 1 || e.ctrlKey || e.metaKey || e.altKey) return
        const active = document.activeElement
        if (
          active &&
          (this.scrollback?.xtermLiveContainer.contains(active) || this.editor.rootContains(active))
        )
          return
        this.editor.focus()
      }
      document.addEventListener('keydown', this._globalKeydown)

      this.scrollback?.scrollbackArea.addEventListener('mousedown', (e) => {
        if (!(e.target as HTMLElement).closest('.cmd-block')) {
          this.scrollback?.deselectBlocks()
        }
      })

      // ── Editor copy-on-select (item 6) ─────────────────────────────────
      this.editor.textarea.addEventListener('mouseup', () => {
        const ta = this.editor!.textarea
        const start = ta.selectionStart
        const end = ta.selectionEnd
        if (start === end) return
        const text = ta.value.slice(start, end)
        if (shouldCopy(text)) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (editor selection)', e)
          })
        }
      })

      // ── DOM block copy-on-select (P0-5) ───────────────────────────────
      this.scrollback?.scrollbackArea.addEventListener('mouseup', () => {
        const sel = window.getSelection()
        if (!sel || sel.isCollapsed) return
        const text = sel.toString()
        if (!text) return
        if (!this.scrollback?.scrollbackArea.contains(sel.anchorNode)) return
        if (shouldCopy(text)) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (block selection)', e)
          })
        }
      })

      // Open the session at the renderer's actual grid size.
      const session = this.sshOpts
        ? await this.client.openSSHSession(this.cols, this.rows, this.sshOpts.profileId)
        : await this.client.openSession(this.cols, this.rows, true)

      if (signal.aborted) {
        session.close()
        this.editor.dispose()
        renderer.dispose()
        this.scrollback.dispose()
        this._readyResolve(false)
        return
      }

      this.session = session
      log.info('nocx: session opened', { sid: session.sessionId, cwd: session.cwd || '' })

      this._cwd = session.cwd || ''
      this._host = this.sshOpts?.host || ''
      this.editor?.setCwd(session.cwd || '')

      // Push initial title + tooltip. Title composition lives here.
      if (this.sshOpts) {
        this.programTitle = this.sshOpts.host
        this.onTooltipChange(
          `SSH ${this.sshOpts.user ? this.sshOpts.user + '@' : ''}${this.sshOpts.host}`,
        )
      } else {
        this.cwdTitle = directoryLabel(session.cwd)
        this.onTooltipChange(cwdTooltip(session.cwd, false))
      }
      this.pushTitle()

      session.onData((data: string) => {
        log.debug('nocx: session data received', { length: data.length })
        renderer.write(data)
        if (this._bufferType === 'normal') {
          host.requestAttention()
        }
      })

      // Keyboard → PTY: xterm.js fires onData for every keystroke when stdin
      // is enabled (setReadOnly(false)). The editor captures keys while it is
      // visible and the terminal is read-only, so these only arrive in RAW mode.
      renderer.onData((data: string) => {
        this.session?.send(data)
      })
      session.onExit((sid: string) => {
        log.info('nocx: session exited', { sid })
        this.inputState.dispatch({ type: 'exit' })
        this.ledger?.finalizeOpen()
        this._disposeAllMarkers()
        host.requestClose()
      })
      session.onReset(() => {
        renderer.reset()
        this.inputState.dispatch({ type: 'reset' })
        this.ledger?.finalizeOpen()
        this._disposeAllMarkers()
      })

      renderer.onTitle((title: string) => {
        this.programTitle = title.trim()
        this.pushTitle()
      })
      renderer.onCwd(({ path }) => {
        this._cwd = path
        this.editor?.setCwd(path)
        this.cwdTitle = directoryLabel(path)
        this.onTooltipChange(cwdTooltip(path, true))
        this.pushTitle()
      })

      renderer.onBell(() => {
        host.requestAttention()
      })

      // ── Clipboard ────────────────────────────────────────────────────
      renderer.onSelectionChange((text) => {
        if (shouldCopy(text)) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (selection)', e)
          })
        }
      })

      renderer.onClipboardWrite((text) => {
        if (this.gate.granted) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (OSC 52)', e)
          })
          return
        }
        if (this.gate.suppressed) return
        if (this.banner.shown) return
        void this.banner.show().then((choice) => {
          if (choice === 'allow') {
            this.gate.allow()
            this.clipboard.writeText(text).catch((e) => {
              console.warn('nocx: clipboard write failed (OSC 52)', e)
            })
          } else if (choice === 'suppress') {
            this.gate.suppress()
          }
        })
      })

      // Paste on right-click AND middle-click.
      const doPaste = async () => {
        try {
          const text = await this.clipboard.readText()
          if (!text) return
          if (this.editor?.isVisible) {
            this.editor.insertText(text)
            return
          }
          if (text.includes('\n') && this._bufferType === 'normal') {
            const confirmed = await showConfirm('Paste multi-line text?', 'Paste', 'Cancel')
            if (!confirmed) return
          }
          renderer.paste(text)
        } catch (e) {
          console.warn('nocx: clipboard read failed (paste)', e)
        }
      }

      target.addEventListener('contextmenu', (e: MouseEvent) => {
        e.preventDefault()
        void doPaste()
      })

      target.addEventListener('mousedown', (e: MouseEvent) => {
        if (e.button === 1) {
          e.preventDefault()
          void doPaste()
        }
      })

      renderer.onResize((cols: number, rows: number) => {
        if (cols === this.cols && rows === this.rows) return
        this.cols = cols
        this.rows = rows
        clearTimeout(this.resizeTimer)
        this.resizeTimer = window.setTimeout(() => {
          session.sendResize(cols, rows)
        }, RESIZE_SETTLE_MS)
      })

      this.renderer = renderer

      // ── Native-mode escape (Ctrl/Cmd+Shift+.) ─────────────────────────
      target.addEventListener('keydown', (e: KeyboardEvent) => {
        if (e.key === '.' && (e.metaKey || e.ctrlKey) && e.shiftKey) {
          e.preventDefault()
          e.stopPropagation()
          this.nativeMode = true
          this.editor?.hide()
          this.renderer?.focus()
          this.session?.send(NATIVE_RESTORE)
        }
      })

      this._mounted = true
      this._readyResolve(true)
      log.info('nocx: terminal content ready', {
        renderer: 'xterm',
        sid: session.sessionId,
      })

      // B.5: replay the latest viewport after async mount completes.
      // The presentation layer delivers viewports via viewportChanged;
      // if one was buffered during mount, apply it now through the
      // renderer's fitViewport path.
      if (this._latestViewport) {
        this.viewportChanged(this._latestViewport)
      }
    } catch (err) {
      const notice = document.createElement('pre')
      notice.className = 'pane-error'
      notice.textContent = `Terminal failed to start:\n\n${err instanceof Error ? err.message : String(err)}`
      target.replaceChildren(notice)
      this._readyResolve(false)
      log.error('nocx: terminal content failed', { error: String(err) })
    }
  }

  // ── B.5 viewport delivery ─────────────────────────────────────────────

  private _latestViewport: ContentViewport | null = null
  private _mounted = false

  viewportChanged(viewport: ContentViewport): void {
    if (this._disposed) return
    this._latestViewport = viewport
    // Pass the authoritative viewport to the renderer (B.5).
    // The renderer computes cols/rows from its own cell metrics.
    if (this._mounted && this.renderer) {
      this.renderer.fitViewport(viewport)
    }
  }

  focus(): void {
    this.renderer?.focus()
  }

  refreshAtlas(): void {
    this.renderer?.refreshAtlas()
  }

  dispose(): void {
    this._disposed = true
    this.mountAbortController?.abort()
    if (this._globalKeydown) {
      document.removeEventListener('keydown', this._globalKeydown)
      this._globalKeydown = null
    }
    this.session?.close()
    this.renderer?.dispose()
    this.editor?.dispose()
    this.scrollback?.dispose()
    this._disposeAllMarkers()
    this.ledger = null
    this.host = null
  }

  private _disposeAllMarkers(): void {
    for (const m of this._markers.values()) m.dispose()
    this._markers.clear()
  }
}
