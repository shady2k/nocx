// ApiClient — the api.* control-plane seam (design §9). One client, one
// method per wire call, every result a GENERATED type: the renderer declares
// nothing of its own, because a hand-written type can want a field the wire
// does not carry — the defect the whole contracts/ directory exists to
// prevent (AGENTS.md testing rule 5). The surface consumes it through
// ApiWorkbenchServices so a test can substitute a fake without a WebSocket
// (the Ports and Files pattern).
//
// One rule this file keeps rather than remembers. Design §13.1: opening a
// collection mints a backend-held handle, and a ROOT is never accepted
// again. Only `openCollection` and `importPostman` put a filesystem path on
// the wire; every other method addresses a folder by that handle plus a path
// RELATIVE to it. The Go side refuses a stray one strictly
// (decodeAPIParams); this is the half that never spells one, and
// api-client.test.ts asserts it across the whole surface rather than per
// method, because it is a property of the surface.

import type { Dispatcher } from '../dispatcher'
import type { ApiCollectionsListResult } from '../generated/api.collections.list'
import type { ApiCollectionsOpenResult } from '../generated/api.collections.open'
import type { ApiCollectionsCreateResult } from '../generated/api.collections.create'
import type { ApiCollectionsCloseResult } from '../generated/api.collections.close'
import type { ApiEnvironmentReadResult } from '../generated/api.environment.read'
import type { ApiEnvironmentWriteResult } from '../generated/api.environment.write'
import type { ApiRequestDeleteResult } from '../generated/api.request.delete'
import type { ApiRequestReadResult } from '../generated/api.request.read'
import type { ApiRequestWriteResult } from '../generated/api.request.write'
import type { ApiRequestSendResult } from '../generated/api.request.send'
import type { ApiImportPostmanResult } from '../generated/api.import.postman'
import type { ApiImportCurlResult } from '../generated/api.import.curl'
import type { FilesOpenResult } from '../generated/files.open'
import type { FilesWatchResult } from '../generated/files.watch'
import type { FilesCloseResult } from '../generated/files.close'
import type { FilesChanged } from '../generated/files.changed'
import type { ApiEnvironment, ApiRequest } from './api-model'

class ApiClient {
  constructor(private dispatcher: Dispatcher) {}

  /** Every collection folder the user currently has open, re-read from disk.
   *  The app remembers the LIST of opened folders and never their contents
   *  (design §6.1), so a request file a colleague's `git pull` added appears
   *  without anything being told about it. */
  listCollections(): Promise<ApiCollectionsListResult> {
    return this.dispatcher.call<ApiCollectionsListResult>('api.collections.list', {})
  }

  /** Open a folder as a collection. The ONE method that accepts a root, and
   *  the one that answers a handle the caller did not already have. */
  openCollection(path: string): Promise<ApiCollectionsOpenResult> {
    return this.dispatcher.call<ApiCollectionsOpenResult>('api.collections.open', { path })
  }

  /** Make a collection and leave it open. A NAME, never a path: the backend
   *  decides where a new collection lives (§13.1), so this method could not
   *  spell a location even if a caller wanted to. The result is the same
   *  handle-and-collection `openCollection` answers, which is what leaves the
   *  renderer one thing to do afterwards rather than two. */
  createCollection(name: string): Promise<ApiCollectionsCreateResult> {
    return this.dispatcher.call<ApiCollectionsCreateResult>('api.collections.create', { name })
  }

  /** Drop the folder from the opened list and stop resolving its handle. An
   *  empty result is still the contract. */
  closeCollection(handle: string): Promise<ApiCollectionsCloseResult> {
    return this.dispatcher.call<ApiCollectionsCloseResult>('api.collections.close', { handle })
  }

  /** ONE environment whole — the values and the route, which the collection
   *  listing deliberately does not carry (§6.4). This is what an editor
   *  reads before it can show a person what they are about to change. */
  readEnvironment(handle: string, relPath: string): Promise<ApiEnvironmentReadResult> {
    return this.dispatcher.call<ApiEnvironmentReadResult>('api.environment.read', {
      handle,
      relPath,
    })
  }

