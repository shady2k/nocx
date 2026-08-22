/**
 * The notes panel in the activity bar (design §6.1) — where a note is
 * FOUND. It deliberately does not edit one: the panel is 240px wide by
 * default, which is a good width for finding and a bad one for writing, and
 * a surface that is bad at its own job is worse than an absent one.
 *
 * Search runs on the backend (the FTS index is there), so what this panel
 * does with the query is hand it over and render rows — it never filters a
 * list it loaded, because that would mean loading every note to look inside
 * it.
 *
 * WHAT THIS COMPONENT IS is the panel's BODY and nothing else — which is
 * why nothing here mentions `SidebarView`. The frame is the kit's and is
 * reached through the DESCRIPTOR in notes-view.tsx, which the shell renders
 * inside `SidebarView` for every panel alike (sidebar.tsx builds it once).
 * The search field and the "+ New note" button used to be here, in a
 * bordered strip at the top of the scrolling body, and both went away with
 * the list the moment anybody scrolled it. They are the descriptor's header
 * action and pinned filter slots now, the way every other panel says it; the
 * state and the query arrive as accessors so the field, the button and the
 * rows read one signal (nocx-708q.3).
 */
import { For, onMount, Show } from 'solid-js'
import { Button } from '../ui/button'
import { RecordRow } from '../ui/record-row'
import { EmptyState } from '../ui/empty-state'
import { IconButton } from '../ui/icon-button'
import { TrashIcon } from '../ui/icons'
import { showConfirm } from '../ui/dialog'
import { showToast } from '../ui/toast'
import { log } from '../log'
import type { NoteRow, NotesState, NotesStore } from './notes-store'

export interface NotesPanelProps {
  store: NotesStore
  /** The store's list, as the view descriptor holds it. An accessor, never
   *  a snapshot: the header's create action reads the same signal, and a
   *  panel that re-derived it would be a second answer to one question. */
  state: () => NotesState
  /** What is typed in the shell's pinned field. Read here only to quote it
   *  back in the "nothing matched" state — the search itself is issued by
   *  the field, because the backend is what searches. */
  query: () => string
  /** Open this note's tab — the panel finds, the tab writes. */
  onOpen: (id: string) => void
  /** Make one and open it: the empty state's button, and the same path the
   *  header action and the chord take. */
  onCreate: () => void
}

/** A row's date, in the person's locale — the one piece of naming that
 *  needs one, which is why it is here and not in the store. */
function when(ms: number): string {
  return new Date(ms).toLocaleDateString()
}

/** A note with nothing in it yet has no derived title (the backend returns
 *  an empty one); the surface names it by when it was made. */
function nameOf(row: NoteRow): string {
  return row.title !== '' ? row.title : `Note — ${when(row.updatedAt)}`
}

export function NotesPanel(props: NotesPanelProps) {
  // Opening the panel re-reads the library. The panel unmounts whenever
  // another view is in front, so this IS "the user came to look" — and the
  // subscription that renders the answer is the descriptor's, made once,
  // because the header's create action has to know whether the store is
  // reachable while this component does not exist.
  onMount(() => void props.store.refresh())

  const rows = (): readonly NoteRow[] => {
    const s = props.state()
    return s.kind === 'ready' ? s.rows : []
  }

  const remove = async (row: NoteRow): Promise<void> => {
    if (!(await showConfirm(`Delete "${nameOf(row)}"?`))) return
    try {
      await props.store.remove(row.id)
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      log.error('Failed to delete note', { message: msg })
      showToast({ level: 'danger', message: `Could not delete the note: ${msg}` })
    }
  }

  return (
    <div class="notes-panel">
      <Show
        when={rows().length > 0}
        fallback={
          <Show
            when={props.state().kind === 'unavailable'}
            fallback={
              <Show
                when={props.query().trim() !== ''}
                fallback={
                  <EmptyState
                    title="No notes yet"
                    description="Write something down without leaving the terminal — ⌥⌘N."
                    action={
                      <Button variant="primary" onClick={() => props.onCreate()}>
                        + New note
                      </Button>
                    }
                  />
                }
              >
                <EmptyState
                  title="Nothing matches"
                  description={`No note contains "${props.query().trim()}".`}
                />
              </Show>
            }
          >
            <EmptyState
              title="Couldn't load your notes"
              description={
                props.state().kind === 'unavailable' ? unavailableMessage(props.state()) : ''
              }
              action={
                <Button variant="default" onClick={() => void props.store.refresh()}>
                  Retry
                </Button>
              }
            />
          </Show>
        }
      >
        <div class="notes-panel__list" role="list" aria-label="Notes">
          <For each={rows()}>
            {(row) => (
              <RecordRow
                density="dense"
                title={nameOf(row)}
                meta={row.excerpt}
                status={{ tone: 'neutral', text: when(row.updatedAt) }}
                onActivate={() => props.onOpen(row.id)}
                actions={
                  <IconButton
                    size="sm"
                    ariaLabel={`Delete ${nameOf(row)}`}
                    onClick={() => void remove(row)}
                  >
                    <TrashIcon />
                  </IconButton>
                }
              />
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}

function unavailableMessage(s: NotesState): string {
  return s.kind === 'unavailable' ? s.message : ''
}
