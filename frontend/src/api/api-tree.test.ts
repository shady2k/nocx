// The tree's shape — the one place that turns a collection's listing into rows.
//
// It is a unit test rather than only a workbench one because the question it
// answers is not reachable from the surface in every state that matters: a
// filter narrowing a folder list, a collapsed parent above a folder that
// matched, a folder the backend lists that holds nothing. What a person can
// do with a folder is asserted in api-workbench.test.tsx, where the control
// is clicked; what a folder IS lives here.
import { describe, expect, it } from 'vitest'
import {
  contentsOf,
  directoryOf,
  filterCollections,
  flattenCollections,
  type ApiTreeRow,
} from './api-tree'
import type { ApiOpenCollection } from './api-model'
import { collectionFixture, collectionsFixture } from './api-test-fixtures'

const NOTHING = new Set<string>()

function tree(
  open: readonly ApiOpenCollection[],
  collapsed: ReadonlySet<string> = NOTHING,
  narrowed = false,
) {
  return flattenCollections(open, collapsed, narrowed).map(
    (r: ApiTreeRow) => `${r.kind}:${r.relPath}@${r.depth}`,
  )
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
    // And it SAYS it is empty. Without the second row the folder is an open
    // disclosure with its own siblings under it, which reads as those
    // siblings being what is in it.
    expect(tree(open)).toEqual(['collection:@0', 'dir:reports@1', 'empty:reports@2'])
  })

  it('an empty folder says so at the depth its contents would have had', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({
          requests: [{ relPath: 'ping.json', name: 'ping', method: 'GET' }],
          folders: ['reports'],
        }),
      }),
    ]
    // The row a person is misled by is `request:ping.json@1` — a SIBLING of
    // the folder, drawn at its depth. The empty row stands between them at
    // depth 2, so what is inside and what is beside cannot be confused.
    expect(tree(open)).toEqual([
      'collection:@0',
      'dir:reports@1',
      'empty:reports@2',
      'request:ping.json@1',
    ])
  })

  it('a collapsed empty folder says nothing — there is nothing open to be inside', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({ requests: [], folders: ['reports'] }),
      }),
    ]
    expect(tree(open, new Set(['h1:reports']))).toEqual(['collection:@0', 'dir:reports@1'])
  })

  it('an open collection with nothing in it says so too, by the same rule', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({ requests: [], folders: [] }),
      }),
    ]
    expect(tree(open)).toEqual(['collection:@0', 'empty:@1'])
  })

  it('a collection holding only malformed files is not empty — they are listed under it', () => {
    const open = [
      collectionsFixture({
        collection: collectionFixture({
          requests: [],
          folders: [],
          malformed: [{ relPath: 'broken.json', reason: 'not JSON' }],
        }),
      }),
    ]
    expect(tree(open)).toEqual(['collection:@0', 'malformed:broken.json@1'])
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
      // The second one is open, and open and empty is the state that says so.
      'empty:users@2',
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
    expect(tree(kept, NOTHING, true)).toEqual([
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
    // NARROWED, so `archive` does not claim to be empty: it holds nothing
    // that matched, which is a different sentence from holding nothing.
    expect(tree(kept, NOTHING, true)).toEqual(['collection:@0', 'dir:archive@1'])
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
    expect(tree(kept, NOTHING, true)).toEqual(['collection:@0', 'dir:v1@1', 'dir:v1/admin@2'])
  })

  it('a collection that matched is kept whole, folders and all', () => {
    const kept = filterCollections(example(), 'acme')
    expect(kept[0]?.collection.folders).toEqual(['users', 'reports', 'archive'])
  })

  it('a collection with nothing left is dropped rather than shown empty', () => {
    expect(filterCollections(example(), 'nothing-of-the-sort')).toEqual([])
  })
})

describe('directoryOf — one owner of where a path lives (AD-8)', () => {
  it("'' at the collection's root", () => {
    expect(directoryOf('ping.json')).toBe('')
  })

  it('one folder deep', () => {
    expect(directoryOf('users/create.json')).toBe('users')
  })

  it('several folders deep, parents included', () => {
    expect(directoryOf('v1/admin/create.json')).toBe('v1/admin')
  })
})

describe('contentsOf — what is DIRECTLY inside a folder', () => {
  // The folder page reads one entry of the index the tree walks whole, so a
  // folder cannot say one thing in the column and another in the page.
  const example = () =>
    collectionFixture({
      requests: [
        { relPath: 'ping.json', name: 'ping', method: 'GET' },
        { relPath: 'users/create.json', name: 'create', method: 'POST' },
        { relPath: 'users/admin/grant.json', name: 'grant', method: 'POST' },
      ],
      folders: ['users', 'users/admin'],
    })

  it("the collection's root holds what is beside it, not what is under it", () => {
    const at = contentsOf(example(), '')
    expect(at.folders).toEqual(['users'])
    expect(at.requests.map((r) => r.relPath)).toEqual(['ping.json'])
  })

  it('a folder holds its own subfolders and the requests beside them', () => {
    const at = contentsOf(example(), 'users')
    expect(at.folders).toEqual(['users/admin'])
    expect(at.requests.map((r) => r.relPath)).toEqual(['users/create.json'])
  })

  it('a folder with nothing in it answers with nothing, not with its parent', () => {
    const at = contentsOf(collectionFixture({ requests: [], folders: ['reports'] }), 'reports')
    expect(at.folders).toEqual([])
    expect(at.requests).toEqual([])
  })
})
