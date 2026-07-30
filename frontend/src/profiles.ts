import { Dispatcher } from './dispatcher'

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
  // Link to a Credential (УЗ) by ID. If set, user/auth/keyPath come from the credential.
  // If empty, user/auth below are used directly (legacy/quick-connect).
  credentialId?: string
  // Override fields (used only if credentialId is empty)
  user?: string
  auth?: AuthMode
  // Note: passwords/keys are NEVER stored here — they live in the Credential's keychain entry.
  keepaliveInterval?: number
  keepaliveCountMax?: number
  readyTimeout?: number
  jumpHost?: string // Profile name or ID of the jump server
  jumpPort?: number // Jump server port
  jumpUser?: string // Jump server username (resolved from credential)
  jumpPassword?: string // Jump server password (resolved from credential store)
  jumpAuthMode?: AuthMode // Jump server auth mode
  agentForward?: boolean
  canBeJumpServer?: boolean // Whether this profile can be used as a jump server
}

export interface SSHProfile extends Base {
  options: SSHProfileOptions
}

export interface ProfileGroup {
  id: string
  parentGroupId?: string
  name: string
  icon?: string
  color?: string
  defaults?: Record<string, unknown>
  editable?: boolean
  children?: ProfileGroup[]
}

// Credential is a reusable authentication identity (nocx-УЗ).
// Stored separately from connections so multiple connections can share it.
export interface Credential {
  id: string
  name: string // Display name (e.g. "work-github", "prod-server")
  username: string
  auth: AuthMode // Auth method: password, publicKey, agent, keyboardInteractive
  // Secret depends on auth method:
  // - password: the password (stored in OS keychain, not here)
  // - publicKey: path to private key or vault:// URL
  // - agent/keyboardInteractive: not needed
  keyPath?: string // Only for publicKey auth
  // The host this credential may be submitted to. Required: an empty host is
  // refused at connect time, because "any host" is what lets this renderer
  // aim a credential at a host it controls (nocx-mon). Matching happens on the
  // backend against the RESOLVED hostname, never this alias.
  host?: string
  // Unset means "this host, any port" — a stated exception, not an oversight:
  // host is the load-bearing half of the identity.
  port?: number
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
  for (const g of groups) {
    map.set(g.id, { ...g, children: [] })
  }

  const roots: TreeNode[] = []
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
  const map = new Map<string, ProfileGroup>()
  for (const g of groups) map.set(g.id, g)

  const path: string[] = []
  let current = id
  for (let depth = 0; current && depth < 32; depth++) {
    const g = map.get(current)
    if (!g) {
      path.unshift(current)
      break
    }
    if (g.name) path.unshift(g.name)
    current = g.parentGroupId ?? ''
  }
  return path
}

// parseQuickConnect parses "user@host:port" / "user@host" / "host" /
// "[host]:port" into a sparse SSHProfile (quick-connect entry).
export function parseQuickConnect(query: string): SSHProfile {
  let user = ''
  let host = query
  let port = 22

  if (host.includes('@')) {
    const at = host.indexOf('@')
    user = host.slice(0, at)
    host = host.slice(at + 1)
  }

  if (host.includes('[')) {
    const close = host.indexOf(']')
    if (close > 0) {
      const inner = host.slice(1, close)
      const rest = host.slice(close + 1)
      host = inner
      if (rest.startsWith(':')) {
        const p = parseInt(rest.slice(1), 10)
        if (p > 0) port = p
      }
    }
  } else if (host.includes(':')) {
    const colon = host.lastIndexOf(':')
    const p = parseInt(host.slice(colon + 1), 10)
    if (p > 0) {
      port = p
      host = host.slice(0, colon)
    }
  }

  return {
    id: '',
    type: 'ssh',
    name: query,
    options: { host, port, user },
  }
}

// newProfileID creates a namespaced profile id client-side for display while
// the user fills the form. On save the profile is sent to the backend, which
// either uses it or generates its own.
export function newProfileID(type: string, name: string): string {
  const uuid =
    typeof crypto !== 'undefined' && crypto.randomUUID
      ? crypto.randomUUID()
      : `nocx-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
  return `${type}:custom:${name}:${uuid}`
}
// ProfileClient is the JSON-RPC client for profile/group/credential CRUD.
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

  importTabby(configYAML: string): Promise<number> {
    return this.call('profiles.importTabby', { config: configYAML })
  }

  // Credential CRUD (УЗ — reusable authentication identities)
  listCredentials(): Promise<Credential[]> {
    return this.call('credentials.list', {})
  }
  createCredential(c: Credential): Promise<Credential> {
    return this.call('credentials.create', c)
  }
  updateCredential(c: Credential): Promise<Credential> {
    return this.call('credentials.update', c)
  }
  deleteCredential(id: string): Promise<boolean> {
    return this.call('credentials.delete', { id })
  }

  // Password storage (OS keychain) — keyed by credential ID
  savePassword(credentialId: string, password: string): Promise<boolean> {
    return this.call('credentials.savePassword', { credentialId, password })
  }
  deletePassword(credentialId: string): Promise<boolean> {
    return this.call('credentials.deletePassword', { credentialId })
  }
  hasPassword(credentialId: string): Promise<boolean> {
    return this.call('credentials.hasPassword', { credentialId })
  }

  // Settings RPC (nocx-9m5 / STORE-5b).  No secret value ever appears in a
  // response — secrets go through secretSet/secretDelete/secretExists only.
  describeSettings(): Promise<{ declarations: unknown[] }> {
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

  async restoreBackup(contents: string, strategy: RestoreStrategy, previewToken: string): Promise<RestoreResult> {
    return this.call('backup.restore', { contents, strategy, previewToken })
  }

  async saveBackupToFile(fileName: string, contents: string): Promise<SaveFileResult | null> {
    return this.call('backup.saveToFile', { fileName, contents })
  }
}

export interface SaveFileResult { path: string }

// ── Backup & Restore types (ADR-0015) ────────────────────────────────────

export type RestoreStrategy = 'merge' | 'replace'

export interface BackupCreateResult {
  fileName: string
  contents: string
  summary: {
    settings: number
    connections: number
    groups: number
    credentialBindingsRemoved: number
    groupCredentialBindingsRemoved: number
    groupDefaultKeysOmitted: number
  }
}

export interface RestorePreview {
  previewToken: string
  createdAt: string
  strategy: RestoreStrategy
  settings: { included: number; changed: number; reset: number }
  connections: { included: number; added: number; updated: number; removed: number }
  groups: { included: number; added: number; updated: number; removed: number }
  connectionsRequiringCredential: Array<{ id: string; name: string }>
  omissions: {
    credentialBindingsRemoved: number
    groupCredentialBindingsRemoved: number
    groupDefaultKeysOmitted: number
  }
}

export interface RestoreResult {
  strategy: RestoreStrategy
  settingsChanged: number
  settingsReset: number
  connectionsAdded: number
  connectionsUpdated: number
  connectionsRemoved: number
  groupsAdded: number
  groupsUpdated: number
  groupsRemoved: number
  groupCredentialBindingsRemoved: number
  connectionsRequiringCredential: Array<{ id: string; name: string }>
}
