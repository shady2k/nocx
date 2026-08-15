// GitClient — the git.* control-plane seam (design §5.2). One client, one
// method per wire call, every result a GENERATED type: the renderer declares
// nothing of its own, because a hand-written type can want a field the wire
// does not carry, which is the defect the whole contracts/ directory exists
// to prevent. The panel consumes it through a services object so tests can
// substitute a fake without a WebSocket (the files/ports pattern).

import type { Dispatcher } from '../dispatcher'
import type { GitOpenResult } from '../generated/git.open'
import type { GitStatusResult } from '../generated/git.status'
import type { GitDiffResult } from '../generated/git.diff'
import type { GitStageResult } from '../generated/git.stage'
import type { GitUnstageResult } from '../generated/git.unstage'
import type { GitStageAllResult } from '../generated/git.stageAll'
import type { GitUnstageAllResult } from '../generated/git.unstageAll'
import type { GitCommitResult } from '../generated/git.commit'
import type { GitHeadMessageResult } from '../generated/git.headMessage'
import type { GitLogResult } from '../generated/git.log'
import type { GitRemoteResult } from '../generated/git.remote'
import type { GitCloseResult } from '../generated/git.close'
import type { GitChangedNotification } from '../generated/git.changed'
import type { ShellFootprintConsentResult } from '../generated/shell.footprint.consent'
import type { ShellOpenUrl } from '../generated/shell.openUrl'

/** The sides of a diff are a closed set — the schema says so, and the panel
 *  switches on the row's list to pick one. */
export type GitDiffSide = 'staged' | 'unstaged' | 'untracked'

/** Not exported: the module's public surface is createGitPanelServices and
 *  the generated types. A class nobody outside constructs is an
 *  implementation detail, and exporting it would invite a second
 *  construction path past the services seam the panel and its tests share. */
class GitClient {
  constructor(private dispatcher: Dispatcher) {}

  /** Resolve the repository the shell is standing in and open a binding for
   *  it. The ONE client-minted input is the verified OSC 7 cwd, which is an
   *  address and never an identity: AD-1's 2026-08-02 amendment permits a
   *  typed fact derived from renderer state to cross, and files.open already
   *  sends the same one. `state` is the discriminator — switch on it first.
   *  The first status rides the result, so a panel open is one round trip. */
  open(sessionId: string, cwd?: string): Promise<GitOpenResult> {
    return this.dispatcher.call<GitOpenResult>('git.open', cwd ? { sessionId, cwd } : { sessionId })
  }

  status(bindingId: string): Promise<GitStatusResult> {
    return this.dispatcher.call<GitStatusResult>('git.status', { bindingId })
  }

  /** One side of one file, bounded. `state` distinguishes a diff from binary,
   *  too-large, gone, and empty — and empty is ordinary, not an error: the
   *  panel polls, so a row can be clicked in the same second an agent reverts
   *  the file. */
  diff(
    bindingId: string,
    path: string,
    side: GitDiffSide,
    maxBytes: number,
  ): Promise<GitDiffResult> {
    return this.dispatcher.call<GitDiffResult>('git.diff', { bindingId, path, side, maxBytes })
  }

  /** Stage the named paths. An empty array is a no-op and never means "all":
   *  "all" is stageAll, because under the status cap the rendered rows are a
   *  prefix, and staging a prefix while calling it everything is the
   *  complete-looking subset the design refuses (D19). */
  stage(bindingId: string, paths: string[]): Promise<GitStageResult> {
    return this.dispatcher.call<GitStageResult>('git.stage', { bindingId, paths })
  }

  unstage(bindingId: string, paths: string[]): Promise<GitUnstageResult> {
    return this.dispatcher.call<GitUnstageResult>('git.unstage', { bindingId, paths })
  }

  /** Whole-repository stage. Refused by the backend while any entry is
   *  conflicted — measured: git add -A marks a conflict resolved using a
   *  worktree file that still holds its markers. */
  stageAll(bindingId: string): Promise<GitStageAllResult> {
    return this.dispatcher.call<GitStageAllResult>('git.stageAll', { bindingId })
  }

  /** Whole-repository unstage, likewise refused during a conflicted merge:
   *  bare git reset deletes MERGE_HEAD, so the button would silently abort
   *  the merge. */
  unstageAll(bindingId: string): Promise<GitUnstageAllResult> {
    return this.dispatcher.call<GitUnstageAllResult>('git.unstageAll', { bindingId })
  }

  /** Commit the index. A failed commit is a STATE carrying git's own output,
   *  not a rejected call: the panel shows that output and keeps the message
   *  in the form, because a hook rejection is the likeliest outcome in this
   *  repository and losing the message with it would be the real defect. */
  commit(bindingId: string, message: string, amend: boolean): Promise<GitCommitResult> {
    return this.dispatcher.call<GitCommitResult>('git.commit', { bindingId, message, amend })
  }

  /** HEAD's full message, for the Amend prefill. Its own method rather than a
   *  status field: it is wanted once, when the box is ticked, and carrying it
   *  in status would read HEAD on every poll to answer a question nobody
   *  asked. */
  headMessage(bindingId: string): Promise<GitHeadMessageResult> {
    return this.dispatcher.call<GitHeadMessageResult>('git.headMessage', { bindingId })
  }

