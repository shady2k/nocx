// FilesClient — the files.* control-plane seam (design §5.2). One client,
// one method per wire call, every result a GENERATED type: the renderer
// declares nothing of its own, because a hand-written type can want a field
// the wire does not carry, which is the defect the whole contracts/
// directory exists to prevent. The panel consumes it through
// FilesPanelServices so tests can substitute a fake without a WebSocket
// (the ports pattern, nocx-wzc4).

import type { Dispatcher } from '../dispatcher'
import type { FilesOpenResult } from '../generated/files.open'
import type { FilesListResult } from '../generated/files.list'
import type { FilesReadResult } from '../generated/files.read'
import type { FilesCloseResult } from '../generated/files.close'
import type { FilesWatchResult } from '../generated/files.watch'
import type { FilesVisibleResult } from '../generated/files.visible'
import type { FilesRevealResult } from '../generated/files.reveal'
import type { FilesChanged } from '../generated/files.changed'

class FilesClient {
  constructor(private dispatcher: Dispatcher) {}

  /** Open a binding for one session: the backend issues the bindingId every
   *  later call echoes, plus the root the tree starts at (D1, D2). rootPath
   *  is the verified OSC 7 cwd when the composition layer has one — the one
   *  client-minted input that is an address, never an identity (D4). */
  open(sessionId: string, rootPath?: string): Promise<FilesOpenResult> {
    return this.dispatcher.call<FilesOpenResult>(
      'files.open',
      rootPath ? { sessionId, rootPath } : { sessionId },
    )
  }

  /** One page of one directory: ok, tooLarge or timedOut — state is the
   *  discriminator and the caller switches on it first (D14). */
  list(bindingId: string, path: string, offset: number, limit: number): Promise<FilesListResult> {
    return this.dispatcher.call<FilesListResult>('files.list', { bindingId, path, offset, limit })
  }

  /** Bounded content plus the canonical identity of what was actually read —
   *  the viewer's singletonKey input; the panel reads to obtain a file's
   *  canonical before handing it to the opener (D12). */
  read(bindingId: string, path: string, maxBytes: number): Promise<FilesReadResult> {
    return this.dispatcher.call<FilesReadResult>('files.read', { bindingId, path, maxBytes })
  }

  /** Replace the binding's watch set: the set the panel currently wants,
   *  root and every expanded directory — the backend diffs, so collapsing
   *  a directory cannot leak a watch. Answers the refresh mode now serving
   *  the set (design §5.5), the only client-visible half of the watching
   *  machinery (D14). */
  watch(bindingId: string, paths: string[]): Promise<FilesWatchResult> {
    return this.dispatcher.call<FilesWatchResult>('files.watch', { bindingId, paths })
  }

  /** Whether the panel showing this binding's tree is on screen. While it
   *  is not, the backend's digest poll issues NO listing for this binding —
   *  a tick is a full enumeration of every watched directory, so a
   *  collapsed sidebar used to cost exactly as much as an open one. A
   *  hidden→visible edge polls once immediately, so nobody waits an
   *  interval for a panel they just opened (design §5.5). */
  visible(bindingId: string, visible: boolean): Promise<FilesVisibleResult> {
    return this.dispatcher.call<FilesVisibleResult>('files.visible', { bindingId, visible })
  }

  /** Show a path in the OS file manager. The backend refuses a remote
   *  binding rather than silently doing nothing (design §5.2); the panel
   *  renders that refusal — and the -32601 an unwired revealer answers —
   *  instead of stubbing either. */
  reveal(bindingId: string, path: string): Promise<FilesRevealResult> {
    return this.dispatcher.call<FilesRevealResult>('files.reveal', { bindingId, path })
  }

  /** Subscribe to the server-initiated files.changed notification (the
   *  SettingsObserver pattern): an invalidation carrying a dirty path and
   *  an optional rev — never entries, so exactly one code path re-renders
   *  a directory. Returns the unsubscribe. */
  subscribeFilesChanged(handler: (params: FilesChanged) => void): () => void {
    return this.dispatcher.subscribe('files.changed', (params: unknown) => {
      const p = params as FilesChanged
      if (p && typeof p.bindingId === 'string' && typeof p.path === 'string') handler(p)
    })
  }

  /** Reconnect hook: the watch set is re-sent so a WebSocket drop cannot
   *  silently detach the panel from the change stream (the backend's dirty
   *  set is flushed to the re-attached subscriber, and re-sending the set
   *  re-establishes the baselines idempotently). */
  onConnect(handler: () => void): () => void {
    return this.dispatcher.onConnect(handler)
  }

  /** Release the binding — its provider, its pooled SSH reference, its
   *  watches. An empty result is still the contract. */
  close(bindingId: string): Promise<FilesCloseResult> {
    return this.dispatcher.call<FilesCloseResult>('files.close', { bindingId })
  }
}

/** The panel's entire backend surface, so a test can substitute a fake. */
export interface FilesPanelServices {
  open(sessionId: string, rootPath?: string): Promise<FilesOpenResult>
  list(bindingId: string, path: string, offset: number, limit: number): Promise<FilesListResult>
  read(bindingId: string, path: string, maxBytes: number): Promise<FilesReadResult>
  watch(bindingId: string, paths: string[]): Promise<FilesWatchResult>
  visible(bindingId: string, visible: boolean): Promise<FilesVisibleResult>
  reveal(bindingId: string, path: string): Promise<FilesRevealResult>
  subscribeFilesChanged(handler: (params: FilesChanged) => void): () => void
  onConnect(handler: () => void): () => void
  close(bindingId: string): Promise<FilesCloseResult>
}

/** Real implementation over the dispatcher. */
export function createFilesPanelServices(dispatcher: Dispatcher): FilesPanelServices {
  const client = new FilesClient(dispatcher)
  return {
    open: (sessionId, rootPath) => client.open(sessionId, rootPath),
    list: (bindingId, path, offset, limit) => client.list(bindingId, path, offset, limit),
    read: (bindingId, path, maxBytes) => client.read(bindingId, path, maxBytes),
    watch: (bindingId, paths) => client.watch(bindingId, paths),
    visible: (bindingId, isVisible) => client.visible(bindingId, isVisible),
    reveal: (bindingId, path) => client.reveal(bindingId, path),
    subscribeFilesChanged: (handler) => client.subscribeFilesChanged(handler),
    onConnect: (handler) => client.onConnect(handler),
    close: (bindingId) => client.close(bindingId),
  }
}
