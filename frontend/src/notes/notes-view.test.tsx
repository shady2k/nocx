// @vitest-environment jsdom
//
// The Notes panel, through the shell a user actually reaches: the real
// `mountSidebar`, the real descriptor `main.tsx` builds, and the real store
// over a fake client. Nothing here mounts `NotesPanel` on its own, because
// the defects this bead is about were all in the arrangement AROUND the
// panel — where the search field sits, where the create button sits, which
// element scrolls — and a test that renders the body in a vacuum cannot see
// any of them. Notes had no test at all before this, which is the other half
// of why it drifted furthest (nocx-708q.3).
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { mountSidebar, type SidebarHandle } from '../sidebar'
import { createNotesView } from './notes-view'
import { NotesStore, type NoteRow, type NotesClientLike } from './notes-store'

const ROWS: NoteRow[] = [
  { id: 'a', title: 'Deploy', excerpt: 'kubectl rollout', updatedAt: 10 },
  { id: 'b', title: 'Postgres', excerpt: 'vacuum full', updatedAt: 20 },
]

function fakeClient(over: Partial<NotesClientLike> = {}): NotesClientLike {
  return {
    list: vi.fn().mockResolvedValue({ notes: ROWS }),
    get: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn().mockResolvedValue({ id: 'a' }),
    search: vi.fn().mockResolvedValue({ matches: [ROWS[0]] }),
    ...over,
  }
}

const handles: SidebarHandle[] = []

afterEach(() => {
  for (const h of handles.splice(0)) h.destroy()
  cleanup()
  document.body.replaceChildren()
})

function mountApp(client: NotesClientLike = fakeClient(), onCreate = vi.fn()) {
  const store = new NotesStore(client)
  const onOpen = vi.fn()
  const view = createNotesView({ store, onOpen, onCreate })
  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)
  handles.push(mountSidebar(bar, panel, [view], []))
  return { bar, panel, store, onOpen, onCreate }
}

/** The panel's rows. RecordRow composes on CollectionRow, so the row's own
 *  identity class is the collection row's — the record part names the title,
 *  meta and status inside it. */
const rowsOf = (panel: HTMLElement): HTMLElement[] => [
  ...panel.querySelectorAll<HTMLElement>('[role="list"] .ui-collection-row'),
]

function searchBox(panel: HTMLElement): HTMLInputElement {
  const el = panel.querySelector<HTMLInputElement>('input[aria-label="Search notes"]')
  if (el === null) throw new Error('no search field')
  return el
}

describe('the Notes panel is the same shape as every other panel', () => {
  it('a user opens Notes and sees their notes', async () => {
    const { panel } = mountApp()
    await vi.waitFor(() => expect(rowsOf(panel)).toHaveLength(2))
    expect(panel.textContent).toContain('Deploy')
    expect(panel.textContent).toContain('Postgres')
  })

  it('pins the search field above the scroller, where it cannot scroll away', async () => {
    // It was the first thing inside the body, in a bordered strip with the
    // create button — so scrolling a long library took both off the top of
    // the panel. Pinned means the shell's filter row, never the body.
    const { panel } = mountApp()
    await vi.waitFor(() => expect(rowsOf(panel)).toHaveLength(2))

    const field = searchBox(panel)
    expect(field.closest('.ui-sidebar-view__filter')).not.toBeNull()
    expect(field.closest('.ui-sidebar-view__body')).toBeNull()
    // And the list it searches IS in the scroller — the pair is the point.
    expect(panel.querySelector('[role="list"]')?.closest('.ui-sidebar-view__body')).not.toBeNull()
  })

  it('searching still asks the backend and renders what came back', async () => {
    // The field moved; what it does did not. The FTS index is on the Go
    // side, so a keystroke is a request, never a predicate over a loaded
    // list.
    const search = vi.fn().mockResolvedValue({ matches: [ROWS[1]] })
    const { panel } = mountApp(fakeClient({ search }))
    await vi.waitFor(() => expect(rowsOf(panel)).toHaveLength(2))

    fireEvent.input(searchBox(panel), { target: { value: 'vacuum' } })
    await vi.waitFor(() => expect(search).toHaveBeenCalledWith('vacuum'))
    await vi.waitFor(() => expect(rowsOf(panel)).toHaveLength(1))
    expect(panel.textContent).toContain('Postgres')
  })

  it('a query that matches nothing quotes the query back', async () => {
    const { panel } = mountApp(fakeClient({ search: vi.fn().mockResolvedValue({ matches: [] }) }))
    await vi.waitFor(() => expect(rowsOf(panel)).toHaveLength(2))

    fireEvent.input(searchBox(panel), { target: { value: 'zzz' } })
    await vi.waitFor(() => expect(panel.textContent).toContain('Nothing matches'))
    expect(panel.textContent).toContain('"zzz"')
  })

  it('keeps "new note" in the HEADER, the way every other panel keeps its action', async () => {
    // It was a full-width primary Button stacked under the search field in
    // the body: a second vocabulary for what Files, Git and Ports all say
    // with one icon beside the panel's name, and it scrolled away too.
    const onCreate = vi.fn()
    const { panel } = mountApp(fakeClient(), onCreate)
    await vi.waitFor(() => expect(rowsOf(panel)).toHaveLength(2))

    const create = panel.querySelector<HTMLElement>('[data-testid="notes-create"]')
    expect(create).not.toBeNull()
    expect(create!.closest('.ui-sidebar-view__header')).not.toBeNull()
    expect(create!.closest('.ui-sidebar-view__body')).toBeNull()

    create!.click()
    expect(onCreate).toHaveBeenCalledTimes(1)
  })

  it('withdraws the create action when the store cannot be read', async () => {
    // An offer that cannot be honoured is a lie (design §8). ABSENT, never
    // disabled — and the header has to know it while the body is showing
    // the failure, which is why the descriptor holds the subscription.
    const { panel } = mountApp(
      fakeClient({ list: vi.fn().mockRejectedValue(new Error('notes db is locked')) }),
    )
    await vi.waitFor(() => expect(panel.textContent).toContain("Couldn't load your notes"))
    expect(panel.textContent).toContain('notes db is locked')
    expect(panel.querySelector('[data-testid="notes-create"]')).toBeNull()
    // The field stays: it is what ISSUES a search, so hiding it would take
    // away the only control left that asks the backend anything.
    expect(panel.querySelector('input[aria-label="Search notes"]')).not.toBeNull()
  })

  it('opens the note a row names', async () => {
    const { panel, onOpen } = mountApp()
    await vi.waitFor(() => expect(rowsOf(panel)).toHaveLength(2))
    rowsOf(panel)[0].click()
    expect(onOpen).toHaveBeenCalledWith('a')
  })
})
