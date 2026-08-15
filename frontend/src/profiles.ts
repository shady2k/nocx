import { Dispatcher } from './dispatcher'
import type { ConnectionTestResult } from './generated/connections.probe'
import type { TrustHostKeyResult } from './generated/connections.trustHostKey'
import type { SaveKeyMaterialMintResult } from './generated/secrets.saveKeyMaterial'
import type { BackupCreateResult } from './generated/backup.create'
import type { BackupRestorePreview as RestorePreview } from './generated/backup.preview'
import type { BackupRestoreResult as RestoreResult } from './generated/backup.restore'
import type { BackupSaveFileResult as SaveFileResult } from './generated/backup.saveToFile'
import type { SettingsDescribe } from './generated/settings.describe'

interface TabbyImportResult {
  profilesImported: number
  groupsImported: number
}
// Profile/group models + IPC client for the connection manager.
// Mirrors the backend internal/profile package (nocx-fxs.1) and the
// JSON-RPC control-plane methods wired in nocx-fxs.5.

// AuthMode controls which auth buckets are tried (null=Auto with full
// fallback-chain; a specific value restricts which buckets are attempted).
export type AuthMode = '' | 'password' | 'publicKey' | 'agent' | 'keyboardInteractive'

export interface BehaviorOnSessionEnd {
  value: 'auto' | 'keep' | 'reconnect' | 'close'
}

// Base holds the generic profile fields shared by all profile types.
export interface Base {
  id: string
  type: string
  name: string
  group?: string
  icon?: string
  color?: string
  disableDynamicTitle?: boolean
  behaviorOnSessionEnd?: 'auto' | 'keep' | 'reconnect' | 'close'
  weight?: number
  isBuiltin?: boolean
  isTemplate?: boolean
}

export interface SSHProfileOptions {
  host: string
  port?: number
  // Secret bindings (ADR-0017 §1): the vault row handles this connection
  // authenticates with. The backend resolves them to stored references —
  // the renderer never holds or names a secret reference (ADR-0011 §2).
  passwordSecret?: string
  keySecret?: string
  keyPassphraseSecret?: string
  user?: string
  auth?: AuthMode
  // KeyPath is the file-based alternative to keySecret: the two are mutually
  // exclusive.
  keyPath?: string
  keepaliveInterval?: number
  keepaliveCountMax?: number
  readyTimeout?: number
  jumpHost?: string // Profile name or ID of the jump server
  jumpPort?: number // Jump server port
  jumpUser?: string // Jump server username
  jumpPassword?: string // Jump server password
  jumpAuthMode?: AuthMode // Jump server auth mode
  agentForward?: boolean
  /** Desired destination mode (raw|script|relay, nocx-mlm7): the
   *  connection-scope default the tab's capability control starts from.
   *  script (the default — N3) wraps and installs automatically; raw adds
   *  nothing; relay is consent-gated. */
  desiredMode?: 'raw' | 'script' | 'relay'
  /** Relay consent for this destination (unknown|granted|denied, spec
   *  §3.5). Persisted per destination, never inherited; script mode never
   *  reads it. Relay without granted behaves as raw. */
  relayConsent?: 'unknown' | 'granted' | 'denied'
  canBeJumpServer?: boolean // Whether this profile can be used as a jump server
  portDiscovery?: 'auto' | 'ask' | 'off'
  /** Stored forwards, opened when the connection comes up (spec §8, D5). */
  forwards?: ForwardSpec[]
}

/** The three forwarding strategies the tunnel model covers (spec D4). */
export type ForwardDirection = 'local' | 'remote' | 'dynamic'

/**
 * One stored forward on a connection profile (spec §8): topology and policy
 * only — never credentials. `bindHost` empty means 127.0.0.1 (the tunnel
 * layer's default); `bindPort` 0 means an ephemeral port the OS allocates;
 * `destination` is "host:port" for local/remote and absent for dynamic.
 */
