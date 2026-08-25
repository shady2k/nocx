// The workbench's one list — what every part of the surface reads.
//
// The store is where "the file is the truth" (design §6.4) becomes code:
// Send writes the draft before it sends, because api.request.send takes a
// handle and a path and sends what the FILE holds. A form that could send
// something the file does not contain would be a second truth.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiStore, type ApiStoreOptions } from './api-store'
import type { ApiWorkbenchServices } from './api-client'
import type { ApiRequestScopeResult } from '../generated/api.request.scope'
import type { ApiEnvironmentRef, ApiRequest } from './api-model'
import {
  COLLECTION_PATH,
  CREATE_REL_PATH,
  CREATED_HANDLE,
  CREATED_NAME,
  DEV_ENV,
  HANDLE,
  PROD_ENV,
  REQUEST,
  WATCH_BINDING,
  WATCH_SESSION,
  collectionFixture,
  collectionsFixture,
  DEFAULT_ROOT,
  createdFixture,
  folderCreatedFixture,
  folderOnDisk,
  sendFixture,
  failedSendFixture,
  stoppedSendFixture,
  servicesFixture,
  watchFixture,
  type WatchFixture,
} from './api-test-fixtures'

// ── The ask that stands between an import and somebody's unsaved work ─────
//
// The store asks the kit for confirmation, the way every other "are you
// sure" in this product does (panes.ts, notes-panel.tsx). The real helper
// mounts a `<dialog>` on `document.body`, and these tests run under node —
// so the helper is mocked and the assertions are on what it was ASKED, which
// is the half that matters: a question naming nothing is a question about
// nothing.
const showConfirmMock = vi.fn<(...args: unknown[]) => Promise<boolean>>()
vi.mock('../ui/dialog', () => ({
  showConfirm: (...args: unknown[]) => showConfirmMock(...args),
}))

function storeWith(over: Partial<ApiWorkbenchServices> = {}, options: ApiStoreOptions = {}) {
  return { store: createApiStore(servicesFixture(over), options) }
}

/** A store that is watching, as the pane's mount leaves it: subscribed, with
 *  the first listing in and its watch set published. */
