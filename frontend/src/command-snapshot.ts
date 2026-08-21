// Command-existence snapshot (OSC 636) — "the command line says whether the
// command exists", answered by the session's own shell.
//
// The frontend cannot know what commands a shell has: aliases, functions,
// builtins and PATH are the shell's live tables (zsh-syntax-highlighting gets
// them by living inside zsh; we cannot — ADR-0004 gives the editor the line
// and the shell never receives it until submit). So the hook asks the shell,
// which answers with the one thing the editor needs: a list of names.
//
// Protocol (VS Code's OSC 633 shape — typed sub-command letters, a nonce,
// hex escaping — but with a private code of our own):
//
//   OSC 636 ; H ; <nonce> ST                      session hello
//   OSC 636 ; S ; <nonce> ; <names> ST            command snapshot
//
// <names> is `;`-joined; each name is hex-escaped: `\\` for backslash,
// `\xHH` for control/C1 bytes and ';' (the field separator), everything else
// literal — raw UTF-8 passes through, the terminal already decoded it. Not
// base64: hex stays debuggable in a transcript.
//
// The nonce is the security boundary. Any process can print an OSC — a
// command's own output can forge a snapshot — so the hook generates a
// per-session nonce, sends it in the hello BEFORE the first prompt (when no
// user command has run and nothing can forge it), and the frontend discards
// every snapshot that does not carry it. The store accepts exactly one hello;
// a forged re-hello cannot re-anchor the nonce.
// Each tab owns its own store instance (see the class doc); the handshake
// rules below are per store, and the "exactly one hello" rule is what keeps
// one tab's nonce from being re-anchored by another.
//
// Policy: unknown sub-commands and malformed/oversized payloads are ignored,
// never errors, and never clear the previous snapshot. An empty snapshot is
// also rejected: "every command is unknown" is the same lie as "every command
// exists", pointing the other way.
//
// The verdicts (resolved/unresolved/indeterminate/unavailable) are computed
// in shell-highlight.ts from this store's answers. This module only holds the
// snapshot and the rules that protect it.

/**
 * Upper bound on the number of names in one OSC 636 snapshot — the
 * SESSION-LOCAL half (aliases, builtins, keywords, functions). 4096, which is
 * the bound the carrier design puts on that half and the cap the shell tiers
 * enforce before they emit. The PATH half has its own, larger bound and
 * arrives by a different route (see `applySharedNames`).
 */
export const MAX_SNAPSHOT_NAMES = 4096

/**
 * Upper bound on the number of names in the SHARED half — the executables on
 * the session target's PATH, computed once per host by the backend. The
 * backend already bounds it; this is the renderer's own refusal, because a
 * bound enforced only by the sender is a bound one bug away from absent.
 */
export const MAX_SHARED_NAMES = 8192

/** Upper bound on the decoded (unescaped) payload length, in characters. */
export const MAX_SNAPSHOT_CHARS = 65536

/**
 * What the product can honestly say about the shared (PATH) half of command
 * discovery. It is the renderer's copy of the wire enum, plus `running`,
 * which the backend never sends: the request either answers or joins a scan
 * already in flight, so `running` is exactly the interval between asking and
 * being answered — and the only state under which telling a user their
 * command names are still loading is true.
 *
 * There is no `off`. Discovery stays on (design D6), bounded and shared
 * rather than removed, and inventing an off state here would smuggle back
 * the decision that was rejected.
 */
export type CommandNamesState = 'running' | 'ready' | 'stale' | 'timed-out' | 'failed'

/** The shared half as the renderer holds it. */
export interface SharedCommandNames {
  readonly state: CommandNamesState
  readonly names: string[]
  readonly ageMs: number
  readonly reason: string
  readonly truncated: boolean
}

export type SnapshotMessage =
  { kind: 'hello'; nonce: string } | { kind: 'snapshot'; nonce: string; names: string[] }

/** The hook's nonce: 32 hex chars. Loose bounds for forward compatibility. */
const NONCE_RE = /^[0-9a-fA-F]{1,128}$/

