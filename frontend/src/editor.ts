// Passive DOM command editor (ADR-0004 §3). Holds text + selection only; a
// registered action decides where a submit goes. Keyboard routing to/from the
// PTY is by FOCUS: while shown the textarea captures keys; while hidden the
// xterm has focus and keys flow to the PTY as usual.
import { FONT_FAMILY, FONT_SIZE, LINE_HEIGHT } from './renderers/font'

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

    const hint = document.createElement('span')
    hint.className = 'nocx-editor-hint'
    hint.textContent = '\u23ce'

    this.chrome.append(this.cwdChip, hint)
    this.root.appendChild(this.chrome)

    // ── Textarea ────────────────────────────────────────────────────────
    this.ta = document.createElement('textarea')
    this.ta.className = 'nocx-editor-input'
    this.ta.rows = 1
    this.ta.spellcheck = false
    this.ta.autocapitalize = 'off'
    // Match the terminal's font exactly (single source of truth with the
    // renderer) so the composed command reads as a continuation of the grid
    // above it, not as an alien UI-font input box.
    this.ta.style.fontFamily = FONT_FAMILY
    this.ta.style.fontSize = `${FONT_SIZE}px`
    this.ta.style.lineHeight = String(LINE_HEIGHT)
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

  private onKeydown = (e: KeyboardEvent): void => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      const doc = this.ta.value
      // Atomic handoff (ADR-0004 §2): hide + clear BEFORE sending, so the
      // committed command is painted once by the shell, not echoed twice.
      this.ta.value = ''
      this.ta.rows = 1
      this.hide()
      this.actions.submit(doc)
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

  hide(): void {
    this.ta.blur()
    this.root.style.display = 'none'
  }

  get isVisible(): boolean {
    return this.root.style.display !== 'none'
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