async function watchingStore(
  over: Partial<ApiWorkbenchServices> = {},
  fixture: WatchFixture = watchFixture(),
) {
  const store = createApiStore(servicesFixture({ watchCollections: fixture.port, ...over }))
  store.startWatching()
  await store.refresh()
  return { store, watch: fixture }
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
        defaultRoot: DEFAULT_ROOT,
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
    // A listing points at what it listed — a pane mounted onto folders from
    // an earlier session is pointed at one of them rather than at nothing.
    expect(store.activeCollection()).toBe(HANDLE)
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
    // The pointer did not move: a create that was refused made no collection
    // for it to move to.
    expect(store.activeCollection()).toBe(HANDLE)
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

  it('keeps the newest scope answer when an older refresh resolves later', async () => {
    const pending: Array<(value: ApiRequestScopeResult) => void> = []
    const requestScope = vi.fn().mockImplementation(
      () =>
        new Promise<ApiRequestScopeResult>((resolve) => {
          pending.push(resolve)
        }),
    )
    const { store } = storeWith({ requestScope })

    const opening = store.openRequest('h1', 'users/create.json')
    await vi.waitFor(() => expect(requestScope).toHaveBeenCalledTimes(1))
    store.editDraft({ ...REQUEST, variables: [{ name: 'id', value: 'draft', enabled: true }] })
    await vi.waitFor(() => expect(requestScope).toHaveBeenCalledTimes(2))

    const newest: ApiRequestScopeResult = {
      variables: [
        {
          name: 'id',
          value: 'draft',
          scope: 'request',
          from: '',
          overridden: false,
          refused: '',
        },
      ],
    }
    const older: ApiRequestScopeResult = {
      variables: [
        {
          name: 'id',
          value: 'folder',
          scope: 'folder',
          from: 'users',
          overridden: false,
          refused: '',
        },
      ],
    }
    pending[1](newest)
    await Promise.resolve()
    pending[0](older)
    await opening

    expect(store.scopeVariables()).toEqual(newest.variables)
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
    // The third argument is the environment, and '' is the right one here:
    // the fixture's collection declares none, so the request goes out
    // exactly as its file has it (§6.2).
    // The third argument is the environment; the fourth is the name this
    // surface gave the exchange, which is what Stop names later.
    expect(send).toHaveBeenCalledWith('h1', 'users/create.json', '', expect.any(String))
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
    expect(run.outcome).toBe('answered')
    expect(run.response?.status).toBe(201)
    // The time and the address are the ATTEMPT's, not the answer's — a run
    // that failed took time and reached a machine too.
    expect(run.timings?.totalMs).toBe(184)
    expect(run.remoteAddr).toBe('10.0.3.17:443')
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

  it('a failed exchange is a run with the request, the route and the phase on it', async () => {
    const { store } = storeWith({ sendRequest: vi.fn().mockResolvedValue(failedSendFixture()) })
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    const run = store.runs()[0]
    expect(run.outcome).toBe('failed')
    expect(run.response).toBeNull()
    expect(run.failure?.phase).toBe('dial')
    expect(run.failure?.reason).toBe('connection refused')
    // THE POINT OF THE WHOLE CHANGE: what was sent survives the failure.
    expect(run.request?.text).toContain('POST /users HTTP/1.1')
    expect(run.timings?.totalMs).toBe(3)
    // …and it is not a refusal, which is the other thing a run can be.
    expect(run.error).toBe('')
  })

  it('a stop comes back as its own outcome, never as a failure', async () => {
    const { store } = storeWith({ sendRequest: vi.fn().mockResolvedValue(stoppedSendFixture()) })
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    const run = store.runs()[0]
    expect(run.outcome).toBe('stopped')
    expect(run.failure?.phase).toBe('stopped')
    expect(run.request?.text).toContain('POST /users HTTP/1.1')
  })

  it('an ask the method REFUSED is a run that says so, and claims no attempt', async () => {
    // What still arrives as a JSON-RPC error is what never became an
    // exchange: an unknown handle, an auth variable nothing can resolve.
    const { store } = storeWith({
      sendRequest: vi.fn().mockRejectedValue(new Error('unknown collection handle')),
    })
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    const run = store.runs()[0]
    expect(store.runs()).toHaveLength(1)
    expect(run.outcome).toBe('refused')
    expect(run.error).toBe('unknown collection handle')
    expect(run.response).toBeNull()
    // No phase and no request text: nothing was attempted, and a run
    // borrowing either would be claiming an attempt that never happened.
    expect(run.failure).toBeNull()
    expect(run.request).toBeNull()
  })

  it('the row exists before the answer does, and the SAME row carries it', async () => {
    // The defect this replaces in one test: the run was built from the
    // result, so nothing existed while the request was in flight.
    let answer: (result: unknown) => void = () => {}
    const send = vi.fn().mockReturnValue(
      new Promise((resolve) => {
        answer = resolve
      }),
    )
    const { store } = storeWith({ sendRequest: send })
    await store.openRequest('h1', 'users/create.json')
    const sending = store.send()

    // Before any answer exists.
    expect(store.runs()).toHaveLength(1)
    const pendingRun = store.runs()[0]
    expect(pendingRun.outcome).toBe('pending')
    expect(pendingRun.method).toBe('POST')
    expect(pendingRun.url).toBe('{{baseUrl}}/users')
    expect(store.pending()?.id).toBe(pendingRun.id)

    answer(sendFixture())
    await sending

    // The SAME row, never a second one.
    expect(store.runs()).toHaveLength(1)
    expect(store.runs()[0].id).toBe(pendingRun.id)
    expect(store.runs()[0].outcome).toBe('answered')
    expect(store.pending()).toBeNull()
  })

  it('stop names the run by the token it was sent under', async () => {
    let answer: (result: unknown) => void = () => {}
    const send = vi.fn().mockReturnValue(
      new Promise((resolve) => {
        answer = resolve
      }),
    )
    const cancel = vi.fn().mockResolvedValue({})
    const { store } = storeWith({ sendRequest: send, cancelRequest: cancel })
    await store.openRequest('h1', 'users/create.json')
    const sending = store.send()

    const token = store.pending()?.token
    expect(token).toBeTruthy()
    await store.stop()
    expect(cancel).toHaveBeenCalledWith(token)

    // The store does NOT settle the row itself: the send's own result is
    // what ends a run, whichever way it ended.
    expect(store.runs()[0].outcome).toBe('pending')
    answer(stoppedSendFixture())
    await sending
    expect(store.runs()[0].outcome).toBe('stopped')
  })

  it('stop with nothing in flight asks the backend nothing', async () => {
    const cancel = vi.fn().mockResolvedValue({})
    const { store } = storeWith({ cancelRequest: cancel })
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    await store.stop()
    expect(cancel).not.toHaveBeenCalled()
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
    // NO ROW. Nothing went out, so there is no exchange to record — a row
    // here would say a request was attempted when none was.
    expect(store.runs()).toEqual([])
  })

  it('refuses to send with nothing selected', async () => {
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { store } = storeWith({ sendRequest: send })
    await store.send()
    expect(send).not.toHaveBeenCalled()
  })

  it('a run opens on its body, and which part is read is per run', async () => {
    // Three parts now, not two: the headers were stacked above the body in
    // one pane, where a long body pushed them off screen and a long header
    // list pushed the body off.
    const { store } = storeWith()
    await store.openRequest('h1', 'users/create.json')
    await store.send()
    await store.send()
    const [newest, oldest] = store.runs()
    expect(newest.view).toBe('body')
    store.setRunView(oldest.id, 'raw')
    expect(store.runs()[1].view).toBe('raw')
    expect(store.runs()[0].view).toBe('body')
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
    await store.importPostman({ path: '/w/acme.json' }, '/w/acme-api')
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

// ── A curl import does not destroy unsaved work (nocx-86wvw) ──────────────
//
// The conversion is a VALUE and not a file (design §10): it fills the form,
// and nothing is written until the request is saved. "Fills the form" was
// read as "fills THIS form" — and the open request is the one thing in the
// workbench a person has unsaved work in, so an import over it took the
// edits with it and asked nothing.
//
// The answer is the kit's `showConfirm`, NAMING what goes, and it is asked
// only when there is something to lose — which, since the draft reaches its
// file on its own, is exactly one state: a draft with nowhere to be written,
// which is a curl line converted with no collection open. Everything else
// the ask used to cover is now SAVED rather than asked about, and that is
// the stronger answer to the defect that bought it.
describe('ApiStore — a curl import over unsaved work', () => {
  /** A store with a request open and one field typed into, which is the
   *  state the defect was reported from. */
  async function withEditedRequest() {
    const { store } = storeWith({
      importCurl: vi.fn().mockResolvedValue({
        request: { ...REQUEST, name: 'ping', url: 'https://h/v1/ping' },
        unsupported: [],
      }),
    })
    await store.openRequest('h1', 'users/create.json')
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://example.test/edited' })
    return store
  }

  beforeEach(() => {
    showConfirmMock.mockReset()
    showConfirmMock.mockResolvedValue(true)
  })

  it('does not ask about edits it is about to SAVE — it writes them and imports', async () => {
    // The ask was bought by an import that took somebody's unsaved edits
    // (nocx-86wvw). Nothing takes them now: the draft reaches its file on
    // its own, and this path flushes before it asks anything, so the state
    // the question was about no longer occurs. The criterion it was written
    // for still holds — the edits are not destroyed — by the other answer.
    const write = vi.fn().mockResolvedValue({})
    const { store } = storeWith({
      writeRequest: write,
      importCurl: vi.fn().mockResolvedValue({
        request: { ...REQUEST, name: 'ping', url: 'https://h/v1/ping' },
        unsupported: [],
      }),
    })
    await store.openRequest('h1', 'users/create.json')
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://example.test/edited' })

    await store.importCurl('curl https://h/v1/ping')

    expect(showConfirmMock).not.toHaveBeenCalled()
    // The edit is in the file it was an edit to, written before the form
    // stopped being about it.
    expect(write).toHaveBeenCalledWith(
      'h1',
      'users/create.json',
      expect.objectContaining({ url: 'https://example.test/edited' }),
    )
    expect(store.draft()?.url).toBe('https://h/v1/ping')
  })

  it('asks nothing when the form is empty — nothing was there to lose', async () => {
    const { store } = storeWith({
      importCurl: vi.fn().mockResolvedValue({ request: REQUEST, unsupported: [] }),
    })
    await store.importCurl('curl https://example.test/users')
    expect(showConfirmMock).not.toHaveBeenCalled()
    expect(store.draft()?.url).toBe('{{baseUrl}}/users')
  })

  it('asks nothing when the open request is exactly what its file holds', async () => {
    const { store } = storeWith({
      importCurl: vi
        .fn()
        .mockResolvedValue({ request: { ...REQUEST, name: 'ping' }, unsupported: [] }),
    })
    await store.openRequest('h1', 'users/create.json')

    await store.importCurl('curl https://h/v1/ping')

    expect(showConfirmMock).not.toHaveBeenCalled()
    expect(store.draft()?.name).toBe('ping')
  })

  it('asks before replacing an imported draft, which has no file to have been saved into', async () => {
    // The second import is the case `dirty` cannot see: there is no saved
    // snapshot to differ from, so everything in the form is unsaved.
    const { store } = storeWith({
      importCurl: vi
        .fn()
        .mockResolvedValueOnce({ request: { ...REQUEST, name: 'first' }, unsupported: [] })
        .mockResolvedValueOnce({ request: { ...REQUEST, name: 'second' }, unsupported: [] }),
    })
    await store.importCurl('curl https://h/first')
    expect(showConfirmMock).not.toHaveBeenCalled()

    showConfirmMock.mockResolvedValue(false)
    await store.importCurl('curl https://h/second')

    expect(showConfirmMock).toHaveBeenCalledTimes(1)
    expect(showConfirmMock.mock.calls[0][0]).toContain('first')
    expect(store.draft()?.name).toBe('first')
  })

  it('a line that is not a curl command is refused without asking anybody to discard anything', async () => {
    const { store } = storeWith({
      importCurl: vi.fn().mockRejectedValue(new Error('not a curl command line')),
    })
    await store.openRequest('h1', 'users/create.json')
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://example.test/edited' })

    await store.importCurl('rm -rf /')

    expect(showConfirmMock).not.toHaveBeenCalled()
    expect(store.error()).toBe('not a curl command line')
    expect(store.draft()?.url).toBe('https://example.test/edited')
  })

  it('the notes of an import nobody accepted are not shown', async () => {
    const store = await withEditedRequest()
    showConfirmMock.mockResolvedValue(false)

    await store.importCurl('curl https://h/v1/ping')

    expect(store.notes()).toEqual([])
  })
})

// ── Watching, so nothing has to be pressed (nocx-19rcp) ───────────────────
//
// A collection is a FOLDER ON DISK and it changes underneath us — a git pull,
// a neighbouring editor, a colleague's branch. The product already answers
// "how does a surface learn a folder changed": files.watch, which the Files
// panel uses. Every assertion below is on the SET handed to it, because the
// call REPLACES the set rather than adding to it — a count cannot tell a
// removal from an addition, and it is the removal that leaks a watch.

describe('ApiStore — watching the open collection roots', () => {
  it('publishes the roots it renders, once the first listing has said what they are', async () => {
    const { watch } = await watchingStore()
    expect(watch.open).toHaveBeenCalledWith(WATCH_SESSION, '/')
    expect(watch.sets()).toEqual([[COLLECTION_PATH]])
  })

  it('a folder opened afterwards joins the set, and the set is sent whole', async () => {
    const { store, watch } = await watchingStore({
      openCollection: vi
        .fn()
        .mockResolvedValue({ handle: 'h2', collection: collectionsFixture().collection }),
    })
    await store.openFolder('/w/orders-api')
    expect(watch.lastSet()).toEqual([COLLECTION_PATH, '/w/orders-api'])
  })

  it('closing a collection removes ITS path from the set, and leaves the others in it', async () => {
    const list = vi.fn().mockResolvedValue({
      collections: [
        collectionsFixture(),
        collectionsFixture({ handle: 'h2', path: '/w/orders-api' }),
      ],
    })
    const { store, watch } = await watchingStore({ listCollections: list })
    expect(watch.lastSet()).toEqual([COLLECTION_PATH, '/w/orders-api'])
    await store.closeFolder('h2')
    expect(watch.lastSet()).toEqual([COLLECTION_PATH])
  })

  it('closing the last collection sends the empty set — a watch nobody holds is a leak', async () => {
    const { store, watch } = await watchingStore()
    await store.closeFolder(HANDLE)
    expect(watch.lastSet()).toEqual([])
  })

  it('a set that has not changed is not sent twice', async () => {
    const { store, watch } = await watchingStore()
    const sent = watch.sets().length
    await store.openRequest(HANDLE, 'users/create.json')
    expect(watch.sets()).toHaveLength(sent)
  })

  it('re-lists when the backend says a watched folder is dirty, with nothing pressed', async () => {
    const list = vi.fn().mockResolvedValue({ collections: [collectionsFixture()] })
    const { watch } = await watchingStore({ listCollections: list })
    expect(list).toHaveBeenCalledTimes(1)
    watch.changed(COLLECTION_PATH)
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2))
  })

  it('re-lists for a file INSIDE a watched root, which is where a request file lands', async () => {
    const list = vi.fn().mockResolvedValue({ collections: [collectionsFixture()] })
    const { watch } = await watchingStore({ listCollections: list })
    watch.changed(`${COLLECTION_PATH}/users`)
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2))
  })

  it('ignores a change for a folder it does not hold, and one for another binding', async () => {
    const list = vi.fn().mockResolvedValue({ collections: [collectionsFixture()] })
    const { watch } = await watchingStore({ listCollections: list })
    watch.changed('/w/somebody-elses-tree')
    watch.changed(COLLECTION_PATH, 'another-binding')
    await Promise.resolve()
    await Promise.resolve()
    expect(list).toHaveBeenCalledTimes(1)
  })
})

describe('ApiStore — when the watch does not come up', () => {
  it('a refused files.watch is on the surface, and the collections stay listed', async () => {
    const { store } = await watchingStore(
      {},
      watchFixture({ watch: vi.fn().mockRejectedValue(new Error('watch limit reached')) }),
    )
    expect(store.watchFailed()).toBe('watch limit reached')
    expect(store.collections().map((c) => c.path)).toEqual([COLLECTION_PATH])
  })

  it('a watch that fails while a NEW folder joins leaves the folders already watched watched', async () => {
    // The contract's own rule, from the client's side: "a newly-added watch
    // that fails to establish must not take the healthy existing watches down
    // with it". The observable is not a flag — it is that a change on the
    // first folder still re-lists it after the second folder's watch was
    // refused.
    const list = vi.fn().mockResolvedValue({ collections: [collectionsFixture()] })
    const watch = vi.fn().mockResolvedValueOnce({ mode: 'polling' })
    watch.mockRejectedValueOnce(new Error('watch limit reached'))
    const fixture = watchFixture({ watch })
    const { store } = await watchingStore(
      {
        listCollections: list,
        openCollection: vi
          .fn()
          .mockResolvedValue({ handle: 'h2', collection: collectionsFixture().collection }),
      },
      fixture,
    )
    await store.openFolder('/w/orders-api')
    expect(store.watchFailed()).toBe('watch limit reached')
    fixture.changed(COLLECTION_PATH)
    await vi.waitFor(() => expect(list).toHaveBeenCalledTimes(2))
  })

  it('refresh is the retry: it re-sends an unchanged set and clears the failure', async () => {
    const watch = vi.fn().mockRejectedValueOnce(new Error('watch limit reached'))
    watch.mockResolvedValue({ mode: 'watching' })
    const { store, watch: fixture } = await watchingStore({}, watchFixture({ watch }))
    expect(store.watchFailed()).toBe('watch limit reached')
    await store.refresh()
    expect(fixture.sets()).toEqual([[COLLECTION_PATH], [COLLECTION_PATH]])
    expect(store.watchFailed()).toBe('')
    expect(store.watchMode()).toBe('watching')
  })

  it('a refused files.open says so and never pretends to watch', async () => {
    const fixture = watchFixture({
      open: vi.fn().mockRejectedValue(new Error('files not available')),
    })
    const { store } = await watchingStore({}, fixture)
    expect(store.watchFailed()).toBe('files not available')
    expect(fixture.watch).not.toHaveBeenCalled()
    expect(store.watchMode()).toBeNull()
    expect(store.collections().map((c) => c.path)).toEqual([COLLECTION_PATH])
  })

  it('with no local session there is nothing to open a binding against, and nothing breaks', async () => {
    const fixture = watchFixture({ localSession: null })
    const { store } = await watchingStore({}, fixture)
    expect(fixture.open).not.toHaveBeenCalled()
    expect(store.watchMode()).toBeNull()
    expect(store.collections().map((c) => c.path)).toEqual([COLLECTION_PATH])
  })

  it('with no watch capability at all the store still lists — the button is the whole answer', async () => {
    const store = createApiStore(servicesFixture())
    store.startWatching()
    await store.refresh()
    expect(store.collections().map((c) => c.path)).toEqual([COLLECTION_PATH])
    expect(store.watchMode()).toBeNull()
    expect(store.watchFailed()).toBe('')
  })
})

describe('ApiStore — the refresh mode the backend reports', () => {
  it('carries a degraded reason through, and drops it the instant watching recovers', async () => {
    const watch = vi
      .fn()
      .mockResolvedValueOnce({ mode: 'polling', degradedReason: 'inotify watch limit reached' })
    watch.mockResolvedValue({ mode: 'watching' })
    const { store } = await watchingStore({}, watchFixture({ watch }))
    expect(store.watchMode()).toBe('polling')
    expect(store.watchDegradedReason()).toBe('inotify watch limit reached')
    await store.refresh()
    expect(store.watchMode()).toBe('watching')
    expect(store.watchDegradedReason()).toBeNull()
  })

  it('designed-mode polling carries no reason, so there is nothing to warn about', async () => {
    const { store } = await watchingStore()
    expect(store.watchMode()).toBe('polling')
    expect(store.watchDegradedReason()).toBeNull()
  })
})

describe('ApiStore — the watch lifecycle, both ends', () => {
  it('a reconnect re-opens the binding the dead connection minted and re-sends the set', async () => {
    const { watch } = await watchingStore()
    expect(watch.open).toHaveBeenCalledTimes(1)
    watch.reconnect()
    await vi.waitFor(() => expect(watch.open).toHaveBeenCalledTimes(2))
    await vi.waitFor(() => expect(watch.sets()).toHaveLength(2))
    expect(watch.lastSet()).toEqual([COLLECTION_PATH])
  })

  it('dispose releases the binding and stops listening — a change after it changes nothing', async () => {
    const list = vi.fn().mockResolvedValue({ collections: [collectionsFixture()] })
    const { store, watch } = await watchingStore({ listCollections: list })
    store.dispose()
    expect(watch.close).toHaveBeenCalledWith(WATCH_BINDING)
    watch.changed(COLLECTION_PATH)
    await Promise.resolve()
    await Promise.resolve()
    expect(list).toHaveBeenCalledTimes(1)
  })
})

// ── Which environment a send goes out under (nocx-pnvnn) ──────────────────
//
// The workbench tests drive the picker a person reaches. These are the two
// properties that outlive one gesture and cannot be seen from a single
// click: a choice must survive the panel re-listing the folder, and it
// belongs to ONE collection.

describe('ApiStore — the environment', () => {
  /** A backend whose one collection declares the given environments. */
  function withEnvironments(envs: ApiEnvironmentRef[]) {
    return {
      listCollections: vi.fn().mockResolvedValue({
        collections: [
          collectionsFixture({ collection: collectionFixture({ environments: envs }) }),
        ],
        defaultRoot: DEFAULT_ROOT,
      }),
    }
  }

  it("a person's choice of NONE survives the next listing", async () => {
    // The interval, both ends. It opens the moment setEnvironment is called
    // and closes when the collection is closed — not when the panel re-reads
    // the folder, which it does on every change on disk. A default that
    // re-applied itself on a refresh would silently put an environment back
    // under a person who deliberately took it off, and they would find out
    // by watching a request reach it.
    const send = vi.fn().mockResolvedValue(sendFixture())
    const { store } = storeWith({ ...withEnvironments([DEV_ENV]), sendRequest: send })
    await store.refresh()
    expect(store.activeEnvironment()).toBe(DEV_ENV.relPath)

    store.setEnvironment('')
    await store.refresh()
    expect(store.activeEnvironment()).toBe('')

    await store.openRequest(HANDLE, 'users/create.json')
    await store.send()
    expect(send).toHaveBeenCalledWith(HANDLE, 'users/create.json', '', expect.any(String))
  })

  it('several environments choose nothing until somebody does', async () => {
    const { store } = storeWith(withEnvironments([DEV_ENV, PROD_ENV]))
    await store.refresh()
    expect(store.activeEnvironment()).toBe('')
    expect(store.environments().map((e) => e.name)).toEqual([DEV_ENV.name, PROD_ENV.name])

    store.setEnvironment(PROD_ENV.relPath)
    expect(store.activeEnvironment()).toBe(PROD_ENV.relPath)
  })

  it('the choice belongs to one collection, and leaves with it', async () => {
    const { store } = storeWith(withEnvironments([DEV_ENV, PROD_ENV]))
    await store.refresh()
    store.setEnvironment(PROD_ENV.relPath)

    // A second folder, freshly made: it has no environments of its own, and
    // it must not inherit a path that names a file inside somebody else's
    // collection — the backend would refuse it, and the panel would have
    // shown an environment that was never there.
    await store.createCollection(CREATED_NAME)
    expect(store.activeCollection()).toBe(CREATED_HANDLE)
    expect(store.environments()).toEqual([])
    expect(store.activeEnvironment()).toBe('')

    // Closing the first folder forgets its choice: nothing can address that
    // handle again, so a remembered row could only be a map that grows.
    await store.closeFolder(HANDLE)
    expect(store.collections().map((c) => c.handle)).toEqual([CREATED_HANDLE])
  })

  it('a run says which environment ANSWERED, not which one was asked for', async () => {
    // The fixture's name is deliberately not its file's stem, so a store
    // that recorded the path it sent — or derived a name from it — fails
    // here. Only the result can say which record answered.
    const send = vi.fn().mockResolvedValue(sendFixture({}, DEV_ENV.name))
    const { store } = storeWith({ ...withEnvironments([DEV_ENV]), sendRequest: send })
    await store.refresh()
    await store.openRequest(HANDLE, 'users/create.json')
    await store.send()
    expect(store.runs()[0].environment).toBe(DEV_ENV.name)
  })

  it('a send that failed records no environment at all', async () => {
    const send = vi.fn().mockRejectedValue(new Error('connection refused'))
    const { store } = storeWith({ ...withEnvironments([DEV_ENV]), sendRequest: send })
    await store.refresh()
    await store.openRequest(HANDLE, 'users/create.json')
    await store.send()
    // The run is there and says why (rule 2), and it does NOT claim an
    // environment: nothing came back, so nothing confirmed one.
    expect(store.runs()[0].error).toContain('connection refused')
    expect(store.runs()[0].environment).toBe('')
  })
})

function emptyCollection() {
  return { name: 'gone', requests: [], malformed: [], environments: [] }
}

// ── A request names itself, and the files stay apart (nocx-lpo2m) ─────────
//
// The offer is the store's because the DRAFT is: it is the one place that
// knows what the name was a moment ago and who changed it, which is the
// whole of "an offer, not a derivation" — the moment a person names the
// request themselves the offer stops for good.

// ── A collection can be given a folder (nocx-8v1fu) ───────────────────────
//
// The half of §6.2 the product could not reach: a collection is a folder and
// it may contain folders, the Postman importer writes them, and a collection
// built inside nocx had none. The store's part is one call and what it does
// with the answer.

describe('ApiStore — making a folder', () => {
  it('names the folder and the EXISTING folder to put it in, never a path', async () => {
    const createFolder = vi.fn().mockResolvedValue(folderCreatedFixture('users/admin'))
    const { store } = storeWith({ createFolder })
    await store.refresh()

    await store.createFolder(HANDLE, 'users', 'admin')

    expect(createFolder).toHaveBeenCalledWith(HANDLE, 'users', 'admin')
  })

  it('draws the tree from the collection the call ANSWERED, without a second listing', async () => {
    // The whole reason the result carries a collection: the caller's next
    // move is the tree, and a listing fetched afterwards would be a second
    // account of one folder taken at a second moment. So the listing spy
    // must not be called again — and the folder must be there anyway.
    const list = vi
      .fn()
      .mockResolvedValue({ collections: [collectionsFixture()], defaultRoot: DEFAULT_ROOT })
    const { store } = storeWith({ listCollections: list })
    await store.refresh()
    expect(list).toHaveBeenCalledTimes(1)

    await store.createFolder(HANDLE, '', 'reports')

    expect(list).toHaveBeenCalledTimes(1)
    expect(store.collections()[0]?.collection.folders).toContain('reports')
  })

  it('a folder that is already there is REFUSED, and the collection is left as it was', async () => {
    // Mkdir's own EEXIST, handed up. Merging into it is what this must not
    // do: the import refuses an existing destination for the same reason,
    // and a create that adopted a folder somebody else made would put two
    // owners on one directory.
    const createFolder = vi.fn().mockRejectedValue(new Error('folder already exists: "users"'))
    const { store } = storeWith({ createFolder })
    await store.refresh()
    const before = store.collections()[0]?.collection.folders

    await store.createFolder(HANDLE, '', 'users')

    expect(store.error()).toBe('folder already exists: "users"')
    expect(store.collections()[0]?.collection.folders).toEqual(before)
  })

  it('a name the backend refuses leaves the reason on the store rather than sanitising it', async () => {
    const createFolder = vi
      .fn()
      .mockRejectedValue(new Error('invalid folder name: a folder name is one path component'))
    const { store } = storeWith({ createFolder })
    await store.refresh()

    await store.createFolder(HANDLE, '', 'a/b')

    expect(store.error()).toContain('one path component')
  })

  it('the folder it just made is a place a new request can be saved into', async () => {
    // The criterion in one check: a folder with nothing in it is not a
    // folder anybody can use until a request can go in it.
    const disk = folderOnDisk({
      createFolder: vi.fn().mockResolvedValue(folderCreatedFixture('reports')),
    })
    const { store } = storeWith(disk.services)
    await store.refresh()

    await store.createFolder(HANDLE, '', 'reports')
    store.pointAt(HANDLE)
    await store.newRequest('reports')

    expect(disk.files.has('reports/untitled-request.json')).toBe(true)
    expect(store.selected()).toEqual({ handle: HANDLE, relPath: 'reports/untitled-request.json' })
  })

  it('a request made with no folder named still goes to the collection root', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    store.pointAt(HANDLE)

    await store.newRequest()

    expect(disk.files.has('untitled-request.json')).toBe(true)
  })

  it('a request made with one OPEN lands beside it, in the folder it lives in', async () => {
    // WHERE A PERSON IS is where the next request goes. The header's plus
    // names no folder — it is the door that needs no aiming — so "no folder
    // named" cannot mean the collection's root while a request in `users/`
    // is in the form: the crumb trail directly above that control reads
    // `acme-api > users > create`, and the file landed somewhere the trail
    // did not say (nocx-8aczn.6). `duplicateRequest` already answers this
    // question the same way, and the copy lands beside its original.
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)

    await store.newRequest()

    expect(disk.files.has('users/untitled-request.json')).toBe(true)
    expect(store.selected()).toEqual({
      handle: HANDLE,
      relPath: 'users/untitled-request.json',
    })
  })

  it('a folder NAMED still wins over the one the person is in', async () => {
    // The row's plus and the row's menu aim at a row, and a collection row's
    // own path is '' — so the aimed door has to be able to say "the root"
    // while a request in a folder is open, and be believed.
    const disk = folderOnDisk({
      createFolder: vi.fn().mockResolvedValue(folderCreatedFixture('reports')),
    })
    const { store } = storeWith(disk.services)
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)

    await store.newRequest('')
    expect(disk.files.has('untitled-request.json')).toBe(true)

    await store.createFolder(HANDLE, '', 'reports')
    await store.newRequest('reports')
    expect(disk.files.has('reports/untitled-request.json')).toBe(true)
  })

  it("never into a folder of somebody ELSE's collection", async () => {
    // Opening a second collection re-points the workbench and leaves the
    // first collection's request in the form. `users/` is a path in the
    // collection that is no longer being written to, so "beside the open
    // request" is not answerable and the root is what is left.
    const disk = folderOnDisk({
      openCollection: vi.fn().mockResolvedValue({
        handle: 'h2',
        collection: collectionFixture({ name: 'other-api', requests: [], folders: [] }),
      }),
    })
    const { store } = storeWith(disk.services)
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)

    await store.openFolder('/w/other-api')
    expect(store.activeCollection()).toBe('h2')
    expect(store.selected()?.handle).toBe(HANDLE)

    await store.newRequest()

    expect(disk.writeRequest).toHaveBeenCalledWith(
      'h2',
      'untitled-request.json',
      expect.objectContaining({ name: 'Untitled request' }),
    )
  })
})

