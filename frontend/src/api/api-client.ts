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
import type { ApiCollectionsCloseResult } from '../generated/api.collections.close'
import type { ApiRequestReadResult } from '../generated/api.request.read'
import type { ApiRequestWriteResult } from '../generated/api.request.write'
import type { ApiRequestSendResult } from '../generated/api.request.send'
import type { ApiImportPostmanResult } from '../generated/api.import.postman'
import type { ApiImportCurlResult } from '../generated/api.import.curl'
import type { ApiRequest } from './api-model'

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

  /** Drop the folder from the opened list and stop resolving its handle. An
   *  empty result is still the contract. */
  closeCollection(handle: string): Promise<ApiCollectionsCloseResult> {
    return this.dispatcher.call<ApiCollectionsCloseResult>('api.collections.close', { handle })
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

  /** Perform the exchange the FILE describes. It takes no request value:
   *  what goes out is what is on disk, which is why the store writes an
   *  edited draft before it sends. */
  sendRequest(handle: string, relPath: string): Promise<ApiRequestSendResult> {
    return this.dispatcher.call<ApiRequestSendResult>('api.request.send', { handle, relPath })
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

/** The workbench's entire backend surface, so a test can substitute a fake. */
export interface ApiWorkbenchServices {
  listCollections(): Promise<ApiCollectionsListResult>
  openCollection(path: string): Promise<ApiCollectionsOpenResult>
  closeCollection(handle: string): Promise<ApiCollectionsCloseResult>
  readRequest(handle: string, relPath: string): Promise<ApiRequestReadResult>
  writeRequest(handle: string, relPath: string, request: ApiRequest): Promise<ApiRequestWriteResult>
  sendRequest(handle: string, relPath: string): Promise<ApiRequestSendResult>
  importPostman(path: string, dest: string): Promise<ApiImportPostmanResult>
  importCurl(line: string): Promise<ApiImportCurlResult>
}

/** Real implementation over the dispatcher. */
export function createApiWorkbenchServices(dispatcher: Dispatcher): ApiWorkbenchServices {
  const client = new ApiClient(dispatcher)
  return {
    listCollections: () => client.listCollections(),
    openCollection: (path) => client.openCollection(path),
    closeCollection: (handle) => client.closeCollection(handle),
    readRequest: (handle, relPath) => client.readRequest(handle, relPath),
    writeRequest: (handle, relPath, request) => client.writeRequest(handle, relPath, request),
    sendRequest: (handle, relPath) => client.sendRequest(handle, relPath),
    importPostman: (path, dest) => client.importPostman(path, dest),
    importCurl: (line) => client.importCurl(line),
  }
}
