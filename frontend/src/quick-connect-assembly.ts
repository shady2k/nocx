/**
 * The quick-connect host assembly — the single derivation of "which hosts do
 * I know", shared by the picker (quick-connect.tsx) and completion
 * (suggest/host-provider.ts). Plain module: no solid-js, no DOM, importable
 * from a node test.
 *
 * One derivation, two consumers (bead nocx-n9i6): completion routes the
 * picker's assembly read-only instead of rebuilding it, so the two can never
 * drift. This module is that assembly, lifted out of the UI module whose
 * solid-js/web import chain made it DOM-bound. The consumers fetch the raw
 * data themselves (each keeps its own RPC pattern) and turn the rows into
 * their own items — the picker adds the run callback, completion reads
 * label/id directly.
 *
 * The answer is typed rows plus the degraded-resolver condition as data —
 * never a human-facing label that would have to be parsed back out.
 */
import type { SSHAliasEntry, SSHAliasUnavailable, SSHProfile } from './profiles'

/** A saved profile that can be connected to (host filled in), as both
 *  surfaces list it. */
export interface HostProfileRow {
  /** The profile id — the stable identity completion dedups on. */
  readonly id: string
  /** `user@host` when the profile has a user, else `host`. */
  readonly label: string
  /** The profile's display name. */
  readonly detail: string
  /** The address opening the row acts on. */
  readonly host: string
  readonly user?: string
  /** The profile's port, when it names one. Carried because the pane a row
   *  opens STORES where it applies, and a row that dropped the port stored
   *  `:22` for every profile — after which the restore could never match the
   *  pane back to the profile it came from and reopened it as an
   *  unauthenticated direct-host dial (nocx-xhm9e). A profile with no port
   *  contributes none: the default belongs to whoever writes the endpoint,
   *  not to this row. */
  readonly port?: number
}

/** A live ~/.ssh/config alias after the saved-profile dedup, as both
 *  surfaces list it. */
export interface HostAliasRow {
  /** `__ssh_alias:<alias>` — the stable identity completion dedups on. */
  readonly id: string
  /** `user@alias` when the alias has a user, else `alias`. */
  readonly label: string
  /** The resolved HostName when it differs from the alias. */
  readonly detail?: string
  /** The alias and its overrides — what opening the row acts on. */
  readonly alias: string
  readonly user?: string
  readonly port?: number
}

/** The alias half's answer: the deduped live rows, and why the resolver
 *  could not answer when it could not — typed data, never a label. */
export interface HostAliasAssembly {
  readonly aliases: HostAliasRow[]
  readonly degraded: SSHAliasUnavailable | null
}

/**
 * The saved-profile half of the host list. A profile saved before its host
 * was filled in is not a connection: opening it hands the backend an empty
 * address and the tab comes up on "Terminal failed to start"; it would also
 * render as a row with an empty primary label — a stray indent rather than a
 * line. The palette lists what can be connected to; finishing such a profile
 * is what the New-connection action is for.
 */
export function profileRows(profiles: SSHProfile[]): HostProfileRow[] {
  return profiles
    .filter((p) => p.options.host != null && p.options.host.trim() !== '')
    .map((p) => {
      const user = p.options.user
      const host = p.options.host
      // A port of 0 is "not set" the same way `undefined` is — the same
      // reading profileIdentity makes, and the two must agree or the restore
      // match below cannot hold.
      const port = p.options.port !== undefined && p.options.port > 0 ? p.options.port : undefined
      return {
        id: p.id,
        label: user ? `${user}@${host}` : host,
        detail: p.name,
        host,
        user,
        port,
      }
    })
}

/**
 * The live half of the host list: ~/.ssh/config aliases, deduped against the
 * saved profiles (an alias already targeted by a saved profile is suppressed
 * — the profile is ours and wins), plus the degraded-resolver condition as
 * typed data. When the resolver could not answer, no aliases are offered and
 * the condition is carried in `degraded` — the surfaces decide how to say so.
 */
export function aliasRows(input: {
  profiles: SSHProfile[]
  aliases: SSHAliasEntry[]
  unavailable: SSHAliasUnavailable | null
}): HostAliasAssembly {
  if (input.unavailable != null) {
    return { aliases: [], degraded: input.unavailable }
  }
  // Get saved profiles for deduplication: an alias already targeted by a
  // saved profile is suppressed (priority is ours).
  const coveredAliases = new Set(
    input.profiles
      .filter((p) => p.options.host != null && p.options.host.trim() !== '')
      .map((p) => p.options.host),
  )
  return {
    aliases: input.aliases
      .filter((a) => !coveredAliases.has(a.alias))
      .map((a) => ({
        id: `__ssh_alias:${a.alias}`,
        label: a.user ? `${a.user}@${a.alias}` : a.alias,
        detail: a.hostName !== a.alias ? a.hostName : undefined,
        alias: a.alias,
        user: a.user,
        port: a.port,
      })),
    degraded: null,
  }
}

