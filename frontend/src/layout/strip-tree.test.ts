import { describe, expect, it } from 'vitest'
import { lineageOrder } from './strip-tree'
import type { Tab } from '../generated/layout.read'

const tab = (id: string, over: Partial<Tab> = {}): Tab => ({
  id,
  workspaceId: 'workspace:default',
  parentId: null,
  name: null,
  colour: null,
  position: 0,
  pinned: false,
  layout: 'row',
  seenAt: null,
  ...over,
})

const shape = (tabs: readonly Tab[]) =>
  lineageOrder(tabs).map((r) => `${'·'.repeat(r.depth)}${r.tab.id}`)

describe('lineageOrder — the vertical strip is a tree, not a list', () => {
  it('puts a lineage child under its parent rather than beside it', () => {
    const tabs = [
      tab('claude', { position: 0 }),
      tab('other', { position: 1 }),
      tab('worker', { position: 2, parentId: 'claude' }),
    ]

    expect(shape(tabs)).toEqual(['claude', '·worker', 'other'])
  })

  it('nests to any depth, one indent per generation', () => {
    const tabs = [
      tab('claude', { position: 0 }),
      tab('worker', { position: 1, parentId: 'claude' }),
      tab('grandchild', { position: 2, parentId: 'worker' }),
    ]

    expect(shape(tabs)).toEqual(['claude', '·worker', '··grandchild'])
  })

  it('orders siblings by the strip order the backend stores — pinned first, then position', () => {
    const tabs = [
      tab('a', { position: 0 }),
      tab('b', { position: 1, pinned: true }),
      tab('c1', { position: 5, parentId: 'a' }),
      tab('c2', { position: 4, parentId: 'a', pinned: true }),
    ]

    expect(shape(tabs)).toEqual(['b', 'a', '·c2', '·c1'])
  })

  it('draws a tab whose parent is not in this set as a top-level row', () => {
    // Provenance is not membership: a tab spawned by one in ANOTHER workspace
    // is a member of its own workspace and must be drawn there. A row that
    // waits for a parent that is not coming is a row the user never sees.
    const tabs = [tab('orphan', { position: 0, parentId: 'elsewhere' })]

    expect(shape(tabs)).toEqual(['orphan'])
  })

  it('draws every tab exactly once even if the edges lead in a circle', () => {
    // The backend refuses a cycle; a walk that would hang if one ever arrived
    // is a defect waiting for a bad row, not a walk.
    const tabs = [
      tab('a', { position: 0, parentId: 'b' }),
      tab('b', { position: 1, parentId: 'a' }),
    ]

    const rows = lineageOrder(tabs)

    expect(rows.map((r) => r.tab.id).sort()).toEqual(['a', 'b'])
  })

  it('answers nothing for no tabs', () => {
    expect(lineageOrder([])).toEqual([])
  })
})
