// @vitest-environment jsdom
// The note tab (nocx-z56hq.3, design §6.2, §6.3): a document the person
// types into that saves itself, names itself, and never reports "saved" for
// a write that did not land.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NoteContent } from './note-content'
import { NotesStore, type NotesClientLike } from './notes-store'
import type { PaneHost } from '../pane-content'

const NOTE = { id: 'n1', title: 'Deploy', body: 'Deploy\nkubectl', createdAt: 1, updatedAt: 1 }

function harness(over: Partial<NotesClientLike> = {}) {
  const titles: string[] = []
  const update =
    over.update ??
    vi
      .fn()
      .mockImplementation((id: string, body: string) =>
        Promise.resolve({ id, title: body.split('\n')[0] ?? '', body, createdAt: 1, updatedAt: 2 }),
      )
  const client: NotesClientLike = {
    list: vi.fn().mockResolvedValue({ notes: [] }),
    get: vi.fn().mockResolvedValue(NOTE),
    create: vi.fn().mockResolvedValue(NOTE),
    update,
    remove: vi.fn().mockResolvedValue({ id: 'n1' }),
    search: vi.fn().mockResolvedValue({ matches: [] }),
    ...over,
  }
  const store = new NotesStore(client)
  // idleMs 0: the test waits on the SAVE, never on a duration.
  const content = new NoteContent('n1', { store, idleMs: 0 })
  const target = document.createElement('div')
  document.body.append(target)
  const host: PaneHost = {
    contentSettled: () => {},
    setTitle: (t) => titles.push(t),
    updateTooltip: () => {},
    requestAttention: () => {},
    requestClose: () => {},
  }
  const lastTitle = (): string | undefined => titles[titles.length - 1]
  return { content, target, host, update, titles, lastTitle, client }
}

/** The editor's own document seam — the same one the surface saves from. */
function type(content: NoteContent, text: string): void {
  const host = (content as unknown as { host: { setDoc(t: string): void } }).host
  host.setDoc(text)
}

