// Fixtures for the API workbench's tests — one place that knows the shape of
// an api.* result, so adding a field to a schema means editing one file
// rather than chasing every case that builds a response by hand
// (test-support/panes-fixtures.ts is the pattern).
//
// The values are the design's own worked example (§9.2, §11): an acme-api
// collection with a POST that answers 201 in 184ms.
import { vi } from 'vitest'
import type { ApiWorkbenchServices, CollectionWatchPort } from './api-client'
import type { FilesChanged } from '../generated/files.changed'
import type { FilesOpenResult } from '../generated/files.open'
import type { FilesWatchResult } from '../generated/files.watch'
import type { FilesCloseResult } from '../generated/files.close'
import type {
  ApiCollection,
  ApiEnvironmentRef,
  ApiOpenCollection,
  ApiRequest,
  ApiResponse,
} from './api-model'
import type { ApiRequestSendResult, Raw, Span } from '../generated/api.request.send'
import type { ApiCollectionsCreateResult } from '../generated/api.collections.create'

/**
 * The value of the secret the worked example binds. It is here so the tests
 * can assert it appears NOWHERE, and it is deliberately absent from every
 * fixture below: the backend elides a secret's bytes and puts a placeholder in
 * their place (§11.2), so a renderer has nothing to leak — and the assertion
 * is what keeps that true the day somebody adds a field.
 */
export const SECRET_VALUE = 'sk-live-9f2c4e7a11b3d8'

/** The placeholder the backend leaves where a secret's bytes were. */
export const SECRET_PLACEHOLDER = '\u2039API_TOKEN\u203a'

export const HANDLE = 'h1'
/** The handle `api.collections.create` mints. Deliberately not HANDLE: a
 *  create that answered the handle a folder already open holds would let a
 *  test pass while the renderer overwrote the wrong row. */
export const CREATED_HANDLE = 'h9'
export const CREATED_NAME = 'orders-api'
/** Where the worked example's collection folder sits — the path that goes
 *  into the watch set. */
export const COLLECTION_PATH = '/w/acme-api'
export const CREATE_REL_PATH = 'users/create.json'
export const LIST_REL_PATH = 'users/list.json'

/** The worked example's two environments. The NAME is deliberately not the
 *  file's stem: the two are separate facts and only the file knows the
 *  first — a fixture whose names were derivable from its paths would let a
 *  renderer that derived one from the other pass every test here, and that
 *  derivation is the second answer to "which environment is this" the send
 *  path must not be given. */
export const DEV_ENV: ApiEnvironmentRef = { relPath: 'environments/default.json', name: 'dev' }
export const PROD_ENV: ApiEnvironmentRef = { relPath: 'environments/prod.json', name: 'production' }

export const REQUEST: ApiRequest = {
  id: 'req-create',
  name: 'create',
  method: 'POST',
  url: '{{baseUrl}}/users',
  headers: [{ name: 'Content-Type', value: 'application/json', enabled: true }],
  query: [],
  body: { kind: 'raw', text: '{"email":"a@b.c","name":"A"}', fileRef: '' },
  // A VARIABLE NAME. There is deliberately no field in the contract where a
  // secret value or an identifier for one could be spelled (design §8).
  auth: { kind: 'bearer', var: 'API_TOKEN', user: '' },
}

/**
 * Build one side of an exchange from its parts, computing the byte offsets.
 *
 * Hand-counted offsets in a fixture are a way to write a test that passes for
 * the wrong reason: the tiling property — in order, no gap, no overlap — is
 * the contract, so the fixture makes it true by CONSTRUCTION and the walker is
 * tested against a payload shaped the way the backend actually sends one.
 */
