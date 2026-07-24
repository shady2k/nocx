// Passive DOM command editor (ADR-0004 §3). Holds text + selection only; a
// registered action decides where a submit goes. Keyboard routing to/from the
// PTY is by FOCUS: while shown the textarea captures keys; while hidden the
// xterm has focus and keys flow to the PTY as usual.

const MAX_ROWS = 10

export interface EditorActions {
  submit: (doc: string) => void
  // cancel discards the composed line the way Ctrl-C does at a shell prompt:
  // the editor clears and the shell is interrupted so a fresh prompt returns.
  // Without it, Ctrl-C in the textarea is a no-op and the stale text corrupts
  // the next command.
  cancel: () => void
}

export class CommandEditor {
  private root: HTMLElement
  private ta: HTMLTextAreaElement
  private chrome: HTMLElement
  private cwdChip: HTMLElement

  constructor(private readonly actions: EditorActions) {
    this.root = document.createElement('div')
    this.root.className = 'nocx-editor'
    this.root.style.display = 'none'

    // ── Editor chrome (header row) ──────────────────────────────────────
    this.chrome = document.createElement('div')
    this.chrome.className = 'nocx-editor-chrome'

    this.cwdChip = document.createElement('span')
    this.cwdChip.className = 'nocx-editor-cwd'
    this.cwdChip.textContent = '📁 ~'

    const submitBtn = document.createElement('button')
    submitBtn.className = 'nocx-editor-submit'
    submitBtn.textContent = '→'
    submitBtn.addEventListener('click', () => this.submit())

    this.chrome.append(this.cwdChip, submitBtn)
    this.root.appendChild(this.chrome)

    // ── Textarea ────────────────────────────────────────────────────────
    this.ta = document.createElement('textarea')
    this.ta.className = 'nocx-editor-input'
    this.ta.rows = 1
    this.ta.spellcheck = false
    this.ta.autocapitalize = 'off'
    this.ta.addEventListener('keydown', this.onKeydown)
    // Auto-grow: resize rows to fit content (1..MAX_ROWS).
    this.ta.addEventListener('input', this.onInput)
    this.root.appendChild(this.ta)
  }

  mount(container: HTMLElement): void {
    container.appendChild(this.root)
  }

  /** Update the cwd chip text. Uses the same short directoryLabel shape. */
  setCwd(cwd: string): void {
    const path = cwd.trim().replace(/\/+$/, '') || '~'
    const parts = path.split('/').filter(Boolean)
    const label = path === '~' || parts.length === 0 ? path : parts.slice(-2).join('/')
    this.cwdChip.textContent = `📁 ${label}`
  }

  // ── keyboard ──────────────────────────────────────────────────────────

  private onInput = (): void => {
    this._grow()
  }

  /** Submit the current textarea value, then hide and clear (ADR-0004 §2). */
  private submit(): void {
    const doc = this.ta.value
    // Atomic handoff (ADR-0004 §2): hide + clear BEFORE sending, so the
    // committed command is painted once by the shell, not echoed twice.
    this.ta.value = ''
    this.ta.rows = 1
    this.hide()
    this.actions.submit(doc)
  }

  private onKeydown = (e: KeyboardEvent): void => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      this.submit()
      return
    }
    // Escape clears the draft without interrupting the shell (Ctrl-C).
    if (e.key === 'Escape') {
      e.preventDefault()
      this.ta.value = ''
      this.ta.rows = 1
      return
    }
    // Ctrl-C cancels the composed line like a shell prompt. A real selection is
    // left alone so Ctrl-C still copies; with nothing selected, interrupt.
    if (e.ctrlKey && !e.metaKey && !e.altKey && (e.key === 'c' || e.key === 'C')) {
      if (this.ta.selectionStart !== this.ta.selectionEnd) return
      e.preventDefault()
      this.ta.value = ''
      this.ta.rows = 1
      this.actions.cancel()
    }
  }

  // ── visibility ────────────────────────────────────────────────────────

  show(): void {
    this.root.style.display = ''
    this.ta.focus()
  }

  /** Focus the textarea if the editor is visible. Safe to call when hidden. */
  focus(): void {
    if (this.root.style.display !== 'none') this.ta.focus()
  }

  /**
   * Insert text at the caret, replacing any selection, then grow + focus.
   * Used by right-click/middle-click paste while the editor owns input: at the
   * prompt the terminal is read-only (setReadOnly), so a paste must land in the
   * composed command, not the (disabled) grid.
   */
  insertText(text: string): void {
    const start = this.ta.selectionStart
    const end = this.ta.selectionEnd
    const v = this.ta.value
    this.ta.value = v.slice(0, start) + text + v.slice(end)
    const caret = start + text.length
    this.ta.selectionStart = this.ta.selectionEnd = caret
    this._grow()
    this.ta.focus()
  }

  hide(): void {
    this.ta.blur()
    this.root.style.display = 'none'
  }

  get isVisible(): boolean {
    return this.root.style.display !== 'none'
  }

  /** Whether the editor's root element contains `el`. Used to scope the
   *  focus-bounce so clicks on the submit button / textarea / cwd chip
   *  are not swallowed. */
  rootContains(el: Node | null): boolean {
    return this.root.contains(el)
  }

  /** The raw textarea element — exposed so the Tab can wire copy-on-select. */
  get textarea(): HTMLTextAreaElement {
    return this.ta
  }

  dispose(): void {
    this.ta.removeEventListener('keydown', this.onKeydown)
    this.ta.removeEventListener('input', this.onInput)
    this.root.remove()
  }

  // ── internal ──────────────────────────────────────────────────────────

  private _grow(): void {
    const lines = this.ta.value.split('\n').length
    this.ta.rows = Math.min(MAX_ROWS, Math.max(1, lines))
  }
}