afterEach(() => {
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('the note tab', () => {
  it('opens the note and names the tab from its title', async () => {
    const h = harness()
    await h.content.mount(h.target, h.host, new AbortController().signal)

    expect(h.target.querySelector<HTMLElement>('.cm-content')?.textContent).toContain('Deploy')
    expect(h.lastTitle()).toBe('Deploy')
    h.content.dispose()
  })

  it('a note with nothing in it yet is named by its date, not left blank', async () => {
    const h = harness({
      get: vi.fn().mockResolvedValue({ id: 'n1', title: '', body: '', createdAt: 1, updatedAt: 1 }),
    })
    await h.content.mount(h.target, h.host, new AbortController().signal)

    expect(h.lastTitle()).toMatch(/^Note — /)
    h.content.dispose()
  })

  it('typing then stopping saves ONCE, and the tab follows the first line', async () => {
    const h = harness()
    await h.content.mount(h.target, h.host, new AbortController().signal)

    type(h.content, 'Renamed\nkubectl')

    await vi.waitFor(() => {
      expect(h.update).toHaveBeenCalledTimes(1)
    })
    expect(h.update).toHaveBeenCalledWith('n1', 'Renamed\nkubectl')
    await vi.waitFor(() => {
      expect(h.lastTitle()).toBe('Renamed')
    })
    h.content.dispose()
  })

  it('a keystroke that lands during a save is written too, not left in the editor', async () => {
    // The fast typist: the draft moved on while the write was in flight, and
    // nothing was scheduled to write the rest.
    let release!: () => void
    const gate = new Promise<void>((r) => {
      release = r
    })
    let calls = 0
    const update = vi.fn().mockImplementation(async (id: string, body: string) => {
      calls += 1
      if (calls === 1) await gate
      return { id, title: 'x', body, createdAt: 1, updatedAt: 2 }
    })
    const h = harness({ update })
    await h.content.mount(h.target, h.host, new AbortController().signal)

    type(h.content, 'one')
    await vi.waitFor(() => {
      expect(update).toHaveBeenCalledTimes(1)
    })
    type(h.content, 'one two')
    release()

    await vi.waitFor(() => {
      expect(update).toHaveBeenCalledTimes(2)
    })
    expect(update.mock.calls[1][1]).toBe('one two')
    h.content.dispose()
  })

  it('a failed save keeps the text and says why — it never reports success', async () => {
    const h = harness({ update: vi.fn().mockRejectedValue(new Error('disk is full')) })
    await h.content.mount(h.target, h.host, new AbortController().signal)

    type(h.content, 'words worth keeping')

    await vi.waitFor(() => {
      const notice = h.target.querySelector('.note-tab__notice') as HTMLElement
      expect(notice.hidden).toBe(false)
      expect(notice.textContent).toContain('disk is full')
    })
    // The words are still on screen: getting back to a state the store
    // agrees with by discarding them is the one repair nobody wants.
    expect(h.target.querySelector<HTMLElement>('.cm-content')?.textContent).toContain(
      'words worth keeping',
    )
    h.content.dispose()
  })

  it('a save that works clears the failure notice', async () => {
    const update = vi
      .fn()
      .mockRejectedValueOnce(new Error('disk is full'))
      .mockImplementation((id: string, body: string) =>
        Promise.resolve({ id, title: 't', body, createdAt: 1, updatedAt: 3 }),
      )
    const h = harness({ update })
    await h.content.mount(h.target, h.host, new AbortController().signal)

    type(h.content, 'first try')
    await vi.waitFor(() => {
      expect((h.target.querySelector('.note-tab__notice') as HTMLElement).hidden).toBe(false)
    })
    type(h.content, 'second try')
    await vi.waitFor(() => {
      expect((h.target.querySelector('.note-tab__notice') as HTMLElement).hidden).toBe(true)
    })
    h.content.dispose()
  })

  it('closing the tab writes what was unsaved', async () => {
    const h = harness()
    await h.content.mount(h.target, h.host, new AbortController().signal)

    // No idle window passes: the tab is closed the moment after typing.
    const editorHost = (h.content as unknown as { host: { setDoc(t: string): void } }).host
    ;(h.content as unknown as { timer: null }).timer = null
    editorHost.setDoc('typed and closed')
    h.content.dispose()

    await vi.waitFor(() => {
      expect(h.update).toHaveBeenCalledWith('n1', 'typed and closed')
    })
  })

  it('closing a note nobody typed into removes it — an empty note is not a note', async () => {
    const remove = vi.fn().mockResolvedValue({ id: 'n1' })
    const h = harness({
      get: vi.fn().mockResolvedValue({ id: 'n1', title: '', body: '', createdAt: 1, updatedAt: 1 }),
      remove,
    })
    await h.content.mount(h.target, h.host, new AbortController().signal)

    h.content.dispose()

    await vi.waitFor(() => {
      expect(remove).toHaveBeenCalledWith('n1')
    })
    // And nothing was written on the way out: there was nothing to write.
    expect(h.update).not.toHaveBeenCalled()
  })

  it('whitespace alone is empty: a note of blank lines is removed too', async () => {
    const remove = vi.fn().mockResolvedValue({ id: 'n1' })
    const h = harness({
      get: vi.fn().mockResolvedValue({ id: 'n1', title: '', body: '', createdAt: 1, updatedAt: 1 }),
      remove,
    })
    await h.content.mount(h.target, h.host, new AbortController().signal)
    type(h.content, '\n  \n\t\n')
    h.content.dispose()

    await vi.waitFor(() => {
      expect(remove).toHaveBeenCalledWith('n1')
    })
  })

  it('a note with words in it is kept, and the words are written', async () => {
    const remove = vi.fn().mockResolvedValue({ id: 'n1' })
    const h = harness({ remove })
    await h.content.mount(h.target, h.host, new AbortController().signal)
    type(h.content, 'worth keeping')
    h.content.dispose()

    await vi.waitFor(() => {
      expect(h.update).toHaveBeenCalledWith('n1', 'worth keeping')
    })
    expect(remove).not.toHaveBeenCalled()
  })

  it('a note that cannot be read says so instead of showing an empty document', async () => {
    const h = harness({ get: vi.fn().mockRejectedValue(new Error('no such note')) })
    await h.content.mount(h.target, h.host, new AbortController().signal)

    const notice = h.target.querySelector('.note-tab__notice') as HTMLElement
    expect(notice.hidden).toBe(false)
    expect(notice.textContent).toContain('no such note')
    h.content.dispose()
  })
})