function tiledRaw(parts: readonly RawPart[]): Raw {
  const encoder = new TextEncoder()
  const spans: Span[] = []
  let text = ''
  let at = 0
  for (const part of parts) {
    const piece = typeof part === 'string' ? part : part.placeholder
    const bytes = encoder.encode(piece).length
    spans.push({
      from: at,
      to: at + bytes,
      kind: typeof part === 'string' ? 'text' : part.damage === '' ? 'secret' : 'secret-damaged',
      name: typeof part === 'string' ? '' : part.name,
      damage: typeof part === 'string' ? '' : part.damage,
    })
    text += piece
    at += bytes
  }
  return { text, spans }
}

/** A run of ordinary text, or one secret whose bytes the backend replaced. */
type RawPart = string | { name: string; placeholder: string; damage: string }

/** The REQUEST side of the design's worked example (§11), with an intact
 *  secret in it. `damage` decides which of §11.1's two secret states it is.
 *
 *  It is its own fixture rather than half a pair because that is where it
 *  lives on the wire now: the sender composes it before it dials, so it is
 *  on the exchange and a run that never answered still carries it. */
export function requestRawFixture(damage = ''): Raw {
  return tiledRaw([
    'POST /users HTTP/1.1\r\nHost: api.internal\r\nAuthorization: Bearer ',
    { name: 'API_TOKEN', placeholder: SECRET_PLACEHOLDER, damage },
    '\r\nContent-Type: application/json\r\n\r\n{"email":"a@b.c","name":"A"}',
  ])
}

/** The RESPONSE side — which exists only when something answered. */
export function responseRawFixture(): Raw {
  return tiledRaw([
    'HTTP/1.1 201 Created\r\nContent-Type: application/json\r\n\r\n{"id":"usr_8f21"}',
  ])
}

/** The folder's CONTENTS, so a test about one field of them names that field
 *  rather than restating the whole collection. No environments by default,
 *  because that is the ordinary state — §6.2 says a collection with none is
 *  a collection — and the tests that are about the picker say so.  */
export function collectionFixture(over: Partial<ApiCollection> = {}): ApiCollection {
  return {
    name: 'acme-api',
    requests: [
      { relPath: CREATE_REL_PATH, name: 'create', method: 'POST' },
      { relPath: LIST_REL_PATH, name: 'list', method: 'GET' },
    ],
    malformed: [],
    environments: [],
    ...over,
  }
}

export function collectionsFixture(over: Partial<ApiOpenCollection> = {}): ApiOpenCollection {
  return {
    handle: HANDLE,
    path: COLLECTION_PATH,
    error: '',
    collection: collectionFixture(),
    ...over,
  }
}

function responseFixture(over: Partial<ApiResponse> = {}): ApiResponse {
  return {
    status: 201,
    headers: [{ name: 'Content-Type', value: 'application/json', enabled: true }],
    text: '{"id":"usr_8f21"}',
    tlsCipherSuite: '',
    binary: false,
    lossy: false,
    truncated: false,
    size: 1229,
    tlsVersion: 'TLS 1.3',
    // raw is REQUIRED on the contract, not optional, and its spans are what
    // the badges are drawn from — so the default fixture carries a real
    // tiling rather than an empty one. An empty `spans` is a legitimate
    // payload for a side with nothing to mark, and api-model.test.ts covers
    // it; a fixture that used it everywhere would let a renderer that ignores
    // spans pass every test in this file.
    raw: responseRawFixture(),
    ...over,
  }
}

/** What `api.request.send` answers when it ANSWERED. `environment` is the
 *  NAME the backend read out of the environment file — never an echo of the
 *  path the caller named — and '' is the send that named none. */
export function sendFixture(
  over: Partial<ApiResponse> = {},
  environment = '',
  route: ApiRequestSendResult['route'] = { kind: 'direct', profileId: '', insecureTls: false },
): ApiRequestSendResult {
  return {
    outcome: 'answered',
    request: requestRawFixture(),
    response: responseFixture(over),
    failure: null,
    environment,
    route,
    remoteAddr: '10.0.3.17:443',
    timings: { dnsMs: 4, connectMs: 21, tlsMs: 38, ttfbMs: 118, totalMs: 184 },
    certificates: [],
  }
}

