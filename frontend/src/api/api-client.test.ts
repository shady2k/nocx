// The api.* control-plane seam. One test per method for what a caller can do
// — the method name and the params that go on the wire — and one per method
// for the call failing, which is the paired half AGENTS.md testing rule 3
// asks for: every external call this code makes has a test where it fails.
//
// Two properties are asserted for the whole surface rather than per method,
// because they are properties of design §13.1 rather than of any one call:
// only api.collections.open and api.import.postman ever put a PATH on the
// wire, and every other call addresses a folder by the handle the backend
// minted. The Go side refuses a stray `root` strictly
// (TestAPIMethods_OnlyOpenAndImportPostmanAcceptAPath); this is the
// renderer's half of the same rule — it never spells one.
import { describe, expect, it, vi } from 'vitest'
import { createApiWorkbenchServices } from './api-client'
import type { Dispatcher } from '../dispatcher'
import type { ApiRequest } from './api-model'

/** A dispatcher whose `call` is a spy. The seam under test is exactly one
 *  method call, so nothing else of the dispatcher is needed. */
function fakeDispatcher(result: unknown = {}): {
  dispatcher: Dispatcher
  call: ReturnType<typeof vi.fn>
} {
  const call = vi.fn().mockResolvedValue(result)
  return { dispatcher: { call } as unknown as Dispatcher, call }
}

/** A dispatcher whose `call` rejects — the failure half of every pair. */
function refusingDispatcher(message: string): Dispatcher {
  return { call: vi.fn().mockRejectedValue(new Error(message)) } as unknown as Dispatcher
}

const REQUEST: ApiRequest = {
  id: 'r1',
  name: 'create user',
  method: 'POST',
  url: '{{baseUrl}}/users',
  headers: [{ name: 'Content-Type', value: 'application/json', enabled: true }],
  query: [],
  body: { kind: 'raw', text: '{"email":"a@b.c"}', fileRef: '' },
  auth: { kind: 'bearer', var: 'API_TOKEN', user: '' },
}