/**
 * The settings a saved profile contributes to a hand-typed ssh line once the
 * line has been matched to the profile (nocx-typed-ssh-profile). Resolved
 * from the profile list — never constructed by the parser — and the -J value
 * is already resolved through the jump profile the source names. Values are
 * raw settings; the planner quotes them before they reach the line.
 */
export interface SshProfileOverlay {
  /** The matching profile's id. */
  readonly profileId: string
  /** The profile's canonical destination identity — user@host:port, port 22
   *  when unset — the same construction the installed fact is keyed on
   *  (internal/ssh IdentityKey). The flags this overlay contributes make the
   *  typed line reach exactly this destination. */
  readonly identity: string
  /** -l value; contributed only when the typed line spells no user. */
  readonly user?: string
  /** -p value; contributed only when the typed line spells no -p. */
  readonly port?: number
  /** -i value (a key file path, possibly ~-prefixed); contributed only when
   *  the typed line spells no -i. Never a bound secret: only a path can ride
   *  a command line (ADR-0011 §2). */
  readonly keyPath?: string
  /** -J value, resolved through the jump profile the source names: user@host
   *  or user@host:port. Absent when the profile names no jump host, or the
   *  named profile is missing or hostless — never a guessed address. */
  readonly jumpHost?: string
}

/**
 * Match a submitted ssh destination to the saved profile it resolves to —
 * the same host-row resolution the host list itself uses (aliasRows dedups
 * aliases against profile hosts by the same exact string), narrowed by the
 * typed user when one is spelled. A profile port never blocks the match: the
 * port is exactly what the overlay adds when the line spells none. Zero or
 * several candidates answer null — fail-open, never a guess.
 */
export function resolveSshProfileOverlay(
  profiles: SSHProfile[],
  typed: { host: string; user?: string },
): SshProfileOverlay | null {
  const candidates = profiles.filter(
    (p) =>
      p.options.host != null &&
      p.options.host.trim() !== '' &&
      p.options.host === typed.host &&
      (typed.user === undefined || p.options.user === undefined || p.options.user === typed.user),
  )
  if (candidates.length !== 1) return null
  const p = candidates[0]
  const jumpHost = resolveJump(p, profiles)
  return {
    profileId: p.id,
    identity: profileIdentity(p),
    ...(p.options.user !== undefined && p.options.user !== '' ? { user: p.options.user } : {}),
    ...(p.options.port !== undefined && p.options.port > 0 ? { port: p.options.port } : {}),
    ...(p.options.keyPath !== undefined && p.options.keyPath !== ''
      ? { keyPath: p.options.keyPath }
      : {}),
    ...(jumpHost !== undefined ? { jumpHost } : {}),
  }
}

/** net.JoinHostPort, frontend-side: bracket a bare IPv6 host. */
function joinHostPort(host: string, port: number): string {
  const h = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host
  return `${h}:${port}`
}

/** The canonical destination identity in the installed fact's construction:
 *  user@host:port, port 22 when unset (internal/ssh IdentityKey). */
function profileIdentity(p: SSHProfile): string {
  const port = p.options.port !== undefined && p.options.port > 0 ? p.options.port : 22
  const hostport = joinHostPort(p.options.host, port)
  return p.options.user !== undefined && p.options.user !== ''
    ? `${p.options.user}@${hostport}`
    : hostport
}

/** The -J value for a profile that names a jump host: the named profile (by
 *  id, then name) resolved to user@host[:port]. A missing or hostless jump
 *  profile contributes nothing — never a guessed address. */
function resolveJump(p: SSHProfile, profiles: SSHProfile[]): string | undefined {
  const target = p.options.jumpHost
  if (target === undefined || target === '') return undefined
  const jump = profiles.find((q) => q.id === target) ?? profiles.find((q) => q.name === target)
  if (jump === undefined || jump.options.host === undefined || jump.options.host.trim() === '')
    return undefined
  const port =
    jump.options.port !== undefined && jump.options.port > 0 ? jump.options.port : undefined
  const hostport = port !== undefined ? joinHostPort(jump.options.host, port) : jump.options.host
  return jump.options.user !== undefined && jump.options.user !== ''
    ? `${jump.options.user}@${hostport}`
    : hostport
}
