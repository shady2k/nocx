/**
 * The Notes view descriptor — the shape every other activity-bar panel has
 * (Files, Git, Operations): the descriptor is the deliverable, the store is
 * reached once here, and the header action, the pinned filter and the panel
 * body all read it.
 *
 * Notes did not have one. Its descriptor was a literal in main.tsx, which is
 * why it was also the panel with no test of its own and the panel that had
 * drifted furthest: the search field and the "+ New note" button rendered in
 * a bordered strip at the top of the SCROLLING body, so both went away with
 * the list the moment anybody scrolled it, and the strip added 8px of inset
 * on top of the body's own so every note title started one step right of the
 * panel's title (nocx-708q.3).
 *
 * The search state lives here rather than in the panel for the same reason
 * the Git filter lives in its store: the field is now in the shell's pinned
 * row, outside the panel component, so the query and the rows it produced
 * have to be reachable from both or they are two answers to one question.
 */
import { createSignal, Show } from 'solid-js'
import type { Component } from 'solid-js'
import type { SidebarViewDescriptor } from '../sidebar'
import { IconButton } from '../ui/icon-button'
import { PlusIcon, TextQuoteIcon } from '../ui/icons'
import { SearchField } from '../ui/search-field'
import { NotesPanel } from './notes-panel'
import type { NotesState, NotesStore } from './notes-store'

const NOTES_VIEW_ID = 'notes'
/** After Files (-1), Ports (0) and Git (1). */
const NOTES_VIEW_ORDER = 2

export interface NotesViewDeps {
  /** The one list every notes surface reads. */
  store: NotesStore
  /** Open this note's tab — the panel finds, the tab writes. */
  onOpen: (id: string) => void
  /** Make one and open it: the header's "+", the empty state's button, and
   *  the same path the ⌥⌘N chord takes. */
  onCreate: () => void
}

export function createNotesView(deps: NotesViewDeps): SidebarViewDescriptor {
  /** The store's list, as a signal every part of the view can read. The
   *  subscription is made ONCE, with the descriptor, and never torn down —
   *  the store outlives the panel here exactly as the Git store does, so
   *  the header action can say whether creating is possible while the
   *  panel is not even mounted. */
  const [state, setState] = createSignal<NotesState>(deps.store.state())
  deps.store.subscribe(setState)

  /** What is typed. The BACKEND searches (the FTS index is there), so this
   *  is not a predicate over a loaded list — it is the thing that was
   *  asked, kept because the empty state has to quote it back. */
  const [query, setQuery] = createSignal('')

  const search = (value: string): void => {
    setQuery(value)
    // Every keystroke asks; the store's generation guard is what keeps a
    // slow answer from overwriting a newer one.
    void deps.store.search(value)
  }

  /** "+ New note", in the HEADER, where every other panel keeps its one
   *  action. It was a full-width primary Button stacked under the search
   *  field in the body — a second vocabulary for the thing Files, Git and
   *  Ports all say with an icon beside the panel's name, and it scrolled
   *  away with the list besides.
   *
   *  Absent — never disabled — while the store is unavailable: an offer
   *  that cannot be honoured is a lie (design §8). The empty state keeps
   *  its own worded button, because an empty panel needs to say what to do
   *  next and an icon does not say it. */
  const NotesActions: Component = () => (
    <Show when={state().kind !== 'unavailable'}>
      <IconButton
        data-testid="notes-create"
        size="sm"
        ariaLabel="New note"
        title="New note"
        onClick={() => deps.onCreate()}
      >
        <PlusIcon />
      </IconButton>
    </Show>
  )

  /** THE FILTER, declared for the shell's pinned slot. Unlike Git's and
   *  Ports', it is present in every state — including the one where the
   *  store could not be read. That is deliberate: this field is what
   *  ISSUES the search, so hiding it in the failed state would remove the
   *  only control that could ask again, and "Retry" in the empty state
   *  re-reads the list rather than the query. */
  const NotesFilter: Component = () => (
    <SearchField
      value={query()}
      onInput={search}
      placeholder="Search notes…"
      ariaLabel="Search notes"
      onKeyDown={(e) => {
        if (e.key === 'Escape' && query() !== '') {
          e.stopPropagation()
          search('')
        }
      }}
    />
  )

  return {
    id: NOTES_VIEW_ID,
    title: 'Notes',
    icon: TextQuoteIcon,
    actions: NotesActions,
    filter: NotesFilter,
    view: () => (
      <NotesPanel
        store={deps.store}
        state={state}
        query={query}
        onOpen={deps.onOpen}
        onCreate={deps.onCreate}
      />
    ),
    order: NOTES_VIEW_ORDER,
  }
}
