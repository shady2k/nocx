// GitStore — the Git panel's binding lifecycle, response-scope discipline
// (D17), poll controller (D13), mutation gate (D18) and commit form
// (design §5.4). Input is the same reactive `activeOrigin()` accessor the
// Files panel takes; git owns repository identity, so every VERIFIED cwd
// change re-asks git.open and the ANSWER — the resolved toplevel, not the
// path — decides whether to re-bind (D4).
//
// The rules that make it correct, each with the failure it stops:
//
// 1. RESPONSES CARRY THE SCOPE THEY WERE ISSUED FOR, PLUS AN EPOCH (D17).
//    Every request captures {paneId, generation, bindingId, epoch}; a
//    response applies only if the first three still match the store AND its
//    epoch is not older than the newest already applied for its class.
//    generation bumps on every re-scope; epoch bumps before EVERY
//    status-producing request — each poll, each manual refresh, each
//    mutation. The epoch is what orders a poll issued BEFORE a mutation
//    that lands AFTER it: the two requests carry identical scope, and a
//    guard that cannot distinguish them cannot order them.
//    The status, log and remote are independent facts, so each class
//    tracks its OWN applied epoch. The control plane delivers responses in
//    completion order, not issue order (a faster backend command's answer
//    overtakes a slower one's), so a shared epoch would let a remote that
//    happens to complete first discard a log read issued BEFORE it — the
//    Commits list then sits idle forever with its answer thrown away
//    (nocx-sfv6). Only a newer-issued response of the SAME class
//    supersedes; a slow in-flight read still applies.
// 2. A STALE OPEN IS CLOSED, NOT MERELY DROPPED (nocx-myts). A successful
//    git.open has registered a live binding on the backend; discarding the
//    response leaks it. If the response is stale the store calls git.close
//    on the bindingId it just received and never stores it. The same rule
//    closes the redundant binding a same-repository re-open mints: git
//    owns identity, the answer says "same toplevel", and the panel keeps
//    the binding diff tabs hold rather than churning it on every cd.
// 3. THE MUTATION LANE (D18). At most one mutation is in flight; while one
//    runs the controls that issue another are disabled. A mutation begins
//    only against the binding as it is at the moment of the call (read
//    untracked); a re-bind between click and send cancels the send because
//    the call never went out. Once sent it is never cancelled, and its
//    result is subject to rule 1 like any other.
// 4. A POLL THAT ERRORS DOES NOT CLEAR THE LISTS. It marks the status
//    stale and leaves the last good one on screen. A rejected status call
//    is the ordinary discovery path for a repository deleted under a live
//    binding — except unknownBinding, which means the binding itself is
//    gone and the store re-resolves through git.open.
// 5. THE AMEND PREFILL CARRIES ITS OWN TOKEN. git.headMessage is not made
//    stale by anything in rule 1's triple: tick Amend, untick it, and the
//    in-flight reply would fill an abandoned form — and tick-untick-tick
//    lets the first reply satisfy the second request's intent. The token
//    bumps on every transition; a reply whose token is not current is
//    dropped, and a re-bind bumps it too because the form belongs to one
//    repository and never crosses to another.
// 6. POLLING RUNS ONLY WHILE THE SIDEBAR'S visible() IS TRUE AND THE STORE
//    IS READY — the same cadence as Ports (POLL_INTERVAL_MS), one poll in
//    flight, never queued, suppressed while a mutation is in flight.
//
// The state the panel renders is one discriminator (rule "state IS A
// DISCRIMINATOR, SWITCHED ON FIRST"): state() answers exactly one of the
// eight render states, and tooManyChanges is gated on
// status.completeness !== 'complete' — a traversal cut below the record
// cap holds every record it observed, so nothing about the lists' length
// says the status is incomplete (D9).
//
// Bindings are bounded by the session, not the WebSocket (AD-9): a
// reconnect changes nothing, the store keeps its bindingId and re-opens
// only when a call answers unknownBinding.
//
// The tree is plain mutable signals behind Solid; mutation-before-render
// is enforced by methods never being called during a render, and
// response-time reads are untracked for the same reason files-store
// states it.

import { createMemo, createSignal, untrack } from 'solid-js'
import { RpcError } from '../dispatcher'
import { POLL_INTERVAL_MS } from '../ports'
import type { ActiveOrigin } from '../pane-content'
import type { GitPanelServices, GitDiffSide } from './git-client'
import type { GitOpenResult } from '../generated/git.open'
import type { GitCommitResult } from '../generated/git.commit'
import type { GitLogResult } from '../generated/git.log'
import type { Status } from '../generated/git.status'
import type { GitError } from '../generated/git.error'

/** The panel's phases — the interstitial and failure scaffolding around the
 *  eight render states. The view renders 'opening' as a spinner and 'failed'
 *  as an error with Retry, exactly as the Files panel renders its phases;
 *  everything resolved is one of the eight state() answers. */
type GitPanelPhase = 'no-origin' | 'opening' | 'ready' | 'failed'

/** Exactly one of these renders (design §5.4). */
type GitPanelState =
  | 'noPane'
  | 'remote'
  | 'noCwd'
  | 'notARepository'
  | 'gitUnavailable'
  | 'gitTooOld'
  | 'ready'
  | 'tooManyChanges'

/** The panel's collapsible sections (nocx-nak2). The Conflicted section and
 *  the commit form deliberately are not in the set: a conflict is a state
 *  that must stay in sight, and the form is what a collapse is for reaching.
 *  Not exported on purpose: it is the store's own vocabulary, and the panel
 *  addresses the sections by these literals at the call site. */
