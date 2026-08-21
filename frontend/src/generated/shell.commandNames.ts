/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/shell.commandNames.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the shell.commandNames JSON-RPC method — the SHARED half of command discovery (carrier design §8). It carries the executables on the session target's PATH, computed once per cache key and shared by every session to that target, so ten tabs to one host in an hour are one enumeration of thousands of files rather than ten. The SESSION-LOCAL half — the shell's own aliases, builtins, keywords and functions — never rides here: it belongs to one shell, it is enumerated by that shell and delivered over OSC 636, and caching it would let one tab claim a function defined in another. The renderer holds the union. `state` is the whole reason this result is not simply a list: a missing snapshot used to render as "command names are still loading", which is true only while a scan is running and a lie for one that timed out, failed, or is being served from a stale cache. There is no `off` state — discovery stays on (D6), bounded and shared rather than removed.
 */
export interface ShellCommandNames {
  /**
   * What the backend can honestly say about this name set. 'ready' a scan completed for exactly this cache key and nothing observed since has invalidated it. 'stale' a rescan was needed and could not be had, or the far side cannot stamp its PATH directories at all, so the previous snapshot is being served and `ageMs` says how old it is. 'timed-out' the scan did not finish inside its deadline; nothing partial is ever published, so `names` is empty. 'failed' the probe or the scan could not be run at all and `reason` says so. 'running' is never sent by the backend — the call either answers or joins the scan already in flight — and exists in this enumeration because it is the renderer's own state between asking and being answered, and the only one under which telling a user to wait is true.
   */
  state: 'running' | 'ready' | 'stale' | 'timed-out' | 'failed'
  /**
   * The executable names on the target's PATH, sorted and deduplicated. Empty for every state except 'ready' and 'stale': a scan that did not complete publishes nothing, because a prefix of an enumeration presented as the whole set marks real commands as nonexistent. At most 8192 names and 65536 encoded bytes; `truncated` says when that bound was reached.
   */
  names: string[]
  /**
   * How old the served snapshot is, in milliseconds. Non-zero only for 'stale' — a stale set offered without its age is indistinguishable from a current one, which is the same lie the "still loading" row was telling in the other direction. A snapshot older than one hour is not served at all.
   */
  ageMs: number
  /**
   * Why the scan did not produce a current answer, in the words the backend has for it. Empty for 'ready' and 'stale'.
   */
  reason: string
  /**
   * True when the name set was cut at its bound, so the renderer can say the list is partial rather than presenting a prefix as the whole.
   */
  truncated: boolean
}