describe('ApiStore — a request names itself while nobody else has', () => {
  it('takes the name from the method and the address, as the URL is edited', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    await store.newRequest()

    const made = store.draft()
    expect(made?.name).toBe('Untitled request')
    store.editDraft({ ...(made as ApiRequest), url: 'http://127.0.0.1:8080/v1/broker-access' })
    expect(store.draft()?.name).toBe('GET broker-access')

    store.editDraft({ ...(store.draft() as ApiRequest), method: 'POST' })
    expect(store.draft()?.name).toBe('POST broker-access')
  })

  it('stops for good the moment a person names it — not when the URL changes, not ever', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    await store.newRequest()

    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v1/broker-access' })
    expect(store.draft()?.name).toBe('GET broker-access')

    store.editDraft({ ...(store.draft() as ApiRequest), name: 'Broker access, live' })
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v2/tenants' })
    expect(store.draft()?.name).toBe('Broker access, live')

    // Not even after the file has been written and read back: the name in
    // it is a name somebody gave, and reopening a request must not put the
    // offer back on.
    await store.saveDraft()
    await store.openRequest(HANDLE, 'untitled-request.json')
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v3/anything' })
    expect(store.draft()?.name).toBe('Broker access, live')
  })

  it('an address with nothing to take a name from leaves the name alone', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    await store.newRequest()

    store.editDraft({ ...(store.draft() as ApiRequest), url: 'http://127.0.0.1:8080' })
    expect(store.draft()?.name).toBe('Untitled request')
    // And the offer is still live: it was absent, not spent.
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'http://127.0.0.1:8080/v1/orders' })
    expect(store.draft()?.name).toBe('GET orders')
  })

  it('two requests offered ONE name are two files — the allocator is what keeps them apart', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()

    await store.newRequest()
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v1/broker-access' })
    await store.saveDraft()
    await store.newRequest()
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v1/broker-access' })
    await store.saveDraft()

    // Same name, two files, and the first still holds what was written into
    // it — a second request that overwrote the first would be the same
    // number of rows and one lost request.
    expect(store.draft()?.name).toBe('GET broker-access')
    const written = [...disk.files.keys()].filter((p) => p !== CREATE_REL_PATH)
    expect(written).toEqual(['untitled-request.json', 'untitled-request-2.json'])
    expect(disk.files.get('untitled-request.json')?.url).toBe('https://h/v1/broker-access')
  })
})