export interface ForwardSpec {
  direction: ForwardDirection
  bindHost?: string
  bindPort?: number
  destination?: string
}

export interface SSHProfile extends Base {
  options: SSHProfileOptions
}

export interface ProfileGroup {
  id: string
  parentGroupId?: string
  name: string
  description?: string
  defaults?: Record<string, unknown>
  order?: number
  color?: string
  icon?: string
}

// TreeNode is a ProfileGroup with its children resolved — the output of
// buildGroupTree.
export interface TreeNode extends ProfileGroup {
  children: TreeNode[]
}

// buildGroupTree turns a flat group list into a nested tree via parentGroupId.
// Orphaned groups (parent not found) become roots.
export function buildGroupTree(groups: ProfileGroup[]): TreeNode[] {
  const map = new Map<string, TreeNode>()
  const roots: TreeNode[] = []

  for (const g of groups) {
    map.set(g.id, { ...g, children: [] })
  }

  for (const g of groups) {
    const node = map.get(g.id)!
    if (g.parentGroupId && map.has(g.parentGroupId)) {
      map.get(g.parentGroupId)!.children.push(node)
    } else {
      roots.push(node)
    }
  }

  return roots
}

// resolveGroupPath walks the parent chain returning breadcrumb names
// (root first, leaf last). Cycle-guarded at 32 levels.
export function resolveGroupPath(groups: ProfileGroup[], id: string): string[] {
  const map = new Map(groups.map((g) => [g.id, g]))
  const path: string[] = []
  let current: ProfileGroup | undefined = map.get(id)
  let guard = 0
  while (current && guard < 32) {
    path.unshift(current.name)
    current = current.parentGroupId ? map.get(current.parentGroupId) : undefined
    guard++
  }
  return path
}

// parseQuickConnect parses "ssh://user@host:port", "user@host:port", "user@host",
// "host", "[host]:port" into a sparse SSHProfile (quick-connect entry).
export function parseQuickConnect(query: string): SSHProfile {
  let user = ''
  let host = ''
  let port = 22
  let rest = query.trim()

  // Strip ssh:// prefix — accept "ssh://user@host:port" as well as "user@host:port"
  const SSH_SCHEME = 'ssh://'
  if (rest.slice(0, SSH_SCHEME.length).toLowerCase() === SSH_SCHEME) {
    rest = rest.slice(SSH_SCHEME.length)
  }

  if (rest.startsWith('[')) {
    // IPv6: [::1]:port or [::1]
    const closeBracket = rest.indexOf(']')
    if (closeBracket === -1) {
      host = rest
    } else {
      host = rest.slice(1, closeBracket)
      if (rest[closeBracket + 1] === ':') {
        port = parseInt(rest.slice(closeBracket + 2), 10) || 22
      }
    }
  } else {
    // IPv4 or hostname
    const atIdx = rest.lastIndexOf('@')
    if (atIdx !== -1) {
      user = rest.slice(0, atIdx)
      rest = rest.slice(atIdx + 1)
    }
    const colonIdx = rest.lastIndexOf(':')
    if (colonIdx !== -1) {
      host = rest.slice(0, colonIdx)
      port = parseInt(rest.slice(colonIdx + 1), 10) || 22
    } else {
      host = rest
    }
  }

  return {
    id: '',
    type: 'ssh',
    name: host,
    options: { host, port, user: user || undefined },
  }
}

/** Build a saved SSHProfile from an SSH config alias. The host is set to the
 *  alias itself (not the resolved hostName) so palette suppression compares
 *  alias-to-alias, and user/port are only included when the config provided
 *  them — absent means "not set" rather than "explicit default". */
export function adoptAliasProfile(
  alias: string,
  user: string | undefined,
  port: number | undefined,
): SSHProfile {
  // No id. The backend mints it (ws.go, profiles.create) and returns the
  // record; the renderer minting one is what nocx-uxs5.10 removed, and the
  // caller must use the id createProfile hands back.
  const profile: SSHProfile = {
    id: '',
    name: alias,
    type: 'ssh',
    options: { host: alias },
  }
  if (user) profile.options.user = user
  if (port) profile.options.port = port
  return profile
}