  /** Put what the editor holds into the file, creating it when nothing
   *  occupies the name — a write to a free name is how an environment comes
   *  to exist, so there is no second create call. */
  writeEnvironment(
    handle: string,
    relPath: string,
    environment: ApiEnvironment,
  ): Promise<ApiEnvironmentWriteResult> {
    return this.dispatcher.call<ApiEnvironmentWriteResult>('api.environment.write', {
      handle,
      relPath,
      environment,
    })
  }

  /** Remove one request file. The tree is what says it is gone: the folder
   *  is re-read afterwards, so the row leaves the same way a colleague's
   *  `git pull` would have taken it (§6.1). */
  deleteRequest(handle: string, relPath: string): Promise<ApiRequestDeleteResult> {
    return this.dispatcher.call<ApiRequestDeleteResult>('api.request.delete', {
      handle,
      relPath,
    })
  }

  /** One request, exactly as its file has it — nothing resolved on the way
   *  out, because the file is the truth and the form is a projection of it
   *  (design §6.4). */
  readRequest(handle: string, relPath: string): Promise<ApiRequestReadResult> {
    return this.dispatcher.call<ApiRequestReadResult>('api.request.read', { handle, relPath })
  }

  /** Put what the form holds into the file. */
  writeRequest(
    handle: string,
    relPath: string,
    request: ApiRequest,
  ): Promise<ApiRequestWriteResult> {
    return this.dispatcher.call<ApiRequestWriteResult>('api.request.write', {
      handle,
      relPath,
      request,
    })
  }

  /** Perform the exchange the FILE describes, under the environment the
   *  caller names.
   *
   *  It takes no request value: what goes out is what is on disk, which is
   *  why the store writes an edited draft before it sends.
   *
   *  `envRelPath` is a PATH inside the collection, addressed exactly as the
   *  request is (§13.1) — and never the environment's name, although the
   *  binding key the backend builds is keyed by that name. The name lives
   *  inside the file and the backend reads it there, in the same breath as
   *  the address and the route (capability.SendInputs); a renderer that sent
   *  the name as well would be a second answer to "which environment is
   *  this", and the two would agree until somebody renamed an environment
   *  without renaming its file. '' is no environment, which is the request
   *  as written on the direct route. */
  sendRequest(handle: string, relPath: string, envRelPath: string): Promise<ApiRequestSendResult> {
    return this.dispatcher.call<ApiRequestSendResult>('api.request.send', {
      handle,
      relPath,
      envRelPath,
    })
  }

  /** Convert a Postman v2.1 export into a collection folder at `dest`, and
   *  answer what the conversion did NOT carry over — a result rather than a
   *  log line, so a soft degrade is visible in the product. */
  importPostman(path: string, dest: string): Promise<ApiImportPostmanResult> {
    return this.dispatcher.call<ApiImportPostmanResult>('api.import.postman', { path, dest })
  }

  /** Convert one pasted curl command line into a request. The line is
   *  PARSED, never executed (design §10), and an import never fires
   *  anything: this answers a value, and sending it is a separate gesture. */
  importCurl(line: string): Promise<ApiImportCurlResult> {
    return this.dispatcher.call<ApiImportCurlResult>('api.import.curl', { line })
  }
}

/**
 * The native directory picker, as this surface consumes it: the chosen
 * ABSOLUTE path, or an empty one when the person changed their mind.
 *
 * Structural rather than the generated `dialog.openDirectory` type, and
 * deliberately so — `key-material-input.tsx` states the same shape for
 * `dialog.openFile` for the same reason. A component prop is not a wire
 * declaration: the client that OWNS the call (`dialog-client.ts`) is typed
 * from the contract, and this is the one field of its result that the
 * workbench uses. Nothing here decodes a payload.
 *
 * A function rather than the client itself, because the surface may hold it
 * detached; `secrets.tsx` and `connections.tsx` bind `openFileDialog` the
 * same way.
 */