  /** The branch's recent commits, newest first, bounded by the backend's
   *  policy (D9). History does not change under the user the way the
   *  working tree does, so the panel reads it when it opens, on manual
   *  refresh and after a commit — never on the poll (D13). */
  log(bindingId: string): Promise<GitLogResult> {
    return this.dispatcher.call<GitLogResult>('git.log', { bindingId })
  }

  /** The raw URL of the remote the current branch tracks (brief,
   *  nocx-hc0m). The conversion to a web page is the renderer's — this
   *  method carries what git said, verbatim. */
  remote(bindingId: string): Promise<GitRemoteResult> {
    return this.dispatcher.call<GitRemoteResult>('git.remote', { bindingId })
  }

  /** Ask the backend (which owns the Wails runtime) to open a URL in the
   *  system browser — the NATIVE half of the url-opener capability
   *  (open-url.ts); the web path opens the tab in-renderer and never
   *  reaches this method. The backend refuses anything that is not an
   *  http(s) URL before the browser sees it, and reports itself
   *  unavailable when no native runtime exists — the panel says so. */
  openUrl(url: string): Promise<ShellOpenUrl> {
    return this.dispatcher.call<ShellOpenUrl>('shell.openUrl', { url })
  }
  /** The consent prompt's Accept: raise the session's machine to the
   *  relay tier (remote-helper design D8). The machine is resolved from
   *  the sessionId server-side — the renderer never sends a fingerprint.
   *  After it resolves, the panel re-opens through git.open, which now
   *  proceeds past consentRequired. */
  grantConsent(sessionId: string): Promise<ShellFootprintConsentResult> {
    return this.dispatcher.call<ShellFootprintConsentResult>('shell.footprint.consent', {
      sessionId,
    })
  }

  /** Release a binding. Also the repair for a stale open: a successful open
   *  the store has already superseded has registered a live repository on the
   *  backend, so dropping the response without this leaks it. */
  close(bindingId: string): Promise<GitCloseResult> {
    return this.dispatcher.call<GitCloseResult>('git.close', { bindingId })
  }

  /** Subscribe to the server-initiated git.changed notification: the binding
   *  is gone, and the only reason is a closed session. Server-initiated means
   *  nothing correlates it and no caller checks its shape, which is exactly
   *  where a defect would hide and why it has a contract of its own. Returns
   *  the unsubscribe. */
  subscribeGitChanged(handler: (params: GitChangedNotification) => void): () => void {
    return this.dispatcher.subscribe('git.changed', (params: unknown) => {
      const p = params as GitChangedNotification
      if (p && typeof p.bindingId === 'string') handler(p)
    })
  }
}

/** The panel's entire backend surface, so a test can substitute a fake —
 *  the files pattern (files-client.ts:88). */
export interface GitPanelServices {
  open(sessionId: string, cwd?: string): Promise<GitOpenResult>
  status(bindingId: string): Promise<GitStatusResult>
  diff(bindingId: string, path: string, side: GitDiffSide, maxBytes: number): Promise<GitDiffResult>
  log(bindingId: string): Promise<GitLogResult>
  stage(bindingId: string, paths: string[]): Promise<GitStageResult>
  unstage(bindingId: string, paths: string[]): Promise<GitUnstageResult>
  stageAll(bindingId: string): Promise<GitStageAllResult>
  unstageAll(bindingId: string): Promise<GitUnstageAllResult>
  commit(bindingId: string, message: string, amend: boolean): Promise<GitCommitResult>
  headMessage(bindingId: string): Promise<GitHeadMessageResult>
  remote(bindingId: string): Promise<GitRemoteResult>
  openUrl(url: string): Promise<ShellOpenUrl>
  close(bindingId: string): Promise<GitCloseResult>
  grantConsent(sessionId: string): Promise<ShellFootprintConsentResult>
  subscribeGitChanged(handler: (params: GitChangedNotification) => void): () => void
}

/** Real implementation over the dispatcher. Bindings are session-bounded
 *  (AD-9): a reconnect changes nothing and there is no watch set to re-send,
 *  so unlike the files seam there is no onConnect hook — the store keeps its
 *  bindingId and re-opens only when a call answers unknownBinding. */
export function createGitPanelServices(dispatcher: Dispatcher): GitPanelServices {
  const client = new GitClient(dispatcher)
  return {
    open: (sessionId, cwd) => client.open(sessionId, cwd),
    status: (bindingId) => client.status(bindingId),
    diff: (bindingId, path, side, maxBytes) => client.diff(bindingId, path, side, maxBytes),
    log: (bindingId) => client.log(bindingId),
    stage: (bindingId, paths) => client.stage(bindingId, paths),
    unstage: (bindingId, paths) => client.unstage(bindingId, paths),
    stageAll: (bindingId) => client.stageAll(bindingId),
    unstageAll: (bindingId) => client.unstageAll(bindingId),
    commit: (bindingId, message, amend) => client.commit(bindingId, message, amend),
    headMessage: (bindingId) => client.headMessage(bindingId),
    remote: (bindingId) => client.remote(bindingId),
    openUrl: (url) => client.openUrl(url),
    close: (bindingId) => client.close(bindingId),
    grantConsent: (sessionId) => client.grantConsent(sessionId),
    subscribeGitChanged: (handler) => client.subscribeGitChanged(handler),
  }
}