// ── Effective profile types (wire format from profiles.effective) ──────────

// EffectiveSourceKind is a closed enum — switch on this, never parse id/label.
export type EffectiveSourceKind = 'profile' | 'group' | 'sshConfig' | 'global' | 'default'
export interface FieldSourceDTO {
  kind: EffectiveSourceKind
  id: string
  label: string
}

// EffectiveFieldDTO is the per-field wire representation.
export interface EffectiveFieldDTO {
  value: unknown
  source: FieldSourceDTO
}

// EffectiveProfileDTO is the per-profile wire representation.
export interface EffectiveProfileDTO {
  id: string
  fields: Record<string, EffectiveFieldDTO>
}

// EffectiveBatchResponse is the response from profiles.effective.
export interface EffectiveBatchResponse {
  profiles: EffectiveProfileDTO[]
  errors?: { id: string; error: string }[]
}

// ── SSH config alias types (wave 7 — nocx-c2ym.3) ─────────────────────
//
// Returned by sshConfig.aliases — live aliases from ~/.ssh/config.

/** One SSH host alias from ~/.ssh/config. */
export interface SSHAliasEntry {
  /** The Host pattern as written by the user. This is the identity. */
  readonly alias: string
  /** Resolved HostName (may equal alias when config sets none). */
  readonly hostName: string
  /** Resolved User (omitted when config sets none). */
  readonly user?: string
  /** Resolved Port (omitted when config sets none). */
  readonly port?: number
}

/** Why the SSH config could not be read. */
export interface SSHAliasUnavailable {
  readonly reason: 'no-ssh-binary' | 'timeout' | 'parse-failure'
  readonly detail: string
}

/** Response from sshConfig.aliases. */
export interface SSHAliasResponse {
  readonly aliases: SSHAliasEntry[]
  /** null when the read succeeded. */
  readonly unavailable: SSHAliasUnavailable | null
}

/** Result from importing ~/.ssh/config aliases. */
export interface SSHConfigImportResult {
  readonly profilesImported: number
  readonly skipped: number
}

/** Where the machine's SSH config lives, as the backend computed it. */
export interface SSHConfigPathResult {
  readonly path: string
  /** False when no SSH config resolver is wired — importing would fail. */
  readonly available: boolean
}

// ── Group impact types (wave 6 — nocx-uxs5) ──────────────────────────
//
// Returned by groups.impact — computed on the backend so inheritance
// is correctly reflected. The frontend renders what the backend answers.

/** One field that would change for a profile under a proposed group change. */
export interface FieldDiff {
  field: string
  oldValue: unknown
  newValue: unknown
  dangerous: boolean
}

/** Effective-field diff for one profile. */
export interface ProfileImpact {
  profileId: string
  profileName: string
  diffs: FieldDiff[]
}

/** What happens to children when a group is deleted. */
export interface DeleteImpact {
  action: string // "promote_to_root" | "refuse"
  reason: string // human-readable explanation
  affectedGroupIds?: string[] // child groups that would be reparented
}

/** Response from groups.impact. */
export interface GroupImpactResponse {
  dangerous: boolean
  affectedProfiles?: ProfileImpact[]
  deleteImpact?: DeleteImpact
}
// PatchParams is the request for profiles.patch.
export interface PatchParams {
  id: string
  set?: Record<string, unknown>
  unset?: string[]
}

// ProfileClient is the JSON-RPC client for profile/group/secret CRUD.
// It speaks the control-plane methods wired in nocx-fxs.5 (AD-1).
// RPC dispatch is delegated to a shared Dispatcher so request-ID allocation
// and response correlation are owned in one place.
export class ProfileClient {
  constructor(private dispatcher: Dispatcher) {}

  private call<T>(method: string, params: unknown): Promise<T> {
    return this.dispatcher.call<T>(method, params)
  }

