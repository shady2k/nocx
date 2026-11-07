/**
 * The notes surface: the tab a note is written in, and the two ways one is
 * reached — the panel's rows and the chord.
 *
 * Wiring lives here rather than in main.tsx for the same reason the file
 * viewer's does: opening a note is a deduplicated tab, and the key that
 * deduplicates it is this module's decision. Asking for the same note twice
 * focuses the tab that is already open — two editors over one document is
 * two drafts of it, and the second one to save wins silently.
 */
import type { ContentDescriptor, SingletonKey, SurfaceType } from '../pane-content'
import { NoteContent } from './note-content'
import type { NotesStore } from './notes-store'
import type { PaneManager } from '../panes'
import { log } from '../log'
import { showToast } from '../ui/toast'
import { t } from '../i18n/locale-model'

/** Stable surface type (B.7), used in restore descriptors and deep links. */
const SURFACE_NOTE: SurfaceType = 'nocx.note' as SurfaceType

interface Wiring {
  readonly tm: PaneManager
  readonly store: NotesStore
}

let wiring: Wiring | null = null

/** The one wiring point; call once, after the PaneManager exists. */
export function registerNotesSurface(tm: PaneManager, store: NotesStore): void {
  wiring = { tm, store }
}

/**
 * Open (or focus) the tab for one note.
 *
 * restoreDescriptor is deliberately null, for the reason the file viewer
 * states: nothing serialises the tab list and nothing reconstructs a tab
 * from a descriptor, so a fifth writer of a field with no reader would be
 * the defect this repo has shipped before.
 */
export function openNote(id: string, title: string): void {
  if (!wiring) throw new Error('nocx: openNote called before registerNotesSurface')
  const descriptor: ContentDescriptor = {
    surfaceType: SURFACE_NOTE,
    singletonKey: `note:${id}` as SingletonKey,
    restoreDescriptor: null,
    supportsAttention: false,
    defaultTitle: title,
  }
  wiring.tm.openPane(new NoteContent(id, { store: wiring.store }), descriptor)
}

/**
 * Make a note and open it — the chord's whole path, and the panel's "+".
 * The note exists before the tab does, because a tab over a note the store
 * has not accepted would have nowhere to save to; and it is created EMPTY,
 * which is the ordinary case rather than an edge one.
 */
export async function createAndOpenNote(): Promise<void> {
  if (!wiring) throw new Error('nocx: createAndOpenNote called before registerNotesSurface')
  try {
    const created = await wiring.store.create('')
    openNote(created.id, created.title)
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err)
    log.error('nocx: could not create a note', { message })
    // The chord did nothing and says why: a keystroke that silently does
    // nothing is indistinguishable from a keystroke nobody bound.
    showToast({
      level: 'danger',
      message: `${t('notes.create.failedHead', 'Could not create a note:')} ${message}`,
    })
  }
}