type GitSection = 'staged' | 'unstaged' | 'commits'

/** The binding the panel holds: the backend-issued id every call echoes,
 *  plus the resolved worktree root — the answer, not the path (D4). */
interface GitBinding {
  bindingId: string
  toplevel: string
}

/** A rejected mutation's account — rendered inline, never a silent no-op. */
interface GitMutationError {
  message: string
}

/** A failed commit's account: git's own output (D11) and whether the
 *  capture bound was reached. */
interface GitCommitFailure {
  output: string
  truncated: boolean
}

export interface GitStore {
  phase(): GitPanelPhase
  /** The one render discriminator — one of the eight states, switched on
   *  first (design §5.4). */
  state(): GitPanelState
  origin(): ActiveOrigin | null
  /** The live binding: id + the resolved worktree root (D4). */
  binding(): GitBinding | null
  /** The last good status. Never cleared by a failed poll (rule 4). */
  status(): Status | null
  /** A poll or post-mutation status read failed after the last good one:
   *  the panel says the view is stale rather than rendering stale as fresh. */
  statusStale(): boolean
  /** The commits read (D13): one of idle (never asked), loading, loaded or
   *  failed — scoped by the same rule 1 triple and epoch as the status, so
   *  a log that lands after the panel re-scoped never renders. */
  logState(): 'idle' | 'loading' | 'loaded' | 'failed'
  /** The branch's recent commits, newest first; null until the first log
   *  read lands. */
  log(): GitLogResult['log'] | null
  /** Why the last log read failed; null unless logState() is 'failed'. */
  logError(): string | null
  /** The tracking-remote fact (brief, nocx-hc0m): 'ok' with a URL to
   *  convert, 'none' — the ordinary answer, never an error — meaning the
   *  panel draws no open-link affordance (design D14), 'failed' meaning
   *  the read failed and no link is drawn either. */
  remoteState(): 'idle' | 'loading' | 'ok' | 'none' | 'failed'
  /** The raw remote URL git reported, or null until an ok read lands. */
  remoteUrl(): string | null
  /** Why the last remote read failed; null unless remoteState() is
   *  'failed'. */
  remoteError(): string | null
  openError(): string | null
  /** The git version the capability probe found — the gitTooOld state
   *  renders it against the floor. */
  gitVersion(): string | null
  /** Whether git runs in the resolved environment or the degraded
   *  os.Environ() fallback (D6); the panel renders degraded before the
   *  first commit. */
  envState(): 'resolved' | 'degraded' | null
  envReason(): string | null
  /** Re-scope to the active tab's origin: re-asks git.open on a cwd change
   *  (the answer decides whether to re-bind), closes the binding on a
   *  different machine or none, and never moves the panel for a frozen
   *  origin (cwdFollow: false). */
  rescope(origin: ActiveOrigin | null): void
  /** The sidebar's visibility — the poll gate (rule 6). */
  setVisible(visible: boolean): void
  /** Manual refresh: a poll under the current scope, or a re-open when the
   *  binding is gone (also the Retry for a failed open). */
  refresh(): void
  /** True while a mutation is in flight — the controls that would issue
   *  another are disabled (D18). */
  mutationInFlight(): boolean
  /** A rejected mutation call's account; cleared by the next mutation. */
  mutationError(): GitMutationError | null
  stage(paths: string[]): void
  unstage(paths: string[]): void
  /** Whole-repository stage — refused while any entry is conflicted (D19):
   *  measured, git add -A marks a conflict resolved using a worktree file
   *  that still holds its markers. */
  stageAll(): void
  /** Whole-repository unstage — likewise refused during a conflicted merge
   *  (bare git reset deletes MERGE_HEAD). */
  unstageAll(): void
  /** True while any entry is conflicted — the visible reason the stage-all
   *  and unstage-all controls refuse. */
  conflictsPresent(): boolean
  /** Whether a collapsible section of the panel is open. The state lives
   *  HERE, not in the component: the panel mounts and unmounts with the
   *  view while the store outlives it (design §5.5), so a collapse
   *  survives a view switch. It belongs to one repository, like the commit
   *  form: adopting a new binding resets it (nocx-nak2). */
  sectionOpen(section: GitSection): boolean
  toggleSection(section: GitSection): void
  /** The panel's path filter (nocx-52by). Same residence rule as the
   *  sections: it lives HERE because the store outlives the panel, so a
   *  filter survives a view switch, and it belongs to one repository —
   *  adopting a new binding clears it, because a query typed against repo
   *  A must never silently hide repo B's files. It is renderer-side only:
   *  setFilter never issues a request, and a poll that lands while a
   *  filter is typed keeps the filter (paths are a stable vocabulary). */
  filter(): string
  setFilter(v: string): void
  // ── The commit form — lives in the store PER BINDING (design §5.4): it
  // survives a view switch, never crosses to another repository, and is
  // never persisted. ─────────────────────────────────────────────────────
  commitSubject(): string
  commitBody(): string
  setCommitSubject(v: string): void
  setCommitBody(v: string): void
  amend(): boolean
  toggleAmend(): void
  commitState(): 'idle' | 'running' | 'failed'
  commitOutput(): GitCommitFailure | null
  /** Commit the index with the form's message. On ok the form clears; on
   *  failed the message stays and git's output is shown (D11). */
  commit(): void
  /** Subscribe to "the status of this path on this side changed" — the
   *  diff tab's Reload offer (worker G's GitDiffDeps.onDiffStale,
   *  filesystem D7). The store's poll is the only producer of statuses;
   *  cb fires at most once per change, and never for a status that did
   *  not move the row. Returns the unsubscribe. */
  onDiffStale(bindingId: string, path: string, side: GitDiffSide, cb: () => void): () => void
  /** Close the binding and reset. Called when the view unmounts; the store
   *  is reusable — the next rescope re-opens. */
  dispose(): void
}

