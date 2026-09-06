/**
 * The note tab — where a note is written (design §6.2, §6.3).
 *
 * The editor is the shared CM6 host in its editable mode, with the markdown
 * language from the file viewer's registry: one owner for "which language
 * this text is", and no second editor implementation anywhere in the
 * product.
 *
 * Saving is not a gesture. It happens when typing stops, and again when the
 * tab goes away — never per keystroke, which would be a disk write on the
 * hot path of somebody's thinking. What that costs is stated in the type
 * below: between an edit and the save landing, the text exists in exactly
 * ONE place, and the tab says so.
 */
import { EditableHost } from '../ui/cm-host'
import { markdownLanguage, viewerHighlighting } from '../ui/document-language'
import { BasePaneContent, type PaneHost } from '../pane-content'
import { log } from '../log'
import { showToast } from '../ui/toast'
import type { Note, NotesStore } from './notes-store'

/** How long after the last keystroke a save runs. Long enough that a
 *  sentence is one write, short enough that closing the lid a second after
 *  typing keeps the words. Not exported: the only caller that overrides it
 *  is a test, and it does so through the deps below. */
const SAVE_IDLE_MS = 500

export interface NoteContentDeps {
  store: NotesStore
  /** Injected for the tests, which cannot wait half a second per case and
   *  must not depend on a real clock (AGENTS.md: a test waits on a state,
   *  never on a duration). */
  idleMs?: number
}

export class NoteContent extends BasePaneContent {
  private root: HTMLElement | null = null
  private noticeEl: HTMLElement | null = null
  private readonly host = new EditableHost()
  private host_mounted = false

  /** What the store last confirmed it holds. The draft differs from this
   *  exactly while there is something unsaved. */
  private saved = ''
  private draft = ''
  private title = ''
  private timer: ReturnType<typeof setTimeout> | null = null
  private saving = false
  private disposed = false
  private paneHost: PaneHost | null = null

  constructor(
    private readonly noteId: string,
    private readonly deps: NoteContentDeps,
  ) {
    super()
  }

  // ── PaneContent ─────────────────────────────────────────────────────────

  async mount(target: HTMLElement, host: PaneHost, signal: AbortSignal): Promise<void> {
    if (this.disposed || signal.aborted) return
    this.paneHost = host

    this.root = document.createElement('div')
    this.root.className = 'note-tab'
    this.noticeEl = document.createElement('div')
    this.noticeEl.className = 'note-tab__notice'
    this.noticeEl.hidden = true
    this.noticeEl.setAttribute('role', 'alert')
    const editor = document.createElement('div')
    editor.className = 'note-tab__editor'
    this.root.append(this.noticeEl, editor)
    target.append(this.root)

    this.host.mount(editor, signal, [markdownLanguage(), viewerHighlighting], (text) =>
      this.onEdited(text),
    )
    this.host_mounted = true

    try {
      const note = await this.deps.store.get(this.noteId)
      if (this.disposed) return
      this.apply(note)
      this.host.focus()
    } catch (err) {
      // The note could not be read. The tab says so and stays: closing it
      // would look like the note never existed.
      this.showNotice(`Could not open this note: ${message(err)}`)
    }
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    this.clearTimer()
    if (this.draft.trim() === '') {
      // An empty note is not a note. The chord creates the record before the
      // tab exists — the editor needs somewhere to save to, and the id is
      // what deduplicates the tab — so "never save an empty one" is kept
      // here, at the only moment it can be: a note closed with nothing in it
      // is removed rather than left in the library as a row called by its
      // date and containing nothing.
      //
      // It applies whether the note was never typed into or emptied
      // deliberately: the store already holds the empty body in the second
      // case (the idle save wrote it), so deleting the record loses nothing
      // that closing the tab had not already lost.
      void this.deps.store.remove(this.noteId).catch((err: unknown) => {
        log.error('nocx: an empty note could not be removed on close', { message: message(err) })
      })
    } else if (this.draft !== this.saved) {
      // The last save runs on the way out, so a note closed a keystroke
      // after it was typed keeps the keystroke. It cannot be awaited here —
      // dispose is synchronous — so a failure lands as a toast rather than
      // on a tab that is already gone. Silence is the one thing it must
      // not be.
      void this.deps.store.update(this.noteId, this.draft).catch((err: unknown) => {
        log.error('nocx: a note could not be saved on close', { message: message(err) })
        showToast({
          level: 'danger',
          message: `"${this.title || 'Note'}" could not be saved: ${message(err)}`,
        })
      })
    }
    if (this.host_mounted) this.host.dispose()
    this.root?.remove()
    this.root = null
  }

  /** The editor lays itself out; nothing here depends on the viewport. */
  viewportChanged(): void {}

  /** The tab was activated: the keyboard belongs in the note. */
  focus(): void {
    this.host.focus()
  }

  // ── The draft ──────────────────────────────────────────────────────────

  /** Every document change: remember it, mark the tab, and restart the idle
   *  timer. The timer restarts rather than accumulating, so a paragraph is
   *  one write and not one per word. */
  private onEdited(text: string): void {
    this.draft = text
    this.clearTimer()
    if (text === this.saved) return
    this.timer = setTimeout(() => void this.save(), this.deps.idleMs ?? SAVE_IDLE_MS)
  }

  private clearTimer(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer)
      this.timer = null
    }
  }

  /** Write the draft. A failure keeps the text and says why — the tab does
   *  not report "saved" for a write that did not land, and it does not
   *  discard the words to get back to a state the store agrees with. */
  private async save(): Promise<void> {
    if (this.disposed || this.saving) return
    const body = this.draft
    if (body === this.saved) return
    this.saving = true
    try {
      const note = await this.deps.store.update(this.noteId, body)
      if (this.disposed) return
      this.saved = body
      this.title = note.title
      this.pushTitle()
      this.hideNotice()
    } catch (err) {
      if (this.disposed) return
      this.showNotice(`Not saved: ${message(err)}`)
    } finally {
      this.saving = false
      // Typing continued while the write was in flight: the draft moved on
      // and needs its own save. Without this the last edits of a fast
      // typist sit in the editor with nothing scheduled to write them.
      if (!this.disposed && this.draft !== this.saved && this.timer === null) {
        this.timer = setTimeout(() => void this.save(), this.deps.idleMs ?? SAVE_IDLE_MS)
      }
    }
  }

  private apply(note: Note): void {
    this.saved = note.body
    this.draft = note.body
    this.title = note.title
    this.host.setDoc(note.body)
    this.pushTitle()
  }

  /** The tab's name is the note's derived title, which the BACKEND decides
   *  (it travels with every note result). An empty one is a note with
   *  nothing in it yet, and that is named by when it was made — the one
   *  piece of naming that needs a locale, which is why it is here and not
   *  in the store. */
  private pushTitle(): void {
    const shown = this.title !== '' ? this.title : untitledName()
    this.paneHost?.setTitle(shown)
  }

  private showNotice(text: string): void {
    if (!this.noticeEl) return
    this.noticeEl.textContent = text
    this.noticeEl.hidden = false
  }

  private hideNotice(): void {
    if (!this.noticeEl) return
    this.noticeEl.hidden = true
    this.noticeEl.textContent = ''
  }
}

/** The name of a note with nothing in it yet. */
function untitledName(): string {
  return `Note — ${new Date().toLocaleDateString()}`
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}
