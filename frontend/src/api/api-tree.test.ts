// The tree's shape — the one place that turns a collection's listing into rows.
//
// It is a unit test rather than only a workbench one because the question it
// answers is not reachable from the surface in every state that matters: a
// filter narrowing a folder list, a collapsed parent above a folder that
// matched, a folder the backend lists that holds nothing. What a person can
// do with a folder is asserted in api-workbench.test.tsx, where the control
// is clicked; what a folder IS lives here.
import { describe, expect, it } from 'vitest'
import { filterCollections, flattenCollections, type ApiTreeRow } from './api-tree'
import type { ApiOpenCollection } from './api-model'
import { collectionFixture, collectionsFixture } from './api-test-fixtures'

const NOTHING = new Set<string>()

function tree(open: readonly ApiOpenCollection[], collapsed: ReadonlySet<string> = NOTHING) {
  return flattenCollections(open, collapsed).map((r: ApiTreeRow) => `${r.kind}:${r.relPath}@${r.depth}`)
}

describe('the tree draws the folders the BACKEND listed', () => {
  it('shows a folder with nothing in it — the state a folder spends its first minutes in', () => {
    // The whole reason `folders` rides on the collection. Derived from the
    // request paths, this row does not exist: nothing is under it yet.
    const open = [
      collectionsFixture({
        collection: collectionFixture({ requests: [], folders: ['reports'] }),
      }),
    ]
    expect(tree(open)).toEqual(['collection:@0', 'dir:reports@1'])
  })

  it('puts the requests inside the folder that holds them, folders first', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({
          requests: [
            { relPath: 'users/create.json', name: 'create', method: 'POST' },
            { relPath: 'ping.json', name: 'ping', method: 'GET' },
          ],
          folders: ['users'],
        }),
      }),
    ]
    expect(tree(open)).toEqual([
      'collection:@0',
      'dir:users@1',
      'request:users/create.json@2',
      'request:ping.json@1',
    ])
  })

  it('nests, parents before their children, however deep the list goes', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({
          requests: [{ relPath: 'v1/users/admin/grant.json', name: 'grant', method: 'POST' }],
          folders: ['v1', 'v1/users', 'v1/users/admin'],
        }),
      }),
    ]
    expect(tree(open)).toEqual([
      'collection:@0',
      'dir:v1@1',
      'dir:v1/users@2',
      'dir:v1/users/admin@3',
      'request:v1/users/admin/grant.json@4',
    ])
  })

  it('a folder that is folded away takes everything under it with it', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({
          requests: [{ relPath: 'v1/users/list.json', name: 'list', method: 'GET' }],
          folders: ['v1', 'v1/users'],
        }),
      }),
    ]
    expect(tree(open, new Set(['h1:v1']))).toEqual(['collection:@0', 'dir:v1@1'])
  })

  it('the collapsed key is the handle and the path, so one collection folds alone', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({ requests: [], folders: ['users'] }),
      }),
      collectionsFixture({
        handle: 'h2',
        collection: collectionFixture({ name: 'other', requests: [], folders: ['users'] }),
      }),
    ]
    expect(tree(open, new Set(['h1:users']))).toEqual([
      'collection:@0',
      'dir:users@1',
      'collection:@0',
      'dir:users@1',
    ])
    // Folded in the first one, and the second one's `users` is untouched —
    // which is only visible in `expanded`, so read it rather than the shape.
    const rows = flattenCollections(open, new Set(['h1:users']))
    expect(rows.filter((r) => r.kind === 'dir').map((r) => r.expanded)).toEqual([false, true])
  })
})

describe('the filter narrows the folders with the rest', () => {
  const example = (): readonly ApiOpenCollection[] => [
    collectionsFixture({
      path: '/w/acme-api',
      collection: collectionFixture({
        name: 'acme-api',
        requests: [
          { relPath: 'users/create.json', name: 'create', method: 'POST' },
          { relPath: 'reports/daily.json', name: 'daily', method: 'GET' },
        ],
        folders: ['users', 'reports', 'archive'],
      }),
    }),
  ]

  it('keeps the folder a match is inside, and drops the ones holding nothing that matched', () => {
    const kept = filterCollections(example(), 'daily')
    expect(kept[0]?.collection.folders).toEqual(['reports'])
    expect(tree(kept)).toEqual([
      'collection:@0',
      'dir:reports@1',
      'request:reports/daily.json@2',
    ])
  })

  it('finds a folder by its own name, even with nothing in it to match', () => {
    // `archive` holds no request at all. A filter that could only match
    // requests would answer "nothing matches" about a folder that is there.
    const kept = filterCollections(example(), 'archive')
    expect(kept[0]?.collection.folders).toEqual(['archive'])
    expect(kept[0]?.collection.requests).toEqual([])
    expect(tree(kept)).toEqual(['collection:@0', 'dir:archive@1'])
  })

  it('keeps the parents of a folder that matched, so the row has something to hang under', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({
          name: 'acme-api',
          requests: [],
          folders: ['v1', 'v1/admin'],
        }),
      }),
    ]
    const kept = filterCollections(open, 'admin')
    expect(kept[0]?.collection.folders).toEqual(['v1', 'v1/admin'])
    expect(tree(kept)).toEqual(['collection:@0', 'dir:v1@1', 'dir:v1/admin@2'])
  })

  it('a collection that matched is kept whole, folders and all', () => {
    const kept = filterCollections(example(), 'acme')
    expect(kept[0]?.collection.folders).toEqual(['users', 'reports', 'archive'])
  })

  it('a collection with nothing left is dropped rather than shown empty', () => {
    expect(filterCollections(example(), 'nothing-of-the-sort')).toEqual([])
  })
})
