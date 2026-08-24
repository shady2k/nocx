/**
 * ConnectionsView — Solid component for the connections manager.
 *
 * Full-width connection list with dialog-based editing (wave 6).
 * Spec §5 of the connection manager design: nothing hidden, nothing asked twice.
 *
 * Pattern follows the manager surfaces: full-width list, editing in a Dialog.
 * Tabby import moved to Export / Backup / Import section.
 */
import { For, Show, createSignal, createMemo, createEffect, on, onMount, type JSX } from 'solid-js'
import { Button } from './ui/button'
import { TextField } from './ui/text-field'
import { Checkbox } from './ui/checkbox'
import { Select, type SelectOption } from './ui/select'
import { EditableRowList } from './ui/row-list'
import { Dialog, showConfirm } from './ui/dialog'
import { Radio } from './ui/radio'
import { Section } from './ui/section'
import { Stack } from './ui/stack'
import { Tabs } from './ui/tabs'
import type { FootprintClient } from './footprint-client'
import { FootprintSection } from './footprint-section'
import { EmptyState } from './ui/empty-state'
import { Field } from './ui/field'
import { FileInput } from './ui/file-input'
import { Badge } from './ui/badge'
import { IconButton } from './ui/icon-button'
import { CollectionView } from './ui/collection-view'
import { RecordRow } from './ui/record-row'
import {
  DEFAULT_KEY_MODE,
  KeyMaterialInput,
  KeyPassphrasePrompt,
  suppliesMaterial,
} from './key-material-input'
import type { KeyInputMode } from './key-material-input'
import type { DialogClient } from './dialog-client'
import { CheckCircleIcon, ChevronDownIcon, PencilIcon, PlugIcon, TrashIcon } from './ui/icons'
import {
  createFormValidation,
  required,
  hostname,
  port as portRule,
  nonNegativeInteger,
  combine,
} from './ui/validation'
import { createSubmitGate } from './ui/submit-gate'
import type {
  SSHProfile,
  ProfileGroup,
  AuthMode,
  TreeNode,
  EffectiveProfileDTO,
  FieldSourceDTO,
  SessionStatus,
  ProbeOutcome,
  ConnectionTestResult,
  GroupImpactResponse,
  SSHConfigPathResult,
  TabbyPreviewResponse,
  ForwardSpec,
  ForwardDirection,
} from './profiles'
import { ProfileClient, buildGroupTree, parseQuickConnect } from './profiles'
import { RpcError } from './dispatcher'
import { HostKeyDialog } from './host-key-dialog'
import { PasswordEditor } from './password-editor'
import { AuthenticationEditor } from './authentication-editor'
import { log } from './log'
import { showToast } from './ui/toast'
import { VaultOperationCancelledError, type VaultController } from './vault'
import type { InventoryEntry, VaultClient } from './vault-client'
import type { DesiredMode, RelayConsent } from './capability'

// ── Provenance helpers ───────────────────────────────────────────────────────

export function sourceLabel(source: FieldSourceDTO): string {
  switch (source.kind) {
    case 'profile':
      return 'set here'
    case 'group':
      return `from group ${source.label || source.id}`
    case 'sshConfig':
      return 'from ~/.ssh/config'
    case 'global':
      return 'from global defaults'
    case 'default':
      return 'default'
  }
}

// ── Secret naming (ADR-0016) ──────────────────────────────────────────────

/** What the secret is, in the generated name. A connection that stores a key
 *  and its passphrase produces two secrets for one login, and `root@host`
 *  twice is not a name — it is the same name written down twice. The kind
 *  badge tells them apart in the list; the NAME has to tell them apart
 *  everywhere else, starting with the picker that chooses between them. */
const SECRET_NAME_PREFIX = {
  password: 'Password for',
  'private-key': 'Key for',
  'key-passphrase': 'Passphrase for',
} as const

type GeneratedSecretKind = keyof typeof SECRET_NAME_PREFIX

// The generated display name for a secret saved on a connection: derived from
// what it is plus host and user — never from any secret material. Falls back
// to the bare login when neither is known, and to the kind alone when nothing
// is: a name is what the user reads, so it is never empty.
function generatedSecretName(
  kind: GeneratedSecretKind,
  user: string | undefined,
  host: string | undefined,
): string {
  return secretNameFor(kind, loginLabel(user, host))
}

/** `root@host`, or whichever half is known. The subject a generated name is
 *  about — and what the passphrase prompt calls the key it is asking for,
 *  which is a different thing from what it calls the secret it stores. */
function loginLabel(user: string | undefined, host: string | undefined): string {
  const u = (user ?? '').trim()
  const h = (host ?? '').trim()
  return u && h ? `${u}@${h}` : u || h
}

/** The same naming for a subject that is already a label — a group's defaults
 *  name their secret, not a login. Never empty: a nameless row cannot be
 *  told from another nameless row. */
function secretNameFor(kind: GeneratedSecretKind, subject: string): string {
  const s = subject.trim()
  return s ? `${SECRET_NAME_PREFIX[kind]} ${s}` : SECRET_NAME_PREFIX[kind].replace(' for', '')
}
// ── Probe outcome helpers ────────────────────────────────────────────────────

// The offered host-key evidence from connections.test, as the renderer shows
// it and echoes it back to connections.trustHostKey. A host key is public
// material (ADR-0011 §3), so it may cross the wire and be displayed.
type HostKeyEvidence = NonNullable<ConnectionTestResult['hostKey']>

function probeOutcomeLabel(outcome: ProbeOutcome): string {
  switch (outcome) {
    case 'accepted':
      return 'Accepted'
    case 'rejected':
      return 'Rejected'
    case 'unreachable':
      return 'Unreachable'
    case 'host-key-unknown':
      // First contact is routine; the words must not borrow the alarm that
      // belongs to a changed key (nocx-6v1p).
      return 'Unknown host key'
    case 'host-key-changed':
      return 'Host key changed'
    case 'needs-interactive':
      return 'Needs interactive auth'
  }
}

// ── Save route decision (pure, tested directly) ─────────────────────────────

/** Describes how to save an existing profile for a given set of dirty fields. */
export type SaveRoute =
  { kind: 'noop' } | { kind: 'update' } | { kind: 'patch'; patchSet: Record<string, unknown> }

/**
 * Decide the save route for an existing profile given its dirty fields.
 *
 * When host or name are dirty, the full profile must go through
 * profiles.update because neither field is in the backend's
 * PatchPathAllowed set. When only options fields are dirty, send
 * just those fields through profiles.patch without pre-filtering:
 * the backend is the authority on what can be patched (nocx-fxs.1).
 *
 * Non-patchable fields: host (on SSHProfileOptions but not in
 * PatchPathAllowed), name (on Base).
 */
export function decideSaveRoute(profile: SSHProfile, dirty: ReadonlySet<string>): SaveRoute {
  if (dirty.size === 0) return { kind: 'noop' }

  const nonPatchable: Record<string, true> = { name: true, host: true, group: true }
  const hasNonPatchable = [...dirty].some((f) => nonPatchable[f])

  if (hasNonPatchable) {
    return { kind: 'update' }
  }

  const patchSet: Record<string, unknown> = {}
  for (const field of dirty) {
    patchSet[`options.${field}`] = profile.options[field as keyof typeof profile.options]
  }

  return { kind: 'patch', patchSet }
}

// ── Stored forwards helpers (spec §8, D5) ─────────────────────────────────

/** The closed direction set a stored forward may carry (spec D4). */
const FORWARD_DIRECTIONS: ForwardDirection[] = ['local', 'remote', 'dynamic']

/** The closed portDiscovery modes, in display order (spec D3). */
const PORT_DISCOVERY_MODES = ['auto', 'ask', 'off'] as const

/** The closed desired modes, in display order (spec §3.5, nocx-mlm7), with
 *  honest labels: raw adds nothing, script wraps and installs automatically
 *  (N3), relay deploys the Tier-B binary and is consent-gated. */
const DESIRED_MODES: { value: DesiredMode; label: string }[] = [
  { value: 'raw', label: 'Raw — no integration' },
  { value: 'script', label: 'Script — install automatically' },
  { value: 'relay', label: 'Relay — requires consent' },
]

/** The closed relay-consent states (spec §3.5), in display order. Consent
 *  is per destination and never inherited; script mode never reads it. */
const RELAY_CONSENTS: { value: RelayConsent; label: string }[] = [
  { value: 'unknown', label: 'Unknown — not asked' },
  { value: 'granted', label: 'Granted' },
  { value: 'denied', label: 'Denied' },
]
/**
 * Whether a stored forward's destination is a usable "host:port". The
 * backend's authority is net.SplitHostPort; this mirrors its acceptance
 * (host, then a numeric port 1–65535) so the editor never lets a row save
 * that the connect-time replay would reject.
 */
export function validForwardDestination(dest: string): boolean {
  const idx = dest.lastIndexOf(':')
  if (idx <= 0 || idx === dest.length - 1) return false
  const p = Number(dest.slice(idx + 1))
  return Number.isInteger(p) && p >= 1 && p <= 65535
}

/** The first invalid row in a forward list, or undefined when all rows are
 *  usable. Mirrors the backend's ValidForwards so the editor and the replay
 *  ask the same question. */
export function firstForwardError(rows: ForwardSpec[]): string | undefined {
  for (let i = 0; i < rows.length; i++) {
    const r = rows[i]
    if (!FORWARD_DIRECTIONS.includes(r.direction)) {
      return `Forward ${i + 1}: unknown direction`
    }
    if (r.direction === 'local' || r.direction === 'remote') {
      if (!r.destination) return `Forward ${i + 1}: destination is required for ${r.direction}`
      if (!validForwardDestination(r.destination)) {
        return `Forward ${i + 1}: destination must be "host:port"`
      }
    }
    if (r.bindPort != null && (r.bindPort < 0 || r.bindPort > 65535)) {
      return `Forward ${i + 1}: bind port must be 0–65535`
    }
  }
  return undefined
}

// ── Import sources ───────────────────────────────────────────────────────────

/**
 * Where a batch of connections can come from.
 *
 * `sshConfig` reads the machine's own ~/.ssh/config and takes no file; Tabby
 * imports a file selected by the user.
 */
type ImportSource = 'sshConfig' | 'tabby'

// ── Props ────────────────────────────────────────────────────────────────────

export interface ConnectionsViewProps {
  client: ProfileClient
  vaultController?: VaultController
  /** Vault inventory for the secret pickers. Optional: the dev-web harness
   *  has no vault, and the pickers then offer nothing. */
  vaultClient?: VaultClient
  onConnect?: (profile: SSHProfile) => void
  /**
   * Monotonic counter — every increment opens a blank profile for editing, the
   * same state the "+ New connection" button produces. A counter rather than a
   * callback ref because the page may not be rendered when the request is made:
   * mounting with a non-zero value is itself the request, which is what makes
   * the palette work on a Settings tab that was not open yet.
   */
  newProfileRequest?: number
  /**
   * Navigate from the Connections page to the Secrets page (in the same
   * Settings tab). The Connections page does not import SecretsSection —
   * it asks its parent to show it, and the parent decides how.
   */
  onNavigateToSecrets?: () => void
  /**
   * Native dialog capability (dialog.*). Absent in the dev-web harness and
   * in tests; the key input's Path mode then degrades to typing the path by
   * hand.
   */
  dialogClient?: DialogClient
  /**
   * The remote-footprint surface (nocx-mlm7 P10): what nocx wrote on which
   * host and the uninstall action. Absent in the dev-web harness and in
   * tests; the section then renders nothing rather than offering an
   * action that cannot run.
   */
  footprintClient?: FootprintClient
}

// ── Component ────────────────────────────────────────────────────────────────

