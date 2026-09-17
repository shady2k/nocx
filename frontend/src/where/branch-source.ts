// ═══════════════════════════════════════════════════════════════════════════
// The pane's branch — ambient decoration on the prompt line (nocx-9bpeq.13,
// spec §3 "Where home and the branch come from"), never the Git panel.
// git.open + git.close, per pane, single-flight: this module asks once per
// settle, reads the branch (or the detached short head) off the inline
// status git.open already returns, and closes the binding immediately —
// there is no live binding to hold, unlike the panel's GitStore.
//
// ── The remote-consent question (spec §3, task item C) ─────────────────────
//
// Read before writing this: internal/transport/ws_git.go's handleOpen (the
// git.open handler) and internal/app/helper_git.go's helperGitFactory (the
// composition root's answer to transport.GitFactoryFor for an SSH session).
//
// Finding: git.open on a NON-local session is not silent. Every consultation
// of the helper selection — and git.open consults it (ws_git.go:605,
// `sel := h.helperFor(sess)`) — runs `probeHelperPlatform` UNCONDITIONALLY,
// before any consent decision (helper_git.go:106, inside helperGitFactory's
// returned closure, executed before `r.Resolve(...)` at helper_git.go:112).
// That probe is a real exec against the session's remote host: bounded and
// read-only ("writes nothing" — helper_git.go:104-105's own comment), but it
// is a remote action the ambient decoration would trigger on every debounced
// settle, for a feature the user never asked to open. Nothing is deployed or
// asked for from here: since ADR-0068 no feature surface may raise the
// helper, git.open answers a refusal state on a machine the connection has
// not chosen the helper for, and the method itself is chosen only on the
// connection or at connect (ADR-0069).
//
// Decision: skip non-local sessions outright. The remote PROBE — not a
// deploy, not a prompt — is still a background exec this ambient feature
// would trigger on a host the user has not asked nocx to reach for git, on
// every settled command in every SSH pane. "Ambient decoration... must never
// raise... a deploy" (spec §3) is the header rule; a background probe on an
// unconsented remote host is the same category of surprise by a narrower
// margin, and the honest fix is not to ask at all until this feature
// consults consent on its own account. `isLocal === false` therefore
// answers undefined and never calls `deps.open`. Branch decoration for SSH
// panes is follow-up work, blocked on a consent-aware ask this module does
// not attempt — flagged to the coordinator to file rather than filed here
// (this task's br access is read-only, per its brief).
// ═══════════════════════════════════════════════════════════════════════════

import type { GitOpenResult } from '../generated/git.open'
import type { GitCloseResult } from '../generated/git.close'
import { log } from '../log'

/** The facts a pane knows when it asks for its branch. Mirrors the fields of
 *  `ActiveOrigin` this module actually consumes, without importing that
 *  type: the source has no opinion about a pane beyond these four facts. */
export interface BranchRequest {
  readonly sessionId: string
  readonly cwd: string | null
  readonly cwdVerified: boolean
  /** True for a local session. See the remote-consent finding above: a
   *  request with this false never reaches `deps.open`. */
  readonly isLocal: boolean
}

/** The source's entire window onto the outside world: the two git.* calls
 *  it needs, already bound to no held state (D18 does not apply here — this
 *  is a snapshot read, not the panel). */
export interface BranchSourceDeps {
  readonly open: (sessionId: string, cwd?: string) => Promise<GitOpenResult>
  readonly close: (bindingId: string) => Promise<GitCloseResult>
}

/** The timer seam, injected so a test can drive the debounce without
 *  depending on real time (AGENTS.md: a test may not depend on timing).
 *  The default is the real thing. */
export interface BranchScheduler {
  setTimeout(fn: () => void, ms: number): unknown
  clearTimeout(handle: unknown): void
}

const realScheduler: BranchScheduler = {
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
}

/** The trailing debounce before a settled pane's branch is actually asked
 *  for: a burst of cwd-verified reports and command settles collapses to
 *  one request. Small and deliberately below anything a person would
 *  notice as latency on the prompt line. */
export const BRANCH_DEBOUNCE_MS = 150

export interface BranchSource {
  /** The last published branch fact for a pane that has never requested is
   *  undefined; call `request` to start finding out. There is no
   *  per-session cache here — the source is one per pane (unlike
   *  session-home, which is one per session): a pane's branch is asked
   *  again on every settle, never reused across panes. */
  branch(): string | undefined
  /**
   * Ask for a fresh read: on a verified cwd change and when a command
   * block settles (design §3). Debounced and single-flight — a burst of
   * calls collapses to the latest, and while a git.open is in flight at
   * most one more call is remembered and fired after it settles.
   *
   * No request at all when `cwdVerified` is false, `cwd` is empty/null, or
   * `isLocal` is false (see the remote-consent finding above) — the last
   * published branch is cleared to undefined instead, since none of those
   * facts describes a pane this source may read a branch for.
   */
  request(req: BranchRequest): void
  /** Learn every time the published branch changes, including to
   *  undefined. Does not replay the current value. Returns the
   *  unsubscribe. */
  subscribe(cb: (branch: string | undefined) => void): () => void
  /** Cancel any pending timer; a rejected or slow git.open still resolves
   *  and closes its binding (nothing to cancel there — see the header),
   *  but its answer is discarded rather than published. Call when the pane
   *  closes. */
  dispose(): void
}