  listProfiles(): Promise<SSHProfile[]> {
    return this.call('profiles.list', {})
  }
  createProfile(p: SSHProfile): Promise<SSHProfile> {
    return this.call('profiles.create', p)
  }
  updateProfile(p: SSHProfile): Promise<SSHProfile> {
    return this.call('profiles.update', p)
  }
  deleteProfile(id: string): Promise<boolean> {
    return this.call('profiles.delete', { id })
  }

  listGroups(): Promise<ProfileGroup[]> {
    return this.call('groups.list', {})
  }
  createGroup(g: ProfileGroup): Promise<ProfileGroup> {
    return this.call('groups.create', g)
  }
  updateGroup(g: ProfileGroup): Promise<ProfileGroup> {
    return this.call('groups.update', g)
  }
  deleteGroup(id: string): Promise<boolean> {
    return this.call('groups.delete', { id })
  }

  /** groups.impact — preview the effect of a proposed group change or delete. */
  groupImpact(params: {
    group?: ProfileGroup
    deleteGroupId?: string
  }): Promise<GroupImpactResponse> {
    return this.call('groups.impact', params)
  }

  /** groups.apply — atomically apply one or more group changes. */
  groupApply(groups: ProfileGroup[]): Promise<ProfileGroup[]> {
    return this.call('groups.apply', groups)
  }

  /** profiles.moveImpact — preview the effect of moving profile(s) to a new group. */
  moveImpact(params: {
    profileIds: string[]
    targetGroupId: string
  }): Promise<GroupImpactResponse> {
    return this.call('profiles.moveImpact', params)
  }

  importTabby(configYAML: string, passphrase?: string): Promise<number> {
    return this.call('profiles.importTabby', { config: configYAML, passphrase })
  }

  tabbyPreview(configYAML: string, passphrase?: string): Promise<TabbyPreviewResponse> {
    const params: Record<string, string> = { config: configYAML }
    if (passphrase) params.passphrase = passphrase
    return this.call('profiles.tabbyPreview', params)
  }

  tabbyExecute(planToken: string): Promise<TabbyImportResult> {
    return this.call('profiles.tabbyExecute', { planToken })
  }

  // Credential CRUD is gone with the aggregate (ADR-0017): the editor binds
  // vault secrets directly, and nothing here talks to credentials.*.

  // ── Secret minting (ADR-0017 §1) ─────────────────────────────────────
  // Each method mints a secret into the vault and returns the row handle
  // the editor names; the profile's options carry that handle and the
  // backend resolves it. `name` is the generated display name the secret
  // owns (ADR-0016); optional, and the backend falls back to rendering.
  savePassword(password: string, name?: string): Promise<{ row: string }> {
    return this.call('secrets.savePassword', { password, name })
  }
  saveKeyMaterial(
    keyText: string,
    name?: string,
  ): Promise<{ row: string } & SaveKeyMaterialMintResult> {
    return this.call('secrets.saveKeyMaterial', { keyText, name })
  }
  saveKeyPassphrase(keyRow: string, passphrase: string, name?: string): Promise<{ row: string }> {
    return this.call('secrets.saveKeyPassphrase', { keyRow, passphrase, name })
  }
  /** secrets.usage — the profiles (by effective resolution) that use the
   *  secret behind a row handle. Names the connections a delete would break
   *  (ADR-0017: the count is the number of profiles whose effective secret
   *  is this one). */
  secretUsage(row: string): Promise<{ profiles: ProfileRef[] }> {
    return this.call('secrets.usage', { row })
  }

  /** sessions.status — live + last-used state for a batch of profile IDs. */
  sessionStatus(profileIds: string[]): Promise<{ statuses: Record<string, SessionStatus> }> {
    return this.call('sessions.status', { profileIds })
  }

  /** connections.test — probe one profile, return typed outcome. */
  connectionTest(profileId: string): Promise<ConnectionTestResult> {
    return this.call('connections.test', { profileId })
  }