export type DirectoryPicker = () => Promise<{ path: string }>

/**
 * Bind the directory picker off the dialog client, when the build has one.
 *
 * `dialog.openDirectory` and its client method are the OTHER half of this
 * change (nocx-39jek) and land beside it. Until both halves are on one tree
 * the method is not on `DialogClient`'s type, so the composition root asks
 * the object rather than the type — which is also the runtime truth it wants
 * either way: absent means the workbench offers no Browse control, and the
 * `make dev-web` harness, which has no Wails and answers `-32601`, is
 * exactly that case.
 *
 * ONE cast, in one place, and it comes out the moment both halves are
 * committed together — after that the client satisfies the shape statically
 * and this becomes `() => client.openDirectoryDialog()`.
 */
export function directoryPicker(client: object): DirectoryPicker | undefined {
  if (!('openDirectoryDialog' in client)) return undefined
  const carrier = client as { openDirectoryDialog: DirectoryPicker }
  return () => carrier.openDirectoryDialog()
}

/**
 * How the workbench learns that a collection folder changed underneath it.
 *
 * IT IS THE FILES PANEL'S MECHANISM, NOT A SECOND ONE. A collection is a
 * folder on disk that a `git pull`, a neighbouring editor or a colleague's
 * branch can rewrite while the tree is on screen, and the product already
 * answers "how does a surface learn a directory changed": `files.watch`, plus
 * the `files.changed` invalidation it turns on. Answering it again inside
 * `api.*` would be two owners of one behaviour — they would agree everywhere
 * anybody looked and disagree the day one of them was edited (AGENTS.md).
 *
 * The port is the SLICE of `files.*` the workbench needs, not the Files
 * panel's client: `files.*` belongs to another module (AD-8), so this
 * declares what the workbench requires and the composition root binds it off
 * the one files client. Nothing here decodes a payload — every shape is the
 * generated contract type.
 *
 * `localSession` is why this is a capability and not a guarantee.
 * `files.open` mints a binding for a SESSION the connection owns, and it
 * builds that session's provider — so a collection folder, which is
 * backend-LOCAL (design §13.1), can only be watched through a LOCAL session.
 * A window with no local session yet has nothing to open a binding against
 * and answers null; the workbench then simply does not watch, which is the
 * one case the header's Refresh is still the whole answer for.
 */
export interface CollectionWatchPort {
  /** The local session a watch binding may be opened against, or null when
   *  this window has none. */
  localSession(): string | null
  open(sessionId: string, rootPath?: string): Promise<FilesOpenResult>
  /** REPLACE the binding's watch set. The backend diffs, so a collection that
   *  has been closed cannot leak a watch, and the swap is atomic: a newly
   *  added path that fails to establish does not take the healthy ones down
   *  with it. */
  watch(bindingId: string, paths: string[]): Promise<FilesWatchResult>
  close(bindingId: string): Promise<FilesCloseResult>
  /** The server-initiated invalidation. Returns the unsubscribe. */
  subscribeChanged(handler: (params: FilesChanged) => void): () => void
  /** The transport re-attached (AD-9): the binding the dead connection minted
   *  is gone with it, so the watch has to be established again. */
  onConnect(handler: () => void): () => void
}