function branchFromStatus(status: GitOpenResult['status']): string | undefined {
  if (status === undefined) return undefined
  if (status.detached) return status.head !== '' ? status.head : undefined
  return status.branch !== '' ? status.branch : undefined
}

function requestUsable(req: BranchRequest): boolean {
  return req.isLocal && req.cwdVerified && req.cwd !== null && req.cwd !== ''
}

export function createBranchSource(
  deps: BranchSourceDeps,
  opts: { debounceMs?: number; scheduler?: BranchScheduler } = {},
): BranchSource {
  const debounceMs = opts.debounceMs ?? BRANCH_DEBOUNCE_MS
  const scheduler = opts.scheduler ?? realScheduler

  let current: string | undefined
  const subs = new Set<(branch: string | undefined) => void>()

  let timer: unknown = null
  let inFlight = false
  let queued: BranchRequest | null = null
  let disposed = false
  // Bumped on every accepted request (usable or not): a git.open answer
  // whose epoch has been superseded is discarded — never published — so a
  // slow or stale reply cannot repaint over a pane that has since moved to
  // a different session or an unverified cwd. The binding it opened is
  // still closed either way (ownership-transfer rule, same as GitStore's
  // stale-open-must-be-closed).
  let epoch = 0

  function publish(branch: string | undefined): void {
    if (current === branch) return
    current = branch
    for (const cb of [...subs]) cb(branch)
  }

  function cancelTimer(): void {
    if (timer !== null) scheduler.clearTimeout(timer)
    timer = null
  }

  function runQueued(): void {
    if (queued === null) return
    const next = queued
    queued = null
    request(next)
  }

  function fire(req: BranchRequest, myEpoch: number): void {
    inFlight = true
    // cwdVerified is guaranteed by requestUsable before fire is ever
    // reached, and requestUsable also guarantees cwd is neither null nor
    // empty.
    deps.open(req.sessionId, req.cwd ?? undefined).then(
      (res) => {
        inFlight = false
        if (res.state === 'ok' && res.bindingId !== undefined) {
          const branch = branchFromStatus(res.status)
          if (myEpoch === epoch) publish(branch)
          if (branch === undefined) {
            // A silent no-branch outcome even on an OK open: no status came
            // back at all (the inline read at git.open time failed — see
            // ws_git.go's own comment on that being a non-fatal degrade),
            // or the repo carries neither a branch name nor a detached
            // head (an unborn HEAD, most likely). Logged rather than
            // surfaced (spec §3: this feature raises no toast, no
            // consent), because "the branch source asked and nothing came
            // back" and "the branch source never asked" read identically
            // from the UI, and nocx-9bpeq.16 round 2 spent real time
            // telling them apart with no visibility into which one this
            // was.
            log.debug('nocx: branch source got no branch off an ok git.open', {
              sessionId: req.sessionId,
              cwd: req.cwd ?? '',
              hasStatus: res.status !== undefined,
              detached: res.status?.detached ?? null,
              unborn: res.status?.unborn ?? null,
            })
          }
          // Always closed, whether or not the answer was stale: a
          // successful open has registered a live binding on the backend,
          // and this source never holds one (unlike GitStore's panel).
          void deps.close(res.bindingId).catch(() => {})
        } else if (myEpoch === epoch) {
          // Every non-ok state (notARepository, noCwd,
          // gitUnavailable, gitTooOld, and the remote-helper refusals) is
          // ambient decoration's silence: no branch, and — deliberately —
          // no notify, no toast, no consent call. There is nothing to
          // close: none of these states carries a bindingId. Logged for
          // the same reason as the ok-but-no-status case above.
          log.debug('nocx: branch source git.open answered a non-ok state', {
            sessionId: req.sessionId,
            cwd: req.cwd ?? '',
            state: res.state,
          })
          publish(undefined)
        }
        runQueued()
      },
      (err) => {
        // A rejected call publishes undefined (if still current) and
        // closes nothing — there is no binding to leak — and the next
        // request tries again: rejection is not remembered anywhere.
        inFlight = false
        log.debug('nocx: branch source git.open rejected', {
          sessionId: req.sessionId,
          cwd: req.cwd ?? '',
          error: err instanceof Error ? err.message : String(err),
        })
        if (myEpoch === epoch) publish(undefined)
        runQueued()
      },
    )
  }

  function request(req: BranchRequest): void {
    if (disposed) return
    epoch++
    const myEpoch = epoch
    if (!requestUsable(req)) {
      cancelTimer()
      queued = null
      publish(undefined)
      return
    }
    if (inFlight) {
      // Single-flight: at most one follow-up, coalesced to the latest
      // request seen while the in-flight call is outstanding.
      queued = req
      return
    }
    cancelTimer()
    timer = scheduler.setTimeout(() => {
      timer = null
      fire(req, myEpoch)
    }, debounceMs)
  }

  return {
    branch: () => current,
    request,
    subscribe(cb) {
      subs.add(cb)
      return () => {
        subs.delete(cb)
      }
    },
    dispose() {
      disposed = true
      cancelTimer()
      queued = null
      subs.clear()
    },
  }
}