  /**
   * connections.trustHostKey — record the offered host key under the exact
   * backend-issued known_hosts identity. knownHostsHost and key are echoed
   * verbatim from hostKey evidence; the renderer never derives route identity.
   */
  trustHostKey(knownHostsHost: string, key: string): Promise<TrustHostKeyResult> {
    return this.call('connections.trustHostKey', { host: knownHostsHost, key })
  }

  /**
   * connections.passwordResolved — answer a backend-raised connection-
   * password prompt (connections.passwordRequest). outcome 'submitted'
   * carries the typed password and whether the user asked to remember it;
   * 'cancelled' dismisses the ask. The backend decides where and whether
   * the password is stored — this call only reports the decision.
   */
  passwordResolved(params: {
    requestId: string
    outcome: 'submitted' | 'cancelled'
    password?: string
    remember?: boolean
  }): Promise<Record<string, never>> {
    return this.call('connections.passwordResolved', params)
  }

  // with per-field provenance. Batch: pass several IDs in one call.
  loadEffective(ids: string[]): Promise<EffectiveBatchResponse> {
    return this.call('profiles.effective', { ids })
  }

  // patchProfile applies atomic set/unset operations to a profile and returns
  // its new effective entry. Use set for overrides, unset to revert to inherited.
  patchProfile(params: PatchParams): Promise<EffectiveProfileDTO> {
    return this.call('profiles.patch', params)
  }

  // ── SSH config aliases (wave 7 — nocx-c2ym.3) ─────────────────────

  /** List SSH host aliases from the machine's SSH config with resolved values. */
  listSSHAliases(): Promise<SSHAliasResponse> {
    return this.call('sshConfig.aliases', {})
  }

  /**
   * Which file listSSHAliases reads, and whether it can be read at all.
   *
   * Cheap by construction — no stat, no `ssh -G` — so a surface may ask merely
   * to name the file in its own text instead of assuming "~/.ssh/config".
   */
  sshConfigPath(): Promise<SSHConfigPathResult> {
    return this.call('sshConfig.path', {})
  }

  /**
   * Import ~/.ssh/config aliases as detached (non-live) nocx profiles.
   * Skips aliases whose name or host already exists among saved profiles.
   * Collision check is frontend-side, so concurrent edits may race — the
   * returned count is best-effort rather than exact.
   */
  async importSSHConfig(): Promise<SSHConfigImportResult> {
    const [aliasResp, existing] = await Promise.all([this.listSSHAliases(), this.listProfiles()])

    if (aliasResp.unavailable) {
      throw new Error(aliasResp.unavailable.detail)
    }

    // Build collision sets from existing profiles.
    const existingNames = new Set(existing.map((p) => p.name))
    const existingHosts = new Set(existing.map((p) => p.options.host))

    // Track seen-in-batch names and hosts for same-source dedup.
    const seenNames = new Set<string>()
    const seenHosts = new Set<string>()

    let imported = 0
    let skipped = 0

    for (const entry of aliasResp.aliases) {
      // Skip by name collision.
      if (existingNames.has(entry.alias) || seenNames.has(entry.alias)) {
        skipped++
        continue
      }
      // Skip by host collision.
      if (existingHosts.has(entry.hostName) || seenHosts.has(entry.hostName)) {
        skipped++
        continue
      }

      seenNames.add(entry.alias)
      seenHosts.add(entry.hostName)

      // No id: the backend mints it on profiles.create and returns the record.
      // Minting one here is what nocx-uxs5.10 removed.
      const p: SSHProfile = {
        id: '',
        type: 'ssh',
        name: entry.alias,
        options: {
          host: entry.hostName,
          ...(entry.user !== undefined ? { user: entry.user } : {}),
          ...(entry.port !== undefined ? { port: entry.port } : {}),
        },
      }

      try {
        await this.createProfile(p)
        imported++
      } catch (e) {
        // Race: profile created between our list and the create call.
        // Only count profile-exists errors as collisions; propagate all others.
        if (String(e).includes('profile already exists')) {
          skipped++
        } else {
          throw e
        }
      }
    }

    return { profilesImported: imported, skipped }
  }
  // Settings RPC (nocx-9m5 / STORE-5b).  No secret value ever appears in a
  // response — secrets go through secretSet/secretDelete/secretExists only.
  describeSettings(): Promise<SettingsDescribe> {
    return this.call('settings.describe', {})
  }
  getSnapshot(): Promise<{
    values: Record<string, unknown>
    overridden: string[]
    revision: number
  }> {
    return this.call('settings.getSnapshot', {})
  }
  setSetting(key: string, value: unknown): Promise<{ ok: true }> {
    return this.call('settings.set', { key, value })
  }
  resetSetting(key: string): Promise<{ ok: true }> {
    return this.call('settings.reset', { key })
  }
  secretSet(key: string, value: string): Promise<{ ok: true }> {
    return this.call('settings.secretSet', { key, value })
  }
  secretDelete(key: string): Promise<{ ok: true }> {
    return this.call('settings.secretDelete', { key })
  }
  secretExists(key: string): Promise<{ exists: boolean }> {
    return this.call('settings.secretExists', { key })
  }
  // ── Backup & Restore RPC methods ─────────────────────────────────────