/** The workbench's entire backend surface, so a test can substitute a fake. */
export interface ApiWorkbenchServices {
  listCollections(): Promise<ApiCollectionsListResult>
  openCollection(path: string): Promise<ApiCollectionsOpenResult>
  createCollection(name: string): Promise<ApiCollectionsCreateResult>
  closeCollection(handle: string): Promise<ApiCollectionsCloseResult>
  readEnvironment(handle: string, relPath: string): Promise<ApiEnvironmentReadResult>
  writeEnvironment(
    handle: string,
    relPath: string,
    environment: ApiEnvironment,
  ): Promise<ApiEnvironmentWriteResult>
  readRequest(handle: string, relPath: string): Promise<ApiRequestReadResult>
  writeRequest(handle: string, relPath: string, request: ApiRequest): Promise<ApiRequestWriteResult>
  deleteRequest(handle: string, relPath: string): Promise<ApiRequestDeleteResult>
  sendRequest(handle: string, relPath: string, envRelPath: string): Promise<ApiRequestSendResult>
  importPostman(path: string, dest: string): Promise<ApiImportPostmanResult>
  importCurl(line: string): Promise<ApiImportCurlResult>
  /**
   * The SSH connections this window knows about, for an environment that
   * routes through one (§6.5).
   *
   * ABSENT rather than empty when this build has no profile store, for the
   * same reason `openDirectory` is: optionality IS the capability, so the
   * environment editor offers "through a connection" only where there are
   * connections to name — a picker over nothing is a control that governs
   * nothing.
   *
   * It is another domain's method reached through another client (AD-8):
   * `profiles.list` belongs to ProfileClient, and the composition root binds
   * it here rather than this client learning to speak it.
   */
  listConnections?: () => Promise<readonly ApiConnection[]>
  /**
   * The native directory picker, when the backend offers one — and ABSENT,
   * not a function that rejects, when it does not.
   *
   * Optionality is the capability. The workbench draws a Browse control only
   * where this exists, so the `make dev-web` harness — no Wails, every
   * `dialog.*` call answered `-32601` — shows no control rather than one
   * that fails when pressed. A rejecting stub would put the refusal after
   * the click, which is the broken-looking fallback design §9 asks us not to
   * ship.
   */
  openDirectory?: DirectoryPicker
  /**
   * How the workbench notices a collection folder changed — and ABSENT when
   * this build cannot watch, for the same reason `openDirectory` is.
   *
   * Optionality is the capability again, and here it has two causes rather
   * than one: a build with no filesystem wired answers `-32601` to every
   * `files.*` call, and a window with no LOCAL session has nothing to open a
   * binding against. In both cases the workbench falls back to the thing that
   * has always worked — the header's Refresh — instead of showing a tree that
   * quietly stopped following the disk.
   */
  watchCollections?: CollectionWatchPort
}

/** One connection an environment may route through: the id the route
 *  stores, and the name a person chose for it. Two fields and no more —
 *  the API surface names a connection, it does not describe one. */
export interface ApiConnection {
  id: string
  name: string
}

/** Real implementation over the dispatcher.
 *
 *  `picker` is threaded through rather than built here: `dialog.*` is not an
 *  api.* method and this client does not own it (AD-8). The composition root
 *  binds it off the one dialog client and hands it in — and `watchCollections`
 *  arrives the same way, off the one files client, for exactly the same
 *  reason. */
export function createApiWorkbenchServices(
  dispatcher: Dispatcher,
  picker?: DirectoryPicker,
  watchCollections?: CollectionWatchPort,
  listConnections?: () => Promise<readonly ApiConnection[]>,
): ApiWorkbenchServices {
  const client = new ApiClient(dispatcher)
  return {
    ...(picker ? { openDirectory: picker } : {}),
    ...(watchCollections ? { watchCollections } : {}),
    ...(listConnections ? { listConnections } : {}),
    listCollections: () => client.listCollections(),
    openCollection: (path) => client.openCollection(path),
    createCollection: (name) => client.createCollection(name),
    closeCollection: (handle) => client.closeCollection(handle),
    readEnvironment: (handle, relPath) => client.readEnvironment(handle, relPath),
    writeEnvironment: (handle, relPath, environment) =>
      client.writeEnvironment(handle, relPath, environment),
    deleteRequest: (handle, relPath) => client.deleteRequest(handle, relPath),
    readRequest: (handle, relPath) => client.readRequest(handle, relPath),
    writeRequest: (handle, relPath, request) => client.writeRequest(handle, relPath, request),
    sendRequest: (handle, relPath, envRelPath) => client.sendRequest(handle, relPath, envRelPath),
    importPostman: (path, dest) => client.importPostman(path, dest),
    importCurl: (line) => client.importCurl(line),
  }
}
