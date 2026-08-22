import { describe, it, expect, vi } from 'vitest'
import { LayoutStore } from './layout-store'
import { tabLabel } from './tab-label'
import { isUuidv7 } from './uuid7'
import type { LayoutClientLike, Replacement, PaneFacts } from './layout-client'
import type { LayoutReadResult, Tab, Pane } from '../generated/layout.read'

// The renderer's cache of a chain it does not own (nocx-isoph.4). What these
// tests are about is the line between the two: the cache may hold what the
// backend said, and may never decide anything itself — so a refused call
// leaves it untouched, and every write lands from the ANSWER rather than from
// the request.

const DEFAULT_WS = 'workspace:default'

function tab(id: string, over: Partial<Tab> = {}): Tab {
  return {
    id,
    workspaceId: DEFAULT_WS,
    parentId: null,
    name: null,
    colour: null,
    position: 0,
    pinned: false,
    layout: 'row',
    seenAt: null,
    ...over,
  }
}

function pane(id: string, tabId: string, over: Partial<Pane> = {}): Pane {
  return {
    id,
    tabId,
    cwd: '',
    kind: 'local',
    endpoint: null,
    sizeShare: 1,
    sandboxGranted: false,
    ...over,
  }
}

function snapshot(over: Partial<LayoutReadResult> = {}): LayoutReadResult {
  return {
    defaultWorkspaceId: DEFAULT_WS,
    workspaces: [{ id: DEFAULT_WS, name: 'default', position: 0, colour: null }],
    tabs: [],
    panes: [],
    ...over,
  }
}

/** The fake the store is exercised against: every method records what it was
 *  asked and answers with what a backend would. */
function fakeClient(over: Partial<LayoutClientLike> = {}): LayoutClientLike & {
  calls: Array<[string, unknown]>
} {
  const calls: Array<[string, unknown]> = []
  const base: LayoutClientLike = {
    read: () => {
      calls.push(['layout.read', {}])
      return Promise.resolve(snapshot())
    },
    createTab: (t) => {
      calls.push(['tabs.create', t])
      return Promise.resolve({
        tab: tab(t.id, { position: t.position, workspaceId: t.workspaceId }),
        firstPane: pane(t.firstPane.id, t.id, { cwd: t.firstPane.cwd, kind: t.firstPane.kind }),
        replayed: false,
      })
    },
    createPane: (p: PaneFacts & { tabId: string }) => {
      calls.push(['panes.create', p])
      return Promise.resolve({ pane: pane(p.id, p.tabId), replayed: false })
    },
    setPaneCwd: (id, cwd) => {
      calls.push(['panes.setCwd', { id, cwd }])
      return Promise.resolve({ pane: pane(id, 'tab-1', { cwd }) })
    },
    renameTab: (id, name) => {
      calls.push(['tabs.rename', { id, name }])
      return Promise.resolve({ tab: tab(id, { name }) })
    },
    recolourTab: (id, colour) => {
      calls.push(['tabs.recolour', { id, colour }])
      return Promise.resolve({ tab: tab(id, { colour }) })
    },
    pinTab: (id, pinned) => {
      calls.push(['tabs.pin', { id, pinned }])
      return Promise.resolve({ tab: tab(id, { pinned }) })
    },
    reorderTabs: (workspaceId, ids) => {
      calls.push(['tabs.reorder', { workspaceId, ids }])
      return Promise.resolve({
        tabs: ids.map((id, position) => tab(id, { position, workspaceId })),
      })
    },
    closeTab: (id, replacement: Replacement) => {
      calls.push(['tabs.close', { id, replacement }])
      return Promise.resolve({ id })
    },
    closePane: (id, replacement: Replacement) => {
      calls.push(['panes.close', { id, replacement }])
      return Promise.resolve({ id })
    },
    createWorkspace: (ws) => {
      calls.push(['workspaces.create', ws])
      return Promise.resolve({
        workspace: { id: ws.id, name: ws.name, position: ws.position, colour: null },
        firstTab: tab(ws.firstTab.id, { workspaceId: ws.id }),
        firstPane: pane(ws.firstPane.id, ws.firstTab.id, {
          cwd: ws.firstPane.cwd,
          kind: ws.firstPane.kind,
        }),
        replayed: false,
      })
    },
    closeWorkspace: (id, replacement: Replacement) => {
      calls.push(['workspaces.close', { id, replacement }])
      return Promise.resolve({ id })
    },
    recolourWorkspace: (id: string, colour: string | null) => {
      calls.push(['workspaces.recolour', { id, colour }])
      return Promise.resolve({ workspace: { id, name: id, position: 0, colour } })
    },
    renameWorkspace: (id: string, name: string) => {
      calls.push(['workspaces.rename', { id, name }])
      return Promise.resolve({ workspace: { id, name, position: 0, colour: null } })
    },
    reorderWorkspaces: (ids: readonly string[]) => {
      calls.push(['workspaces.reorder', { ids: [...ids] }])
      return Promise.resolve({
        workspaces: ids.map((id, position) => ({ id, name: id, position, colour: null })),
      })
    },
  }
  return Object.assign({ calls }, base, over)
}