// ── A request can be duplicated (nocx-bp44a) ──────────────────────────────
//
// The parts existed — `freePath` names a file nothing occupies and
// `writeRequest` writes one — and there was no way to reach them: somebody
// wanting the same call with one header changed retyped it, or edited the
// original and lost it.

describe('ApiStore — duplicating a request', () => {
  it('copies the FILE, beside the original, under a name the allocator freed', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()

    await store.duplicateRequest(HANDLE, CREATE_REL_PATH)

    // The source is READ rather than taken from the form: the file is the
    // truth, and the request being copied is very often not the one open.
    expect(disk.readRequest).toHaveBeenCalledWith(HANDLE, CREATE_REL_PATH)
    const copy = disk.files.get('users/create-copy.json')
    expect(copy?.name).toBe('create copy')
    expect(copy?.auth).toEqual(REQUEST.auth)
    // Selected, so the change the person came to make is the next thing.
    expect(store.selected()).toEqual({ handle: HANDLE, relPath: 'users/create-copy.json' })
    expect(store.draft()?.name).toBe('create copy')
  })

  it('twice gives two copies, told apart in the tree', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()

    await store.duplicateRequest(HANDLE, CREATE_REL_PATH)
    await store.duplicateRequest(HANDLE, CREATE_REL_PATH)

    expect([...disk.files.keys()]).toEqual([
      CREATE_REL_PATH,
      'users/create-copy.json',
      'users/create-copy-2.json',
    ])
    // Two rows a person can tell apart, which is why they made the copy.
    expect(disk.files.get('users/create-copy-2.json')?.name).toBe('create copy 2')
  })

  it('a source that will not read writes nothing and says why', async () => {
    const disk = folderOnDisk({
      readRequest: vi.fn().mockRejectedValue(new Error('bad JSON')),
    })
    const { store } = storeWith(disk.services)
    await store.refresh()

    await store.duplicateRequest(HANDLE, CREATE_REL_PATH)

    expect(disk.writeRequest).not.toHaveBeenCalled()
    expect(store.error()).toBe('bad JSON')
  })

  it('a copy the disk refuses leaves the folder as it was and says why', async () => {
    const disk = folderOnDisk({
      writeRequest: vi.fn().mockRejectedValue(new Error('read-only file system')),
    })
    const { store } = storeWith(disk.services)
    await store.refresh()

    await store.duplicateRequest(HANDLE, CREATE_REL_PATH)

    expect([...disk.files.keys()]).toEqual([CREATE_REL_PATH])
    expect(store.error()).toBe('read-only file system')
    expect(store.selected()).toBeNull()
  })
})