describe('ApiClient — one method per contract', () => {
  it('lists the open collections with no params of its own', async () => {
    const { dispatcher, call } = fakeDispatcher({ collections: [] })
    await createApiWorkbenchServices(dispatcher).listCollections()
    expect(call).toHaveBeenCalledWith('api.collections.list', {})
  })

  it('opens a folder by the path the user gave', async () => {
    const { dispatcher, call } = fakeDispatcher({ handle: 'h1', collection: {} })
    await createApiWorkbenchServices(dispatcher).openCollection('/w/acme-api')
    expect(call).toHaveBeenCalledWith('api.collections.open', { path: '/w/acme-api' })
  })

  it('creates a collection by NAME — the backend decides where it goes', async () => {
    const { dispatcher, call } = fakeDispatcher({ handle: 'h9', collection: {} })
    await createApiWorkbenchServices(dispatcher).createCollection('orders-api')
    expect(call).toHaveBeenCalledWith('api.collections.create', { name: 'orders-api' })
  })

  it('closes a folder by handle, never by path', async () => {
    const { dispatcher, call } = fakeDispatcher({})
    await createApiWorkbenchServices(dispatcher).closeCollection('h1')
    expect(call).toHaveBeenCalledWith('api.collections.close', { handle: 'h1' })
  })

  it('reads a request by handle plus a path within it', async () => {
    const { dispatcher, call } = fakeDispatcher({ request: REQUEST })
    await createApiWorkbenchServices(dispatcher).readRequest('h1', 'users/create.json')
    expect(call).toHaveBeenCalledWith('api.request.read', {
      handle: 'h1',
      relPath: 'users/create.json',
    })
  })

  it('writes the request the form holds', async () => {
    const { dispatcher, call } = fakeDispatcher({})
    await createApiWorkbenchServices(dispatcher).writeRequest('h1', 'users/create.json', REQUEST)
    expect(call).toHaveBeenCalledWith('api.request.write', {
      handle: 'h1',
      relPath: 'users/create.json',
      request: REQUEST,
    })
  })

  it('sends the request the FILE holds — the handle, the path, the environment and the token it can be stopped by', async () => {
    const { dispatcher, call } = fakeDispatcher({ response: {} })
    await createApiWorkbenchServices(dispatcher).sendRequest(
      'h1',
      'users/create.json',
      'environments/prod.json',
      'run-7',
    )
    // The environment is named by its PATH inside the collection, exactly as
    // the request is. There is no field here for its NAME, and that is the
    // point: the backend reads that off the file in the same breath as the
    // address and the route, and a second answer to "which environment is
    // this" is what the send path must not be given.
    expect(call).toHaveBeenCalledWith('api.request.send', {
      handle: 'h1',
      relPath: 'users/create.json',
      envRelPath: 'environments/prod.json',
      token: 'run-7',
    })
  })

  it('stops a run by the token it was sent under — never by the JSON-RPC id', async () => {
    const { dispatcher, call } = fakeDispatcher({})
    await createApiWorkbenchServices(dispatcher).cancelRequest('run-7')
    // The token and NOTHING else. No handle and no path: the token already
    // names exactly one running exchange, and a second way to address it
    // would be a second answer to "which run is this".
    expect(call).toHaveBeenCalledWith('api.request.cancel', { token: 'run-7' })
  })

  it('no environment is an empty envRelPath, not an absent one', async () => {
    const { dispatcher, call } = fakeDispatcher({ response: {} })
    await createApiWorkbenchServices(dispatcher).sendRequest('h1', 'users/create.json', '', 'run-1')
    expect(call).toHaveBeenCalledWith('api.request.send', {
      handle: 'h1',
      relPath: 'users/create.json',
      envRelPath: '',
      token: 'run-1',
    })
  })

  it('imports a Postman export from a path into a destination', async () => {
    const { dispatcher, call } = fakeDispatcher({ unsupported: [] })
    await createApiWorkbenchServices(dispatcher).importPostman('/w/acme.json', '/w/acme-api')
    expect(call).toHaveBeenCalledWith('api.import.postman', {
      path: '/w/acme.json',
      dest: '/w/acme-api',
    })
  })

  it('imports a curl line as a line — parsed, never executed', async () => {
    const { dispatcher, call } = fakeDispatcher({ request: REQUEST, unsupported: [] })
    await createApiWorkbenchServices(dispatcher).importCurl('curl -X POST https://a/b')
    expect(call).toHaveBeenCalledWith('api.import.curl', { line: 'curl -X POST https://a/b' })
  })

  it('never spells a filesystem path except when opening or importing', async () => {
    const { dispatcher, call } = fakeDispatcher({})
    const s = createApiWorkbenchServices(dispatcher)
    await s.listCollections()
    await s.createCollection('orders-api')
    await s.closeCollection('h1')
    await s.readRequest('h1', 'a.json')
    await s.writeRequest('h1', 'a.json', REQUEST)
    await s.sendRequest('h1', 'a.json', 'environments/dev.json', 'run-1')
    await s.cancelRequest('run-1')
    for (const [, params] of call.mock.calls) {
      expect(Object.keys(params as object)).not.toContain('path')
      expect(Object.keys(params as object)).not.toContain('root')
    }
  })
})

describe('ApiClient — every call has a test where it fails', () => {
  const cases: Array<
    [string, (s: ReturnType<typeof createApiWorkbenchServices>) => Promise<unknown>]
  > = [
    ['listCollections', (s) => s.listCollections()],
    ['openCollection', (s) => s.openCollection('/w/x')],
    ['createCollection', (s) => s.createCollection('orders-api')],
    ['closeCollection', (s) => s.closeCollection('h1')],
    ['readRequest', (s) => s.readRequest('h1', 'a.json')],
    ['writeRequest', (s) => s.writeRequest('h1', 'a.json', REQUEST)],
    ['sendRequest', (s) => s.sendRequest('h1', 'a.json', 'environments/dev.json', 'run-1')],
    ['cancelRequest', (s) => s.cancelRequest('run-1')],
    ['importPostman', (s) => s.importPostman('/w/a.json', '/w/b')],
    ['importCurl', (s) => s.importCurl('curl https://a')],
  ]

  for (const [name, invoke] of cases) {
    it(`${name} rejects with the backend's reason rather than swallowing it`, async () => {
      const s = createApiWorkbenchServices(refusingDispatcher('the folder is gone'))
      await expect(invoke(s)).rejects.toThrow('the folder is gone')
    })
  }
})
