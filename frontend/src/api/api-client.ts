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
// again. Only `openCollection` and `importPostman` can put a filesystem path
// on the wire — and the import only when the gesture answered with one
// (ImportSource); every other method addresses a folder by that handle plus a path
// RELATIVE to it. The Go side refuses a stray one strictly
// (decodeAPIParams); this is the half that never spells one, and
// api-client.test.ts asserts it across the whole surface rather than per
// method, because it is a property of the surface.

import type { Dispatcher } from '../dispatcher'
import type { ApiCollectionsListResult } from '../generated/api.collections.list'
import type { ApiCollectionsOpenResult } from '../generated/api.collections.open'
import type { ApiCollectionsCreateResult } from '../generated/api.collections.create'
import type { ApiCollectionsCreateFolderResult } from '../generated/api.collections.createFolder'
import type { ApiCollectionsCloseResult } from '../generated/api.collections.close'
import type { ApiEnvironmentReadResult } from '../generated/api.environment.read'
import type { ApiFolderReadResult } from '../generated/api.folder.read'
import type { ApiFolderWriteResult } from '../generated/api.folder.write'
import type { ApiEnvironmentWriteResult } from '../generated/api.environment.write'
import type { ApiEnvironmentBindSecretResult } from '../generated/api.environment.bindSecret'
import type { ApiRequestDeleteResult } from '../generated/api.request.delete'
import type { ApiRequestReadResult } from '../generated/api.request.read'
import type { ApiRequestScopeResult } from '../generated/api.request.scope'
import type { ApiRequestWriteResult } from '../generated/api.request.write'
import type { ApiRequestMoveResult } from '../generated/api.request.move'
import type { ApiRequestSendResult } from '../generated/api.request.send'
import type { ApiRequestCancelResult } from '../generated/api.request.cancel'
import type { ApiImportPostmanResult } from '../generated/api.import.postman'
import type { ApiImportCurlResult } from '../generated/api.import.curl'
import type { FilesOpenResult } from '../generated/files.open'
import type { FilesWatchResult } from '../generated/files.watch'
import type { FilesCloseResult } from '../generated/files.close'
import type { FilesChanged } from '../generated/files.changed'
import type { FilesDropped } from '../generated/files.dropped'
import type { ApiEnvironment, ApiRequest, ApiRoute } from './api-model'
type ApiRequestVariable = ApiRequest['variables'][number]

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

  /**
   * Make ONE folder inside a collection the user has open.
   *
   * The grammar is `createCollection`'s one level down: a NAME that is a
   * single component, and the EXISTING folder to put it in, addressed by the
   * handle plus a path relative to it (§13.1). `''` is the collection root.
   *
   * NESTING IS REPEATED CALLS, and the name is never a path. A single
   * relative path with the intermediate folders made along the way succeeds
   * for a request nobody made — a misspelled month would silently mint the
   * misspelling — so the caller names one folder at a time and is told when
   * the parent is not there. The renderer's half of that is here: it passes
   * back the `relPath` the last call ANSWERED as the next call's parent,
   * rather than joining a parent and a name itself.
   */
  createFolder(
    handle: string,
    parentRelPath: string,
    name: string,
  ): Promise<ApiCollectionsCreateFolderResult> {
    return this.dispatcher.call<ApiCollectionsCreateFolderResult>('api.collections.createFolder', {
      handle,
      parentRelPath,
      name,
    })
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
  /** Read the folder-level facts the page edits. The collection listing only
   * carries presence, so this call is the editor's whole read. */
  readFolder(handle: string, relPath: string): Promise<ApiFolderReadResult> {
    return this.dispatcher.call<ApiFolderReadResult>('api.folder.read', { handle, relPath })
  }

  /** Replace the folder-level facts and return the canonical persisted rows.
   * An empty list is the backend's absence state. */
  writeFolder(
    handle: string,
    relPath: string,
    variables: ApiFolderWriteResult['variables'],
  ): Promise<ApiFolderWriteResult> {
    return this.dispatcher.call<ApiFolderWriteResult>('api.folder.write', {
      handle,
      relPath,
      variables,
    })
  }

  /** Give a secret variable its VALUE.
   *
   *  THE ONE METHOD ON THIS CLIENT THAT SENDS A CREDENTIAL. It goes one way:
   *  the value into the vault, under the binding this collection and
   *  environment own, while the environment FILE keeps only the name — there
   *  is no field in that format a value could be written into (design §8).
   *  Nothing echoes it back: the result is empty because the identifier for
   *  stored credential material never leaves the backend (ADR-0011) and the
   *  value came from here, so returning either would hand back the one thing
   *  this method exists to take away.
   *
   *  Until this, only an IMPORT could mint a binding, so a variable a person
   *  declared secret in the editor had no way to be given a value at all. */
  bindSecret(
    handle: string,
    relPath: string,
    variable: string,
    value: string,
  ): Promise<ApiEnvironmentBindSecretResult> {
    return this.dispatcher.call<ApiEnvironmentBindSecretResult>('api.environment.bindSecret', {
      handle,
      relPath,
      variable,
      value,
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

  /** Explain the effective request scope using the draft's own rows. */
  requestScope(
    handle: string,
    relPath: string,
    envRelPath: string,
    variables: readonly ApiRequestVariable[],
  ): Promise<ApiRequestScopeResult> {
    return this.dispatcher.call<ApiRequestScopeResult>('api.request.scope', {
      handle,
      relPath,
      envRelPath,
      variables,
    })
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
  /** Move one request file to another path INSIDE the same collection — a
   *  rename on the backend, never a write-then-delete. The result carries
   *  the NEW relPath, which is the address the caller must use afterwards:
   *  a request open in the form has to be re-pointed at it, and deriving
   *  the new path itself would be the second answer this surface refuses
   *  to make. */
  moveRequest(handle: string, relPath: string, toRelPath: string): Promise<ApiRequestMoveResult> {
    return this.dispatcher.call<ApiRequestMoveResult>('api.request.move', {
      handle,
      relPath,
      toRelPath,
    })
  }
  sendRequest(
    handle: string,
    relPath: string,
    envRelPath: string,
    token: string,
  ): Promise<ApiRequestSendResult> {
    return this.dispatcher.call<ApiRequestSendResult>('api.request.send', {
      handle,
      relPath,
      envRelPath,
      token,
    })
  }

  /** Stop the exchange running under `token`.
   *
   *  THE TOKEN IS OURS, and that is the whole design of this pair. The
   *  dispatcher mints a JSON-RPC id per call and consumes it when the result
   *  arrives; it is never handed to the caller that asked, and opening it up
   *  so one button could name a request would be a second addressing scheme
   *  over the same thing. So the store mints a name, sends it, and stops it
   *  by that name.
   *
   *  It answers EMPTY. The stopped exchange reports itself on the
   *  `api.request.send` result of the very request that was stopped, which
   *  comes back as `outcome: "stopped"` — two methods reporting one
   *  exchange's end would be two accounts of it, and this surface would have
   *  to decide which to believe. A token naming nothing that is running is
   *  REFUSED rather than answered, because "there was nothing to stop" and
   *  "it is stopped" are different facts. */
  cancelRequest(token: string): Promise<ApiRequestCancelResult> {
    return this.dispatcher.call<ApiRequestCancelResult>('api.request.cancel', { token })
  }

  /** Convert a Postman v2.1 export into a collection folder at `dest`, and
   *  answer what the conversion did NOT carry over — a result rather than a
   *  log line, so a soft degrade is visible in the product.
   *
   *  The export arrives as an ImportSource: a path on the backend's machine,
   *  the document itself, or a URL the backend fetches over a route. The
   *  three spread onto the params as the one field each carries, so this
   *  method never has to know which is which. */
  importPostman(source: ImportSource, dest: string): Promise<ApiImportPostmanResult> {
    return this.dispatcher.call<ApiImportPostmanResult>('api.import.postman', { ...source, dest })
  }

  /** Convert one pasted curl command line into a request. The line is
   *  PARSED, never executed (design §10), and an import never fires
   *  anything: this answers a value, and sending it is a separate gesture. */
  importCurl(line: string): Promise<ApiImportCurlResult> {
    return this.dispatcher.call<ApiImportCurlResult>('api.import.curl', { line })
  }
}

/**
 * What a native picker answers: the chosen ABSOLUTE path, or an empty one
 * when the person changed their mind.
 *
 * Structural rather than the generated `dialog.*` types, and deliberately
 * so — `key-material-input.tsx` states the same shape for `dialog.openFile`
 * for the same reason. A component prop is not a wire declaration: the
 * client that OWNS the call (`dialog-client.ts`) is typed from the contract,
 * and this is the one field of its result that the workbench uses. Nothing
 * here decodes a payload.
 */
type ChosenPath = { path: string }

/**
 * WHERE THE EXPORT AN IMPORT READS COMES FROM — the one question
 * `api.import.postman` asks about its source, with three answers rather than
 * one, chosen by what the gesture could answer with.
 *
 * `path` names a file on the BACKEND'S machine, which is the narrow case:
 * `apicoll.DefaultRoot()` is `paths.DataDir()` of the process running Go,
 * and `make dev-web` is documented as forwarding both ports over SSH, so
 * that machine is not always the person's. Typed into the field, or handed
 * over by the Wails window's own drop — where Go took the path off the
 * runtime and the backend IS the person's machine.
 *
 * `document` is the export itself: a browser drop and the kit's file input
 * both yield bytes, and bytes reach a backend wherever it runs (spec §1a).
 * `apiimport.ImportInto` already takes a READER; only
 * `capability.ImportPostman` opened a file first.
 *
 * `url` names where the backend should FETCH it, and it is the general case
 * in the direction the document cannot serve: an export behind a network the
 * renderer is not on. The `route` rides with the url because it is part of
 * how that document is reached — and an absent route IS the direct one, so
 * it is absent from the object rather than present and undefined, which is a
 * key `decodeAPIParams` would refuse once the source is spread.
 *
 * Never two of them. A union rather than three optional fields, because "a
 * path AND a document" is a state with no meaning and the type is where it
 * stops being expressible.
 */
export type ImportSource =
  { path: string } | { document: string } | { url: string; route?: ApiRoute }

/**
 * The native directory picker, as this surface consumes it.
 *
 * A function rather than the client itself, because the surface may hold it
 * detached; `secrets.tsx` and `connections.tsx` bind `openFileDialog` the
 * same way.
 */
export type DirectoryPicker = () => Promise<ChosenPath>

/**
 * The native FILE picker, for the one thing this surface reads rather than
 * writes: a Postman export.
 *
 * Its own type beside DirectoryPicker although the shapes are identical,
 * because they are two capabilities and either can be absent on its own —
 * two `dialog.*` methods, each of which answers -32601 independently. A
 * single picker type would make "this build can choose a folder" and "this
 * build can choose a file" one fact, and the surface would then draw a
 * control for whichever it had not got.
 */
export type FilePicker = () => Promise<ChosenPath>

/**
 * The native window drop, as the workbench needs it.
 *
 * A THIRD optional capability beside the two pickers, and separate from them
 * for the reason they are separate from each other: each can be absent on its
 * own. This one is absent whenever there is no Wails runtime — `make dev-web`
 * and the e2e harness — and that is a different question from whether this
 * window has a local session, which is why the port answers the session and
 * the composition root decides whether the port exists at all.
 *
 * ABSENT IS NOT "NO DROP" — it is the OTHER drop. Where there is no runtime a
 * drop is an ordinary DOM event carrying `File` objects, and the workbench
 * takes those and imports the document itself (ImportSource). This port is
 * the route that answers with a PATH, and its presence is also how the
 * surface knows a DOM drop belongs to Go rather than to it.
 *
 * There is no `onDrop` and cannot be one: in the Wails window the drop never
 * becomes a DOM event carrying a path, so the answer arrives as a
 * `files.dropped` notification.
 */
export interface NativeDropPort {
  /** The local session a drop belongs to, READ AT CALL TIME — a latched id
   *  outlives its tab, and the backend refuses a target naming a session it
   *  does not have open. */
  session(): string | null
  /** files.dropped. Returns the unsubscribe. */
  subscribe(handler: (p: FilesDropped) => void): () => void
}

/**
 * The `dialog.*` methods the workbench binds its pickers off — the SLICE of
 * the one dialog client this surface needs (AD-8), declared structurally so
 * this module never learns to speak that domain.
 */
export interface SystemDialogs {
  openFileDialog(): Promise<ChosenPath>
  openDirectoryDialog(): Promise<ChosenPath>
}

/** The two native pickers, each present only where one can be opened. */
export interface NativePickers {
  directory?: DirectoryPicker
  file?: FilePicker
}

/**
 * Bind the native pickers off the dialog client — WHERE THIS BUILD HAS A
 * RUNTIME THAT CAN SERVE `dialog.*`, and nowhere else.
 *
 * IT USED TO ASK THE OBJECT, and that was never a probe (nocx-h9f8y).
 * `'openFileDialog' in client` is TRUE ON EVERY BUILD — the client is a class
 * instance and the method is on its prototype — so a build with no Wails
 * (`make dev-web`, devharness, the e2e container, a nocx reached over a
 * forwarded port) was handed a picker that answers -32601 when it is pressed.
 * And a handed-in picker is drawn INSTEAD of the kit's own FileInput, so the
 * route that cannot travel hid the one that can: the person pressed a
 * control, read an error, and only then met the picker that would have
 * worked (drop-zone.tsx). The capability "retired" after the failure the
 * capability check exists to prevent.
 *
 * `served` is the question actually being asked, and it is asked ONCE — by
 * the composition root, off `hasWailsWebview()`, the same reading that
 * decides whether there is a native drop (main.tsx). It is passed rather
 * than asked here for the reason the kit's DropZone is passed it too: the
 * answer has an owner and a place where it is read (wails-runtime.ts), and a
 * second reading is a second answer.
 *
 * BOTH PICKERS, ONE CALL, because there is only ever one reason for neither
 * to exist and this is it. They are still two capabilities that retire
 * INDEPENDENTLY — either `dialog.*` method can report itself unavailable on
 * its own once the runtime is there, and the surface keeps a signal per
 * picker for exactly that (api-pane.tsx).
 */
export function nativePickers(client: SystemDialogs, served: boolean): NativePickers {
  if (!served) return {}
  return {
    directory: () => client.openDirectoryDialog(),
    file: () => client.openFileDialog(),
  }
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
  createFolder(
    handle: string,
    parentRelPath: string,
    name: string,
  ): Promise<ApiCollectionsCreateFolderResult>
  closeCollection(handle: string): Promise<ApiCollectionsCloseResult>
  readEnvironment(handle: string, relPath: string): Promise<ApiEnvironmentReadResult>
  writeEnvironment(
    handle: string,
    relPath: string,
    environment: ApiEnvironment,
  ): Promise<ApiEnvironmentWriteResult>
  readFolder(handle: string, relPath: string): Promise<ApiFolderReadResult>
  writeFolder(
    handle: string,
    relPath: string,
    variables: ApiFolderWriteResult['variables'],
  ): Promise<ApiFolderWriteResult>
  /** Give a secret variable its value — the one call that carries a
   *  credential, and it carries it one way (ApiClient.bindSecret). */
  bindSecret(
    handle: string,
    relPath: string,
    variable: string,
    value: string,
  ): Promise<ApiEnvironmentBindSecretResult>
  readRequest(handle: string, relPath: string): Promise<ApiRequestReadResult>
  requestScope(
    handle: string,
    relPath: string,
    envRelPath: string,
    variables: readonly ApiRequestVariable[],
  ): Promise<ApiRequestScopeResult>
  writeRequest(handle: string, relPath: string, request: ApiRequest): Promise<ApiRequestWriteResult>
  moveRequest(handle: string, relPath: string, toRelPath: string): Promise<ApiRequestMoveResult>
  deleteRequest(handle: string, relPath: string): Promise<ApiRequestDeleteResult>
  sendRequest(
    handle: string,
    relPath: string,
    envRelPath: string,
    token: string,
  ): Promise<ApiRequestSendResult>
  /** Stop the exchange running under a token this surface minted. */
  cancelRequest(token: string): Promise<ApiRequestCancelResult>
  importPostman(source: ImportSource, dest: string): Promise<ApiImportPostmanResult>
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
   * The native file picker, when the backend offers one — and ABSENT, not a
   * function that rejects, when it does not. Optionality is the capability,
   * exactly as it is for `openDirectory`: the ask draws a Browse control on
   * the export field only where there is a picker to reach, so the
   * `make dev-web` harness shows a typeable field rather than a button that
   * fails when pressed.
   */
  openFile?: FilePicker
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
  /**
   * The native window drop, when this build has one — and ABSENT, not a port
   * that never fires, when it does not.
   *
   * Optionality is the capability for a third time, and the cause here is
   * neither picker's: there is no Wails runtime. What that absence means has
   * changed and the comment here said the older thing: a browser drop hands
   * the renderer `File` objects with no location, and since
   * `api.import.postman` also takes the DOCUMENT, no location is needed —
   * bytes reach the backend wherever it runs. So the ask offers a drop under
   * `make dev-web` too, and this port's absence selects which route it is.
   */
  nativeDrop?: NativeDropPort
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
  files?: FilePicker,
  nativeDrop?: NativeDropPort,
): ApiWorkbenchServices {
  const client = new ApiClient(dispatcher)
  return {
    ...(picker ? { openDirectory: picker } : {}),
    ...(files ? { openFile: files } : {}),
    ...(watchCollections ? { watchCollections } : {}),
    ...(listConnections ? { listConnections } : {}),
    ...(nativeDrop ? { nativeDrop } : {}),
    listCollections: () => client.listCollections(),
    openCollection: (path) => client.openCollection(path),
    createCollection: (name) => client.createCollection(name),
    createFolder: (handle, parentRelPath, name) => client.createFolder(handle, parentRelPath, name),
    closeCollection: (handle) => client.closeCollection(handle),
    readEnvironment: (handle, relPath) => client.readEnvironment(handle, relPath),
    writeEnvironment: (handle, relPath, environment) =>
      client.writeEnvironment(handle, relPath, environment),
    readFolder: (handle, relPath) => client.readFolder(handle, relPath),
    writeFolder: (handle, relPath, variables) => client.writeFolder(handle, relPath, variables),
    bindSecret: (handle, relPath, variable, value) =>
      client.bindSecret(handle, relPath, variable, value),
    readRequest: (handle, relPath) => client.readRequest(handle, relPath),
    requestScope: (handle, relPath, envRelPath, variables) =>
      client.requestScope(handle, relPath, envRelPath, variables),
    writeRequest: (handle, relPath, request) => client.writeRequest(handle, relPath, request),
    deleteRequest: (handle, relPath) => client.deleteRequest(handle, relPath),
    moveRequest: (handle, relPath, toRelPath) => client.moveRequest(handle, relPath, toRelPath),
    sendRequest: (handle, relPath, envRelPath, token) =>
      client.sendRequest(handle, relPath, envRelPath, token),
    cancelRequest: (token) => client.cancelRequest(token),
    importPostman: (source, dest) => client.importPostman(source, dest),
    importCurl: (line) => client.importCurl(line),
  }
}