/**
 * What it answers when the attempt did NOT answer — a run all the same.
 *
 * It carries the request text and the route, because that is the whole point
 * of the shape: the sender holds both before it dials, so a failure is a row
 * with the same detail an answered one has minus what never came back. A
 * fixture that left them out would let a renderer that drops them pass.
 */
export function failedSendFixture(
  failure: ApiRequestSendResult['failure'] = { phase: 'dial', reason: 'connection refused' },
  over: Partial<ApiRequestSendResult> = {},
): ApiRequestSendResult {
  return {
    outcome: 'failed',
    request: requestRawFixture(),
    response: null,
    failure,
    environment: '',
    route: { kind: 'direct', profileId: '', insecureTls: false },
    remoteAddr: '',
    timings: { dnsMs: 3, connectMs: 0, tlsMs: 0, ttfbMs: 0, totalMs: 3 },
    certificates: [],
    ...over,
  }
}

/** And what it answers for a run the person stopped: an outcome of its own,
 *  never a failure, so nothing downstream has to word or tone somebody's own
 *  Stop as something that went wrong. */
export function stoppedSendFixture(): ApiRequestSendResult {
  return failedSendFixture(
    { phase: 'stopped', reason: 'context canceled' },
    { outcome: 'stopped', remoteAddr: '10.0.3.17:443' },
  )
}

/**
 * What `api.collections.create` answers: the same handle-and-collection an
 * open does, and nothing in it. A folder that has just been made has no
 * requests and no malformed files — the schema says so in as many words —
 * and there is no path, because the backend decided where it lives (§13.1).
 */
export function createdFixture(name = CREATED_NAME): ApiCollectionsCreateResult {
  return {
    handle: CREATED_HANDLE,
    collection: { name, requests: [], malformed: [], environments: [] },
  }
}

/** The local session the workbench opens its watch binding against. */
export const WATCH_SESSION = 'sess-local'
/** The binding `files.open` mints for that watch. */
export const WATCH_BINDING = 'bind-1'

/** A collection watch a test can drive: the spies the store calls, plus the
 *  two things only the BACKEND can do — announce that a folder changed, and
 *  re-attach after a reconnect. Both are reached through the handlers the
 *  store itself registered, so a test that fires one is exercising the
 *  subscription the product uses rather than a method of its own. */
export interface WatchFixture {
  port: CollectionWatchPort
  open: ReturnType<typeof vi.fn>
  watch: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  /** The backend says one watched directory is dirty. */
  changed(path: string, bindingId?: string): void
  /** The transport re-attached (AD-9). */
  reconnect(): void
  /** Every path set handed to files.watch, in order — the assertion the
   *  design asks for, because files.watch REPLACES the set and a count
   *  cannot tell a removal from an addition. */
  sets(): string[][]
  /** The set the backend is holding now — the last one sent, or undefined
   *  before anything was. */
  lastSet(): string[] | undefined
}

