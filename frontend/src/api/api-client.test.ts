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
import { createApiWorkbenchServices, nativePickers } from './api-client'
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
  variables: [],
  body: { kind: 'raw', text: '{"email":"a@b.c"}', fileRef: '' },
  auth: { kind: 'bearer', token: 'API_TOKEN', password: '', user: '' },
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

  it('makes ONE folder inside a collection — a name, and the folder to put it in', async () => {
    const { dispatcher, call } = fakeDispatcher({ relPath: 'reports', collection: {} })
    await createApiWorkbenchServices(dispatcher).createFolder('h1', '', 'reports')
    expect(call).toHaveBeenCalledWith('api.collections.createFolder', {
      handle: 'h1',
      parentRelPath: '',
      name: 'reports',
    })
  })

  it('nests by naming the parent it already has, never by a path with separators in it', async () => {
    // §13.1's grammar one level down: the caller names a COMPONENT and the
    // backend derives the location. A client that spelled `users/admin` as
    // the name would be asking the backend to sanitise a path, which is the
    // thing that grammar exists to make impossible.
    const { dispatcher, call } = fakeDispatcher({ relPath: 'users/admin', collection: {} })
    await createApiWorkbenchServices(dispatcher).createFolder('h1', 'users', 'admin')
    expect(call).toHaveBeenCalledWith('api.collections.createFolder', {
      handle: 'h1',
      parentRelPath: 'users',
      name: 'admin',
    })
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

  it('reads the backend-computed request scope with its draft variables', async () => {
    const { dispatcher, call } = fakeDispatcher({ variables: [] })
    const variables = [{ name: 'id', value: 'draft', enabled: true }]
    await createApiWorkbenchServices(dispatcher).requestScope(
      'h1',
      'users/create.json',
      'environments/prod.json',
      variables,
    )
    expect(call).toHaveBeenCalledWith('api.request.scope', {
      handle: 'h1',
      relPath: 'users/create.json',
      envRelPath: 'environments/prod.json',
      variables,
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
  it('reads folder variables by handle and folder path', async () => {
    const { dispatcher, call } = fakeDispatcher({ variables: [] })
    await createApiWorkbenchServices(dispatcher).readFolder('h1', 'users')
    expect(call).toHaveBeenCalledWith('api.folder.read', {
      handle: 'h1',
      relPath: 'users',
    })
  })

  it('writes folder variables by handle and folder path', async () => {
    const { dispatcher, call } = fakeDispatcher({ variables: [] })
    await createApiWorkbenchServices(dispatcher).writeFolder('h1', 'users', [
      { name: 'baseUrl', value: 'https://example.test', enabled: true },
    ])
    expect(call).toHaveBeenCalledWith('api.folder.write', {
      handle: 'h1',
      relPath: 'users',
      variables: [{ name: 'baseUrl', value: 'https://example.test', enabled: true }],
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

  it('sends a secret value ONE WAY — the value in, nothing back', async () => {
    // The one call on this client that carries a credential. What it puts on
    // the wire is asserted exactly, because an extra field here is an extra
    // place a credential travels.
    const { dispatcher, call } = fakeDispatcher({})
    const answer = await createApiWorkbenchServices(dispatcher).bindSecret(
      'h1',
      'environments/dev.json',
      'token',
      'sk-live-value',
    )
    expect(call).toHaveBeenCalledWith('api.environment.bindSecret', {
      handle: 'h1',
      relPath: 'environments/dev.json',
      variable: 'token',
      value: 'sk-live-value',
    })
    // NOTHING COMES BACK. The identifier for stored credential material never
    // leaves the backend and the value came from here, so a result carrying
    // either would hand back the thing this method exists to take away.
    expect(answer).toEqual({})
  })

  it('imports a Postman export from a path into a destination', async () => {
    const { dispatcher, call } = fakeDispatcher({ unsupported: [] })
    await createApiWorkbenchServices(dispatcher).importPostman(
      { path: '/w/acme.json' },
      '/w/acme-api',
    )
    expect(call).toHaveBeenCalledWith('api.import.postman', {
      path: '/w/acme.json',
      dest: '/w/acme-api',
    })
  })

  it('imports the DOCUMENT itself when that is what the gesture answered with', async () => {
    // A browser drop and the kit's file input hold bytes and no location, and
    // the backend is not always on the person's machine (spec §1a). The two
    // routes are one method: exactly one source field goes on the wire, so a
    // backend reading `path` cannot also be handed a document.
    const { dispatcher, call } = fakeDispatcher({ unsupported: [] })
    await createApiWorkbenchServices(dispatcher).importPostman(
      { document: '{"info":{"name":"Acme"}}' },
      '/w/acme-api',
    )
    // Exactly these params: `toHaveBeenCalledWith` is an equality on the
    // whole object, so this is also the assertion that no `path` went out
    // beside the document — the union is what keeps that unexpressible.
    expect(call).toHaveBeenCalledWith('api.import.postman', {
      document: '{"info":{"name":"Acme"}}',
      dest: '/w/acme-api',
    })
  })

  it('imports from a URL, carrying the route the backend should fetch it over', async () => {
    // The third source is the general case in the direction the document
    // cannot serve: an export behind a network the renderer is not on. The
    // route rides WITH the url because it is part of how that document is
    // reached, and it spreads onto the params like the other two sources —
    // `importPostman` never learns which member it was handed.
    const { dispatcher, call } = fakeDispatcher({ unsupported: [] })
    await createApiWorkbenchServices(dispatcher).importPostman(
      {
        url: 'https://h/a.json',
        route: { kind: 'connection', profileId: 'p', insecureTls: false },
      },
      '/w/acme',
    )
    expect(call).toHaveBeenCalledWith('api.import.postman', {
      url: 'https://h/a.json',
      route: { kind: 'connection', profileId: 'p', insecureTls: false },
      dest: '/w/acme',
    })
  })

  it('omits route entirely when there is none, rather than sending it undefined', async () => {
    // `decodeAPIParams` refuses a field it does not declare, and a key
    // present-and-undefined is still a key on the wire once it is spread.
    // Absent route IS the direct one, so the absence is the spelling.
    const { dispatcher, call } = fakeDispatcher({ unsupported: [] })
    await createApiWorkbenchServices(dispatcher).importPostman(
      { url: 'https://h/a.json' },
      '/w/acme',
    )
    expect(Object.keys(call.mock.calls[0][1] as object)).toEqual(['url', 'dest'])
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
    await s.readFolder('h1', 'users')
    await s.writeFolder('h1', 'users', [])
    await s.createFolder('h1', 'users', 'admin')
    await s.closeCollection('h1')
    await s.readRequest('h1', 'a.json')
    await s.writeRequest('h1', 'a.json', REQUEST)
    await s.bindSecret('h1', 'environments/dev.json', 'token', 'v')
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
    ['createCollection', (s) => s.createCollection('orders-api')],
    ['openCollection', (s) => s.openCollection('/w/x')],
    ['readFolder', (s) => s.readFolder('h1', 'users')],
    ['writeFolder', (s) => s.writeFolder('h1', 'users', [])],
    ['createFolder', (s) => s.createFolder('h1', 'users', 'admin')],
    ['closeCollection', (s) => s.closeCollection('h1')],
    ['readRequest', (s) => s.readRequest('h1', 'a.json')],
    ['writeRequest', (s) => s.writeRequest('h1', 'a.json', REQUEST)],
    ['bindSecret', (s) => s.bindSecret('h1', 'environments/dev.json', 'token', 'v')],
    ['sendRequest', (s) => s.sendRequest('h1', 'a.json', 'environments/dev.json', 'run-1')],
    ['cancelRequest', (s) => s.cancelRequest('run-1')],
    ['importPostman', (s) => s.importPostman({ path: '/w/a.json' }, '/w/b')],
    ['importCurl', (s) => s.importCurl('curl https://a')],
  ]

  for (const [name, invoke] of cases) {
    it(`${name} rejects with the backend's reason rather than swallowing it`, async () => {
      const s = createApiWorkbenchServices(refusingDispatcher('the folder is gone'))
      await expect(invoke(s)).rejects.toThrow('the folder is gone')
    })
  }
})

// ── The pickers are bound where they can open, and nowhere else ───────────
//
// nocx-h9f8y. The probe used to be `'openFileDialog' in client`, which a
// class instance answers TRUE on every build — so a Wails-less one was handed
// a picker that answers -32601 when pressed, and the kit then drew that
// control INSTEAD of its own FileInput, which would have worked. These assert
// the question that is actually being asked: can this build serve `dialog.*`.
describe('nativePickers', () => {
  /** The dialog client's shape, as a class — a PROTOTYPE method, which is
   *  what made the old probe answer true everywhere. */
  class FakeDialogClient {
    file = vi.fn().mockResolvedValue({ path: '/w/acme.json' })
    directory = vi.fn().mockResolvedValue({ path: '/w/collections' })
    openFileDialog(): Promise<{ path: string }> {
      return this.file() as Promise<{ path: string }>
    }
    openDirectoryDialog(): Promise<{ path: string }> {
      return this.directory() as Promise<{ path: string }>
    }
  }

  it('hands over neither picker where no runtime serves dialog.*', () => {
    // The method IS on the object — that is the whole point — and it is
    // still not a capability this build has.
    const client = new FakeDialogClient()
    expect('openFileDialog' in client).toBe(true)
    expect(nativePickers(client, false)).toEqual({})
  })

  it('binds both onto the dialog client where one does', async () => {
    const client = new FakeDialogClient()
    const pickers = nativePickers(client, true)
    await expect(pickers.file?.()).resolves.toEqual({ path: '/w/acme.json' })
    await expect(pickers.directory?.()).resolves.toEqual({ path: '/w/collections' })
    expect(client.file).toHaveBeenCalledTimes(1)
    expect(client.directory).toHaveBeenCalledTimes(1)
  })

  it("lets the picker's own refusal through rather than swallowing it", async () => {
    // The paired failure half: a runtime that is there and a method that
    // reports itself unavailable anyway is what the surface retires on.
    const client = new FakeDialogClient()
    client.file.mockRejectedValue(new Error('-32601 method not found'))
    const pickers = nativePickers(client, true)
    await expect(pickers.file?.()).rejects.toThrow('-32601')
  })
})