/**
 * Parse an OSC 636 payload (the string between `ESC ] 636 ;` and ST).
 * Returns null for anything that is not a well-formed hello or snapshot —
 * the caller treats null as "ignore silently" (whitelist on receive).
 */
export function parseOsc636(payload: string): SnapshotMessage | null {
  if (payload.length === 0) return null
  const first = payload.indexOf(';')
  if (first <= 0) return null
  const kind = payload.slice(0, first)
  const rest = payload.slice(first + 1)

  if (kind === 'H') {
    // Hello: exactly two fields. A trailing ';' or third field is malformed.
    if (rest.includes(';')) return null
    if (!NONCE_RE.test(rest)) return null
    return { kind: 'hello', nonce: rest }
  }

  if (kind === 'S') {
    const second = rest.indexOf(';')
    if (second <= 0) return null
    const nonce = rest.slice(0, second)
    if (!NONCE_RE.test(nonce)) return null
    // Everything after the second ';' is the encoded name list. Names never
    // contain a raw ';' (the hook escapes it), so no further splitting.
    const names = decodeNames(rest.slice(second + 1))
    if (names === null) return null
    return { kind: 'snapshot', nonce, names }
  }

  return null
}

/**
 * Decode the `;`-joined, hex-escaped name list. `;` is the separator because
 * xterm.js strips control bytes — a newline-separated list would arrive with
 * every name welded together — and the hook escapes `;` inside names, so a
 * raw `;` is unambiguously a separator. Returns null when the encoding is
 * malformed, the list is empty, or a cap is exceeded — all of which leave
 * the previous snapshot in place.
 */
function decodeNames(encoded: string): string[] | null {
  const names: string[] = []
  let total = 0
  for (const part of encoded.split(';')) {
    if (part.length === 0) continue // the hook joins names with trailing ';'
    const name = decodeHex(part)
    if (name === null) return null
    total += name.length
    if (total > MAX_SNAPSHOT_CHARS) return null
    names.push(name)
  }
  if (names.length === 0) return null
  if (names.length > MAX_SNAPSHOT_NAMES) return null
  return names
}

/**
 * Decode one hex-escaped name: `\\` → backslash, `\xHH` → the byte, anything
 * else literal. A backslash followed by anything else is a malformed escape.
 */
function decodeHex(s: string): string | null {
  let out = ''
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]
    if (ch !== '\\') {
      out += ch
      continue
    }
    const next = s[i + 1]
    if (next === '\\') {
      out += '\\'
      i += 1
      continue
    }
    if (next === 'x') {
      const hex = s.slice(i + 2, i + 4)
      if (/^[0-9a-fA-F]{2}$/.test(hex)) {
        out += String.fromCharCode(parseInt(hex, 16))
        i += 3
        continue
      }
    }
    return null
  }
  return out
}

/**
 * The snapshot store: holds the established session nonce and the current
 * snapshot, and applies the ingest policy. `status` answers the four-state
 * question "is there a snapshot to judge against"; `has` answers "is this
 * literal name in it".
 *
 * One instance serves ONE tab. The renderer creates its store in its
 * constructor, its own OSC 636 handler feeds it, and that same tab's editor
 * and frozen headers read it. Two tabs never share a store, so a second tab
 * can never be judged against the first tab's command set. The "one hello
 * per session" rule is correct within a store; it only misfired when one
 * store was serving several sessions.
 */
export class CommandSnapshotStore {
  private nonce: string | null = null
  private _names: Set<string> | null = null
  private _shared: Set<string> | null = null
  private _sharedState: CommandNamesState = 'running'
  private _sharedAgeMs = 0
  private _sharedReason = ''
  private _sharedTruncated = false
  private listeners = new Set<() => void>()

  ingest(payload: string): void {
    const msg = parseOsc636(payload)
    if (msg === null) return
    if (msg.kind === 'hello') {
      // Exactly one hello per session. A later hello is discarded, so a
      // forger cannot re-anchor the nonce after the first prompt.
      if (this.nonce !== null) return
      this.nonce = msg.nonce
      return
    }
    // Snapshot: the nonce must have been established and must match.
    if (this.nonce === null || msg.nonce !== this.nonce) return
    this._names = new Set(msg.names)
    for (const cb of this.listeners) cb()
  }

