// ═══════════════════════════════════════════════════════════════════════════
// The session's home directory — one fact, one owner (AD-8). Moved out of
// terminal-links/open.ts (nocx-9bpeq.13, spec §3 "Where home and the branch
// come from"): that file derived and cached this for its own use only, and
// the prompt line (nocx-9bpeq.12/.16) needs the same fact without minting a
// second files.open binding for the session — a binding holds a provider
// and, for ssh, a pooled connection reference, so a second one per consumer
// would leak both at the rate consumers ask.
//
// One binding per session, not one per consumer or per click. AD-6: neither
// consumer invents a home — it is read back off the binding's own root, the
// only place the fact already crosses the wire (see homeFromRoot below).
// ═══════════════════════════════════════════════════════════════════════════

import type { FilesOpenResult } from '../generated/files.open'

/** The source's entire window onto the outside world: one files.open per
 *  session, and the liveness seam that says when a binding died. Both are
 *  the shapes terminal-links/open.ts already depended on, unchanged. */
export interface SessionHomeDeps {
  /** files.open for one session — the binding every later files.* (and
   *  git.*, for the branch source) call echoes. `rootPath` is the panel's
   *  starting directory, not a sandbox. */
  readonly openBinding: (sessionId: string, rootPath?: string) => Promise<FilesOpenResult>
  /** Subscribe to a binding's liveness, so a dead one is not handed out
   *  again. The composition root owns it. */
  readonly onBindingLiveness: (bindingId: string, cb: (live: boolean) => void) => () => void
}

export interface SessionHomeSource {
  /** The home directory known for a session right now, or undefined.
   *  Synchronous, and never triggers a call — call `ensure` for that. */
  home(sessionId: string): string | undefined
  /**
   * Open (or reuse) the one binding for this session. Idempotent: a session
   * with a binding already open or already opening is never asked twice —
   * a click and a prompt-line render arriving for the same session share
   * the one in-flight or live promise. `cwd`/`cwdVerified` name the
   * directory to open at; they are consulted only while no binding exists
   * yet for the session, exactly like the opener's original `rootPath`
   * derivation.
   *
   * A rejected open is not remembered: the next call tries again, rather
   * than awaiting the same failed promise forever.
   */
  ensure(sessionId: string, cwd?: string | null, cwdVerified?: boolean): Promise<FilesOpenResult>
  /**
   * Learn when a session's home becomes known, or changes to a different
   * value. Does not replay a value already known at subscribe time — read
   * `home(sessionId)` first for that. Returns the unsubscribe.
   */
  subscribe(sessionId: string, cb: (home: string) => void): () => void
}

/**
 * The home directory a binding's root reveals, or undefined.
 *
 * Both providers abbreviate a path under home to `~…` for DISPLAY (see
 * `displayOf` in internal/filesystem/local and its sftp twin). That
 * abbreviation is the only statement about home that already crosses the
 * wire, so `~/…` is expanded by reading it back off a binding we open
 * anyway, rather than by adding a round trip or a field to ask "what is
 * home" — the answer was already in the reply.
 */
export function homeFromRoot(root: FilesOpenResult['root']): string | undefined {
  const { path, display } = root
  if (display === '~') return path
  if (!display.startsWith('~/')) return undefined
  const tail = display.slice(1) // '/repo'
  if (!path.endsWith(tail)) return undefined
  const home = path.slice(0, path.length - tail.length)
  return home === '' ? undefined : home
}

export function createSessionHomeSource(deps: SessionHomeDeps): SessionHomeSource {
  // One binding per session, not one per consumer: a binding holds a
  // provider and, for ssh, a pooled connection reference. Dropped the
  // moment the liveness seam says the binding died, which is what keeps a
  // reconnected session from being handed the id of a binding that is gone.
  const bindings = new Map<string, Promise<FilesOpenResult>>()
  const homes = new Map<string, string>()
  const subs = new Map<string, Set<(home: string) => void>>()

  function notify(sessionId: string, home: string): void {
    const set = subs.get(sessionId)
    if (set === undefined) return
    for (const cb of [...set]) cb(home)
  }

  function ensure(
    sessionId: string,
    cwd?: string | null,
    cwdVerified?: boolean,
  ): Promise<FilesOpenResult> {
    const cached = bindings.get(sessionId)
    if (cached !== undefined) return cached
    const rootPath = cwdVerified === true && cwd !== null && cwd !== undefined ? cwd : undefined
    const pending = deps.openBinding(sessionId, rootPath).then((res) => {
      const home = homeFromRoot(res.root)
      // A home once known is a stable identity fact and is never unset by a
      // later read that could not derive one — the same rule the original
      // opener cache applied.
      if (home !== undefined && homes.get(sessionId) !== home) {
        homes.set(sessionId, home)
        notify(sessionId, home)
      }
      deps.onBindingLiveness(res.bindingId, (live) => {
        if (!live && bindings.get(sessionId) === pending) bindings.delete(sessionId)
      })
      return res
    })
    // A rejected open must not be remembered: the next call would await the
    // same failed promise forever and the caller would never get a second
    // try.
    pending.catch(() => {
      if (bindings.get(sessionId) === pending) bindings.delete(sessionId)
    })
    bindings.set(sessionId, pending)
    return pending
  }

  return {
    home: (sessionId) => homes.get(sessionId),
    ensure,
    subscribe(sessionId, cb) {
      let set = subs.get(sessionId)
      if (set === undefined) {
        set = new Set()
        subs.set(sessionId, set)
      }
      set.add(cb)
      return () => {
        set.delete(cb)
        if (set.size === 0) subs.delete(sessionId)
      }
    },
  }
}
