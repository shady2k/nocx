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
import { type ProfileClient, type SSHAliasEntry } from './profiles'
import { RpcError } from './dispatcher'

// How long the grid must hold still before the PTY is told about it.
const RESIZE_SETTLE_MS = 80

/**
 * How long output is treated as the shell's answer to a resize rather than as
 * unread activity.
 *
 * Generous on purpose. Getting it wrong in one direction lights an indicator
 * that lies about a tab; getting it wrong in the other costs one missed
 * indicator on a tab the user resized a moment ago and is therefore watching.
 * Those are not symmetric.
 */
const RESIZE_ECHO_MS = 400

/**
 * Whether `el` is somewhere the user types on purpose.
 *
 * Used to keep the terminal's document-level key rescue off other people's
 * fields. `isContentEditable` is checked too: a rich-text surface is a text
 * entry even though it is neither an input nor a textarea.
 */
function isTextEntry(el: Element | null): boolean {
  if (!(el instanceof HTMLElement)) return false
  if (el.isContentEditable) return true
  const tag = el.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT'
}

// No placeholder title — see the descriptor in tabs.ts for why. A tab with no
// name yet shows nothing rather than a word that is never the answer.
const FALLBACK_TITLE = ''

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
  /** Timestamp until which incoming data is the echo of a resize we sent. */
  private echoUntil = 0
  private host: TabHost | null = null
  /** Whether the editor currently owns DOM keyboard input (owned from input-state). */
  private _editorOwned = false
  /** In-flight alias-fetch counter — generation for stale-request gating. */
  private _aliasFetchId = 0

  // ── Title composition ────────────────────────────────────────────────
  // Title = programTitle || cwdTitle (no placeholder — nocx-83a)
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
    /** Live SSH config alias source for the editor hint (w7-hint). Null when
     *  unavailable (tests, raw-mode-only contexts). */
    private readonly profileClient: ProfileClient | null,
    private readonly onTooltipChange: (tooltip: string) => void,
    private readonly sshOpts?: {
      profileId: string
      host: string
      user?: string
      port?: number
    },
    /** Pushes the strip's optional second line — the tab's location, or '' when the
     *  title already says it. Only this class holds both halves of that question. */
    private readonly onSubtitleChange?: (subtitle: string) => void,
    /** Called when the session is an alias (not a saved profile) and can be
     *  adopted as a nocx connection. True = adoptable, False = not. */
    private readonly onAdoptabilityChange?: (adoptable: boolean) => void,
    /** Called when an SSH connection fails because the vault is sealed. */
    private readonly onVaultSealed?: () => void,
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

  /** Push the composed title to the host: program title, else the cwd label. */
  private pushTitle(): void {
    if (!this.host) return
    const title = this.programTitle || this.cwdTitle
    this.host.setTitle(title)
    // The location line earns a row only when the title is a name of its own.
    // With no program title the title IS the location, and a second line would
    // print the first one again.
    this.onSubtitleChange?.(this.programTitle ? this.locationLine() : '')
  }

  /** Where this tab is: `user@host` for SSH, the working directory otherwise. */
  private locationLine(): string {
    if (this.sshOpts) {
      return this.sshOpts.user ? `${this.sshOpts.user}@${this.sshOpts.host}` : this.sshOpts.host
    }
    return this._cwd
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
        // A taller editor is a shorter scrollback. Keep the bottom of the
        // transcript where it belongs — just above the editor — instead of
        // letting it slide underneath.
        resized: () => this.scrollback?.scrollToBottom(),
        /** Detect `ssh <partial>` pattern and show matching aliases. */
        onInputChange: (text) => this._onEditorInput(text),
        /** Hint acceptance — no cache to invalidate. */
        onAcceptHint: () => {},
      })

      this.editor.mount(target)

      if (signal.aborted) {
        this.editor.dispose()
        renderer.dispose()
        this.scrollback.dispose()
        this._readyResolve(false)
        return
      }

      // False until the first OSC 133 marker: a markerless session (plain
      // SSH) keeps the terminal visible in the unstructured full-pane mode.
      let shellIntegrated = false

      renderer.onCommandMarker((marker) => {
        // Any OSC 133 marker means the remote shell has nocx integration:
        // from here the scrollback-block layout owns the presentation and
        // the unstructured full-pane mode is never used again.
        shellIntegrated = true
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
        } else if (!shellIntegrated) {
          // A markerless session returning from an alt-screen program must
          // not collapse to the hidden idle layout: leave fullscreen first
          // (setUnstructured declines while an alt-screen program owns the
          // pane), then fill the pane again.
          this.scrollback?.exitFullscreen()
          this.scrollback?.setUnstructured()
        } else {
          this.scrollback?.exitFullscreen()
        }
      })
      this.inputState.onChange((m) => {
        console.debug('nocx: input-state', m.state, 'trusted=', m.trusted, 'owned=', m.owned)
        this._editorOwned = m.owned
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
          // Markerless session (still no OSC 133): the terminal must stay
          // visible — the scrollback-block model never takes over.
          if (!shellIntegrated) {
            this.scrollback?.setUnstructured()
          } else {
            this.scrollback?.setIdle()
          }
        }
      })

      // The input-state machine starts RAW and onChange may not fire for the
      // initial state: present an unintegrated session with the terminal
      // visible from the first byte.
      this.scrollback?.setUnstructured()

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
        // Somebody else's text control has the focus — the tab strip's filter, a
        // settings field, a dialog. This handler is on `document`, so it sees
        // every keystroke in the window, and the rescue it performs (pull focus
        // into the prompt so typing "just works" after a click on the pane) is
        // exactly wrong when the user is deliberately typing somewhere else: the
        // first character lands in the field, focus jumps, and the rest goes to
        // the shell. Whitelisting the editor and the grid was not enough, because
        // any control OUTSIDE the terminal is equally not ours.
        if (isTextEntry(active)) return
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

      // Alias tab: profileId is empty, open by host so the backend
      // resolves through ~/.ssh/config (ssh -G). Saved-profile tabs
      // use openSSHSession with the real profileId.
      const session = this.sshOpts
        ? this.sshOpts.profileId
          ? await this.client.openSSHSession(this.cols, this.rows, this.sshOpts.profileId)
          : await this.client.openSSHSessionByHost(
              this.cols,
              this.rows,
              this.sshOpts.host,
              this.sshOpts.user,
            )
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
      // The block header's `user@host`. Empty for a local shell, where the
      // machine is implied and printing it on every block would be noise.
      this.scrollback?.blockManager.setLocation(this.sshOpts ? this.locationLine() : '')
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

      // Signal adoptability for alias tabs (no saved profile yet).
      // Must come after the session opens so adoption is only offered
      // to sessions that actually connected — a failed connect never
      // reaches this point (it throws to the outer catch).
      if (this.sshOpts && !this.sshOpts.profileId) {
        this.onAdoptabilityChange?.(true)
      }
      this.pushTitle()

      session.onData((data: string) => {
        log.debug('nocx: session data received', { length: data.length })
        renderer.write(data)
        this.scheduleLiveResize()
        if (this._bufferType === 'normal' && Date.now() >= this.echoUntil) {
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
          // A resize makes the shell redraw its prompt, and that redraw arrives
          // on `session.onData` looking exactly like output the user has not
          // seen. It is not: we asked for it. Switching the strip from vertical
          // to horizontal resizes every pane at once, so every inactive tab lit
          // its activity indicator for something the user did to the WINDOW
          // rather than to any tab (nocx-6w4z).
          this.echoUntil = Date.now() + RESIZE_ECHO_MS
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
      // Vault-sealed errors should surface as Unlock dialog, not generic error.
      if (err instanceof RpcError) {
        const data = err.data as { reason?: string } | undefined
        if (data?.reason === 'vault-sealed') {
          this.onVaultSealed?.()
          this._readyResolve(false)
          return
        }
      }
      const notice = document.createElement('pre')
      notice.className = 'pane-error'
      notice.textContent = `Terminal failed to start:\n\n${err instanceof Error ? err.message : String(err)}`
      target.replaceChildren(notice)
      this._readyResolve(false)
      log.error('nocx: terminal content failed', { error: String(err) })
    }
  }

  // ── Live-region sizing ────────────────────────────────────────────────

  private liveResizeFrame = 0

  /**
   * Re-measure the live region on the next frame.
   *
   * Coalesced to one animation frame because a busy command delivers dozens of
   * chunks per frame and every one of them would otherwise read the grid and
   * write a style — a layout thrash on the hot path, for a height that can only
   * be painted once per frame anyway.
   */
  private scheduleLiveResize(): void {
    if (this.liveResizeFrame !== 0) return
    this.liveResizeFrame = requestAnimationFrame(() => {
      this.liveResizeFrame = 0
      if (this._disposed || !this.renderer || !this.scrollback) return
      // Height first, refit second, and the order is the whole point. Reaching
      // the ceiling collapses the editor, which grows the scroller — so the
      // usable height is only correct AFTER this call. Refitting first meant
      // the grid stayed at the old size until the next chunk of output arrived,
      // and `top` refreshes every three seconds: the pane visibly re-laid
      // itself several seconds after the program started.
      this.scrollback.setLiveHeight(this.renderer.liveContentHeight())
      this.refitIfResized()
    })
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
      this.renderer.fitViewport(this.usableViewport(viewport))
    }
  }

  /**
   * The delivered viewport, less the chrome the grid can never be shown in.
   *
   * B.5 says this class does not interpret container geometry, and it still
   * does not: the pane's box is handed to it. What it subtracts is its OWN
   * furniture — the editor is a flex sibling inside the pane, so the scroller
   * that displays the grid is shorter than the pane by exactly the editor's
   * height.
   *
   * Measured while `top` ran: the pane was 682px, the editor 76, the scroller
   * 606 — and the grid had been fitted to the full 682, producing a 665px
   * screen. `top` filled all of its rows and the bottom four had nowhere to be
   * drawn. Clamping the live region cannot fix that; it only decides where the
   * clipping happens. The grid has to be the size of the space it is shown in
   * (nocx-6w4z).
   *
   * The width is the same statement one axis over, and it was left unmade until
   * nocx-vydj. The delivered width is `pane.getBoundingClientRect().width`, a
   * BORDER box — it counts the `padding: 0 10px` on `.pane`, which is breathing
   * room around the text and not space the grid may use. `cols` was therefore
   * computed from 20px that do not exist, and the last columns were laid out
   * past the right edge of `.xterm-inner`, whose `overflow: hidden` cut them
   * mid-glyph.
   *
   * That it read as a Wails-only defect is the scrollbar gutter: measured at a
   * 1232px pane, `.scrollback-area` is 1212 wide in both engines, but its
   * clientWidth is 1202 in Chromium and 1212 in WebKit, because
   * `scrollbar-gutter: stable` reserves in one and is ignored by the other. Same
   * build, same grid, two different overhangs — 20px in a browser, 10 in
   * WKWebView. Neither is correct, and subtracting a constant for the padding
   * would have fixed only the browser.
   *
   * `clientWidth` of the scroller answers both at once: it is the content box,
   * so the pane's padding is already gone, and it excludes the scrollbar
   * whether or not the engine reserved one.
   */
  private lastFitHeight = 0

  /**
   * Re-fit the grid when the space it is shown in has changed size.
   *
   * `viewportChanged` only fires when the PANE's geometry changes, and the
   * things that resize the grid's home are inside the pane: the editor
   * appearing, and the editor being taken away again when a program fills the
   * pane. Neither is a change the pane itself ever sees. The very first fit therefore ran while the
   * editor was still `display: none`, took the whole pane, and was never
   * revisited: 682px of grid living in a 606px scroller, four rows permanently
   * below the fold.
   *
   * No loop: fitting changes the row count, which changes the PTY size, which
   * produces output, which lands back here — and the usable height is the same,
   * so nothing refits.
   */
  private refitIfResized(): void {
    const v = this._latestViewport
    if (!v || !this.renderer) return
    const usable = this.usableViewport(v)
    if (usable.height === this.lastFitHeight) return
    this.lastFitHeight = usable.height
    this.renderer.fitViewport(usable)
  }

  private usableViewport(viewport: ContentViewport): ContentViewport {
    const area = this.scrollback?.scrollbackArea
    // Zero before first layout — the delivered box is the better guess then,
    // and the next viewport delivery corrects it. Each axis falls back on its
    // own: jsdom reports 0 for both, a real pane mid-layout can report one.
    const height = area && area.clientHeight > 0 ? area.clientHeight : viewport.height
    const width = area && area.clientWidth > 0 ? area.clientWidth : viewport.width
    return { ...viewport, width, height }
  }

  /**
   * Focus whichever surface owns input right now.
   *
   * At the prompt that is the editor, and the grid is deliberately read-only
   * while the editor is up (`setReadOnly(true)` on the input-state change). So
   * focusing the renderer unconditionally parked the caret in a widget that
   * drops every keystroke — and neither focus-bounce path rescues it, because
   * both stand down when the focus is already inside the live xterm container,
   * which is exactly where `renderer.focus()` puts it.
   *
   * This is why a freshly created tab typed fine and a tab you switched back to
   * did not: the new tab's `editor.show()` focuses its own textarea, while
   * `TabManager.activate()` ends with `tab.focus()` and took that focus away
   * again on every return.
   */
  focus(): void {
    if (this.editor?.isVisible) {
      this.editor.focus()
      return
    }
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

  // ── SSH alias hint support (w7-hint) ─────────────────────────────────

  /** Called on every textarea input change. Detects `ssh <partial>` commands
   *  and fetches matching aliases from the live ~/.ssh/config source.
   *  No client-side caching — every activation fetches fresh (coordinator contract). */
  private _onEditorInput(text: string): void {
    // Only when the editor owns keyboard input (PROMPT_READY with owned=true).
    if (!this._editorOwned || !this.profileClient) {
      this.editor?.hideAliasHints()
      return
    }

    // Detect `ssh <partial>` at the start of the line (possibly after whitespace).
    const trimmed = text.trimStart()
    const match = trimmed.match(/^ssh\s+(\S*)/)
    if (!match) {
      this.editor?.hideAliasHints()
      return
    }

    const partial = match[1]
    const fetchId = ++this._aliasFetchId

    // Fetch fresh aliases on every activation. Guard against stale responses
    // with a generation counter: a newer fetch invalidates an older one.
    this.profileClient
      .listSSHAliases()
      .then((resp) => {
        if (fetchId !== this._aliasFetchId) return // stale — newer text superseded this
        if (resp.unavailable) {
          this.editor?.hideAliasHints()
          return
        }
        const filtered = this._filterAliases(resp.aliases, partial)
        this.editor?.showAliasHints(filtered)
      })
      .catch(() => {
        // Fetch failed (network, backend down). Silently hide hints — the
        // feature degrades transparently rather than showing stale/flaky data.
        this.editor?.hideAliasHints()
      })
  }

  /** Filter SSH config aliases by case-insensitive prefix match.
   *  Excludes wildcard patterns (Host * etc. are rules, not targets). */
  private _filterAliases(aliases: SSHAliasEntry[], partial: string): SSHAliasEntry[] {
    const lower = partial.toLowerCase()
    return aliases.filter(
      // No wildcard filter here on purpose. sshConfig.aliases already excludes
      // patterns on the backend (internal/ssh/aliases.go, containsWildcard),
      // and a second copy of that rule in the renderer is a rule that drifts —
      // the two versions of it disagreed on '!' and on brackets before this
      // line was removed.
      (a) => a.alias.toLowerCase().startsWith(lower),
    )
  }
}