// ── Where the person is, and what the form is holding ─────────────────────
//
// Two questions that look like one and are not. `activeFolder` is WHERE THE
// PANEL IS POINTED — a place a person walked to in the tree, which the plus
// and the curl ask read when nobody names a folder. `draftFolder` is where
// THE THING IN THE FORM will be written, which only a draft with no file
// behind it has, and which the person chose in the import ask. They agree
// almost always and come apart the moment somebody walks off after importing
// — and a pending file that followed the person around would be a promise the
// ask made and the surface broke.

describe('ApiStore — the folder the panel is pointed at', () => {
  it('starts at the root and follows the request that is opened', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()

    expect(store.activeFolder()).toBe('')
    await store.openRequest(HANDLE, CREATE_REL_PATH)
    expect(store.activeFolder()).toBe('users')
  })

  it('a person can walk into a folder, and what they make lands there', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()

    store.enterFolder(HANDLE, 'users')

    expect(store.activeCollection()).toBe(HANDLE)
    expect(store.activeFolder()).toBe('users')
    await store.newRequest()
    expect(disk.files.has('users/untitled-request.json')).toBe(true)
  })

  it('pointing at a collection is standing at its root, not in the last folder', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    store.enterFolder(HANDLE, 'users')

    store.pointAt(HANDLE)

    expect(store.activeFolder()).toBe('')
  })

  it('a folder that leaves the listing stops being where anybody is', async () => {
    // The same re-validation the active COLLECTION gets when a handle stops
    // being listed: a place that is not there is not a place, and the
    // allocator would otherwise be handed a path the backend just lost.
    const list = vi
      .fn()
      .mockResolvedValue({ collections: [collectionsFixture()], defaultRoot: DEFAULT_ROOT })
    const { store } = storeWith({ listCollections: list })
    await store.refresh()
    store.enterFolder(HANDLE, 'users')

    list.mockResolvedValue({
      collections: [
        collectionsFixture({ collection: collectionFixture({ requests: [], folders: [] }) }),
      ],
      defaultRoot: DEFAULT_ROOT,
    })
    await store.refresh()

    expect(store.activeFolder()).toBe('')
  })

  it('a curl import does not move the person — that is what the ask reads', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)

    await store.importCurl('curl https://h/v1/ping')

    // The imported request got its own file, in the folder the ask named —
    // and where the person STANDS did not move with it.
    expect(store.selected()?.relPath.startsWith('users/')).toBe(true)
    expect(store.activeFolder()).toBe('users')
  })
})