  async createBackup(): Promise<BackupCreateResult> {
    return this.call('backup.create', {})
  }

  async previewBackupRestore(contents: string, strategy: RestoreStrategy): Promise<RestorePreview> {
    return this.call('backup.preview', { contents, strategy })
  }

  async restoreBackup(
    contents: string,
    strategy: RestoreStrategy,
    previewToken: string,
  ): Promise<RestoreResult> {
    return this.call('backup.restore', { contents, strategy, previewToken })
  }

  async saveBackupToFile(fileName: string, contents: string): Promise<SaveFileResult> {
    return this.call('backup.saveToFile', { fileName, contents })
  }
}

// ── Secret usage types ──────────────────────────────────────────────────
//
// Returned by secrets.usage — resolved on the backend so inheritance is
// correctly reflected (a secret used through a group default is still "in
// use", and the frontend should not attempt to compute it).
export interface ProfileRef {
  profileId: string
  profileName: string
  source: 'profile' | 'group' | 'global' // 'group' and 'global' = inherited
  groupId?: string
  groupName?: string
}

/**
 * Closed-enum outcome from connections.test. Derived from the generated
 * ConnectionTestResult so the renderer's union and the wire enum cannot
 * drift apart.
 */
export type ProbeOutcome = ConnectionTestResult['outcome']

export type { ConnectionTestResult }

/** Session state for one profile ID from sessions.status. */
export interface SessionStatus {
  live: boolean
  lastUsed?: string
}

export type RestoreStrategy = RestorePreview['strategy']
export type { BackupCreateResult, RestorePreview, RestoreResult, SaveFileResult }

// ── Tabby import preview types (bead nocx-kqw6) ──────────────────────────

/** One profile to import and what would happen. */
export interface ProfileEntry {
  name: string
  action: 'new' | 'overwrite' | 'needs-review'
}

/** One secret the import would create. */
export interface SecretEntry {
  name: string
  type: 'password' | 'passphrase'
}

/** One skipped secret and why. */
export interface SkippedInfo {
  secretType: string
  reason: string
}

/** One collision and the policy that applies. */
export interface CollisionInfo {
  kind: string
  name: string
  policy: string
}

/** Response from profiles.tabbyPreview. */
export interface TabbyPreviewResponse {
  profilesToImport: number
  groupsToImport: number
  secretsToImport: number
  profileEntries?: ProfileEntry[]
  groupNames?: string[]
  secretEntries?: SecretEntry[]
  skippedSecrets?: SkippedInfo[]
  collisions?: CollisionInfo[]
  secretProvider: string
  planToken: string
}
