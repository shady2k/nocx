// The workbench's one list — what every part of the surface reads.
//
// The store is where "the file is the truth" (design §6.4) becomes code:
// Send writes the draft before it sends, because api.request.send takes a
// handle and a path and sends what the FILE holds. A form that could send
// something the file does not contain would be a second truth.
import { describe, expect, it, vi } from 'vitest'
import { createApiStore } from './api-store'
import type { ApiWorkbenchServices } from './api-client'
import type { ApiRequest } from './api-model'
import {
  CREATED_HANDLE,
  CREATED_NAME,
  REQUEST,
  createdFixture,
  sendFixture,
  servicesFixture,
} from './api-test-fixtures'

function storeWith(over: Partial<ApiWorkbenchServices> = {}) {
  return { store: createApiStore(servicesFixture(over)) }
}

describe('ApiStore — the collections', () => {
  it('starts empty and holds what api.collections.list answered', async () => {
    const { store } = storeWith()
    expect(store.collections()).toEqual([])
    await store.refresh()
    expect(store.collections().map((c) => c.path)).toEqual(['/w/acme-api'])
  })

  it('a folder that could not be re-read stays in the list, saying why', async () => {
    const { store } = storeWith({
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          { handle: 'h1', path: '/w/gone', collection: emptyCollection(), error: 'no such folder' },
        ],
      }),
    })
    await store.refresh()
    expect(store.collections()[0].error).toBe('no such folder')
  })

  it('opening a folder adds it to the same list the listing feeds', async () => {
    const { store } = storeWith()
    await store.openFolder('/w/acme-api')
    expect(store.collections().map((c) => c.handle)).toEqual(['h1'])
    expect(store.collections()[0].collection.requests).toHaveLength(2)
  })

  it('opening the same folder twice leaves one row', async () => {
    const { store } = storeWith()
    await store.openFolder('/w/acme-api')
    await store.openFolder('/w/acme-api')
    expect(store.collections()).toHaveLength(1)
  })

  it('a refused open leaves the list alone and reports the reason', async () => {
    const { store } = storeWith({
      openCollection: vi.fn().mockRejectedValue(new Error('not a directory')),
    })
    await store.openFolder('/w/nope')
    expect(store.collections()).toEqual([])
    expect(store.error()).toBe('not a directory')
  })

  it('creating a collection adopts what the create answered — no second open', async () => {
    const open = vi.fn()
    const create = vi.fn().mockResolvedValue(createdFixture())
    const { store } = storeWith({ createCollection: create, openCollection: open })

    await store.createCollection(CREATED_NAME)

    expect(create).toHaveBeenCalledWith(CREATED_NAME)
    // The whole point of create answering an open's shape: one thing to do
    // afterwards rather than two.
    expect(open).not.toHaveBeenCalled()
    expect(store.collections().map((c) => c.handle)).toEqual([CREATED_HANDLE])
    expect(store.collections()[0].collection.name).toBe(CREATED_NAME)
    expect(store.collections()[0].collection.requests).toEqual([])
  })

  it('the collection just created is the one the workbench is pointed at', async () => {
    const { store } = storeWith()
    await store.refresh()
    expect(store.activeCollection()).toBe('')
    await store.createCollection(CREATED_NAME)
    expect(store.activeCollection()).toBe(CREATED_HANDLE)
  })

  it('a refused name leaves the list alone and reports the reason the backend gave', async () => {
    const { store } = storeWith({
      createCollection: vi.fn().mockRejectedValue(new Error('a folder called orders-api exists')),
    })
    await store.refresh()
    const before = store.collections()

    await store.createCollection('orders-api')

    expect(store.collections()).toEqual(before)
    expect(store.error()).toBe('a folder called orders-api exists')
    expect(store.activeCollection()).toBe('')
  })

  it('closing the collection that was created stops pointing at it', async () => {
    const { store } = storeWith()
    await store.createCollection(CREATED_NAME)
    await store.closeFolder(CREATED_HANDLE)
    expect(store.activeCollection()).toBe('')
  })

  it('closing a folder removes it and releases the handle', async () => {
    const close = vi.fn().mockResolvedValue({})
    const { store } = storeWith({ closeCollection: close })
    await store.openFolder('/w/acme-api')
    await store.closeFolder('h1')
    expect(close).toHaveBeenCalledWith('h1')
    expect(store.collections()).toEqual([])
  })
})

describe('ApiStore — the request in the form', () => {
  it('opening a request puts what the file holds into the draft', async () => {
    const { store } = storeWith()
    await store.refresh()
    await store.openRequest('h1', 'users/create.json')
    expect(store.draft()?.method).toBe('POST')
    expect(store.draft()?.url).toBe('{{baseUrl}}/users')
    expect(store.selected()).toEqual({ handle: 'h1', relPath: 'users/create.json' })
  })

  it('a request that cannot be read leaves the previous draft and says why', async () => {
    const read = vi
      .fn()
      .mockResolvedValueOnce({ request: REQUEST })
      .mockRejectedValueOnce(new Error('bad JSON'))
    const { store } = storeWith({ readRequest: read })
    await store.openRequest('h1', 'users/create.json')
    await store.openRequest('h1', 'users/broken.json')
    expect(store.draft()?.url).toBe('{{baseUrl}}/users')
    expect(store.error()).toBe('bad JSON')
  })
})