export function watchFixture(
  over: {
    localSession?: string | null
    open?: ReturnType<typeof vi.fn>
    watch?: ReturnType<typeof vi.fn>
    close?: ReturnType<typeof vi.fn>
  } = {},
): WatchFixture {
  let onChanged: ((p: FilesChanged) => void) | null = null
  let onConnect: (() => void) | null = null
  const open =
    over.open ??
    vi.fn().mockResolvedValue({
      bindingId: WATCH_BINDING,
      endpointId: null,
      root: { path: '/', display: '/', inferred: false, inferredReason: '' },
    })
  // 'polling' with NO reason is what a healthy LOCAL binding answers today —
  // internal/transport says so in as many words, because a reason there would
  // light the degrade badge for every user forever. The default fixture is
  // therefore the case that must NOT warn.
  const watch = over.watch ?? vi.fn().mockResolvedValue({ mode: 'polling' })
  const close = over.close ?? vi.fn().mockResolvedValue({})
  const port: CollectionWatchPort = {
    localSession: () => (over.localSession === undefined ? WATCH_SESSION : over.localSession),
    // The casts are the seam between a `vi.fn()` (which answers `any`) and
    // the port's declared shape. They are HERE, once, rather than at every
    // call site in every test — and they are what makes a fixture that
    // answers the wrong shape a type error in this file instead of a
    // mysterious undefined in a test three files away.
    open: (sessionId, rootPath) => open(sessionId, rootPath) as Promise<FilesOpenResult>,
    watch: (bindingId, paths) => watch(bindingId, paths) as Promise<FilesWatchResult>,
    close: (bindingId) => close(bindingId) as Promise<FilesCloseResult>,
    subscribeChanged: (handler) => {
      onChanged = handler
      return () => {
        onChanged = null
      }
    },
    onConnect: (handler) => {
      onConnect = handler
      return () => {
        onConnect = null
      }
    },
  }
  return {
    port,
    open,
    watch,
    close,
    changed: (path, bindingId = WATCH_BINDING) => onChanged?.({ bindingId, path }),
    reconnect: () => onConnect?.(),
    sets: () => watch.mock.calls.map((c: unknown[]) => c[1] as string[]),
    lastSet: () => {
      const all = watch.mock.calls.map((c: unknown[]) => c[1] as string[])
      return all[all.length - 1]
    },
  }
}

/** One environment whole, as api.environment.read answers it: the shape an
 *  editor opens onto. Its name and path match the ref the collection fixture
 *  lists, so a test that picks that environment and opens it is exercising
 *  one environment rather than two that happen to share a panel. */
const ENVIRONMENT = {
  name: 'Local',
  values: { baseUrl: 'https://api.example.test' },
  secretVars: [],
  route: { kind: 'direct' as const, profileId: '', insecureTls: false },
}

/** A backend that has no collections open — the state a person starts in,
 *  and exactly when they need to be able to make one. */
export function noCollections(): Partial<ApiWorkbenchServices> {
  return {
    listCollections: vi.fn().mockResolvedValue({ collections: [], defaultRoot: DEFAULT_ROOT }),
  }
}

/** Where this build puts a collection made with no place named — the value
 *  `api.collections.list` answers beside the rows. It is a real path rather
 *  than '' because '' is the DEGRADED state (a build with no app directory)
 *  and a fixture that used it everywhere would let a surface that ignores
 *  the field pass every test. */
export const DEFAULT_ROOT = '/home/dev/.local/share/nocx/collections'

/** Every backend call the workbench makes, as spies that succeed. A test
 *  that is about a failure overrides exactly the one call it is about. */
export function servicesFixture(over: Partial<ApiWorkbenchServices> = {}): ApiWorkbenchServices {
  const opened = collectionsFixture()
  return {
    listCollections: vi
      .fn()
      .mockResolvedValue({ collections: [collectionsFixture()], defaultRoot: DEFAULT_ROOT }),
    openCollection: vi
      .fn()
      .mockResolvedValue({ handle: opened.handle, collection: opened.collection }),
    createCollection: vi.fn().mockResolvedValue(createdFixture()),
    closeCollection: vi.fn().mockResolvedValue({}),
    readEnvironment: vi.fn().mockResolvedValue({ environment: ENVIRONMENT }),
    writeEnvironment: vi.fn().mockResolvedValue({}),
    deleteRequest: vi.fn().mockResolvedValue({}),
    readRequest: vi.fn().mockResolvedValue({ request: REQUEST }),
    writeRequest: vi.fn().mockResolvedValue({}),
    sendRequest: vi.fn().mockResolvedValue(sendFixture()),
    cancelRequest: vi.fn().mockResolvedValue({}),
    importPostman: vi.fn().mockResolvedValue({ unsupported: [] }),
    importCurl: vi.fn().mockResolvedValue({ request: REQUEST, unsupported: [] }),
    ...over,
  }
}