/** The observable state of one path on one diff side — a fingerprint, not
 *  a row: null when the path has no entry on that side. "The row moved"
 *  is this value changing across applied statuses (D7). An untracked file
 *  belongs to the `untracked` side; the same entry in the unstaged list is
 *  null for the `unstaged` side. */
function sideState(st: Status | null, path: string, side: GitDiffSide): string | null {
  if (st === null) return null
  if (side === 'staged') {
    for (const e of st.staged) if (e.path === path) return `x:${e.x}`
    return null
  }
  for (const e of st.unstaged) {
    if (e.path !== path) continue
    if (side === 'untracked') return e.y === '?' ? 'y:?' : null
    return e.y === '?' ? null : `y:${e.y}`
  }
  return null
}

/** The scope a response was issued for (rule 1). */
interface ScopeCtx {
  paneId: number
  generation: number
  bindingId: string | null
  epoch: number
}

/** unknownBinding — the wire's -32602 mapped by the git transport from
 *  git.ErrUnknownBinding (ws_git.go). The answer to "the binding is
 *  gone", and the trigger for re-resolving through git.open.
 *
 *  The code alone cannot say that: six distinct domain refusals share
 *  -32602 (gitErrorCode maps them all to invalid-params), so the transport
 *  now carries data.reason on the wire (contracts/git.error.schema.json,
 *  nocx-bpqil) and THIS is the only reason the store re-resolves on. A
 *  conflicted stage-all, an amend on an unborn branch or a nothing-to-
 *  commit refusal is a repository state a person should read — re-opening
 *  the repository cannot fix it, and swallowing it made the operation
 *  appear to do nothing. */
function isUnknownBinding(e: unknown): boolean {
  if (!(e instanceof RpcError) || e.code !== -32602) return false
  // The discriminator is the schema-declared vocabulary
  // (contracts/git.error.schema.json): only the fixed reason separates an
  // unknown binding from the other five refusals that share the code.
  return isGitErrorReason(e.data, 'unknown-binding')
}

/** True when `data` is a git error payload (contracts/git.error.schema.json)
 *  carrying exactly the given reason. Mirrors the dispatcher's
 *  isSaturationData: the payload is checked by shape, then the fixed reason
 *  discriminates. */
function isGitErrorReason(data: unknown, reason: GitError['reason']): boolean {
  if (typeof data !== 'object' || data === null) return false
  const d = data as GitError
  return d.reason === reason
}