describe('ApiStore — sending', () => {
  it('writes the draft before it sends, because the file is what gets sent', async () => {
    const write = vi.fn().mockResolvedValue({})
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { store } = storeWith({ writeRequest: write, sendRequest: send })
    await store.openRequest('h1', 'users/create.json')
    const edited: ApiRequest = { ...REQUEST, url: 'https://example.test/users' }
    store.editDraft(edited)
    await store.send()
    expect(write).toHaveBeenCalledWith('h1', 'users/create.json', edited)
    expect(send).toHaveBeenCalledWith('h1', 'users/create.json')
  })

  it('does not write a draft nobody edited', async () => {
    const write = vi.fn().mockResolvedValue({})
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { store } = storeWith({ writeRequest: write, sendRequest: send })
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    expect(write).not.toHaveBeenCalled()
    expect(send).toHaveBeenCalled()
  })

  it('a run carries the status, the elapsed time and the size it came back with', async () => {
    const { store } = storeWith()
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    const run = store.runs()[0]
    expect(run.response?.status).toBe(201)
    expect(run.response?.timings.totalMs).toBe(184)
    expect(run.response?.size).toBe(1229)
  })

  it('a second send adds a second run rather than replacing the first', async () => {
    const send = vi
      .fn()
      .mockResolvedValueOnce(sendFixture({ status: 201 }))
      .mockResolvedValueOnce(sendFixture({ status: 422 }))
    const { store } = storeWith({ sendRequest: send })
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    await store.send()
    expect(store.runs().map((r) => r.response?.status)).toEqual([422, 201])
  })

  it('a send that fails becomes a run that says so, not a run that vanishes', async () => {
    const { store } = storeWith({
      sendRequest: vi.fn().mockRejectedValue(new Error('dial tcp: connection refused')),
    })
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    expect(store.runs()).toHaveLength(1)
    expect(store.runs()[0].error).toBe('dial tcp: connection refused')
    expect(store.runs()[0].response).toBeNull()
  })

  it('a write that fails stops the send — the file never held what would go out', async () => {
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { store } = storeWith({
      writeRequest: vi.fn().mockRejectedValue(new Error('read-only file system')),
      sendRequest: send,
    })
    await store.openRequest('h1', 'users/create.json')
    store.editDraft({ ...REQUEST, url: 'https://example.test/x' })
    await store.send()
    expect(send).not.toHaveBeenCalled()
    expect(store.error()).toBe('read-only file system')
    expect(store.runs()).toEqual([])
  })

  it('refuses to send with nothing selected', async () => {
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { store } = storeWith({ sendRequest: send })
    await store.send()
    expect(send).not.toHaveBeenCalled()
  })

  it('a run is shown pretty until the reader asks for raw, and the choice is per run', async () => {
    const { store } = storeWith()
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    await store.send()
    const [newest, oldest] = store.runs()
    expect(newest.view).toBe('pretty')
    store.setRunView(oldest.id, 'raw')
    expect(store.runs()[1].view).toBe('raw')
    expect(store.runs()[0].view).toBe('pretty')
  })
})

describe('ApiStore — import', () => {
  it('a curl line becomes the draft, and what it could not carry is kept', async () => {
    const { store } = storeWith({
      importCurl: vi.fn().mockResolvedValue({
        request: { ...REQUEST, url: 'https://example.test/users' },
        unsupported: [{ what: '--oauth2-bearer', why: 'OAuth2 is out of scope' }],
      }),
    })
    await store.importCurl('curl --oauth2-bearer X https://example.test/users')
    expect(store.draft()?.url).toBe('https://example.test/users')
    expect(store.notes()).toEqual([{ what: '--oauth2-bearer', why: 'OAuth2 is out of scope' }])
  })

  it('an imported draft is not attached to any file, so Send has nothing to send yet', async () => {
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { store } = storeWith({
      importCurl: vi.fn().mockResolvedValue({ request: REQUEST, unsupported: [] }),
      sendRequest: send,
    })
    await store.importCurl('curl https://example.test/users')
    expect(store.selected()).toBeNull()
    await store.send()
    expect(send).not.toHaveBeenCalled()
  })

  it('a Postman import re-reads the collections and keeps what it dropped', async () => {
    const list = vi.fn().mockResolvedValue({ collections: [] })
    const { store } = storeWith({
      importPostman: vi.fn().mockResolvedValue({
        unsupported: [{ what: 'pre-request script', why: 'no scripting sandbox' }],
      }),
      listCollections: list,
    })
    await store.importPostman('/w/acme.json', '/w/acme-api')
    expect(list).toHaveBeenCalled()
    expect(store.notes().map((n) => n.what)).toEqual(['pre-request script'])
  })

  it('a refused import leaves the draft alone and reports the reason', async () => {
    const { store } = storeWith({
      importCurl: vi.fn().mockRejectedValue(new Error('not a curl command line')),
    })
    await store.importCurl('rm -rf /')
    expect(store.draft()).toBeNull()
    expect(store.error()).toBe('not a curl command line')
  })
})

function emptyCollection() {
  return { name: 'gone', requests: [], malformed: [] }
}
