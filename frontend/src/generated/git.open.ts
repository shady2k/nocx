/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/git.open.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the git.open JSON-RPC method: the outcome table of resolving a repository for a session (design §5.1, remote-helper design §6), plus the binding that names it for every later git.* call. sessionId appears exactly once on the wire — here — and the authorisation is connState's, not the global session registry's (D15): a connection that learned another connection's session id gets a refusal, not a repository. state is the discriminator: every outcome is a RESULT state, never a JSON-RPC error, because each one is something the panel can render (not a repository, git absent, git too old, no verified cwd, consent required, an unsupported platform, a failed deploy, an exec the host refused, a version-mismatched helper). The first status rides this result — otherwise every open is two round trips and a guaranteed frame of empty lists — and is present exactly when the inline read succeeded.
 */
export interface GitOpenResult {
  /**
   * The open outcome (remote-helper design §6). noCwd is decided by the transport from the session's origin before the repo factory is invoked; the refusal states (consentRequired, unsupportedPlatform, deployFailed, execForbidden, helperVersionMismatch) are decided by the helper selection and dial; the factory itself answers ok, notARepository, gitUnavailable or gitTooOld.
   */
  state:
    | 'ok'
    | 'notARepository'
    | 'gitUnavailable'
    | 'gitTooOld'
    | 'noCwd'
    | 'consentRequired'
    | 'unsupportedPlatform'
    | 'deployFailed'
    | 'execForbidden'
    | 'helperVersionMismatch'
  /**
   * The refusal's account: what failed and what to do about it, present exactly when state is one of unsupportedPlatform, deployFailed, execForbidden or helperVersionMismatch. Each refusal state names its recovery — which platform, what failed, how to reinstall — never a generic error.
   */
  message?: string
  /**
   * The backend-issued id every later git.* call echoes. Minted from crypto/rand so it cannot be guessed or enumerated; it is an address, not a bearer token — every later call re-checks that the binding's session is in the requesting connection's connState (Acquire performs that one check). Present iff state is ok.
   */
  bindingId?: string
  /**
   * The worktree root, as git rev-parse reported it. Part of the binding's identity: two linked worktrees of one repository are different working trees, so the panel keys its singleton tabs on this value. Present iff state is ok.
   */
  toplevel?: string
  /**
   * The git version the capability probe found, when the probe ran (e.g. '2.55.0'). On gitTooOld this is what the panel compares against the floor.
   */
  gitVersion?: string
  /**
   * Whether git will run in the environment resolved from the user's shell or in the degraded os.Environ() fallback (design D6). degraded also covers the brief window before the background resolution settles (nocx-6pz0): the panel must never claim resolved while the resolution is still running. The panel renders degraded before the first commit: a hook that silently could not find its tools is the exact failure the decision exists to prevent.
   */
  envState?: 'resolved' | 'degraded'
  /**
   * Why the environment is degraded; present exactly when envState is degraded.
   */
  envReason?: string
  status?: Status
}
/**
 * The first status, taken inline so an open is one round trip. Absent when the inline read failed — the binding is still live and the panel's first poll retries, so a failed inline read is not an open failure.
 */
export interface Status {
  /**
   * The current branch name; empty when the HEAD is detached, in which case detached is true.
   */
  branch: string
  /**
   * True when HEAD is detached (porcelain v2's branch.head is '(detached)').
   */
  detached: boolean
  /**
   * True when the branch is unborn (porcelain v2's branch.oid is '(initial)'): no commits exist yet, HEAD cannot be resolved, and individual unstaging is impossible (design D19 — unstage-ALL is the operation that works there).
   */
  unborn: boolean
  /**
   * Short hash of HEAD; empty when the branch is unborn.
   */
  head: string
  /**
   * The branch's upstream, in the form git itself prints (e.g. 'origin/main'); empty when the branch has none.
   */
  upstream: string
  /**
   * Commits ahead of the upstream; 0 when there is no upstream.
   */
  ahead: number
  /**
   * Commits behind the upstream; 0 when there is no upstream.
   */
  behind: number
  /**
   * Index-side changes: one entry per file whose index column is not clean. Never null.
   */
  staged: Entry[]
  /**
   * Worktree-side changes: one entry per file whose worktree column is not clean, including untracked files (X='?', Y='?'). Never null.
   */
  unstaged: Entry[]
  /**
   * Files with an unresolved conflict (the porcelain v2 'u' records). These are never stageable from the panel and never land in staged or unstaged (design: merge conflicts as a surface, out of scope). Never null.
   */
  conflicted: Entry[]
  /**
   * Number of status records observed. Its meaning is fixed by completeness: exact when complete or capped, a lower bound when cut (design D9).
   */
  total: number
  /**
   * ONE discriminator for how much of the repository's status the lists hold. The panel switches on it first: a traversal stopped by the work ceiling after 100 records must not look complete (design D9). 'cut' also covers a count read that was stopped or failed: the lists are then complete and total exact, but no entry carries counts — counts are all-or-nothing, because a partial count set makes rows past the cut look like rows with nothing to count (brief nocx-i4ki).
   */
  completeness: 'complete' | 'capped' | 'cut'
}
export interface Entry {
  /**
   * The file path, repository-relative, in git's own spelling.
   */
  path: string
  /**
   * The porcelain v2 index-side status column ('A', 'M', 'D', 'R', 'C', 'U', '?', ...). A file can be in both lists — X and Y both non-'.' — which is why a row's key is {side, path}, not path.
   */
  x: string
  /**
   * The porcelain v2 worktree-side status column ('.' when that side is clean, '?' for an untracked file, 'U' for a conflicted one).
   */
  y: string
  /**
   * Lines added to this file on this side, from git diff --numstat. Absent means no count exists — the file is untracked or binary, the entry is conflicted, or the count read was bounded out (design D9, brief nocx-i4ki). Absent is NOT zero: a real 0/0 answer (a pure rename, an empty file) arrives as 0.
   */
  added?: number
  /**
   * Lines deleted from this file on this side, from git diff --numstat. Absent means no count exists, exactly as for added.
   */
  deleted?: number
}