describe('LayoutStore', () => {
  // ── nocx-isoph.7: a workspace can be renamed and the set reordered ──

  it("writes a renamed workspace from the backend's answer, not from the request", async () => {
    // The answer and the request differ on purpose here. A store that cached
    // what it SENT would look identical in the happy case and be wrong in
    // every case where the backend normalised, trimmed or refused — and the
    // symptom would be a name that survives until the next read and then
    // changes under the user.
    const client = fakeClient({
      read: () =>
        Promise.resolve(
          snapshot({ workspaces: [{ id: 'ws-1', name: 'old', position: 0, colour: null }] }),
        ),
      renameWorkspace: (id: string) =>
        Promise.resolve({
          workspace: { id, name: 'what the backend stored', position: 0, colour: null },
        }),
    })
    const store = new LayoutStore(client)
    await store.load()

    await store.renameWorkspace('ws-1', 'what the user typed')

    expect(store.workspaces().map((w) => w.name)).toEqual(['what the backend stored'])
  })

  it('replaces the whole set on a reorder, in the order the backend answered', async () => {
    const client = fakeClient({
      read: () =>
        Promise.resolve(
          snapshot({
            workspaces: [
              { id: 'ws-1', name: 'one', position: 0, colour: null },
              { id: 'ws-2', name: 'two', position: 1, colour: null },
            ],
          }),
        ),
    })
    const store = new LayoutStore(client)
    await store.load()

    await store.reorderWorkspaces(['ws-2', 'ws-1'])

    expect(store.workspaces().map((w) => w.id)).toEqual(['ws-2', 'ws-1'])
    // The WHOLE order crosses the wire, because that is what the backend
    // takes — it refuses anything that is not a permutation of what it holds.
    expect(client.calls).toContainEqual(['workspaces.reorder', { ids: ['ws-2', 'ws-1'] }])
  })

  it('leaves the set exactly as it was when a reorder is refused', async () => {
    const client = fakeClient({
      read: () =>
        Promise.resolve(
          snapshot({
            workspaces: [
              { id: 'ws-1', name: 'one', position: 0, colour: null },
              { id: 'ws-2', name: 'two', position: 1, colour: null },
            ],
          }),
        ),
      reorderWorkspaces: () => Promise.reject(new Error('not a permutation')),
    })
    const store = new LayoutStore(client)
    await store.load()

    await expect(store.reorderWorkspaces(['ws-2', 'ws-1'])).rejects.toThrow('not a permutation')

    // No optimistic move to snap back from: the strip never moved.
    expect(store.workspaces().map((w) => w.id)).toEqual(['ws-1', 'ws-2'])
  })

  it('draws itself from the read and holds nothing before it', async () => {
    const client = fakeClient({
      read: () =>
        Promise.resolve(
          snapshot({
            tabs: [tab('t1', { colour: '#ff8800', pinned: true, name: 'release' })],
            panes: [pane('p1', 't1', { cwd: '/repos/nocx' })],
          }),
        ),
    })
    const store = new LayoutStore(client)
    expect(store.tabs()).toEqual([])

    await store.load()
    expect(store.tabs().map((t) => t.id)).toEqual(['t1'])
    // The decoration is the backend's and arrives whole: this is the epic's
    // headline, at the store's own seam.
    expect(store.tab('t1')?.colour).toBe('#ff8800')
    expect(store.tab('t1')?.pinned).toBe(true)
    expect(store.tab('t1')?.name).toBe('release')
    expect(store.panesOf('t1').map((p) => p.cwd)).toEqual(['/repos/nocx'])
    expect(store.defaultWorkspaceId()).toBe(DEFAULT_WS)
  })

  it('mints UUIDv7 ids for the tab and its pane, and puts them in the default workspace', async () => {
    const client = fakeClient()
    const store = new LayoutStore(client)
    await store.load()

    const opened = store.openTab({ kind: 'local', endpoint: null, cwd: '' })
    await opened.created

    expect(isUuidv7(opened.tabId)).toBe(true)
    expect(isUuidv7(opened.paneId)).toBe(true)
    const [, params] = client.calls.find(([m]) => m === 'tabs.create')!
    expect(params).toMatchObject({
      id: opened.tabId,
      workspaceId: DEFAULT_WS,
      position: 0,
      firstPane: {
        id: opened.paneId,
        kind: 'local',
        endpoint: null,
        sizeShare: 1,
      },
    })
    expect(store.tabs().map((t) => t.id)).toEqual([opened.tabId])
    expect(store.tabOf(opened.paneId)?.id).toBe(opened.tabId)
  })

  it('opens a tab in the workspace the caller names, and in the default when it names none', async () => {
    // The window shows ONE workspace (§4.3), so a new tab belongs to the one
    // in front of the person who asked for it. The store does not know which
    // that is and must not guess: the caller says, and says the default when
    // that is the answer.
    const client = fakeClient()
    const store = new LayoutStore(client)
    await store.load()

    await store.openTab({ kind: 'local', endpoint: null, cwd: '' }, 'ws-1').created
    await store.openTab({ kind: 'local', endpoint: null, cwd: '' }).created

    const creates = client.calls.filter(([m]) => m === 'tabs.create').map(([, p]) => p)
    expect(creates[0]).toMatchObject({ workspaceId: 'ws-1' })
    expect(creates[1]).toMatchObject({ workspaceId: DEFAULT_WS })
  })

  it('refuses to open a tab before it has been told where tabs go', () => {
    const store = new LayoutStore(fakeClient())
    expect(() => store.openTab({ kind: 'local', endpoint: null, cwd: '' })).toThrow(
      /before the first read/,
    )
  })

  it('carries an ssh pane to the wire with its endpoint, and a local one without', async () => {
    const client = fakeClient()
    const store = new LayoutStore(client)
    await store.load()

    await store.openTab({ kind: 'ssh', endpoint: 'deploy@srv-01:22', cwd: '' }).created
    await store.openTab({ kind: 'local', endpoint: 'deploy@srv-01:22', cwd: '' }).created

    const creates = client.calls.filter(([m]) => m === 'tabs.create').map(([, p]) => p) as Array<{
      firstPane: PaneFacts
    }>
    expect(creates[0].firstPane).toMatchObject({ kind: 'ssh', endpoint: 'deploy@srv-01:22' })
    // An endpoint on a local pane would be refused on the way in — the empty
    // string is a real value meaning the local machine — so it never leaves.
    expect(creates[1].firstPane).toMatchObject({ kind: 'local', endpoint: null })
  })

  it('takes the decoration from the answer, not from what it asked for', async () => {
    const client = fakeClient({
      // A backend that answers with something else entirely: what the store
      // shows must be what the store HOLDS, never an echo of the request.
      recolourTab: (id) => Promise.resolve({ tab: tab(id, { colour: '#000000' }) }),
    })
    const store = new LayoutStore(client)
    await store.load()
    const opened = store.openTab({ kind: 'local', endpoint: null, cwd: '' })
    await opened.created

    await store.recolour(opened.tabId, '#ff8800')
    expect(store.tab(opened.tabId)?.colour).toBe('#000000')
  })

  it('reorders from the answer, and a refusal leaves the strip exactly where it was', async () => {
    const client = fakeClient()
    const store = new LayoutStore(client)
    await store.load()
    const a = store.openTab({ kind: 'local', endpoint: null, cwd: '' })
    await a.created
    const b = store.openTab({ kind: 'local', endpoint: null, cwd: '' })
    await b.created
    expect(store.tabs().map((t) => t.id)).toEqual([a.tabId, b.tabId])

    await store.reorder(DEFAULT_WS, [b.tabId, a.tabId])
    expect(store.tabs().map((t) => t.id)).toEqual([b.tabId, a.tabId])

    // THE ASSERTION THIS WHOLE DESIGN IS FOR: the backend refuses, and the
    // strip does not move. There is no optimistic reorder to snap back from,
    // because the cache is only ever written from an answer.
    const refusing = new LayoutStore(
      fakeClient({
        read: () =>
          Promise.resolve(
            snapshot({ tabs: [tab('t1', { position: 0 }), tab('t2', { position: 1 })] }),
          ),
        reorderTabs: () => Promise.reject(new Error('not a permutation')),
      }),
    )
    await refusing.load()
    const changes = vi.fn()
    refusing.onChange(changes)
    await expect(refusing.reorder(DEFAULT_WS, ['t2', 't1'])).rejects.toThrow('not a permutation')
    expect(refusing.tabs().map((t) => t.id)).toEqual(['t1', 't2'])
    expect(changes).not.toHaveBeenCalled()
  })

  it('closes a pane with a minted replacement and re-reads what is left', async () => {
    let read = snapshot({ tabs: [tab('t1')], panes: [pane('p1', 't1')] })
    const calls: string[] = []
    const client = fakeClient({
      read: () => {
        calls.push('layout.read')
        return Promise.resolve(read)
      },
      closePane: (id, replacement) => {
        calls.push('panes.close')
        // The backend's close is a transaction: the tab went with its last
        // pane and the replacement was minted. The renderer learns that by
        // ASKING, which is what the re-read is for.
        read = snapshot({
          tabs: [tab(replacement.tabId)],
          panes: [pane(replacement.paneId, replacement.tabId)],
        })
        return Promise.resolve({ id })
      },
    })
    const store = new LayoutStore(client)
    await store.load()

    const replacement = await store.closePane('p1')

    expect(isUuidv7(replacement.tabId)).toBe(true)
    expect(isUuidv7(replacement.paneId)).toBe(true)
    expect(store.tabs().map((t) => t.id)).toEqual([replacement.tabId])
    expect(store.panes().map((p) => p.id)).toEqual([replacement.paneId])
    // The read after the close is not an optimisation to remove: without it
    // the renderer would have to reconstruct a transaction it did not run.
    expect(calls).toEqual(['layout.read', 'panes.close', 'layout.read'])
  })

  it('creates a workspace together with its first tab and that tab s first pane', async () => {
    // §4.1: creation is always creation-with-content. There is no moment at
    // which an empty workspace exists, so the store cannot offer one either.
    const client = fakeClient()
    const store = new LayoutStore(client)
    await store.load()

    const made = store.createWorkspace('refactor-auth', 'blue', {
      kind: 'local',
      endpoint: null,
      cwd: '',
    })
    await made.created

    expect(isUuidv7(made.workspaceId)).toBe(true)
    expect(isUuidv7(made.tabId)).toBe(true)
    expect(isUuidv7(made.paneId)).toBe(true)
    const [, params] = client.calls.find(([m]) => m === 'workspaces.create')!
    expect(params).toMatchObject({
      id: made.workspaceId,
      name: 'refactor-auth',
      firstTab: { id: made.tabId },
      firstPane: { id: made.paneId, kind: 'local', endpoint: null, sizeShare: 1 },
    })
    expect(store.workspaces().map((w) => w.id)).toContain(made.workspaceId)
    expect(store.tab(made.tabId)?.workspaceId).toBe(made.workspaceId)
    expect(store.panesOf(made.tabId).map((p) => p.id)).toEqual([made.paneId])
  })

  it('leaves the cache untouched when a workspace create is refused', async () => {
    const store = new LayoutStore(
      fakeClient({ createWorkspace: () => Promise.reject(new Error('name is required')) }),
    )
    await store.load()
    const changes = vi.fn()
    store.onChange(changes)

    const made = store.createWorkspace('', 'blue', {
      kind: 'local',
      endpoint: null,
      cwd: '',
    })

    await expect(made.created).rejects.toThrow('name is required')
    expect(store.workspaces().map((w) => w.id)).toEqual([DEFAULT_WS])
    expect(changes).not.toHaveBeenCalled()
  })

  it('closes a workspace with a minted replacement and re-reads what is left', async () => {
    let read = snapshot({
      workspaces: [
        { id: DEFAULT_WS, name: 'default', position: 0, colour: null },
        { id: 'ws-1', name: 'refactor-auth', position: 1, colour: null },
      ],
      tabs: [tab('t1', { workspaceId: 'ws-1' })],
      panes: [pane('p1', 't1')],
    })
    const calls: string[] = []
    const client = fakeClient({
      read: () => {
        calls.push('layout.read')
        return Promise.resolve(read)
      },
      closeWorkspace: (id, replacement) => {
        calls.push('workspaces.close')
        // One transaction on the other side: the workspace, its tabs and
        // their panes go together, and the replacement is minted because the
        // close emptied the application.
        read = snapshot({
          tabs: [tab(replacement.tabId)],
          panes: [pane(replacement.paneId, replacement.tabId)],
        })
        return Promise.resolve({ id })
      },
    })
    const store = new LayoutStore(client)
    await store.load()

    const replacement = await store.closeWorkspace('ws-1')

    expect(isUuidv7(replacement.tabId)).toBe(true)
    expect(isUuidv7(replacement.paneId)).toBe(true)
    expect(store.workspaces().map((w) => w.id)).toEqual([DEFAULT_WS])
    expect(store.tabs().map((t) => t.id)).toEqual([replacement.tabId])
    expect(calls).toEqual(['layout.read', 'workspaces.close', 'layout.read'])
  })

  it('resolves which panes a workspace holds, including the rows nobody drew', async () => {
    // MEMBERSHIP IS RESOLVED HERE, and this test is the reason: the chain is
    // the only thing that knows an ssh row the renderer never drew is still a
    // member. A caller resolving membership from what is on screen would
    // close a workspace and leave part of it behind.
    const store = new LayoutStore(
      fakeClient({
        read: () =>
          Promise.resolve(
            snapshot({
              workspaces: [
                { id: DEFAULT_WS, name: 'default', position: 0, colour: null },
                { id: 'ws-1', name: 'refactor-auth', position: 1, colour: null },
              ],
              tabs: [
                tab('t1', { workspaceId: 'ws-1' }),
                tab('t2', { workspaceId: 'ws-1' }),
                tab('t3'),
              ],
              panes: [
                pane('p1', 't1'),
                pane('p2', 't2', { kind: 'ssh', endpoint: 'deploy@srv-01:22' }),
                pane('p3', 't3'),
              ],
            }),
          ),
      }),
    )
    await store.load()

    expect(store.panesOfWorkspace('ws-1').map((p) => p.id)).toEqual(['p1', 'p2'])
    expect(store.tabsOfWorkspace('ws-1').map((t) => t.id)).toEqual(['t1', 't2'])
    expect(store.panesOfWorkspace('nothing-here')).toEqual([])
  })

  it('notifies its listeners once per accepted change', async () => {
    const client = fakeClient()
    const store = new LayoutStore(client)
    const changes = vi.fn()
    store.onChange(changes)

    await store.load()
    expect(changes).toHaveBeenCalledTimes(1)
    const opened = store.openTab({ kind: 'local', endpoint: null, cwd: '' })
    await opened.created
    expect(changes).toHaveBeenCalledTimes(2)
    await store.pin(opened.tabId, true)
    expect(changes).toHaveBeenCalledTimes(3)
  })
})

describe('tabLabel', () => {
  it('names an unnamed tab from its panes, and a named one from the name', () => {
    // §4.5, and the two halves of the bead's second criterion: a tab holding
    // two panes is labelled from BOTH their titles; a tab the user names
    // shows that name instead.
    expect(tabLabel(null, ['nocx', 'srv-01'])).toBe('nocx · srv-01')
    expect(tabLabel('release', ['nocx', 'srv-01'])).toBe('release')
  })

  it('says nothing rather than something wrong while a pane has no title yet', () => {
    expect(tabLabel(null, [])).toBe('')
    expect(tabLabel(null, ['', '  '])).toBe('')
    expect(tabLabel('  ', ['nocx'])).toBe('nocx')
    // A pane with no title yet does not leave a dangling separator.
    expect(tabLabel(null, ['nocx', ''])).toBe('nocx')
  })
})