describe('ApiStore — a curl import lands where the ask said', () => {
  it('with no folder named it takes the one the person is standing in', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    store.enterFolder(HANDLE, 'users')

    await store.importCurl('curl https://h/v1/ping')
    expect(store.draftFolder()).toBe('users')

    await store.saveDraftAs()

    expect(disk.files.has('users/create.json')).toBe(true)
    expect(store.selected()?.relPath.startsWith('users/')).toBe(true)
  })

  it('the ask names one, and that is where it goes whatever the tree does after', async () => {
    // The promise the ask made: walking somewhere else between Convert and
    // Save must not carry the pending file along.
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()

    await store.importCurl('curl https://h/v1/ping', 'users')
    store.enterFolder(HANDLE, '')

    await store.saveDraftAs()

    expect(
      [...disk.files.keys()].some((k) => k.startsWith('users/') && k !== CREATE_REL_PATH),
    ).toBe(true)
  })

  it('the collection root is still sayable, and is not "nobody named one"', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    store.enterFolder(HANDLE, 'users')

    await store.importCurl('curl https://h/v1/ping', '')
    await store.saveDraftAs()

    expect(store.selected()?.relPath.includes('/')).toBe(false)
  })
})

describe('ApiStore — closing the request', () => {
  it('empties the form and leaves the file exactly where it was', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services)
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)

    await store.closeRequest()

    expect(store.draft()).toBeNull()
    expect(store.selected()).toBeNull()
    expect(disk.files.get(CREATE_REL_PATH)).toEqual(REQUEST)
    // The person did not leave the folder by closing what was in it.
    expect(store.activeFolder()).toBe('users')
  })

  it('asks nothing about a request that has a file — closing writes the last edit', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services, { idleMs: 0 })
    await store.refresh()

    // Nothing in the form: nothing to say.
    expect(store.closeQuestion()).toBe('')

    // A file it has drifted from is not a cost: the close writes it.
    await store.openRequest(HANDLE, CREATE_REL_PATH)
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v9/moved' })
    expect(store.closeQuestion()).toBe('')

    await store.closeRequest()

    expect(store.draft()).toBeNull()
    expect(disk.files.get(CREATE_REL_PATH)?.url).toBe('https://h/v9/moved')
  })

  it('asks about the one draft that has nowhere to be written', async () => {
    // A curl line converted with no collection open. There is no folder to
    // write it into, so closing it does take it — and that is the only state
    // left in which closing takes anything.
    const { store } = storeWith({
      importCurl: vi.fn().mockResolvedValue({ request: REQUEST, unsupported: [] }),
    })
    await store.importCurl('curl https://h/v1/ping')

    expect(store.selected()).toBeNull()
    expect(store.closeQuestion()).toContain('never been saved')

    await store.closeRequest()
    expect(store.draft()).toBeNull()
    expect(store.draftFolder()).toBe('')
  })
})