export function createGitStore(
  services: GitPanelServices,
  opts: { pollIntervalMs?: number } = {},
): GitStore {
  const pollIntervalMs = opts.pollIntervalMs ?? POLL_INTERVAL_MS
  /** True between dispose() and the next rescope(): late responses from a
   *  previous life of this store must not touch the reset state. */
  let closed = true
  /** Bumps on every re-scope — the tab-switch half of rule 1. */
  let generation = 0
  /** Bumps before EVERY status-producing request — the ordering half of
   *  rule 1 (D17). */
  let epoch = 0
  /** The newest status epoch already applied; a status response whose
   *  epoch is older is dropped, whichever order the responses land in.
   *  The log and remote reads keep their own per-class epochs (rule 1):
   *  one class completing out of issue order must not invalidate an
   *  earlier-issued read of another. */
  let lastAppliedEpoch = 0
  let lastLogEpoch = 0
  let lastRemoteEpoch = 0
  let visible = false
  let pollTimer: ReturnType<typeof setInterval> | null = null
  let pollInFlight = false
  /** The D7 staleness subscriptions: {bindingId, path, side} → the diff
   *  tab's Reload offer. Fired from applyStatus, at most once per change. */
  const staleSubs = new Set<{
    bindingId: string
    path: string
    side: GitDiffSide
    cb: () => void
  }>()
  /** Bumps on every Amend transition AND every re-bind: a prefill reply
   *  whose token is not current is dropped (rule 5). */
  let amendToken = 0

  const [phase, setPhase] = createSignal<GitPanelPhase>('no-origin')
  const [origin, setOrigin] = createSignal<ActiveOrigin | null>(null)
  /** The git.open outcome — sticky across the opening interstitial so
   *  state() keeps answering truthfully; the generation guard is what
   *  prevents a stale open from satisfying a new scope, never this value. */
  const [openState, setOpenState] = createSignal<GitOpenResult['state'] | null>(null)
  const [binding, setBinding] = createSignal<GitBinding | null>(null)
  const [status, setStatus] = createSignal<Status | null>(null)
  const [statusStale, setStatusStale] = createSignal(false)
  /** The panel's path filter (nocx-52by): the one string that narrows both
   *  lists. Lives per repository like the sections and the commit form —
   *  resetRepositoryState clears it with them. */
  const [filter, setFilter] = createSignal('')
  const [logState, setLogState] = createSignal<'idle' | 'loading' | 'loaded' | 'failed'>('idle')
  const [log, setLog] = createSignal<GitLogResult['log'] | null>(null)
  /** The collapsible sections' open state (nocx-nak2). A Record so one
   *  toggle flips one key and the panel's three reads share one signal. */
  const [sectionsOpen, setSectionsOpen] = createSignal<Record<GitSection, boolean>>({
    staged: true,
    unstaged: true,
    commits: true,
  })
  const [logError, setLogError] = createSignal<string | null>(null)
  const [remoteState, setRemoteState] = createSignal<'idle' | 'loading' | 'ok' | 'none' | 'failed'>(
    'idle',
  )
  const [remoteUrl, setRemoteUrl] = createSignal<string | null>(null)
  const [remoteError, setRemoteError] = createSignal<string | null>(null)
  const [openError, setOpenError] = createSignal<string | null>(null)
  const [gitVersion, setGitVersion] = createSignal<string | null>(null)
  const [envState, setEnvState] = createSignal<'resolved' | 'degraded' | null>(null)
  const [envReason, setEnvReason] = createSignal<string | null>(null)
  const [mutationInFlight, setMutationInFlight] = createSignal(false)
  const [mutationError, setMutationError] = createSignal<GitMutationError | null>(null)
  const [commitSubject, setCommitSubject] = createSignal('')
  const [commitBody, setCommitBody] = createSignal('')
  const [amend, setAmend] = createSignal(false)
  const [commitState, setCommitState] = createSignal<'idle' | 'running' | 'failed'>('idle')
  const [commitOutput, setCommitOutput] = createSignal<GitCommitFailure | null>(null)

  // state() is the one render discriminator (design §5.4, rule 4 of the
  // store header). tooManyChanges gates on completeness, not on a
  // truncation boolean and not on the lists' length (D9).
  const state = createMemo<GitPanelState>(() => {
    const o = origin()
    if (o === null) return 'noPane'
    if (o.kind === 'ssh') return 'remote'
    if (!o.cwdVerified || o.cwd === null) return 'noCwd'
    const os = openState()
    switch (os) {
      case 'notARepository':
      case 'gitUnavailable':
      case 'gitTooOld':
        return os
      case 'ok': {
        const s = status()
        if (s !== null && s.completeness !== 'complete') return 'tooManyChanges'
        return 'ready'
      }
      default:
        // 'noCwd', 'remoteUnsupported', or null (opening / not yet
        // answered). The view renders phase() === 'opening' as a spinner
        // before consulting this discriminator, so the null case is never
        // painted; the transport's own guards answer the same way.
        return os === 'remoteUnsupported' ? 'remote' : 'noCwd'
    }
  })

  /** The response's request is still the view's current scope: same tab,
   *  same generation, and — once a binding exists — the same binding.
   *  Untracked: a response applies to the state it lands in, never
   *  re-derived for a state that replaced it (files-store rule 2). */
  function scopeCurrent(ctx: ScopeCtx): boolean {
    if (closed) return false
    const o = untrack(origin)
    const b = untrack(binding)
    if (o === null || ctx.paneId !== o.paneId) return false
    if (ctx.generation !== generation) return false
    if (ctx.bindingId !== null && (b === null || ctx.bindingId !== b.bindingId)) return false
    return true
  }

  /** A git.open response applies only if it is still the newest scope
   *  request: the open that establishes a binding wins by generation. */
  function openCurrent(ctx: { paneId: number; generation: number }): boolean {
    if (closed) return false
    const o = untrack(origin)
    return o !== null && ctx.paneId === o.paneId && ctx.generation === generation
  }

  function messageOf(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  /** Apply a status-producing response: scope first, then epoch ordering
   *  (rule 1). Sets the status and clears the stale mark, then reports
   *  the row moves the D7 staleness subscribers are watching for. */
  function applyStatus(ctx: ScopeCtx, res: Status | undefined): void {
    if (res === undefined) return
    if (!scopeCurrent(ctx)) return
    if (ctx.epoch < lastAppliedEpoch) return
    lastAppliedEpoch = ctx.epoch
    const prev = untrack(status)
    setStatus(res)
    setStatusStale(false)
    fireStale(res, prev)
  }

  /** The panel's poll observed a subscribed row move (D7): the diff tab
   *  holding that {path, side} is stale and offers Reload. Fires at most
   *  once per change — the comparison is against the last applied status,
   *  so an unchanged status never re-fires, and a status that was dropped
   *  by rule 1 never fires at all. */
  function fireStale(next: Status, prev: Status | null): void {
    if (staleSubs.size === 0) return
    const b = untrack(binding)
    for (const sub of [...staleSubs]) {
      if (b === null || sub.bindingId !== b.bindingId) continue
      if (sideState(next, sub.path, sub.side) === sideState(prev, sub.path, sub.side)) continue
      sub.cb()
    }
  }

  /** The binding is gone (session closed, deleted, or a close raced us):
   *  re-resolve through git.open against the CURRENT origin — the answer
   *  decides what the panel is showing (design §5.5). */
  function reResolve(): void {
    const o = untrack(origin)
    if (o === null) return
    openScope(o)
  }

  /** One status-producing request: bump the epoch, issue the call, apply
   *  under rule 1. The scope is captured AT ISSUE TIME, untracked. */
  function issueStatus(): void {
    const o = untrack(origin)
    const b = untrack(binding)
    if (o === null || b === null) return
    epoch++
    const ctx: ScopeCtx = { paneId: o.paneId, generation, bindingId: b.bindingId, epoch }
    pollInFlight = true
    services.status(b.bindingId).then(
      (res) => {
        pollInFlight = false
        applyStatus(ctx, res.status)
        // The environment fact rides the poll (nocx-69ey): Open's answer
        // is provisional (nocx-6pz0) — whatever settled by open, degraded
        // in the pre-settle window — so the warning it may have shown must
        // be withdrawable, and the poll is the repeating channel that
        // carries the settled answer. Same rule 1 triple as the status.
        applyEnv(ctx, res.envState, res.envReason)
      },
      (e) => {
        pollInFlight = false
        onStatusError(ctx, e)
      },
    )
  }

  /** The environment fact, applied from a status response. A poll that
   *  landed after the panel re-scoped must not paint the wrong binding's
   *  environment, so it is guarded exactly like the status (rule 1). */
  function applyEnv(
    ctx: ScopeCtx,
    state: 'resolved' | 'degraded',
    reason: string | undefined,
  ): void {
    if (!scopeCurrent(ctx)) return
    if (ctx.epoch < lastAppliedEpoch) return
    setEnvState(state)
    setEnvReason(reason ?? null)
  }

  function onStatusError(ctx: ScopeCtx, e: unknown): void {
    if (!scopeCurrent(ctx)) return
    if (isUnknownBinding(e)) {
      reResolve()
      return
    }
    // A poll that errors does not clear the lists (rule 4): the last good
    // status stays on screen, marked stale.
    setStatusStale(true)
  }

  /** The log belongs to one repository, exactly like the commit form: a
   *  re-bind, a refusal, a closed session or a dispose clears it, and the
   *  next trigger re-reads under the new scope. */
  function resetLog(): void {
    setLogState('idle')
    setLog(null)
    setLogError(null)
  }

  /** The remote belongs to one repository, exactly like the log and the
   *  commit form: a re-bind, a refusal, a closed session or a dispose
   *  clears it, and the next trigger re-reads under the new scope. */
  function resetRemote(): void {
    setRemoteState('idle')
    setRemoteUrl(null)
    setRemoteError(null)
  }

  /** One remote read (brief, nocx-hc0m), scoped by rule 1 and ordered by
   *  the same epoch as the status and the log. The remote is the most
   *  stable fact this panel holds — git remote add happens in the terminal
   *  beside it, not every few seconds — so it is read on the open, on
   *  manual refresh and when the panel becomes visible, never by the poll
   *  (D13), exactly like the log. The none state is ordinary (detached
   *  HEAD, no upstream, a local-path remote): the panel draws no link. */
  function issueRemote(): void {
    const o = untrack(origin)
    const b = untrack(binding)
    if (o === null || b === null) return
    epoch++
    const ctx: ScopeCtx = { paneId: o.paneId, generation, bindingId: b.bindingId, epoch }
    setRemoteState('loading')
    services.remote(b.bindingId).then(
      (res) => {
        if (!scopeCurrent(ctx) || ctx.epoch < lastRemoteEpoch) {
          // Dropped by rule 1: never paint a stale remote onto a
          // repository it did not come from.
          setRemoteState(untrack(remoteUrl) === null ? 'idle' : 'ok')
          return
        }
        lastRemoteEpoch = ctx.epoch
        if (res.state === 'ok') {
          setRemoteUrl(res.url ?? null)
          setRemoteState('ok')
          setRemoteError(null)
          return
        }
        setRemoteUrl(null)
        setRemoteState('none')
        setRemoteError(null)
      },
      (e) => {
        if (!scopeCurrent(ctx) || ctx.epoch < lastRemoteEpoch) {
          setRemoteState(untrack(remoteUrl) === null ? 'idle' : 'ok')
          return
        }
        if (isUnknownBinding(e)) {
          reResolve()
          return
        }
        // A failed read draws no link — the panel never opens a URL it
        // could not derive — and the next trigger retries.
        setRemoteState('failed')
        setRemoteError(messageOf(e))
      },
    )
  }

  /** One log read, scoped by rule 1 and ordered by the same epoch as the
   *  status: D13's "history does not change under the user" is why it is
   *  read on the open, on manual refresh and after a commit — never by the
   *  poll — and the epoch is why a commit's log cannot be overwritten by a
   *  refresh's log that was issued before the commit landed. */
  function issueLog(): void {
    const o = untrack(origin)
    const b = untrack(binding)
    if (o === null || b === null) return
    epoch++
    const ctx: ScopeCtx = { paneId: o.paneId, generation, bindingId: b.bindingId, epoch }
    setLogState('loading')
    services.log(b.bindingId).then(
      (res) => {
        if (!scopeCurrent(ctx) || ctx.epoch < lastLogEpoch) {
          // Dropped by rule 1: a newer-issued LOG superseded this read,
          // and its answer can never land. Say what is true — the last
          // good log, or idle when there was none (the next trigger
          // re-reads) — never a stuck "loading". A response from another
          // class (status, remote) never supersedes a log read: the
          // classes are independent facts (rule 1).
          setLogState(untrack(log) === null ? 'idle' : 'loaded')
          return
        }
        lastLogEpoch = ctx.epoch
        setLog(res.log)
        setLogState('loaded')
        setLogError(null)
      },
      (e) => {
        if (!scopeCurrent(ctx) || ctx.epoch < lastLogEpoch) {
          setLogState(untrack(log) === null ? 'idle' : 'loaded')
          return
        }
        if (isUnknownBinding(e)) {
          reResolve()
          return
        }
        setLogState('failed')
        setLogError(messageOf(e))
      },
    )
  }

  function openScope(o: ActiveOrigin): void {
    const prev = untrack(binding)
    generation++
    setBinding(null)
    setPhase('opening')
    setOpenError(null)
    epoch++ // the open's inline status is a status-producing response too
    // bindingId null: the open is not scoped to a binding — its response
    // either establishes one or is stale by generation (rule 1, open half).
    const ctx: ScopeCtx = { paneId: o.paneId, generation, bindingId: null, epoch }
    resetLog()
    resetRemote()
    services
      .open(o.sessionId, o.cwd ?? undefined)
      .then((res) => {
        if (!openCurrent(ctx)) {
          // Stale: a newer open superseded this one. A successful open has
          // already registered a live binding on the backend — dropping
          // the response without closing it would leak it (nocx-myts).
          if (res.state === 'ok' && res.bindingId !== undefined) {
            void services.close(res.bindingId).catch(() => {})
          }
          return
        }
        if (res.state !== 'ok') {
          // cd'd out, git vanished, git too old, or a transport guard.
          // The old binding, if any, is superseded: close it. The state
          // renders from openState, never from a stale status.
          if (prev !== null) void services.close(prev.bindingId).catch(() => {})
          setOpenState(res.state)
          setGitVersion(res.gitVersion ?? null)
          setEnvState(res.envState ?? null)
          setEnvReason(res.envReason ?? null)
          setStatus(null)
          setStatusStale(false)
          resetRepositoryState()
          setPhase('ready')
          return
        }
        // ok. git owns repository identity: the answer, not the path,
        // decides whether to re-bind (D4). Same toplevel → keep the live
        // binding (diff tabs hold it) and close the freshly-minted one
        // this re-ask created; different toplevel (or none before) → adopt.
        if (prev !== null && prev.toplevel === res.toplevel) {
          // The binding was cleared when the open was issued; prev is still
          // live, so the keep case restores it.
          setBinding(prev)
          if (res.bindingId !== undefined && res.bindingId !== prev.bindingId) {
            void services.close(res.bindingId).catch(() => {})
          }
        } else {
          // A different repository (or none before): the old binding is
          // superseded — close it and adopt the answer.
          if (prev !== null) void services.close(prev.bindingId).catch(() => {})
          if (res.bindingId === undefined || res.toplevel === undefined) {
            // (nil, ok, nil) is an internal error — a binding without
            // identity is not a repository to commit into.
            setPhase('failed')
            setOpenError('git.open answered ok without a binding')
            return
          }
          setBinding({ bindingId: res.bindingId, toplevel: res.toplevel })
          setStatus(null)
          setStatusStale(false)
          resetRepositoryState()
        }
        setOpenState('ok')
        setGitVersion(res.gitVersion ?? null)
        setEnvState(res.envState ?? null)
        setEnvReason(res.envReason ?? null)
        // The inline status is guarded like any other status-producing
        // response (rule 1); absent means the inline read failed — the
        // binding is live and the next poll retries.
        applyStatus(ctx, res.status)
        setPhase('ready')
        if (visible && res.status === undefined) issueStatus()
        // D13: history does not change under the user the way the working
        // tree does, so the log is read once per scope — here — and never
        // by the poll.
        if (visible) issueLog()
        if (visible) issueRemote()
      })
      .catch((e) => {
        if (!openCurrent(ctx)) return
        setPhase('failed')
        setOpenError(messageOf(e))
      })
  }

  /** The commit form belongs to one repository: adopting a new binding
   *  discards the previous repository's draft and voids any in-flight
   *  prefill. It is never persisted (design §5.4). */
  function resetCommitForm(): void {
    setCommitSubject('')
    setCommitBody('')
    setAmend(false)
    amendToken++
    setCommitState('idle')
    setCommitOutput(null)
  }

  /** The public half of the collapse state (nocx-nak2): the panel renders
   *  a section's open state from these. Reads are reactive — the panel
   *  reads the accessor inside its JSX — and a toggle flips one key in
   *  place, so the other two sections' state objects are preserved. */
  function sectionOpen(section: GitSection): boolean {
    return sectionsOpen()[section]
  }

  function toggleSection(section: GitSection): void {
    setSectionsOpen((prev) => ({ ...prev, [section]: !prev[section] }))
  }

  /** One repository context is gone or replaced: the commit draft, the
   *  section collapses and the path filter all belong to the repository the
   *  panel was showing, and none may leak into the next (design §5.4;
   *  nocx-nak2; nocx-52by). NOT the post-commit clear — that one only
   *  empties the form and calls resetCommitForm directly. */
  function resetRepositoryState(): void {
    resetCommitForm()
    setSectionsOpen({ staged: true, unstaged: true, commits: true })
    setFilter('')
  }

  function rescope(next: ActiveOrigin | null): void {
    // A frozen origin (a diff tab) has no opinion: activating it must
    // never re-bind the panel to a cwd snapshot that may be stale (design
    // §5.4). The panel keeps showing the shell's repository as it was.
    if (next !== null && !next.cwdFollow) return
    const prev = untrack(origin)
    if (next === null) {
      if (prev === null) return
      const b = untrack(binding)
      if (b !== null) void services.close(b.bindingId).catch(() => {})
      setBinding(null)
      setOpenState(null)
      setStatus(null)
      setStatusStale(false)
      resetRepositoryState()
      setOrigin(null)
      setPhase('no-origin')
      resetLog()
      resetRemote()
      return
    }
    // The frontend's own guards, decided BEFORE any backend call (D3, D14):
    // an SSH tab and a session with no verified cwd never reach git.open.
    if (next.kind === 'ssh' || !next.cwdVerified || next.cwd === null) {
      const b = untrack(binding)
      if (b !== null) void services.close(b.bindingId).catch(() => {})
      setBinding(null)
      setOpenState(next.kind === 'ssh' ? 'remoteUnsupported' : 'noCwd')
      setStatus(null)
      setStatusStale(false)
      resetRepositoryState()
      setOrigin(next)
      setPhase('ready')
      resetLog()
      resetRemote()
      return
    }
    // Same session AND same verified cwd AND a live binding: nothing moved —
    // record the newer origin (the paneId may differ) and keep everything,
    // including the commit form, which survives a view switch.
    const b = untrack(binding)
    if (
      prev !== null &&
      next.sessionId === prev.sessionId &&
      next.cwd === prev.cwd &&
      next.cwdVerified === prev.cwdVerified &&
      b !== null
    ) {
      setOrigin(next)
      return
    }
    // This scope commits to the live state: from here, responses apply
    // under rule 1 until dispose() closes the store again.
    closed = false
    setOrigin(next)
    openScope(next)
  }

  // ── Polling (rule 6, D13) ─────────────────────────────────────────────

  function pollTick(): void {
    if (!visible) return
    if (pollInFlight || mutationInFlight()) return
    if (phase() !== 'ready' || untrack(binding) === null) return
    issueStatus()
  }

  function setVisible(v: boolean): void {
    visible = v
    if (v) {
      if (pollTimer === null) pollTimer = setInterval(pollTick, pollIntervalMs)
      // An immediate status when the panel becomes visible makes it fresh
      // the moment it is seen rather than up to one interval later.
      if (
        phase() === 'ready' &&
        untrack(binding) !== null &&
        !pollInFlight &&
        !mutationInFlight()
      ) {
        issueStatus()
        // The log rides the same "fresh the moment it is seen" read as the
        // status — one-shot, never the timer (D13).
        issueLog()
        issueRemote()
      }
      return
    }
    if (pollTimer !== null) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }
  function refresh(): void {
    const o = untrack(origin)
    if (o === null) return
    if (phase() === 'failed' || untrack(binding) === null) {
      // The Retry for a failed open, and the re-ask after a refusing answer
      // (git init in the shell deserves a working Refresh).
      rescope(o)
      return
    }
    if (phase() === 'ready') {
      if (pollInFlight || mutationInFlight()) return
      issueStatus()
      issueLog()
      issueRemote()
    }
  }

  // ── Mutations (rule 3, D18) ───────────────────────────────────────────

  /** Begin a mutation under the binding as it is at the moment of the
   *  call (untracked): at most one in flight (D18), the epoch bumped, the
   *  lane taken. Null when the lane is busy or there is nothing to mutate. */
  function beginMutation(): { ctx: ScopeCtx; b: GitBinding } | null {
    if (mutationInFlight()) return null
    const o = untrack(origin)
    const b = untrack(binding)
    if (o === null || b === null) return null
    epoch++
    const ctx: ScopeCtx = { paneId: o.paneId, generation, bindingId: b.bindingId, epoch }
    setMutationInFlight(true)
    setMutationError(null)
    return { ctx, b }
  }

  function onMutationError(ctx: ScopeCtx, e: unknown): void {
    setMutationInFlight(false)
    if (!scopeCurrent(ctx)) return
    if (isUnknownBinding(e)) {
      reResolve()
      return
    }
    // The mutation may have happened while the response failed (D17's
    // "stage succeeds, the status that follows it fails"): the panel says
    // the view is stale rather than reverting the row.
    setStatusStale(true)
    setMutationError({ message: messageOf(e) })
  }

  function stage(paths: string[]): void {
    if (paths.length === 0) return
    const m = beginMutation()
    if (m === null) return
    services.stage(m.b.bindingId, paths).then(
      (res) => {
        setMutationInFlight(false)
        applyStatus(m.ctx, res.status)
      },
      (e) => onMutationError(m.ctx, e),
    )
  }

  function unstage(paths: string[]): void {
    if (paths.length === 0) return
    const m = beginMutation()
    if (m === null) return
    services.unstage(m.b.bindingId, paths).then(
      (res) => {
        setMutationInFlight(false)
        applyStatus(m.ctx, res.status)
      },
      (e) => onMutationError(m.ctx, e),
    )
  }

  /** The D19 refusals are one rule: while a merge is unresolved the panel
   *  does not touch the index — measured, both commands are destructive
   *  in exactly that state. */
  function conflictsPresent(): boolean {
    // Reads status REACTIVELY. It was untrack() and that was a defect an e2e
    // caught: the panel disables stage-all/unstage-all and shows the refusal
    // through this predicate, so an untracked read froze both at whatever the
    // answer was when the panel opened. A conflict that develops WHILE the
    // panel is open — the ordinary case, since a merge happens in the
    // terminal beside it — then left the two most destructive controls
    // enabled with no reason shown. The unit tests only ever opened onto an
    // already-conflicted repository, which is why they agreed with it.
    const s = status()
    return s !== null && s.conflicted.length > 0
  }

  function stageAll(): void {
    if (conflictsPresent()) return
    const m = beginMutation()
    if (m === null) return
    services.stageAll(m.b.bindingId).then(
      (res) => {
        setMutationInFlight(false)
        applyStatus(m.ctx, res.status)
      },
      (e) => onMutationError(m.ctx, e),
    )
  }

  function unstageAll(): void {
    if (conflictsPresent()) return
    const m = beginMutation()
    if (m === null) return
    services.unstageAll(m.b.bindingId).then(
      (res) => {
        setMutationInFlight(false)
        applyStatus(m.ctx, res.status)
      },
      (e) => onMutationError(m.ctx, e),
    )
  }

  // ── The commit form (design §5.4) ─────────────────────────────────────

  function fullCommitMessage(): string {
    const subject = commitSubject().trim()
    const body = commitBody().trim()
    return body === '' ? subject : `${subject}\n\n${body}`
  }

  function toggleAmend(): void {
    const next = !amend()
    setAmend(next)
    amendToken++
    if (!next) return
    // Fetch once, when the box is ticked. The token bumped on the tick;
    // an untick (or a re-bind, which resets the form) bumps it again, and
    // a reply whose token is not current is discarded (rule 5).
    const token = amendToken
    const b = untrack(binding)
    if (b === null) return
    services.headMessage(b.bindingId).then(
      (res) => {
        if (token !== amendToken) return
        if (res.state !== 'ok' || res.message === undefined) return
        // Only into an empty form — never over text the user has typed.
        if (untrack(commitSubject) !== '' || untrack(commitBody) !== '') return
        const nl = res.message.indexOf('\n')
        if (nl < 0) {
          setCommitSubject(res.message.trim())
          return
        }
        setCommitSubject(res.message.slice(0, nl).trim())
        setCommitBody(res.message.slice(nl).replace(/^\n+/, '').trim())
      },
      () => {
        // A failed prefill is not actionable: the form stays as it is.
      },
    )
  }

  function commit(): void {
    if (mutationInFlight()) return
    if (commitSubject().trim() === '') return
    const m = beginMutation()
    if (m === null) return
    setCommitState('running')
    services.commit(m.b.bindingId, fullCommitMessage(), amend()).then(
      (res) => applyCommitResult(m.ctx, res),
      (e) => {
        setCommitState('idle')
        onMutationError(m.ctx, e)
      },
    )
  }

  function applyCommitResult(ctx: ScopeCtx, res: GitCommitResult): void {
    setMutationInFlight(false)
    if (!scopeCurrent(ctx)) return
    if (res.state === 'ok') {
      // The commit happened. The form clears (its message was this
      // commit's); the fresh post-commit status applies under rule 1, and
      // if its read failed the panel says the view is stale rather than
      // rendering the last lists fresh.
      resetCommitForm()
      if (res.statusStale === true) {
        setStatusStale(true)
      } else if (res.status !== undefined) {
        applyStatus(ctx, res.status)
      }
      if (visible) issueLog()
      return
    }
    // failed — git's own account, shown in the panel, message stays (D11).
    setCommitState('failed')
    setCommitOutput({ output: res.output ?? '', truncated: res.outputTruncated })
  }

  // ── The notification: a binding is gone (design §5.2) ─────────────────

  const unsubChanged = services.subscribeGitChanged((p) => {
    const b = untrack(binding)
    if (b === null || p.bindingId !== b.bindingId) return
    // The session that owned the binding closed. The panel drops to noPane:
    // the active-origin accessor follows the tab change, and the next
    // rescope re-opens against whatever is in front.
    setPhase('no-origin')
    setOrigin(null)
    setBinding(null)
    setOpenState(null)
    setStatus(null)
    setStatusStale(false)
    resetRepositoryState()
    resetLog()
    resetRemote()
  })

  // ── The D7 staleness seam (worker G's GitDiffDeps.onDiffStale) ────────

  /** Subscribe to "the status of this path on this side changed" since the
   *  diff was read. The baseline is the status in place at subscribe time;
   *  every applied status is compared against it, and cb fires at most
   *  once per change — the next fire needs another move. A status dropped
   *  by rule 1 never fires (it was never applied), and a subscription for
   *  a binding the store has moved away from goes quiet until the diff
   *  tab's own liveness seam says the binding is gone. */
  function onDiffStale(
    bindingId: string,
    path: string,
    side: GitDiffSide,
    cb: () => void,
  ): () => void {
    const sub = { bindingId, path, side, cb }
    staleSubs.add(sub)
    return () => {
      staleSubs.delete(sub)
    }
  }

  function dispose(): void {
    closed = true
    setVisible(false)
    const b = binding()
    unsubChanged()
    if (b !== null) void services.close(b.bindingId).catch(() => {})
    staleSubs.clear()
    setPhase('no-origin')
    setOrigin(null)
    setBinding(null)
    setOpenState(null)
    setStatus(null)
    setStatusStale(false)
    resetRepositoryState()
    resetLog()
    resetRemote()
  }

  return {
    phase,
    state,
    origin,
    binding,
    status,
    statusStale,
    openError,
    logState,
    log,
    logError,
    remoteState,
    remoteUrl,
    remoteError,
    gitVersion,
    envState,
    envReason,
    rescope,
    setVisible,
    refresh,
    mutationInFlight,
    mutationError,
    stage,
    unstage,
    stageAll,
    unstageAll,
    conflictsPresent,
    sectionOpen,
    toggleSection,
    filter,
    setFilter,
    commitSubject,
    commitBody,
    setCommitSubject,
    setCommitBody,
    amend,
    toggleAmend,
    commitState,
    commitOutput,
    commit,
    onDiffStale,
    dispose,
  }
}