export function ConnectionsView(props: ConnectionsViewProps) {
  // ── Data state ──────────────────────────────────────────────────────────
  const [profiles, setProfiles] = createSignal<SSHProfile[]>([])
  const [groups, setGroups] = createSignal<ProfileGroup[]>([])
  /** The vault inventory for the secret pickers (ADR-0017). Empty when the
   *  vault is sealed or absent (dev harness). */
  const [secretRows, setSecretRows] = createSignal<InventoryEntry[]>([])

  /** The pickers only ever offer live vault data: while the vault is sealed
   *  or uninitialized, stale rows from an earlier load must not be offered.
   *  Without a controller there is no status to read — trust the loaded
   *  rows, exactly as loadAll does without one. */
  const vaultOffersSecrets = createMemo(() => {
    const state = props.vaultController?.status()?.state
    return state === undefined || state === 'unsealed'
  })

  // ── Selection / dialog state ─────────────────────────────────────────────
  const [editing, setEditing] = createSignal<SSHProfile | null>(null)
  const [dialogOpen, setDialogOpen] = createSignal(false)
  const [profilePasswordOpen, setProfilePasswordOpen] = createSignal(false)
  const [profilePasswordValue, setProfilePasswordValue] = createSignal('')
  /** An in-flight password mint (W8). A Save pressed while it is resolving
   *  waits for the mint's bind, which persists the binding and updates the
   *  draft — the save must write the state that carries the binding. */
  let mintInFlight: Promise<void> | null = null
  /**
   * Row handles minted in THIS editor session, mapped to the display name
   * they were stored under. The inventory cannot know about a mint until it
   * is reloaded, but the name is decided at mint time (ADR-0016: the secret
   * owns its name; secrets.savePassword stores the requested name unchanged),
   * so the surface can trust the binding it just made instead of waiting for
   * a round trip. Cleared when the dialog closes, with the draft it belongs
   * to (W3).
   */
  const [mintedPasswordNames, setMintedPasswordNames] = createSignal<Map<string, string>>(new Map())
  const [passphraseAsk, setPassphraseAsk] = createSignal<{
    keyRow: string
    keyName: string
    passphraseName: string
    resolve: (outcome: { saved: boolean; row?: string }) => void
  } | null>(null)
  // Promise.withResolvers needs ES2024 and this project targets ES2021, so the
  // resolver is captured via the executor form.
  const askKeyPassphrase = (
    keyRow: string,
    keyName: string,
    passphraseName: string,
  ): Promise<{ saved: boolean; row?: string }> =>
    new Promise((resolve) => setPassphraseAsk({ keyRow, keyName, passphraseName, resolve }))
  // ── Effective/provenance state ─────────────────────────────────────────
  const [effectiveData, setEffectiveData] = createSignal<Record<string, EffectiveProfileDTO>>({})
  const [dirtyFields, setDirtyFields] = createSignal<Set<string>>(new Set())
  const [profileMoveImpact, setProfileMoveImpact] = createSignal<GroupImpactResponse | null>(null)
  // ── Connection test state per profile ────────────────────────────────
  const [probeBusy, setProbeBusy] = createSignal<Set<string>>(new Set())

  // ── Host key accept state (nocx-ved0) ────────────────────────────────
  // A probe that fails on the host key IS the question — first contact or a
  // changed key — so it is raised as a decision dialog rather than a toast.
  // changed=false is the routine accept (unknown host); changed=true is the
  // MITM-signature case and must never be the default action.
  const [pendingHostKey, setPendingHostKey] = createSignal<{
    profile: SSHProfile
    evidence: HostKeyEvidence
  } | null>(null)
  const [hostKeyBusy, setHostKeyBusy] = createSignal(false)
  const [sessionStatuses, setSessionStatuses] = createSignal<Record<string, SessionStatus>>({})

  // ── Filter ─────────────────────────────────────────────────────────────
  const [searchQuery, setSearchQuery] = createSignal('')

  // ── Group collapse (expand/collapse members per group) ─────────────
  const [collapsedGroups, setCollapsedGroups] = createSignal<Set<string>>(new Set())
  // ── Quick-connect dialog (creation starts from one field) ─────────────
  const [quickConnectOpen, setQuickConnectOpen] = createSignal(false)
  const [quickConnectValue, setQuickConnectValue] = createSignal('')
  const [importSource, setImportSource] = createSignal<ImportSource>('sshConfig')
  const [importOpen, setImportOpen] = createSignal(false)
  const [importFile, setImportFile] = createSignal<File | null>(null)
  const [importBusy, setImportBusy] = createSignal(false)
  const [importPassphrase, setImportPassphrase] = createSignal('')
  const [previewResult, setPreviewResult] = createSignal<TabbyPreviewResponse | null>(null)
  const [previewOpen, setPreviewOpen] = createSignal(false)
  // Where the SSH config actually is, per the backend. Null until asked.
  const [sshConfigPath, setSSHConfigPath] = createSignal<SSHConfigPathResult | null>(null)
  // ── Group editor dialog ──────────────────────────────────────────────
  const [editingGroup, setEditingGroup] = createSignal<ProfileGroup | null>(null)
  const [groupDialogOpen, setGroupDialogOpen] = createSignal(false)
  const [groupDraft, setGroupDraft] = createSignal<ProfileGroup | null>(null)
  const [groupImpact, setGroupImpact] = createSignal<GroupImpactResponse | null>(null)
  const [groupImpactBusy, setGroupImpactBusy] = createSignal(false)
  const [groupApplyBusy, setGroupApplyBusy] = createSignal(false)
  const [deleteGroupId, setDeleteGroupId] = createSignal<string | null>(null)

  /** The name behind deleteGroupId, for the confirmation to say out loud. */
  const deleteGroupName = createMemo(() => {
    const id = deleteGroupId()
    if (!id) return ''
    return groups().find((g) => g.id === id)?.name ?? ''
  })
  const [deleteImpact, setDeleteImpact] = createSignal<GroupImpactResponse | null>(null)
  const [deleteConfirmOpen, setDeleteConfirmOpen] = createSignal(false)
  const [deleteBusy, setDeleteBusy] = createSignal(false)
  const [dangerConfirmed, setDangerConfirmed] = createSignal(false)
  const [groupSection, setGroupSection] = createSignal('general')
  const [profileSection, setProfileSection] = createSignal('general')
  // ── Four-way key input state (publicKey auth) ────────────────────────
  // The mode vocabulary and the suppliesMaterial predicate live in
  // KeyMaterialInput — the same component the Secrets page uses. The state
  // itself stays here: the save paths and the bound-row secret own it.

  // Profile editor key state
  const [profileKeyMode, setProfileKeyMode] = createSignal<KeyInputMode>(DEFAULT_KEY_MODE)
  const [profileKeyText, setProfileKeyText] = createSignal('')
  const [profileKeyFingerprint, setProfileKeyFingerprint] = createSignal<string | undefined>(
    undefined,
  )
  const [profileKeyTextError, setProfileKeyTextError] = createSignal<string | undefined>(undefined)

  // Group editor key state
  const [groupKeyMode, setGroupKeyMode] = createSignal<KeyInputMode>(DEFAULT_KEY_MODE)
  const [groupKeyText, setGroupKeyText] = createSignal('')
  const [groupKeyFingerprint, setGroupKeyFingerprint] = createSignal<string | undefined>(undefined)
  const [groupKeyTextError, setGroupKeyTextError] = createSignal<string | undefined>(undefined)

  /** The impact, but only when it names a consequence. Null otherwise. */
  const groupImpactWorthShowing = createMemo(() => {
    const i = groupImpact()
    if (!i) return null
    if ((i.affectedProfiles?.length ?? 0) === 0 && !i.dangerous) return null
    return i
  })

  /**
   * The rail's sections. Fixed, and deliberately without a blast-radius entry:
   * the preview exists so that the consequence is seen BEFORE applying, and a
   * section is something the user can decline to open. It is pinned under the
   * pane instead, visible from whichever section made the change.
   */
  /**
   * The name is required, and the message belongs under the field: it is field
   * validation, answered by editing the field, and it clears as you type.
   */
  const groupValidation = createFormValidation(
    { name: () => required('Name')(groupDraft()?.name ?? '') },
    { controlId: (field) => (field === 'name' ? 'group-name' : field) },
  )

  // The one kit-owned answer to "how a form refuses a submit". The group
  // editor is a Tabs surface too: the offending field may be on a section the
  // user is not looking at, so the reveal hook opens the General section
  // before the gate tries to focus it — without this the dialog would report
  const groupGate = createSubmitGate(groupValidation, {
    reveal: () => {
      setGroupSection('general')
    },
  })

  // ── Data loading ────────────────────────────────────────────────────────
  async function loadAll() {
    try {
      const [p, g] = await Promise.all([props.client.listProfiles(), props.client.listGroups()])
      setProfiles(p ?? [])
      setGroups(g ?? [])
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to load connections', { message })
      showToast({
        level: 'danger',
        message: `Could not load connections: ${message}. The list may be out of date`,
      })
    }
  }

  /** Load the vault inventory for the secret pickers. Called when an editor
   *  opens — the vault is accessed for data at that moment, not when the
   *  connections LIST is shown (the list does not need the vault). If the
   *  vault is sealed, the dispatcher's vault seam raises the unlock prompt
   *  and retries on success; cancelling leaves the rows empty. */
  async function loadSecretRows(): Promise<void> {
    // Clear first: a re-opened editor must not briefly offer rows from a
    // previous load while this request awaits (possibly an unlock dialog).
    setSecretRows([])
    let rows: InventoryEntry[] = []
    try {
      const inv = await props.vaultClient?.inventory()
      rows = inv?.entries ?? []
    } catch {
      rows = []
    }
    setSecretRows(rows)
  }

  // ── Import ────────────────────────────────────────────────────────────

  const IMPORT_SOURCES = createMemo((): { value: ImportSource; label: string }[] => {
    const cfg = sshConfigPath()
    return [
      // The path is the backend's answer, not a guess. Until it arrives the
      // option is named without one rather than with a plausible fiction.
      { value: 'sshConfig', label: cfg?.path ? `SSH config (${cfg.path})` : 'SSH config' },
      { value: 'tabby', label: 'Tabby config (.yml/.yaml)' },
    ]
  })

  const importHint = createMemo(() => {
    switch (importSource()) {
      case 'sshConfig': {
        const cfg = sshConfigPath()
        if (cfg && !cfg.available) {
          return 'This build has no SSH config reader wired, so there is nothing to import from.'
        }
        const where = cfg?.path ? `this machine’s ${cfg.path}` : 'this machine’s SSH config'
        return `Reads ${where} and saves its aliases as connections. An alias whose name or host is already saved is skipped, so running it twice is safe.`
      }
      case 'tabby':
        return 'Connections, groups and secrets from a Tabby configuration. A preview is shown before anything is written so you can review collisions and skipped secrets.'
    }
  })

  function openImportDialog() {
    setImportSource('sshConfig')
    setImportFile(null)
    setImportPassphrase('')
    setImportOpen(true)
    // Asked on open rather than on mount: it is only ever needed to draw this
    // dialog, and most sessions never open it.
    if (sshConfigPath() === null) {
      props.client
        .sshConfigPath()
        .then(setSSHConfigPath)
        .catch((err: unknown) => {
          // Not worth a toast — the label falls back to naming no path, and
          // the import itself reports its own failure if it comes to that.
          log.warn('Could not resolve the SSH config path', { message: (err as Error).message })
        })
    }
  }

  function closeImportDialog() {
    setImportOpen(false)
    setImportFile(null)
    setImportPassphrase('')
  }

  async function runImport() {
    const source = importSource()
    const file = importFile()
    if (source !== 'sshConfig' && !file) {
      showToast({ level: 'warning', message: 'Choose a file to import' })
      return
    }

    setImportBusy(true)
    try {
      switch (source) {
        case 'sshConfig': {
          const { profilesImported, skipped } = await props.client.importSSHConfig()
          if (profilesImported === 0 && skipped === 0) {
            showToast({ level: 'info', message: 'No SSH config aliases to import' })
          } else if (skipped > 0) {
            // Sticky: "12 imported" alone reads as everything, and the
            // skipped ones are the part the user may want to go look at.
            showToast({
              level: 'warning',
              duration: 0,
              message:
                `Imported ${profilesImported} connections from ~/.ssh/config, ` +
                `${skipped} skipped (name or host already saved)`,
            })
          } else {
            showToast({
              level: 'success',
              message: `Imported ${profilesImported} connections from ~/.ssh/config`,
            })
          }
          break
        }
        case 'tabby': {
          // Preview first, then open preview dialog for confirmation.
          const preview = await props.client.tabbyPreview(
            await file!.text(),
            importPassphrase() || undefined,
          )
          setPreviewResult(preview)
          closeImportDialog()
          setPreviewOpen(true)
          break
        }
      }
      closeImportDialog()
      await loadAll()
    } catch (err) {
      const message = (err as Error).message
      log.error('Import failed', { source, message })
      showToast({ level: 'danger', message: `Import failed: ${message}` })
    } finally {
      setImportBusy(false)
    }
  }

  function closePreview() {
    setPreviewOpen(false)
    setPreviewResult(null)
  }

  async function executeImport() {
    const preview = previewResult()
    if (!preview) return

    const doExecute = async (): Promise<void> => {
      const result = await props.client.tabbyExecute(preview.planToken)
      setPreviewOpen(false)
      setPreviewResult(null)
      showToast({
        level: 'success',
        message: `Imported ${result.profilesImported} connections, ${result.groupsImported} groups`,
      })
      await loadAll()
    }

    if (props.vaultController) {
      try {
        await props.vaultController.saveSecretWithVault(doExecute, 'import connections')
      } catch (err) {
        // The user cancelled the vault prompt — nothing ran, nothing failed.
        // The preview stays open so they can retry or close it deliberately.
        if (err instanceof VaultOperationCancelledError) return
        const message = (err as Error).message
        log.error('Tabby import failed', { message })
        showToast({ level: 'danger', message: `Tabby import failed: ${message}` })
      }
    } else {
      try {
        await doExecute()
      } catch (err) {
        const message = (err as Error).message
        log.error('Tabby import failed', { message })
        showToast({ level: 'danger', message: `Tabby import failed: ${message}` })
      }
    }
  }
  function openGroupEditor(group: ProfileGroup) {
    setEditingGroup(group)
    setGroupDraft(JSON.parse(JSON.stringify(group)) as ProfileGroup)
    setGroupImpact(null)
    setDangerConfirmed(false)
    setGroupSection('general')
    groupValidation.reset()
    const gd = group.defaults ?? {}
    setGroupKeyMode(gd.keySecret ? 'secret' : gd.keyPath ? 'path' : DEFAULT_KEY_MODE)
    setGroupKeyText('')
    setGroupKeyFingerprint(undefined)
    setGroupKeyTextError(undefined)
    void loadSecretRows()
    setGroupDialogOpen(true)
  }

  /**
   * Open the group editor on a blank group.
   *
   * The id stays empty: the backend mints it on groups.create, the same way it
   * mints a profile id. A renderer that invented one would have to know the
   * store's uniqueness rule, and it is not the renderer's rule.
   */
  function startNewGroup() {
    openGroupEditor({ id: '', name: '' })
  }

  function closeGroupEditor() {
    setGroupDialogOpen(false)
    setEditingGroup(null)
    setGroupDraft(null)
    setGroupImpact(null)
    setGroupImpactBusy(false)
    setDangerConfirmed(false)
    setGroupKeyMode(DEFAULT_KEY_MODE)
    setGroupKeyText('')
    setGroupKeyFingerprint(undefined)
    setGroupKeyTextError(undefined)
  }

  async function computeGroupImpact(draft: ProfileGroup) {
    if (!draft.id) return
    setDangerConfirmed(false)
    setGroupImpactBusy(true)
    try {
      const result = await props.client.groupImpact({ group: draft })
      setGroupImpact(result)
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to compute group impact', { message })
      setGroupImpact(null)
    } finally {
      setGroupImpactBusy(false)
    }
  }

  async function saveGroup() {
    const draft = groupDraft()
    if (!draft) return
    // The gate refuses: every failing field is revealed, the first is focused
    // (the reveal hook opened the section holding it first), and the count is
    // announced through the toast region.
    if (!(await groupGate())) return
    setGroupApplyBusy(true)
    try {
      // Key material save (publicKey paste mode in group defaults)
      const defaults = draft.defaults ?? {}
      if (defaults.auth === 'publicKey' && suppliesMaterial(groupKeyMode()) && groupKeyText()) {
        const generatedName = secretNameFor('private-key', draft.name)
        const saveKeymat = () => props.client.saveKeyMaterial(groupKeyText(), generatedName)
        let result: { row: string; fingerprint: string; passphraseWanted: boolean } | null = null
        const run = async () => {
          result = await saveKeymat()
        }
        try {
          if (props.vaultController) {
            await props.vaultController.saveSecretWithVault(run, 'save this key')
          } else {
            await run()
          }
        } catch (err) {
          if (err instanceof VaultOperationCancelledError) return
          const message = (err as Error).message
          log.error('Failed to save key material for group defaults', { message })
          showToast({ level: 'danger', message: `Could not save the key: ${message}` })
          return
        }
        const saved = result!
        setGroupDefaultsField('keySecret', saved.row)
        setGroupDefaultsField('keyPath', undefined)
        if (saved.passphraseWanted) {
          const outcome = await askKeyPassphrase(
            saved.row,
            // The prompt names the LOGIN the key belongs to — not the key's
            // own generated name ("Key for …"), which would title the prompt
            // "Passphrase for Key for deploy@host".
            draft.name,
            secretNameFor('key-passphrase', draft.name),
          )
          if (outcome.saved && outcome.row) {
            setGroupDefaultsField('keyPassphraseSecret', outcome.row)
          }
        }
        setGroupKeyText('')
        // Recurse to save the updated draft: the recursion re-reads
        // groupDraft(), which the setGroupDefaultsField calls above updated.
        return saveGroup()
      }
      // A group whose defaults name vault secrets IS a vault access: while
      // the vault is sealed the user must be asked to unlock, never silently
      // allowed to bind rows the vault cannot currently back.
      const gd = draft.defaults ?? {}
      const hasBindings = !!(gd.passwordSecret || gd.keySecret || gd.keyPassphraseSecret)
      const persist = async (): Promise<void> => {
        if (!draft.id) {
          await props.client.createGroup(draft)
        } else {
          await props.client.groupApply([draft])
        }
      }
      if (hasBindings && props.vaultController) {
        await props.vaultController.saveSecretWithVault(persist, 'use a saved secret')
      } else {
        await persist()
      }
      closeGroupEditor()
      await loadAll()
      showToast({ level: 'success', message: `Saved group "${draft.name}"` })
    } catch (err) {
      if (
        err instanceof RpcError &&
        typeof err.data === 'object' &&
        err.data &&
        'reason' in err.data &&
        err.data.reason === 'invalid-key'
      ) {
        const detail = (err as Error).message
        setGroupKeyTextError(detail)
        showToast({ level: 'danger', message: `Could not save the key: ${detail}` })
        log.error('Invalid key material in group defaults', { message: detail })
        setGroupApplyBusy(false)
        return
      }
      const message = (err as Error).message
      log.error('Failed to save group', { message })
      showToast({ level: 'danger', message: `Could not save group: ${message}` })
    } finally {
      setGroupApplyBusy(false)
    }
  }

  function confirmDeleteGroup(group: ProfileGroup) {
    setDeleteGroupId(group.id)
    void computeDeleteImpact(group.id)
    setDeleteConfirmOpen(true)
  }

  async function computeDeleteImpact(groupId: string) {
    setDeleteBusy(true)
    try {
      const result = await props.client.groupImpact({ deleteGroupId: groupId })
      setDeleteImpact(result)
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to compute delete impact', { message })
      setDeleteImpact(null)
      showToast({ level: 'danger', message: `Could not preview deletion: ${message}` })
    } finally {
      setDeleteBusy(false)
    }
  }

  async function executeDeleteGroup() {
    const gid = deleteGroupId()
    if (!gid) return
    setDeleteBusy(true)
    try {
      await props.client.deleteGroup(gid)
      setDeleteConfirmOpen(false)
      setDeleteGroupId(null)
      setDeleteImpact(null)
      await loadAll()
      showToast({ level: 'success', message: 'Group deleted' })
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to delete group', { message })
      showToast({ level: 'danger', message: `Could not delete group: ${message}` })
    } finally {
      setDeleteBusy(false)
    }
  }

  function cancelDeleteGroup() {
    setDeleteConfirmOpen(false)
    setDeleteGroupId(null)
    setDeleteImpact(null)
  }

  function setGroupField(key: keyof ProfileGroup, value: unknown) {
    const current = groupDraft()
    if (!current) return
    const updated = { ...current, [key]: value }
    setGroupDraft(updated)
    void computeGroupImpact(updated)
  }

  function setGroupDefaultsField(key: string, value: unknown) {
    const current = groupDraft()
    if (!current) return
    // Convert types based on field — the backend expects typed sparse values.
    let typed: unknown = value
    if (value === '' || value === undefined || value === null) {
      typed = undefined // unset — let the empty delete below handle it
    } else {
      const numericFields = new Set([
        'port',
        'keepaliveInterval',
        'keepaliveCountMax',
        'readyTimeout',
      ])
      if (numericFields.has(key)) {
        const n = Number(value)
        typed = isNaN(n) ? undefined : n
      } else if (key === 'agentForward') {
        typed = value === true || value === 'true'
      }
    }
    const defaults = { ...(current.defaults || {}), [key]: typed }
    if (typed === undefined || typed === null) {
      delete defaults[key]
    }
    const updated = { ...current, defaults } as ProfileGroup
    setGroupDraft(updated)
    void computeGroupImpact(updated)
  }

  /**
   * The group's defaults, split the way the connection editor splits the same
   * settings. Nine fields in one list is what made this dialog a tube; they
   * were never one subject anyway — a secret binding and a keepalive
   * interval are not read at the same moment.
   */
  const CONNECTION_DEFAULTS: { key: string; label: string }[] = [
    { key: 'port', label: 'Port' },
    { key: 'jumpHost', label: 'Jump server' },
  ]

  const ADVANCED_DEFAULTS: { key: string; label: string }[] = [
    { key: 'keepaliveInterval', label: 'Keepalive interval (ms)' },
    { key: 'keepaliveCountMax', label: 'Keepalive count max' },
    { key: 'readyTimeout', label: 'Ready timeout (ms)' },
    { key: 'agentForward', label: 'Agent forward' },
    { key: 'portDiscovery', label: 'Port discovery' },
    { key: 'desiredMode', label: 'Delivery mode' },
  ]

  /** Human-readable field labels for the impact summary. */
  function fieldLabel(key: string): string {
    const m: Record<string, string> = {
      port: 'port',
      user: 'username',
      auth: 'auth mode',
      jumpHost: 'jump server',
      keepaliveInterval: 'keepalive interval',
      keepaliveCountMax: 'keepalive count max',
      readyTimeout: 'ready timeout',
      agentForward: 'agent forwarding',
      portDiscovery: 'port discovery',
      desiredMode: 'delivery mode',
    }
    return m[key] ?? key
  }

  function renderImpactSummary(impact: GroupImpactResponse): JSX.Element {
    const profiles = impact.affectedProfiles ?? []
    const dangerous = impact.dangerous
    if (profiles.length === 0) return <p class="cm-impact-none">No connections affected</p>

    const dangerousCount = profiles.filter((p) => p.diffs.some((d) => d.dangerous)).length

    return (
      <div class="cm-impact">
        <p class="cm-impact-count" role="status">
          Affects <strong>{profiles.length}</strong> connection{profiles.length === 1 ? '' : 's'}
          <Show when={dangerous}>
            <span class="cm-impact-dangerous"> &middot; includes auth-affecting changes</span>
          </Show>
        </p>
        <Show when={dangerous}>
          <div class="cm-impact-danger-badge" role="alert">
            This change affects authentication for {dangerousCount} connection
            {dangerousCount === 1 ? '' : 's'} and requires explicit confirmation.
          </div>
        </Show>
        <table class="cm-impact-table" role="list">
          <For each={profiles}>
            {(pi) => (
              <tr class="cm-impact-row" role="listitem">
                <td class="cm-impact-profile">{pi.profileName}</td>
                <td class="cm-impact-diffs">
                  <For each={pi.diffs}>
                    {(d) => (
                      <span
                        class="cm-impact-diff"
                        classList={{ 'cm-impact-diff-dangerous': d.dangerous }}
                      >
                        <Show when={d.dangerous} fallback={<Badge tone="warning">changed</Badge>}>
                          <Badge tone="danger">dangerous</Badge>
                        </Show>
                        {fieldLabel(d.field)}:{' '}
                        {typeof d.oldValue === 'string'
                          ? d.oldValue
                          : (JSON.stringify(d.oldValue) ?? '(none)')}{' '}
                        →{' '}
                        {typeof d.newValue === 'string'
                          ? d.newValue
                          : (JSON.stringify(d.newValue) ?? '(none)')}
                      </span>
                    )}
                  </For>
                </td>
              </tr>
            )}
          </For>
        </table>
      </div>
    )
  }
  function renderGroupEditor(): JSX.Element {
    // Read inside the accessors, never once at the top. A read up here makes
    // the whole editor one computation, so every keystroke rebuilt the form's
    // DOM and took the caret with it — the field lost focus after the first
    // character typed. Read per value and Solid updates the one attribute.
    function gv(key: string): unknown {
      const draft = groupDraft()
      if (!draft) return undefined
      if (key === 'name') return draft.name
      if (key === 'description') return draft.description ?? ''
      return (draft.defaults ?? {})[key]
    }

    function setG(key: string, v: string) {
      if (key === 'name' || key === 'description') {
        setGroupField(key, v)
      } else {
        setGroupDefaultsField(key, v)
      }
    }
    const jumpOptions = createMemo((): SelectOption[] =>
      jumpServerProfiles().map((p) => ({
        value: p.id,
        label: p.name,
      })),
    )

    // Not a component — a render helper for one row, so the fields are read
    // once at call time on purpose. Named parameters would trip the
    // no-destructure rule, which cannot tell the two apart.
    function renderDefault(field: { key: string; label: string }): JSX.Element {
      const key = field.key
      const label = field.label
      if (key === 'jumpHost') {
        return (
          <Field for={`group-default-${key}`} label={label}>
            <div class="cm-field-row">
              <Select
                value={gv(key) as string}
                onChange={(v) => setG(key, v || '')}
                options={jumpOptions()}
                placeholder="&mdash; Not set (inherit) &mdash;"
              />
            </div>
          </Field>
        )
      }
      if (key === 'agentForward') {
        return (
          <Checkbox
            label={label}
            checked={gv(key) === true}
            onChange={(v) => setG(key, v ? 'true' : '')}
          />
        )
      }
      if (key === 'portDiscovery') {
        return (
          <Field for={`group-default-${key}`} label={label}>
            <div class="cm-field-row">
              <Select
                value={(gv(key) as string) ?? ''}
                onChange={(v) => setG(key, v || '')}
                options={PORT_DISCOVERY_MODES.map((m) => ({ value: m, label: m }))}
                placeholder="&mdash; Not set (inherit) &mdash;"
              />
            </div>
          </Field>
        )
      }
      if (key === 'desiredMode') {
        return (
          <Field for={`group-default-${key}`} label={label}>
            <div class="cm-field-row">
              <Select
                value={(gv(key) as string) ?? ''}
                onChange={(v) => setG(key, v || '')}
                options={DESIRED_MODES.map((m) => ({ value: m.value, label: m.label }))}
                placeholder="&mdash; Not set (inherit) &mdash;"
              />
            </div>
          </Field>
        )
      }
      return (
        <TextField
          id={`group-default-${key}`}
          label={label}
          value={gv(key) != null ? String(gv(key)) : ''}
          type={
            key === 'port' ||
            key.includes('Timeout') ||
            key.includes('Count') ||
            key.includes('interval')
              ? 'number'
              : 'text'
          }
          onInput={(v) => setG(key, v)}
          placeholder="&mdash; Not set (inherit) &mdash;"
        />
      )
    }

    function renderDefaults(fields: { key: string; label: string }[]): JSX.Element {
      return (
        <Stack>
          <p class="cm-hint">
            Inherited by every connection in this group and its subgroups, unless the connection
            overrides it.
          </p>
          <For each={fields}>{(f) => renderDefault(f)}</For>
        </Stack>
      )
    }

    const groupKeyPathValue = () => (gv('keyPath') as string | undefined) ?? ''
    function handleGroupKeyPathChange(v: string | undefined) {
      setGroupDefaultsField('keyPath', v || undefined)
      // A path and a bound key secret are mutually exclusive (ADR-0017).
      setGroupDefaultsField('keySecret', undefined)
    }
    function renderConnectionDefaults(): JSX.Element {
      // An accessor, not a value: this function is called from a JSX position,
      // so a read here is a read by the computation that builds the whole tab.
      // `const auth = gv('auth')` made every keystroke in User rebuild the
      // section and drop the caret — the same defect the comment at the top of
      // this editor describes, reintroduced one line lower.
      const auth = () => gv('auth')
      return (
        <Stack>
          <p class="cm-hint">
            Inherited by every connection in this group and its subgroups, unless the connection
            overrides it.
          </p>
          <AuthenticationEditor
            id="group-default-auth"
            username={(gv('user') as string | undefined) || undefined}
            onUsernameChange={(value) => setGroupDefaultsField('user', value)}
            auth={auth() === undefined ? undefined : (auth() as AuthMode)}
            onAuthChange={(value) => setGroupDefaultsField('auth', value)}
            passwordSecrets={
              vaultOffersSecrets() ? secretRows().filter((e) => e.kind === 'password') : []
            }
            passwordSecret={(gv('passwordSecret') as string | undefined) || undefined}
            onPasswordSecretChange={(value) => setGroupDefaultsField('passwordSecret', value)}
            publicKeyAction={
              <Field for="group-default-key" label="Private Key">
                <KeyMaterialInput
                  id="group-default-key"
                  mode={groupKeyMode()}
                  onModeChange={(value) => {
                    const prev = groupKeyMode()
                    if (value === 'material') {
                      handleGroupKeyPathChange(undefined)
                    } else if (prev === 'material') {
                      setGroupKeyText('')
                      setGroupKeyFingerprint(undefined)
                      setGroupKeyTextError(undefined)
                    }
                    if (value === 'secret') {
                      // Entering secret mode: the bound row replaces any
                      // typed/pasted material, and a path cannot stay set.
                      setGroupKeyText('')
                      setGroupKeyFingerprint(undefined)
                      setGroupKeyTextError(undefined)
                      setGroupDefaultsField('keyPath', undefined)
                    } else if (prev === 'secret') {
                      setGroupDefaultsField('keySecret', undefined)
                    }
                    if (value === 'path' || value === 'file') {
                      setGroupKeyText('')
                      setGroupKeyFingerprint(undefined)
                      setGroupKeyTextError(undefined)
                    }
                    setGroupKeyMode(value)
                  }}
                  secrets={
                    vaultOffersSecrets() ? secretRows().filter((e) => e.kind === 'private-key') : []
                  }
                  secretValue={(gv('keySecret') as string | undefined) || undefined}
                  onSecretChange={(v) => {
                    if (v) {
                      setGroupDefaultsField('keySecret', v)
                      setGroupDefaultsField('keyPath', undefined)
                      setGroupKeyText('')
                      setGroupKeyFingerprint(undefined)
                      setGroupKeyTextError(undefined)
                    } else {
                      setGroupDefaultsField('keySecret', undefined)
                    }
                  }}
                  pathValue={groupKeyPathValue()}
                  onPathChange={(v) => handleGroupKeyPathChange(v || undefined)}
                  pathPlaceholder="— Not set (inherit) —"
                  materialValue={groupKeyText()}
                  onMaterialChange={(v) => {
                    setGroupKeyText(v)
                    setGroupKeyTextError(undefined)
                  }}
                  error={groupKeyTextError()}
                  fingerprint={groupKeyFingerprint()}
                  openFileDialog={props.dialogClient?.openFileDialog.bind(props.dialogClient)}
                />
              </Field>
            }
          />
          <For each={CONNECTION_DEFAULTS}>{(field) => renderDefault(field)}</For>
        </Stack>
      )
    }

    return (
      <div class="cm-group-form">
        <Tabs
          items={[
            {
              id: 'general',
              label: 'General',
              content: () => (
                <Stack>
                  <TextField
                    id="group-name"
                    label="Name"
                    required
                    value={gv('name') as string}
                    error={groupValidation.error('name')}
                    onInput={(v) => {
                      setG('name', v)
                      groupValidation.answer('name', v)
                    }}
                    onBlur={() => groupValidation.touch('name')}
                  />
                  <TextField
                    id="group-description"
                    label="Description"
                    value={gv('description') as string}
                    onInput={(v) => setG('description', v)}
                  />
                </Stack>
              ),
            },
            {
              id: 'connection',
              label: 'Connection',
              content: renderConnectionDefaults,
            },
            {
              id: 'advanced',
              label: 'Advanced',
              content: () => renderDefaults(ADVANCED_DEFAULTS),
            },
          ]}
          active={groupSection()}
          onChange={setGroupSection}
          ariaLabel="Group sections"
        />

        {/* Pinned under the pane, not filed as a section. What a change is
            about to do to other connections has to be in front of the person
            making it, and a section is something you can decline to open. */}
        {/* Only when it has something to say. The block is a warning, and a
            warning that fires on every edit to report that nothing happened
            teaches the reader to stop looking at it — which is exactly the
            moment it needs to be read. */}
        <Show when={groupImpactWorthShowing()}>
          {(i) => (
            <div class="cm-group-impact">
              <Show when={!groupImpactBusy()} fallback={<p>Computing impact…</p>}>
                {renderImpactSummary(i())}
                <Show when={i().dangerous}>
                  <div class="cm-danger-confirm">
                    <Checkbox
                      label="I understand this will change authentication for affected connections"
                      checked={dangerConfirmed()}
                      onChange={(v) => setDangerConfirmed(v)}
                    />
                  </div>
                </Show>
              </Show>
            </div>
          )}
        </Show>
      </div>
    )
  }

  async function loadEffective(ids: string[]) {
    if (ids.length === 0) return
    try {
      const res = await props.client.loadEffective(ids)
      setEffectiveData((prev) => {
        const next = { ...prev }
        for (const eff of res.profiles) {
          next[eff.id] = eff
        }
        return next
      })
    } catch (err) {
      log.error('Failed to load effective data', { message: (err as Error).message })
    }
  }

  async function loadSessionStatuses() {
    const pids = profiles().map((x) => x.id)
    if (pids.length === 0) return
    try {
      const res = await props.client.sessionStatus(pids)
      setSessionStatuses(res.statuses ?? {})
    } catch (err) {
      log.error('Failed to load session status', { message: (err as Error).message })
    }
  }
  async function handleTest(profile: SSHProfile) {
    setProbeBusy((prev) => new Set(prev).add(profile.id))
    try {
      const res = await props.client.connectionTest(profile.id)
      if (
        res.outcome === 'needs-interactive' &&
        profile.options.keySecret &&
        !profile.options.keyPassphraseSecret
      ) {
        // An encrypted vault key with no stored passphrase: the key is fine,
        // it is only locked. Ask for the passphrase, bind it, and re-test —
        // not an error the user must decode.
        const outcome = await askKeyPassphrase(
          profile.options.keySecret,
          loginLabel(profile.options.user, profile.options.host),
          secretNameFor('key-passphrase', loginLabel(profile.options.user, profile.options.host)),
        )
        if (outcome.saved && outcome.row) {
          const bound = {
            ...profile,
            options: { ...profile.options, keyPassphraseSecret: outcome.row },
          }
          try {
            await props.client.updateProfile(bound)
            await loadAll()
          } catch (err) {
            const message = (err as Error).message
            log.error('Failed to save the key passphrase binding', { message })
            showToast({ level: 'danger', message: `Could not save the passphrase: ${message}` })
            return
          }
          return handleTest(bound)
        }
        return
      }
      if (
        (res.outcome === 'host-key-unknown' || res.outcome === 'host-key-changed') &&
        res.hostKey
      ) {
        // The outcome IS the question, and a toast cannot carry a decision:
        // raise the accept dialog. The two cases are separate controls — a
        // changed key never gets the routine accept button (nocx-6v1p).
        setPendingHostKey({
          profile,
          evidence: res.hostKey,
        })
        return
      }
      showToast({
        level: res.outcome === 'accepted' ? 'success' : 'warning',
        message: res.detail
          ? `${probeOutcomeLabel(res.outcome)}: ${res.detail}`
          : probeOutcomeLabel(res.outcome),
      })
    } catch (err) {
      const message = (err as Error).message
      log.error('Connection test failed', { profileId: profile.id, message })
      showToast({ level: 'danger', message: `Test failed: ${message}` })
    } finally {
      setProbeBusy((prev) => {
        const next = new Set(prev)
        next.delete(profile.id)
        return next
      })
    }
  }

  /**
   * Accept the pending host key: append it to known_hosts via the client,
   * then re-probe — the accept only means something if the next test
   * succeeds. Declining (closing the dialog) writes nothing at all.
   */
  async function acceptPendingHostKey() {
    const pending = pendingHostKey()
    if (!pending) return
    setHostKeyBusy(true)
    try {
      await props.client.trustHostKey(pending.evidence.knownHostsHost, pending.evidence.key)
      setPendingHostKey(null)
      await handleTest(pending.profile)
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to trust host key', { host: pending.evidence.host, message })
      showToast({ level: 'danger', message: `Could not trust the host key: ${message}` })
    } finally {
      setHostKeyBusy(false)
    }
  }

  // Initial load on mount — profiles, session status, and effective data.
  // loadAll triggers loadSessionStatuses and loadEffective internally after
  // profiles are set, so the async continuation does not need to be tracked.
  onMount(() => {
    void loadAll()
  })

  // After profiles load, fetch session status and effective data for them.
  createEffect(
    on(
      () =>
        profiles()
          .map((x) => x.id)
          .join(','),
      (ids) => {
        if (!ids) return
        void loadSessionStatuses()
        void loadEffective(ids.split(','))
      },
    ),
  )
  // The palette's "New connection" request.
  createEffect(
    on(
      () => props.newProfileRequest ?? 0,
      (n) => {
        if (n > 0) startNewProfile()
      },
    ),
  )

  // ── Form / dialog helpers ─────────────────────────────────────────────

  function startNewProfile() {
    setQuickConnectValue('')
    setQuickConnectOpen(true)
  }

  function closeQuickConnect() {
    setQuickConnectOpen(false)
    setQuickConnectValue('')
  }

  function handleQuickConnect() {
    const q = quickConnectValue().trim()
    if (!q) {
      showToast({ level: 'warning', message: 'Enter a host, alias, or connection string' })
      return
    }

    const parsed = parseQuickConnect(q)
    const profile: SSHProfile = {
      id: '',
      type: 'ssh',
      name: parsed.options.host || 'New connection',
      options: {
        host: parsed.options.host,
        port: parsed.options.port ?? 22,
        user: parsed.options.user ?? '',
        auth: '',
      },
    }

    // If the input had an ssh:// prefix, that's expected — nothing lost.
    // Report any other part that didn't survive: if the original contained
    // an '@' or ':' but parsing left the host empty, the format was wrong.
    const hadAtOrColon = q.includes('@') || q.includes(':')
    if (!parsed.options.host && hadAtOrColon) {
      showToast({
        level: 'warning',
        message: `Could not parse "${q}": try "user@host:port" or "ssh://user@host:port"`,
      })
    }

    closeQuickConnect()
    setProfileSection('general')
    setEditing(profile)
    setDirtyFields(new Set<string>())
    profileValidation.reset()
    setProfileKeyMode(DEFAULT_KEY_MODE)
    setProfileKeyText('')
    setProfileKeyFingerprint(undefined)
    setProfileKeyTextError(undefined)
    setDialogOpen(true)
    void loadSecretRows()
  }

  function openEditDialog(profile: SSHProfile) {
    setProfileSection('general')
    setEditing(profile)
    setDirtyFields(new Set<string>())
    profileValidation.reset()
    // Open on the key source the connection actually uses: the bound secret,
    // the path, or the default. A saved connection must show what it
    // authenticates with, not the first segment (b5bu).
    setProfileKeyMode(
      profile.options.keySecret ? 'secret' : profile.options.keyPath ? 'path' : DEFAULT_KEY_MODE,
    )
    setProfileKeyText('')
    setProfileKeyFingerprint(undefined)
    setProfileKeyTextError(undefined)
    setDialogOpen(true)
    void loadSecretRows()
  }

  function closeDialog() {
    setDialogOpen(false)
    setEditing(null)
    setProfilePasswordOpen(false)
    setProfilePasswordValue('')
    setMintedPasswordNames(new Map())
    setDirtyFields(new Set<string>())
    setProfileMoveImpact(null)
    setProfileKeyMode(DEFAULT_KEY_MODE)
    setProfileKeyText('')
    setProfileKeyFingerprint(undefined)
    setProfileKeyTextError(undefined)
  }

  // ── Validation ──────────────────────────────────────────────────────────

  const formProfile = createMemo<SSHProfile | null>(() => {
    return editing()
  })

  /**
   * THE owner of "what value does this field resolve to while the editor is
   * open". The inputs paint it and validation reads it, so a fallback the
   * field invents cannot disagree with a validator that never saw it
   * (nocx-a88r: the port input painted 22 while the validator rejected the
   * empty draft). Draft edits win while dirty; an explicit stored value wins
   * over the inherited one; the effective cascade (group/global/hardcoded
   * default) answers only the fields this profile omits.
   */
  function fieldValue(key: string): unknown {
    const draft = editing()
    if (!draft) return undefined
    const dirty = dirtyFields()
    if (dirty.has(key)) {
      return (draft.options as unknown as Record<string, unknown>)[key]
    }
    const own = (draft.options as unknown as Record<string, unknown>)[key]
    if (own !== undefined && own !== null) return own
    const eff = effectiveData()[draft.id]?.fields[key]
    if (eff !== undefined) return eff.value
    return undefined
  }

  /** The resolved value as text — the shape the validators judge. A value
   *  that is neither string nor number (an object, a bare boolean) is not
   *  text a rule can judge, so it reads as empty. */
  function fieldText(key: string): string {
    const v = fieldValue(key)
    if (typeof v === 'string') return v
    if (typeof v === 'number') return String(v)
    return ''
  }
  // Where each rule's control lives, and which Tabs section holds it. Rule
  // keys are logical names (`host`, `port`); the control ids are `profile-*`.
  // The forwards list has no single focusable control — its error is a
  // row-level message under the list — so it maps to no id, and the gate says
  // it could not focus rather than pretend it did. A control on an unopened
  // panel is NOT in the DOM (inactive panels carry `hidden`), which is why
  // the gate's reveal hook must open the section before focusing.
  const PROFILE_CONTROL_ID: Record<string, string | undefined> = {
    name: 'profile-name',
    host: 'profile-host',
    port: 'profile-port',
    keepaliveInterval: 'profile-keepalive-interval',
    keepaliveCountMax: 'profile-keepalive-count',
    readyTimeout: 'profile-ready-timeout',
    forwards: undefined,
  }
  const PROFILE_SECTION: Record<string, string> = {
    name: 'general',
    host: 'general',
    port: 'general',
    key: 'auth',
    keepaliveInterval: 'advanced',
    keepaliveCountMax: 'advanced',
    readyTimeout: 'advanced',
    forwards: 'forwards',
  }

  const profileValidation = createFormValidation(
    {
      name: () => required('Name')(formProfile()?.name ?? ''),
      host: () => combine(required('Host'), hostname())(formProfile()?.options.host ?? ''),
      port: () => combine(required('Port'), portRule())(fieldText('port')),
      // A Public Key connection with no key is a dead end: nothing to offer
      // at connect time. The key may come from a stored secret, a path, or
      // pasted material — whichever it is, a missing key blocks save.
      key: () => {
        const p = formProfile()
        if (!p || p.options.auth !== 'publicKey') return undefined
        if (p.options.keySecret || p.options.keyPath) return undefined
        if (suppliesMaterial(profileKeyMode()) && profileKeyText()) return undefined
        return 'Choose a private key: a file, a path, pasted material, or a stored secret'
      },
      keepaliveInterval: () =>
        nonNegativeInteger('Keepalive interval')(
          String(formProfile()?.options.keepaliveInterval ?? ''),
        ),
      keepaliveCountMax: () =>
        nonNegativeInteger('Keepalive count max')(
          String(formProfile()?.options.keepaliveCountMax ?? ''),
        ),
      readyTimeout: () =>
        nonNegativeInteger('Ready timeout')(String(formProfile()?.options.readyTimeout ?? '')),
      // The stored forwards must be a list the connect-time replay accepts —
      // the editor and the backend ask the same question (firstForwardError
      // mirrors ValidForwards). An invalid row blocks save, never ships.
      forwards: () => {
        const rows = formProfile()?.options.forwards
        if (!rows || rows.length === 0) return undefined
        return firstForwardError(rows)
      },
    },
    {
      // The key material editor's focusable inputs carry the mode's suffix
      // (`profile-key-path` / `profile-key-text`); in secret or file mode
      // there is no text control to focus.
      controlId: (field) => {
        if (field === 'key') {
          if (profileKeyMode() === 'path') return 'profile-key-path'
          if (profileKeyMode() === 'material') return 'profile-key-text'
          return undefined
        }
        return PROFILE_CONTROL_ID[field]
      },
    },
  )

  // The one kit-owned answer to "how a form refuses a submit": reveal every
  // failing field, focus the first one, and announce how many need attention.
  // A failing field may live on an unopened panel — keepalive fields and the
  // forwards list are not in the DOM until their section is shown, so the
  // reveal hook opens the panel holding the field before focus is attempted.
  const profileGate = createSubmitGate(profileValidation, {
    reveal: (field) => {
      setProfileSection(PROFILE_SECTION[field])
    },
  })

  // ── Save / delete / connect ────────────────────────────────────────────

  async function saveProfile(profile: SSHProfile) {
    if (!(await profileGate())) return

    // A Save landing while a password mint is still resolving waits for the
    // mint's bind: the bind persists the binding AND updates the draft, so
    // the save must write the freshest state — the one that carries it.
    // Without this, a save pressed in that window writes auth without
    // passwordSecret, the exact on-disk shape of the report (W8).
    if (mintInFlight) {
      await mintInFlight
      const fresh = editing()
      if (fresh) profile = fresh
    }

    // Key material save (publicKey paste/upload mode)
    if (
      profile.options.auth === 'publicKey' &&
      suppliesMaterial(profileKeyMode()) &&
      profileKeyText()
    ) {
      const generatedName = generatedSecretName(
        'private-key',
        profile.options.user,
        profile.options.host,
      )
      const saveKeymat = () => props.client.saveKeyMaterial(profileKeyText(), generatedName)
      let result: { row: string; fingerprint: string; passphraseWanted: boolean } | null = null
      const run = async () => {
        result = await saveKeymat()
      }
      try {
        if (props.vaultController) {
          await props.vaultController.saveSecretWithVault(run, 'save this key')
        } else {
          await run()
        }
      } catch (err) {
        if (err instanceof VaultOperationCancelledError) {
          return
        }
        if (
          err instanceof RpcError &&
          typeof err.data === 'object' &&
          err.data &&
          'reason' in err.data &&
          err.data.reason === 'invalid-key'
        ) {
          // Both, and neither alone is enough. The field error marks WHICH
          // control is wrong and survives on screen while it is corrected; the
          // toast is what makes the press of Create visibly do something —
          // without it the button appeared inert, which is how this was
          // reported. The backend's own sentence is used rather than a generic
          // one: it distinguishes a public key from a corrupt file from a
          // PEM-encrypted key needing conversion.
          const detail = (err as Error).message
          setProfileKeyTextError(detail)
          showToast({ level: 'danger', message: `Could not save the key: ${detail}` })
          log.error('Invalid key material', { message: detail })
          return
        }
        const message = (err as Error).message
        log.error('Failed to save key material', { message })
        showToast({ level: 'danger', message: `Could not save the key: ${message}` })
        return
      }
      const saved = result!
      setProfileKeyFingerprint(saved.fingerprint)
      let keyPassphraseRow: string | undefined
      if (saved.passphraseWanted) {
        const outcome = await askKeyPassphrase(
          saved.row,
          // The prompt names the LOGIN the key belongs to, not the key's own
          // generated name — see the group flow for the same reasoning.
          loginLabel(profile.options.user, profile.options.host),
          secretNameFor('key-passphrase', loginLabel(profile.options.user, profile.options.host)),
        )
        if (outcome.saved && outcome.row) keyPassphraseRow = outcome.row
      }
      // Bind the minted rows on the draft and re-enter: the save below must
      // persist the profile with the bindings on it.
      const linked = {
        ...profile,
        options: {
          ...profile.options,
          keySecret: saved.row,
          keyPath: undefined,
          ...(keyPassphraseRow ? { keyPassphraseSecret: keyPassphraseRow } : {}),
        },
      }
      setEditing(linked)
      setProfileKeyText('')
      setDirtyFields((prev) => {
        const next = new Set(prev)
        next.add('keySecret')
        if (keyPassphraseRow) next.add('keyPassphraseSecret')
        return next
      })
      await saveProfile(linked)
      return
    }
    const isNew = !profile.id || !profiles().some((x) => x.id === profile.id)

    const hasBindings = !!(
      profile.options.passwordSecret ||
      profile.options.keySecret ||
      profile.options.keyPassphraseSecret
    )
    // Saving a profile that names vault secrets IS a vault access: while the
    // vault is sealed the user must be asked to unlock, never silently
    // allowed to bind rows the vault cannot currently back (the backend
    // resolves the row even when sealed, which is exactly the silent path
    // this closes).
    let persisted: SSHProfile | null = null
    const persist = async (): Promise<void> => {
      if (isNew) {
        persisted = await props.client.createProfile(profile)
      } else {
        persisted = await props.client.updateProfile(profile)
      }
    }
    const persistGuarded = async (): Promise<SSHProfile> => {
      if (hasBindings && props.vaultController) {
        await props.vaultController.saveSecretWithVault(persist, 'use a saved secret')
        return persisted!
      }
      await persist()
      return persisted!
    }

    if (isNew) {
      try {
        const saved = await persistGuarded()
        closeDialog()
        await loadAll()
        void loadSessionStatuses()
        void loadEffective([saved.id])
        showToast({ level: 'success', message: `Saved "${saved.name}"` })
      } catch (err) {
        const message = (err as Error).message
        log.error('Failed to save', { message })
        showToast({ level: 'danger', message: `Could not save the connection: ${message}` })
      }
      return
    }

    const dirty = new Set(dirtyFields())
    // If the group changed from the original, force a full update.
    const origProfile = profiles().find((p) => p.id === profile.id)
    if (origProfile && origProfile.group !== profile.group) {
      dirty.add('group')
    }
    const route = decideSaveRoute(profile, dirty)

    switch (route.kind) {
      case 'noop':
        closeDialog()
        return

      case 'update':
        try {
          const saved = await persistGuarded()
          closeDialog()
          await loadAll()
          await loadEffective([saved.id])
          void loadSessionStatuses()
          showToast({ level: 'success', message: `Saved "${saved.name}"` })
        } catch (err) {
          const message = (err as Error).message
          log.error('Failed to save', { message })
          showToast({ level: 'danger', message: `Could not save the connection: ${message}` })
        }
        return

      case 'patch':
        try {
          const eff = await (route.patchSet &&
          Object.keys(route.patchSet).some((k) =>
            ['options.passwordSecret', 'options.keySecret', 'options.keyPassphraseSecret'].includes(
              k,
            ),
          ) &&
          props.vaultController
            ? (async () => {
                let out: EffectiveProfileDTO
                const client = props.client
                await props.vaultController!.saveSecretWithVault(async () => {
                  out = await client.patchProfile({ id: profile.id, set: route.patchSet })
                }, 'use a saved secret')
                return out!
              })()
            : props.client.patchProfile({ id: profile.id, set: route.patchSet }))
          setEffectiveData((prev) => ({ ...prev, [profile.id]: eff }))
          closeDialog()
          showToast({ level: 'success', message: `Saved "${profile.name}"` })
        } catch (err) {
          const message = (err as Error).message
          log.error('Failed to save', { message })
          showToast({ level: 'danger', message: `Could not save the connection: ${message}` })
        }
        return
    }
  }

  async function computeMoveImpact(profileId: string, targetGroupId: string) {
    setProfileMoveImpact(null)
    try {
      const result = await props.client.moveImpact({ profileIds: [profileId], targetGroupId })
      setProfileMoveImpact(result)
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to compute move impact', { message })
      setProfileMoveImpact(null)
    }
  }

  async function deleteProfile(profile: SSHProfile) {
    if (!(await showConfirm(`Delete "${profile.name}"?`))) return
    try {
      await props.client.deleteProfile(profile.id)
      closeDialog()
      await loadAll()
      void loadSessionStatuses()
      showToast({ level: 'success', message: `Deleted "${profile.name}"` })
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to delete profile', { message })
      showToast({ level: 'danger', message: `Could not delete "${profile.name}": ${message}` })
    }
  }

  // ── Derived data ──────────────────────────────────────────────────────

  const jumpServerProfiles = createMemo(() => profiles().filter((p) => p.options.canBeJumpServer))

  // ── Filtered + grouped list ──────────────────────────────────────────

  const filteredProfiles = createMemo(() => {
    const q = searchQuery().toLowerCase()
    if (!q) return profiles()
    return profiles().filter(
      (p) =>
        p.name.toLowerCase().includes(q) ||
        p.options.host.toLowerCase().includes(q) ||
        (p.options.user || '').toLowerCase().includes(q),
    )
  })

  const tree = createMemo(() => buildGroupTree(groups()))

  const ungrouped = createMemo(() =>
    filteredProfiles().filter((p) => !p.group || !groups().some((g) => g.id === p.group)),
  )

  // Profiles in each group, filtered
  function groupProfiles(groupId: string): SSHProfile[] {
    return filteredProfiles().filter((p) => p.group === groupId)
  }

  // ── Row render helpers ───────────────────────────────────────────────
  function renderRow(p: SSHProfile) {
    const status = () => sessionStatuses()[p.id]
    const isTesting = () => probeBusy().has(p.id)
    // The row's status: the kit's dot + text, the connections idiom. A live
    // session is the ok tone; a disconnected one is neutral — a state that
    // has nothing to say in colour. The last-used date rides the same
    // sentence, one row, one status.
    const statusLine = () => {
      const st = status()
      if (!st) return undefined
      const lastUsed = st.lastUsed
        ? ` · last used ${new Date(st.lastUsed).toLocaleDateString()}`
        : ''
      return {
        tone: st.live ? ('ok' as const) : ('neutral' as const),
        text: `${st.live ? 'Connected' : 'Disconnected'}${lastUsed}`,
      }
    }
    return (
      <RecordRow
        title={p.name}
        kind={{ label: p.type.toUpperCase() }}
        meta={`${p.options.user ? `${p.options.user}@` : ''}${p.options.host}:${p.options.port || 22}`}
        status={statusLine()}
        actions={
          <>
            <IconButton
              size="sm"
              title="Edit"
              ariaLabel={`Edit ${p.name}`}
              onClick={() => openEditDialog(p)}
            >
              <PencilIcon />
            </IconButton>
            <Button
              variant="ghost"
              size="sm"
              disabled={isTesting()}
              onClick={() => void handleTest(p)}
              ariaLabel={`Test connection to ${p.name}`}
            >
              <CheckCircleIcon />
              {isTesting() ? 'Testing...' : 'Test'}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              title="Connect"
              ariaLabel={`Connect to ${p.name}`}
              onClick={() => props.onConnect?.(p)}
            >
              <PlugIcon />
              Connect
            </Button>
          </>
        }
      />
    )
  }

  /** Does this group, or anything under it, hold a connection the filter kept? */
  function groupHasMatches(node: TreeNode): boolean {
    if (groupProfiles(node.id).length > 0) return true
    return node.children.some((c) => groupHasMatches(c))
  }

  function renderGroupSection(node: TreeNode) {
    // An empty group used to render nothing at all, so creating one looked
    // like the button had failed — and with no header there was no way to
    // reach its editor to rename or delete it either. While a filter is
    // active it is noise, so that is the only case that still hides it.
    if (searchQuery().trim() !== '' && !groupHasMatches(node)) return null
    const gp = groupProfiles(node.id)
    const collapsed = () => collapsedGroups().has(node.id)
    const toggleCollapse = () => {
      setCollapsedGroups((prev) => {
        const next = new Set(prev)
        if (next.has(node.id)) next.delete(node.id)
        else next.add(node.id)
        return next
      })
    }
    return (
      <>
        <div
          class="cm-group-header"
          role="heading"
          aria-level={2}
          data-collapsed={collapsed() ? 'true' : 'false'}
        >
          <IconButton
            size="sm"
            title={collapsed() ? 'Expand group' : 'Collapse group'}
            ariaLabel={`${collapsed() ? 'Expand' : 'Collapse'} group ${node.name}`}
            aria-expanded={!collapsed()}
            onClick={toggleCollapse}
          >
            <ChevronDownIcon />
          </IconButton>
          <span class="cm-group-name">{node.name}</span>
          <span class="cm-group-actions">
            <IconButton
              size="sm"
              title="Edit group"
              ariaLabel={`Edit group ${node.name}`}
              onClick={() => openGroupEditor(node)}
            >
              <PencilIcon />
            </IconButton>
            <IconButton
              size="sm"
              title="Delete group"
              ariaLabel={`Delete group ${node.name}`}
              onClick={() => confirmDeleteGroup(node)}
            >
              <TrashIcon />
            </IconButton>
          </span>
        </div>
        <Show when={!collapsed()}>
          <Show when={gp.length === 0 && node.children.length === 0}>
            <p class="cm-group-empty">
              No connections here yet — pick this group in a connection&rsquo;s editor to move it
              in.
            </p>
          </Show>
          <For each={gp}>{(p) => renderRow(p)}</For>
          <For each={node.children}>{(child) => renderGroupSection(child)}</For>
        </Show>
      </>
    )
  }
  // ── Dialog form (profile editor) ──────────────────────────────────────

  /**
   * The profile arrives as an ACCESSOR, and that is load-bearing rather than
   * a style choice.
   *
   * This function is called from a JSX insert position, so Solid wraps the
   * call in a computation and tracks whatever it reads. Taking the profile by
   * value meant reading `editing()` at the call site, so every keystroke —
   * each one writes a new profile object through setOption — invalidated that
   * computation and rebuilt the entire form. The input the user was typing
   * into was replaced by a new element mid-word: one character landed, focus
   * fell to `<body>`, and the next keystroke went nowhere. Measured in a
   * browser, not deduced: `document.activeElement` was `BODY` after one key.
   *
   * With an accessor, nothing is read when the form is built, so the call
   * runs once and only the individual bindings that read it re-run.
   */
  function renderProfileForm(profile: () => SSHProfile) {
    function setOption(key: keyof SSHProfile['options'], value: unknown) {
      const updated = { ...profile(), options: { ...profile().options, [key]: value } }
      setEditing(updated)
      setDirtyFields((prev: Set<string>) => {
        const next = new Set(prev)
        next.add(key)
        return next
      })
    }

    // ── Stored forwards (spec §8, D5) ──────────────────────────────────
    // The list is a single options field: every row edit rewrites it and
    // marks the field dirty, so the save route carries the whole list and
    // the backend patch replaces it wholesale.
    const forwards = (): ForwardSpec[] =>
      (fieldValue('forwards') as ForwardSpec[] | undefined) ?? []

    function updateForward(index: number, patch: Partial<ForwardSpec>) {
      const rows = forwards().map((r, i) => (i === index ? { ...r, ...patch } : r))
      setOption('forwards', rows)
    }

    function removeForward(index: number) {
      setOption(
        'forwards',
        forwards().filter((_, i) => i !== index),
      )
    }

    function addForward() {
      setOption('forwards', [
        ...forwards(),
        { direction: 'local', bindHost: '', bindPort: 0, destination: '' },
      ])
    }

    function renderForwardRow(row: () => ForwardSpec, index: number): JSX.Element {
      return (
        <div class="cm-forward-row">
          <Field for={`forward-${index}-direction`} label="Direction">
            <Select
              value={row().direction}
              onChange={(v) => updateForward(index, { direction: v as ForwardDirection })}
              options={FORWARD_DIRECTIONS.map((d) => ({ value: d, label: d }))}
            />
          </Field>
          <TextField
            id={`forward-${index}-bindhost`}
            label="Bind host"
            value={row().bindHost ?? ''}
            placeholder="127.0.0.1"
            onInput={(v) => updateForward(index, { bindHost: v || undefined })}
          />
          <TextField
            id={`forward-${index}-bindport`}
            label="Bind port"
            type="number"
            min={0}
            value={row().bindPort != null ? String(row().bindPort) : '0'}
            onInput={(v) => {
              const n = parseInt(v, 10)
              updateForward(index, { bindPort: isNaN(n) ? 0 : n })
            }}
          />
          <Show when={row().direction !== 'dynamic'}>
            <TextField
              id={`forward-${index}-destination`}
              label="Destination"
              value={row().destination ?? ''}
              placeholder="host:port"
              onInput={(v) => updateForward(index, { destination: v })}
            />
          </Show>
        </div>
      )
    }

    function onNameChange(v: string) {
      const updated = { ...profile(), name: v }
      setEditing(updated)
      setDirtyFields((prev: Set<string>) => {
        const next = new Set(prev)
        next.add('name')
        return next
      })
    }

    const keyPathValue = () => fvStr('keyPath')
    function handleKeyPathChange(v: string | undefined) {
      setOption('keyPath', v || undefined)
      // A path and a bound key secret are mutually exclusive (ADR-0017).
      setOption('keySecret', undefined)
    }

    /** The bound password secret's display name, for the Password action.
     *  The inventory is the first word, but a row minted in THIS editor
     *  session is not in it until the post-mint reload lands — the binding
     *  made a moment ago must not read as "No password set" in the meantime
     *  (W3). The minted name is authoritative: the backend stores the
     *  requested name unchanged (ADR-0016). */
    const boundPasswordName = createMemo(() => {
      const row = fvStr('passwordSecret')
      if (!row) return undefined
      return secretRows().find((e) => e.id === row)?.name ?? mintedPasswordNames().get(row)
    })

    const isSaved = () => !!profile().id && profiles().some((x) => x.id === profile().id)
    function fvStr(key: string): string {
      const v = fieldValue(key)
      if (typeof v === 'string') return v
      if (typeof v === 'number') return String(v)
      if (typeof v === 'boolean') return v ? 'true' : ''
      return ''
    }

    function fvNum(key: string): number {
      const v = fieldValue(key)
      return v != null && typeof v === 'number' ? v : 0
    }

    function fvBool(key: string): boolean {
      const v = fieldValue(key)
      return v === true
    }

    const jumpOptions = createMemo((): SelectOption[] =>
      jumpServerProfiles().map((p) => ({
        value: p.id,
        label: p.name,
      })),
    )
    const groupOptions = createMemo((): SelectOption[] =>
      groups().map((g) => ({
        value: g.id,
        label: g.name,
      })),
    )

    function fieldRow(field: string, textField: JSX.Element) {
      void field
      return <div class="cm-field-row">{textField}</div>
    }

    return (
      <div class="cm-form">
        <Tabs
          items={[
            {
              id: 'general',
              label: 'General',
              content: () => (
                <Stack>
                  <TextField
                    id="profile-name"
                    label="Name"
                    required
                    value={profile().name}
                    error={profileValidation.error('name')}
                    onInput={(v) => {
                      onNameChange(v)
                      profileValidation.answer('name', v)
                    }}
                    onBlur={() => profileValidation.touch('name')}
                  />
                  {fieldRow(
                    'host',
                    <TextField
                      id="profile-host"
                      label="Host"
                      required
                      value={fvStr('host')}
                      error={profileValidation.error('host')}
                      onInput={(v) => {
                        setOption('host', v)
                        profileValidation.answer('host', v)
                      }}
                      onBlur={() => profileValidation.touch('host')}
                    />,
                  )}
                  {fieldRow(
                    'port',
                    <TextField
                      id="profile-port"
                      label="Port"
                      required
                      value={fvNum('port')}
                      type="number"
                      error={profileValidation.error('port')}
                      onInput={(v) => {
                        const n = parseInt(v, 10)
                        setOption('port', isNaN(n) ? 0 : n)
                        profileValidation.answer('port', v)
                      }}
                      onBlur={() => profileValidation.touch('port')}
                    />,
                  )}
                  <Show when={isSaved()}>
                    <Field for="profile-group" label="Group">
                      <Select
                        value={profile().group ?? ''}
                        onChange={(v) => {
                          const targetGroupId = v || ''
                          setEditing({ ...profile(), group: targetGroupId || undefined })
                          setDirtyFields((prev) => new Set(prev).add('group'))
                          if (profile().id) void computeMoveImpact(profile().id, targetGroupId)
                        }}
                        options={groupOptions()}
                        placeholder="&mdash; No group &mdash;"
                      />
                    </Field>
                  </Show>
                </Stack>
              ),
            },
            {
              id: 'auth',
              label: 'Authentication',
              content: () => (
                <Stack>
                  <AuthenticationEditor
                    id="profile-auth"
                    username={fvStr('user')}
                    onUsernameChange={(value) => setOption('user', value)}
                    auth={fvStr('auth') as AuthMode}
                    onAuthChange={(value) => setOption('auth', value)}
                    passwordSecrets={
                      vaultOffersSecrets() ? secretRows().filter((e) => e.kind === 'password') : []
                    }
                    passwordSecret={fvStr('passwordSecret') || undefined}
                    onPasswordSecretChange={(value) => {
                      setOption('passwordSecret', value)
                      // A picked secret replaces any typed-new draft: a later
                      // "Set Password" must not silently override the pick.
                      setProfilePasswordValue('')
                    }}
                    passwordAction={
                      <Field for="profile-password-action" label="Password">
                        <div class="secret-action">
                          <span class="secret-description">
                            {boundPasswordName()
                              ? `Password: ${boundPasswordName()}`
                              : 'No password set'}
                          </span>
                          <div class="secret-actions">
                            <Button variant="default" onClick={() => setProfilePasswordOpen(true)}>
                              {boundPasswordName() ? 'Change Password' : 'Set Password'}
                            </Button>
                          </div>
                        </div>
                        <p class="cm-hint">
                          Setting a password saves it to the connection immediately — closing this
                          editor afterwards won't undo it.
                        </p>
                      </Field>
                    }
                    publicKeyAction={
                      <Field
                        for="profile-key"
                        label="Private Key"
                        error={profileValidation.error('key')}
                      >
                        <KeyMaterialInput
                          id="profile-key"
                          mode={profileKeyMode()}
                          onModeChange={(value) => {
                            const prev = profileKeyMode()
                            if (value === 'material') {
                              handleKeyPathChange(undefined)
                            } else if (prev === 'material') {
                              setProfileKeyText('')
                              setProfileKeyFingerprint(undefined)
                              setProfileKeyTextError(undefined)
                            }
                            if (value === 'secret') {
                              // Entering secret mode: the bound row replaces
                              // any typed/pasted material, and a path cannot
                              // stay set.
                              setProfileKeyText('')
                              setProfileKeyFingerprint(undefined)
                              setProfileKeyTextError(undefined)
                              setOption('keyPath', undefined)
                            } else if (prev === 'secret') {
                              setOption('keySecret', undefined)
                            }
                            if (value === 'path' || value === 'file') {
                              setProfileKeyText('')
                              setProfileKeyFingerprint(undefined)
                              setProfileKeyTextError(undefined)
                            }
                            setProfileKeyMode(value)
                          }}
                          secrets={
                            vaultOffersSecrets()
                              ? secretRows().filter((e) => e.kind === 'private-key')
                              : []
                          }
                          secretValue={fvStr('keySecret') || undefined}
                          onSecretChange={(v) => {
                            if (v) {
                              setOption('keySecret', v)
                              setOption('keyPath', undefined)
                              setProfileKeyText('')
                              setProfileKeyFingerprint(undefined)
                              setProfileKeyTextError(undefined)
                            } else {
                              setOption('keySecret', undefined)
                            }
                          }}
                          pathValue={keyPathValue()}
                          onPathChange={(v) => handleKeyPathChange(v || undefined)}
                          materialValue={profileKeyText()}
                          onMaterialChange={(v) => {
                            setProfileKeyText(v)
                            setProfileKeyTextError(undefined)
                          }}
                          error={profileKeyTextError()}
                          fingerprint={profileKeyFingerprint()}
                          openFileDialog={props.dialogClient?.openFileDialog.bind(
                            props.dialogClient,
                          )}
                        />
                      </Field>
                    }
                  />
                </Stack>
              ),
            },
            {
              id: 'advanced',
              label: 'Advanced',
              content: () => (
                <Stack>
                  {fieldRow(
                    'keepaliveInterval',
                    <TextField
                      id="profile-keepalive-interval"
                      label="Keepalive interval (ms)"
                      value={fvNum('keepaliveInterval')}
                      type="number"
                      min={0}
                      error={profileValidation.error('keepaliveInterval')}
                      onInput={(v) => {
                        const n = parseInt(v, 10)
                        setOption('keepaliveInterval', isNaN(n) ? 0 : n)
                        profileValidation.answer('keepaliveInterval', v)
                      }}
                      onBlur={() => profileValidation.touch('keepaliveInterval')}
                    />,
                  )}
                  {fieldRow(
                    'keepaliveCountMax',
                    <TextField
                      id="profile-keepalive-count"
                      label="Keepalive count max"
                      value={fvNum('keepaliveCountMax')}
                      type="number"
                      min={0}
                      error={profileValidation.error('keepaliveCountMax')}
                      onInput={(v) => {
                        const n = parseInt(v, 10)
                        setOption('keepaliveCountMax', isNaN(n) ? 0 : n)
                        profileValidation.answer('keepaliveCountMax', v)
                      }}
                      onBlur={() => profileValidation.touch('keepaliveCountMax')}
                    />,
                  )}
                  {fieldRow(
                    'readyTimeout',
                    <TextField
                      id="profile-ready-timeout"
                      label="Ready timeout (ms)"
                      value={fvNum('readyTimeout')}
                      type="number"
                      min={0}
                      error={profileValidation.error('readyTimeout')}
                      onInput={(v) => {
                        const n = parseInt(v, 10)
                        setOption('readyTimeout', isNaN(n) ? 0 : n)
                        profileValidation.answer('readyTimeout', v)
                      }}
                      onBlur={() => profileValidation.touch('readyTimeout')}
                    />,
                  )}
                  <Field for="jump-host" label="Jump server">
                    <div class="cm-field-row">
                      <Select
                        value={fvStr('jumpHost')}
                        onChange={(v) => setOption('jumpHost', v || undefined)}
                        options={jumpOptions()}
                        placeholder="&mdash; None &mdash;"
                      />
                    </div>
                  </Field>
                  <Field for="port-discovery" label="Port discovery">
                    <div class="cm-field-row">
                      <Select
                        value={fvStr('portDiscovery')}
                        onChange={(v) => setOption('portDiscovery', v || undefined)}
                        options={PORT_DISCOVERY_MODES.map((m) => ({ value: m, label: m }))}
                        placeholder="&mdash; Inherited &mdash;"
                      />
                    </div>
                  </Field>
                  <Field for="desired-mode" label="Delivery mode">
                    <div class="cm-field-row">
                      <Select
                        value={fvStr('desiredMode')}
                        onChange={(v) => setOption('desiredMode', v || undefined)}
                        options={DESIRED_MODES.map((m) => ({ value: m.value, label: m.label }))}
                        placeholder="&mdash; Inherited &mdash;"
                      />
                    </div>
                  </Field>
                  <Show when={fvStr('desiredMode') === 'relay'}>
                    <Field for="relay-consent" label="Relay consent">
                      <div class="cm-field-row">
                        <Select
                          value={fvStr('relayConsent') || 'unknown'}
                          onChange={(v) => setOption('relayConsent', v || undefined)}
                          options={RELAY_CONSENTS.map((m) => ({ value: m.value, label: m.label }))}
                          placeholder="&mdash; Unknown &mdash;"
                        />
                      </div>
                      <p class="cm-hint">
                        The relay deploys a binary on the destination; that needs explicit consent
                        per host, and a relay selection without granted consent behaves as raw.
                      </p>
                    </Field>
                  </Show>
                  <div class="cm-check-group">
                    <Checkbox
                      label="Agent forward"
                      checked={fvBool('agentForward')}
                      onChange={(v) => setOption('agentForward', v)}
                    />
                    <Checkbox
                      label="Can be used as jump server"
                      checked={fvBool('canBeJumpServer')}
                      onChange={(v) => setOption('canBeJumpServer', v)}
                    />
                  </div>
                </Stack>
              ),
            },
            {
              id: 'forwards',
              label: 'Forwards',
              content: () => (
                <Stack>
                  <p class="cm-hint">
                    Forwards opened automatically when this connection comes up. A forward that
                    fails — a busy local port, a refused acquire — fails its row only; the session
                    and every other forward still establish.
                  </p>
                  <EditableRowList
                    rows={forwards()}
                    ariaLabel="Stored forwards"
                    addLabel="Add forward"
                    emptyLabel="No forwards — add one to open it automatically on connect."
                    removeLabel={(i) => `Remove forward ${i + 1}`}
                    onRemove={removeForward}
                    onAdd={addForward}
                    renderRow={(row, i) => renderForwardRow(row, i)}
                    error={profileValidation.error('forwards')}
                  />
                </Stack>
              ),
            },
          ]}
          active={profileSection()}
          onChange={setProfileSection}
          ariaLabel="Connection sections"
        />

        {/* Pinned under the pane, like the group editor's blast radius: what a
            move does to inherited settings has to be in front of the person
            making it, not filed behind a section they can decline to open. */}
        <Show
          when={
            profileMoveImpact() &&
            ((profileMoveImpact()!.affectedProfiles?.length ?? 0) > 0 ||
              profileMoveImpact()!.dangerous)
          }
        >
          <div class="cm-group-impact">{renderImpactSummary(profileMoveImpact()!)}</div>
        </Show>
      </div>
    )
  }

  // ── Main render ────────────────────────────────────────────────────────

  return (
    <div class="cm-root">
      <CollectionView
        searchValue={searchQuery()}
        onSearch={setSearchQuery}
        searchPlaceholder="Filter connections"
        searchLabel="Filter connections"
        actions={
          <>
            <Button variant="default" onClick={openImportDialog}>
              Import…
            </Button>
            <Button variant="default" onClick={startNewGroup}>
              New group
            </Button>
            <Button variant="primary" onClick={startNewProfile}>
              + New connection
            </Button>
          </>
        }
        hasItems={profiles().length > 0 || groups().length > 0}
        empty={
          <EmptyState
            title="No connections yet"
            description="Add one by hand, or import from ~/.ssh/config, Tabby, or an export."
            action={
              <>
                <Button variant="primary" onClick={startNewProfile}>
                  + New connection
                </Button>
                <Button variant="default" onClick={openImportDialog}>
                  Import…
                </Button>
              </>
            }
          />
        }
      >
        <div role="list" aria-label="Connection list">
          <For each={tree()}>{(node) => renderGroupSection(node)}</For>
          <Show when={ungrouped().length > 0}>
            <div class="cm-group-header" role="heading" aria-level={2}>
              <span class="cm-group-name">{groups().length > 0 ? 'Ungrouped' : 'Connections'}</span>
            </div>
            <For each={ungrouped()}>{(p) => renderRow(p)}</For>
          </Show>
        </div>
        {/* A filter that matches nothing hid every row and every group and
              said nothing, which is indistinguishable from the list failing
              to load. */}
        <Show when={searchQuery().trim() !== '' && filteredProfiles().length === 0}>
          <EmptyState
            title="Nothing matches this filter"
            description={`No connection's name, host or user contains "${searchQuery().trim()}".`}
          />
        </Show>
      </CollectionView>

      {/* The remote footprint (nocx-mlm7 P10): hosts nocx has installed
          shell integration on, and the uninstall action for the ones a
          saved connection reaches. Placed here, never repainted. */}
      <FootprintSection client={props.footprintClient} />

      {/* Editor Dialog */}
      <Show when={editing()}>
        {(profile) => (
          <Dialog
            open={dialogOpen()}
            onClose={closeDialog}
            title={profile().id ? `Edit Connection: ${profile().name}` : 'New Connection'}
            size="lg"
            onSubmit={() => void saveProfile(profile())}
            footer={
              <>
                <Button variant="primary" onClick={() => void saveProfile(profile())}>
                  {profile().id ? 'Save Connection' : 'Create Connection'}
                </Button>
                <Show when={profile().id}>
                  <Button variant="danger" onClick={() => void deleteProfile(profile())}>
                    Delete Connection
                  </Button>
                </Show>
                <Button variant="default" onClick={closeDialog}>
                  Cancel
                </Button>
              </>
            }
          >
            {renderProfileForm(profile)}
            <PasswordEditor
              open={profilePasswordOpen()}
              value={profilePasswordValue()}
              prompt={`Password for ${
                editing()?.options.user || editing()?.options.host || 'connection'
              }`}
              onClose={() => setProfilePasswordOpen(false)}
              onSave={(password) => {
                // Mint-and-bind at the action moment (ADR-0017, W8): the
                // secret is stored AND the binding is written to the stored
                // profile when the user presses OK — not when the profile is
                // saved. The editor closes immediately; the mint continues in
                // the background and its bind persists the binding, so there
                // is no window in which a secret exists bound to nothing, and
                // a later Cancel of the editor will not undo the write (the
                // action row says so). A new profile has nothing to bind to
                // yet: the binding rides the draft and creation persists both
                // halves.
                const current = profile()
                const generatedName = secretNameFor(
                  'password',
                  loginLabel(current.options.user, current.options.host),
                )
                const savePw = () => props.client.savePassword(password, generatedName)
                let row: { row: string } | null = null
                const run = async () => {
                  row = await savePw()
                }
                const bind = async () => {
                  if (!row) return
                  const mintedRow = row
                  // Merge into the LIVE draft, not the OK-press snapshot: the
                  // mint runs behind a vault prompt and the user may have
                  // edited other fields in the meantime (W8).
                  setEditing((prev) =>
                    prev
                      ? { ...prev, options: { ...prev.options, passwordSecret: mintedRow.row } }
                      : prev,
                  )
                  setDirtyFields((prev) => new Set(prev).add('passwordSecret'))
                  setMintedPasswordNames((prev) => {
                    const next = new Map(prev)
                    next.set(mintedRow.row, generatedName)
                    return next
                  })
                  setProfilePasswordValue('')
                  // The inventory does not know about this row yet — reload it
                  // so the pickers and any later read see the mint. The action
                  // row already names the secret from mintedPasswordNames, so
                  // the display never depends on the reload landing.
                  void loadSecretRows()
                  // The stored profile must carry the pair the editor always
                  // keeps together: a password secret is only ever offered
                  // under the Password method (authentication-editor.tsx), so
                  // auth travels with the binding, or a mint-then-cancel on a
                  // profile without a method would store an invisible secret.
                  const isNew = !current.id || !profiles().some((p) => p.id === current.id)
                  if (!isNew) {
                    try {
                      await props.client.patchProfile({
                        id: current.id,
                        set: {
                          'options.auth': 'password',
                          'options.passwordSecret': mintedRow.row,
                        },
                      })
                    } catch (err) {
                      // The secret was minted, so the failure is the binding:
                      // name the split out loud, or the half-done state reads
                      // as a silent success (AGENTS.md: a soft degrade must be
                      // visible in the product). The draft keeps the binding,
                      // so Save Connection retries the write.
                      const message = (err as Error).message
                      log.error('Failed to persist the password binding', { message })
                      showToast({
                        level: 'danger',
                        message: `The password was stored but this connection was not updated to use it: ${message}. Save Connection to retry.`,
                      })
                    }
                  }
                }
                const fail = (err: unknown) => {
                  if (err instanceof VaultOperationCancelledError) {
                    // The editor closed on OK, so the mint kept running in the
                    // background; a cancelled vault prompt leaves the password
                    // unsaved with nothing on screen to say why. Silence here
                    // reads as success (AGENTS.md: a soft degrade must be
                    // visible in the product).
                    log.warn('Password save cancelled: the vault prompt was closed', {})
                    showToast({
                      level: 'warning',
                      message: 'Password was not saved — the vault prompt was cancelled.',
                    })
                    return
                  }
                  const message = (err as Error).message
                  log.error('Failed to save password', { message })
                  showToast({ level: 'danger', message: `Could not save the password: ${message}` })
                }
                const mint = (async () => {
                  try {
                    if (props.vaultController) {
                      await props.vaultController.saveSecretWithVault(run, 'save this password')
                    } else {
                      await run()
                    }
                    await bind()
                  } catch (err) {
                    fail(err)
                  }
                })()
                // A Save pressed while the mint is still resolving must wait
                // for the bind: the save must write a profile that carries
                // the binding (W8 — the save that landed before the bind
                // produced the auth-without-secret profile on disk).
                mintInFlight = mint
                void mint.finally(() => {
                  mintInFlight = null
                })
              }}
            />
          </Dialog>
        )}
      </Show>

      {/* Quick-connect Dialog — creation starts from one field */}
      <Dialog
        open={quickConnectOpen()}
        onClose={closeQuickConnect}
        title="New Connection"
        size="lg"
        onSubmit={handleQuickConnect}
        footer={
          <>
            <Button variant="primary" onClick={handleQuickConnect}>
              Next
            </Button>
            <Button variant="default" onClick={closeQuickConnect}>
              Cancel
            </Button>
          </>
        }
      >
        <TextField
          id="quick-connect-input"
          label="Host or connection string"
          value={quickConnectValue()}
          onInput={(v) => setQuickConnectValue(v)}
          placeholder="deploy@host:2222 or ssh://user@host:2222"
        />
        <p class="cm-hint">
          Paste a host, alias, or connection string above. Parsed fields will be filled into the
          form.
        </p>
      </Dialog>

      {/* Import Dialog — bringing connections in from elsewhere */}
      <Dialog
        open={importOpen()}
        onClose={closeImportDialog}
        title="Import Connections"
        size="lg"
        footer={
          <>
            <Button variant="primary" disabled={importBusy()} onClick={() => void runImport()}>
              {importBusy() ? 'Importing…' : importSource() === 'tabby' ? 'Preview' : 'Import'}
            </Button>
            <Button variant="default" disabled={importBusy()} onClick={closeImportDialog}>
              Cancel
            </Button>
          </>
        }
      >
        <Field for="cm-import-source" label="Source">
          <div class="cm-radio-group">
            <For each={IMPORT_SOURCES()}>
              {(src) => (
                <Radio
                  value={src.value}
                  checked={importSource() === src.value}
                  onChange={(v) => {
                    setImportSource(v as ImportSource)
                    // A file chosen for one source is not a file for another.
                    setImportFile(null)
                  }}
                  name="cm-import-source"
                  label={src.label}
                />
              )}
            </For>
          </div>
        </Field>

        {/* The file row is always present, and disabled for the source that
            takes no file. Showing it conditionally made the dialog change
            height under the pointer as the user moved down the radio list —
            the buttons they were reaching for moved away from them. Keyed on
            the source so switching between the two file sources remounts the
            picker: FileInput holds the chosen name internally and would
            otherwise still display a file we have just discarded. */}
        <Show when={importSource()} keyed>
          {(src) => (
            <Field for="cm-import-file" label="File">
              <FileInput
                id="cm-import-file"
                accept={src === 'tabby' ? '.yml,.yaml' : '.json'}
                disabled={importBusy() || src === 'sshConfig'}
                onChange={setImportFile}
              />
            </Field>
          )}
        </Show>

        {/* Passphrase field for encrypted Tabby vaults. */}
        <Show when={importSource() === 'tabby'}>
          <Field for="cm-import-passphrase" label="Vault passphrase (if encrypted)">
            <TextField
              id="cm-import-passphrase"
              type="password"
              value={importPassphrase()}
              onInput={(v) => setImportPassphrase(v)}
              placeholder="Leave blank unless the Tabby vault is encrypted"
            />
          </Field>
        </Show>

        <p class="cm-hint cm-import-hint">{importHint()}</p>
      </Dialog>

      {/* Tabby Import Preview Dialog */}
      <Show when={previewOpen() && previewResult()}>
        {(preview) => (
          <Dialog
            open={previewOpen()}
            onClose={closePreview}
            title="Tabby Import Preview"
            size="lg"
            footer={
              <>
                <Button
                  variant="primary"
                  disabled={importBusy()}
                  onClick={() => void executeImport()}
                >
                  {importBusy() ? 'Importing…' : 'Import'}
                </Button>
                <Button variant="default" onClick={closePreview}>
                  Cancel
                </Button>
              </>
            }
          >
            <Stack gap="default">
              <p>
                The Tabby configuration contains <strong>{preview().profilesToImport}</strong>{' '}
                {preview().profilesToImport === 1 ? 'profile' : 'profiles'},{' '}
                <strong>{preview().groupsToImport}</strong>{' '}
                {preview().groupsToImport === 1 ? 'group' : 'groups'}, and{' '}
                <strong>{preview().secretsToImport}</strong>{' '}
                {preview().secretsToImport === 1 ? 'secret' : 'secrets'}.
              </p>

              <Show when={preview().profileEntries && preview().profileEntries!.length > 0}>
                <p>
                  <strong>Profiles</strong>
                </p>
                <For each={preview().profileEntries || []}>
                  {(entry) => (
                    <p>
                      {entry.name} —{' '}
                      {entry.action === 'new'
                        ? 'new'
                        : entry.action === 'overwrite'
                          ? 'will overwrite existing'
                          : 'needs review'}
                    </p>
                  )}
                </For>
              </Show>

              <Show when={preview().groupNames && preview().groupNames!.length > 0}>
                <p>
                  <strong>Groups</strong>
                </p>
                <For each={preview().groupNames || []}>{(name) => <p>{name}</p>}</For>
              </Show>

              <Show when={preview().secretEntries && preview().secretEntries!.length > 0}>
                <p>
                  <strong>Secrets</strong>
                </p>
                <For each={preview().secretEntries || []}>
                  {(entry) => (
                    <p>
                      {entry.name} ({entry.type})
                    </p>
                  )}
                </For>
              </Show>

              <Show when={preview().collisions && preview().collisions!.length > 0}>
                <p>
                  <strong>Collisions</strong>
                </p>
                <For each={preview().collisions || []}>
                  {(c) => (
                    <p>
                      {c.kind} "{c.name}" —{' '}
                      {c.policy === 'overwrite'
                        ? 'will be overwritten'
                        : c.policy === 'refuse'
                          ? 'import refused (already exists)'
                          : 'needs review'}
                    </p>
                  )}
                </For>
              </Show>

              <Show when={preview().skippedSecrets && preview().skippedSecrets!.length > 0}>
                <p>
                  <strong>Skipped secrets</strong>
                </p>
                <For each={preview().skippedSecrets || []}>
                  {(s) => (
                    <p>
                      {s.secretType}: {s.reason}
                    </p>
                  )}
                </For>
              </Show>

              <p>
                <strong>Destination:</strong> {preview().secretProvider}
              </p>
            </Stack>
          </Dialog>
        )}
      </Show>

      {/* Group Editor Dialog */}
      <Show when={editingGroup()}>
        {(group) => (
          <Dialog
            open={groupDialogOpen()}
            onClose={closeGroupEditor}
            // The live draft name, not the stored one: the title is where the
            // group's identity stays readable once the user is two sections
            // deep in defaults, and a title showing the old name while the
            // General field shows a new one is worse than no title at all.
            title={
              group().id
                ? `Edit Group: ${groupDraft()?.name || group().name}`
                : groupDraft()?.name
                  ? `New Group: ${groupDraft()!.name}`
                  : 'New Group'
            }
            size="lg"
            onSubmit={() => void saveGroup()}
            footer={
              <>
                <Button
                  variant={groupImpact()?.dangerous ? 'danger' : 'primary'}
                  disabled={groupApplyBusy() || (groupImpact()?.dangerous && !dangerConfirmed())}
                  onClick={() => void saveGroup()}
                >
                  {groupApplyBusy() ? 'Applying…' : group().id ? 'Save Group' : 'Create Group'}
                </Button>
                <Button variant="default" onClick={closeGroupEditor} disabled={groupApplyBusy()}>
                  Cancel
                </Button>
              </>
            }
          >
            {renderGroupEditor()}
          </Dialog>
        )}
      </Show>

      {/* Group Delete Confirmation Dialog */}
      <Show when={deleteConfirmOpen()}>
        <Dialog
          open={deleteConfirmOpen()}
          onClose={cancelDeleteGroup}
          // Which group. A confirmation that does not name what it is about to
          // destroy is asking the user to remember which row they clicked.
          title={deleteGroupName() ? `Delete Group: ${deleteGroupName()}` : 'Delete Group'}
          footer={
            <>
              <Button
                variant="danger"
                disabled={deleteBusy() || deleteImpact()?.deleteImpact?.action === 'refuse'}
                onClick={() => void executeDeleteGroup()}
              >
                {deleteBusy()
                  ? 'Deleting…'
                  : deleteImpact()?.deleteImpact?.action === 'refuse'
                    ? 'Cannot Delete'
                    : 'Delete Group'}
              </Button>
              {/* autofocus on Cancel, deliberately. A native showModal()
                  focuses the first focusable descendant, and this dialog's
                  body is text — so focus landed on "Delete Group" and one
                  Enter, pressed by someone who was still typing a moment ago,
                  destroyed the group. The safe action takes the focus; the
                  destructive one has to be aimed at. */}
              <Button
                variant="default"
                onClick={cancelDeleteGroup}
                disabled={deleteBusy()}
                autofocus
              >
                Cancel
              </Button>
            </>
          }
        >
          <Show when={deleteBusy() && !deleteImpact()}>
            <p>Computing impact…</p>
          </Show>
          <Show when={deleteImpact()?.deleteImpact} keyed>
            {(di) => (
              <div class="cm-delete-impact">
                <Show when={di.action === 'refuse'}>
                  <div class="cm-impact-danger-badge" role="alert">
                    {di.reason}
                  </div>
                  <p>This group cannot be deleted through this dialog.</p>
                </Show>
                <Show when={di.action === 'promote_to_root'}>
                  {/* The written sentence first, the backend's rationale after
                      it. "group has no children" is why the backend chose this
                      action, not what the user is agreeing to. */}
                  <p>
                    Delete the group <strong>{deleteGroupName()}</strong>? Its connections and
                    subgroups move to the top level; nothing is deleted with it.
                  </p>
                  <p class="cm-delete-reason">{di.reason}</p>
                  <Show
                    when={
                      deleteImpact()?.affectedProfiles &&
                      deleteImpact()!.affectedProfiles!.length > 0
                    }
                  >
                    <Section title="Affected Connections">
                      {renderImpactSummary(deleteImpact()!)}
                    </Section>
                  </Show>
                </Show>
              </div>
            )}
          </Show>
        </Dialog>
      </Show>
      {/* Key-passphrase ask — raised when a saved key wants its passphrase.
          Rendered at the root so the top-sheet Prompt floats above the editor
          dialog it interrupts. */}
      <Show when={passphraseAsk()}>
        {(ask) => (
          <KeyPassphrasePrompt
            open
            keyName={ask().keyName}
            keyRow={ask().keyRow}
            passphraseName={ask().passphraseName}
            client={props.client}
            onResult={(outcome, row) => {
              const resolve = ask().resolve
              setPassphraseAsk(null)
              resolve(outcome === 'saved' ? { saved: true, row } : { saved: false })
            }}
          />
        )}
      </Show>
      {/* Host-key decision — the same consent surface open-time failures use.
          Unknown is routine; changed is dangerous and names both fingerprints. */}
      <Show when={pendingHostKey()} keyed>
        {(pending) => (
          <HostKeyDialog
            evidence={pending.evidence}
            busy={hostKeyBusy()}
            onAccept={() => void acceptPendingHostKey()}
            onClose={() => setPendingHostKey(null)}
          />
        )}
      </Show>
    </div>
  )
}