// ── Saving is not a gesture ───────────────────────────────────────────────
//
// The Save button was pressed for insurance rather than for a decision: Send
// already wrote the file before sending, so the only thing it bought was not
// losing an experiment on the way there. The file is written when typing
// stops instead — the rhythm the note tab already settled on in this product
// — and the tests wait on the WRITE, never on a duration (`idleMs: 0`).

describe('ApiStore — the draft reaches its file by itself', () => {
  it('an edit lands in the file with nothing pressed', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services, { idleMs: 0 })
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)

    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v2/typed' })

    await vi.waitFor(() => expect(disk.files.get(CREATE_REL_PATH)?.url).toBe('https://h/v2/typed'))
    expect(store.dirty()).toBe(false)
  })

  it('a burst of typing is ONE write, not one per keystroke', async () => {
    // The whole reason it waits for the pause: a write per character is a
    // disk write on the hot path of somebody's thinking, and a file a
    // colleague sees in a diff churning once per keystroke.
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services, { idleMs: 20 })
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)
    disk.writeRequest.mockClear()

    for (const url of ['https://h/v', 'https://h/v2', 'https://h/v2/t']) {
      store.editDraft({ ...(store.draft() as ApiRequest), url })
    }

    await vi.waitFor(() => expect(disk.files.get(CREATE_REL_PATH)?.url).toBe('https://h/v2/t'))
    expect(disk.writeRequest).toHaveBeenCalledTimes(1)
  })

  it('opening another request writes the one being left first', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(disk.services, { idleMs: 10_000 })
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v3/left' })

    // The timer has not fired and will not for ten seconds; the open is what
    // lands the edit.
    await store.newRequest()

    expect(disk.files.get(CREATE_REL_PATH)?.url).toBe('https://h/v3/left')
  })

  it('a DELETE is not undone by a write that was already scheduled', async () => {
    // The partial-failure question this rhythm raises: between the last
    // keystroke and the write, the file can stop existing. A timer that
    // fired afterwards would put it back, and the delete would look like it
    // had worked until the next listing.
    const disk = folderOnDisk({ deleteRequest: vi.fn().mockResolvedValue({}) })
    const { store } = storeWith(disk.services, { idleMs: 0 })
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v4/doomed' })
    disk.writeRequest.mockClear()

    await store.deleteRequest(HANDLE, CREATE_REL_PATH)

    expect(store.draft()).toBeNull()
    // Nothing is written after the delete, however long anybody waits.
    await new Promise((resolve) => setTimeout(resolve, 5))
    expect(disk.writeRequest).not.toHaveBeenCalled()
  })

  it('a MOVE writes to the path the edits belonged to, before the rename', async () => {
    const disk = folderOnDisk({
      moveRequest: vi.fn((_h: string, _from: string, to: string) =>
        Promise.resolve({ relPath: to }),
      ),
    })
    const { store } = storeWith(disk.services, { idleMs: 10_000 })
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)
    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v5/moving' })

    await store.moveRequest(HANDLE, CREATE_REL_PATH, 'create.json')

    // Written where it was, then renamed — and the move is no longer refused
    // for edits nobody could have saved by hand.
    expect(disk.writeRequest).toHaveBeenCalledWith(
      HANDLE,
      CREATE_REL_PATH,
      expect.objectContaining({ url: 'https://h/v5/moving' }),
    )
    expect(store.error()).toBe('')
  })

  it('a write that FAILS is said out loud — a silent one is typing that goes nowhere', async () => {
    const disk = folderOnDisk()
    const { store } = storeWith(
      { ...disk.services, writeRequest: vi.fn().mockRejectedValue(new Error('read-only volume')) },
      { idleMs: 0 },
    )
    await store.refresh()
    await store.openRequest(HANDLE, CREATE_REL_PATH)

    store.editDraft({ ...(store.draft() as ApiRequest), url: 'https://h/v6/nowhere' })

    await vi.waitFor(() => expect(store.error()).toContain('read-only volume'))
    // And it still reads as unsaved, because it is.
    expect(store.dirty()).toBe(true)
  })
})