  /**
   * Apply the shared half — the answer to `shell.commandNames`.
   *
   * The two halves are kept apart because they are two different truths. The
   * session-local half is THIS shell's tables and may never be cached for
   * another session; the shared half is the target's PATH, identical for
   * every session to it. `has` and `matching` read the union; `status`
   * requires both, for the reason on `status` itself.
   */
  applySharedNames(result: SharedCommandNames): void {
    this._sharedState = result.state
    this._sharedAgeMs = result.ageMs
    this._sharedReason = result.reason
    this._sharedTruncated = result.truncated
    if (result.state === 'ready' || result.state === 'stale') {
      // Over-long is refused rather than truncated: a prefix of an
      // enumeration presented as the whole set marks real commands as
      // nonexistent, which is the failure the state machine exists to avoid.
      this._shared = result.names.length > MAX_SHARED_NAMES ? null : new Set(result.names)
    } else {
      // A scan that did not complete publishes nothing, and must not leave
      // the last one standing under a state that says otherwise.
      this._shared = null
    }
    for (const cb of this.listeners) cb()
  }

  /** The shared half's state, for the surface. */
  get commandNamesState(): CommandNamesState {
    return this._sharedState
  }

  /** How old the served shared snapshot is, in ms. Meaningful when stale. */
  get commandNamesAgeMs(): number {
    return this._sharedAgeMs
  }

  /** The backend's own words for a failure. Empty unless failed/timed-out. */
  get commandNamesReason(): string {
    return this._sharedReason
  }

  /** True when the shared set was cut at its bound. */
  get commandNamesTruncated(): boolean {
    return this._sharedTruncated
  }

  /**
   * Whether this store can be asked "does this command exist".
   *
   * BOTH halves are required, and that is the whole point of the getter. A
   * shell's own tables answer for aliases and functions; the PATH set answers
   * for everything else. With only one of them, every name the other half
   * owns would come back "does not exist" — the editor would strike through
   * `git` because the PATH scan had not landed yet. `unavailable` means
   * indeterminate, which is the honest verdict until both are in.
   *
   * Completion does not go through here: it offers what it has (`matching`)
   * and names the shared half's state in its own row, because offering a
   * partial list is useful where asserting a partial absence is a lie.
   */
  get status(): 'unavailable' | 'ready' {
    if (this._names === null || this._shared === null) return 'unavailable'
    return 'ready'
  }

  has(name: string): boolean {
    if (this._names !== null && this._names.has(name)) return true
    return this._shared !== null && this._shared.has(name)
  }

  /** Fires when a snapshot is applied (a discard notifies nobody). */
  subscribe(cb: () => void): () => void {
    this.listeners.add(cb)
    return () => {
      this.listeners.delete(cb)
    }
  }

  /**
   * The command names starting with `prefix`, sorted — the completion
   * provider's read seam (design §8.5: the command provider reads the
   * snapshot the running shell produced). A fresh array every call: the
   * store never hands out its internal set, so no consumer can corrupt the
   * snapshot for the rest of the session. Empty when no snapshot has
   * applied — "unavailable" and "no commands" must not look alike, so the
   * caller checks `status` first.
   */
  matching(prefix: string): string[] {
    if (this._names === null && this._shared === null) return []
    const out = new Set<string>()
    for (const n of this._names ?? []) if (n.startsWith(prefix)) out.add(n)
    for (const n of this._shared ?? []) if (n.startsWith(prefix)) out.add(n)
    return [...out].sort()
  }

  /** Whether either half has anything to offer — the completion provider's
   *  question, which is not `status`'s. */
  get hasAnyNames(): boolean {
    return this._names !== null || this._shared !== null
  }

  reset(): void {
    this.nonce = null
    this._names = null
    this._shared = null
    this._sharedState = 'running'
    this._sharedAgeMs = 0
    this._sharedReason = ''
    this._sharedTruncated = false
  }
}
