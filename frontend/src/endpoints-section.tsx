/**
 * AI Endpoints surface — the Settings page listing configured AI endpoints
 * (bead nocx-kn9q, design §4.5, ADR-0030).
 *
 * Follows the connections manager shape: a full-width CollectionView list
 * with dialog-based add/edit. The four endpoints.* methods are the whole
 * wire; the key is an input that never crosses back (ADR-0030 §3).
 *
 * Kit contract (frontend/src/ui/README.md): the schema is a field on the
 * record and there is NO select while one implementation exists; the Test
 * button and the address restriction belong to nocx-edio. Validation goes
 * through the kit's createFormValidation + createSubmitGate.
 */
import { For, Show, createEffect, createMemo, createSignal, on, onMount } from 'solid-js'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import { CollectionView } from './ui/collection-view'
import { RecordRow } from './ui/record-row'
import { Dialog, showConfirm } from './ui/dialog'
import { EditableRowList } from './ui/row-list'
import { EmptyState } from './ui/empty-state'
import { IconButton } from './ui/icon-button'
import { CheckCircleIcon, PencilIcon, TrashIcon } from './ui/icons'
import { Spinner } from './ui/spinner'
import { Stack } from './ui/stack'
import { TextField } from './ui/text-field'
import { SuggestionField } from './ui/suggestion-field'
import { absoluteHttpUrl, combine, createFormValidation, required } from './ui/validation'
import { createSubmitGate } from './ui/submit-gate'
import { showToast } from './ui/toast'
import { credentialLine } from './agent-status-line'
import { log } from './log'
import type { StatusDotTone } from './ui/status-dot'
import type { AgentClient } from './agent'
// INLINED by agent.status.schema.json's cross-file ref, so the generated
// agent.status.ts exports both AgentStatusResult and its own copy of
// EndpointsProbeResult. This module consumes the latter (the type is
// structurally identical) so the dead-export ratchet sees every generated
// export used — the same union trick endpoints.ts documents.
import type { EndpointsProbeResult } from './generated/agent.status'
import { EndpointClient, type Endpoint } from './endpoints'
import { SecretSource, type SecretSourceMode } from './secret-source'
import type { InventoryEntry, VaultClient } from './vault-client'
import { VaultOperationCancelledError, type VaultController } from './vault'
/** The schema's one value today (design §4.5, decision 2). Display label
 *  only; the select appears when the second implementation does. */
const SCHEMA_LABEL: Record<Endpoint['schema'], string> = {
  'openai-compatible': 'OpenAI-compatible',
}

/** The Test outcome sentence — the vocabulary nocx-q27y decided. Names
 *  WHICH check ran ("the endpoint is reachable" and "the model answered"
 *  are different facts) and what it found. ONE mapping, shared by the
 *  editor's Test and the row's Test, so the two surfaces cannot drift. */
function probeOutcomeLine(
  p: EndpointsProbeResult | null,
): { tone: 'danger' | 'success'; text: string } | null {
  if (!p) return null
  if (!p.ok) return { tone: 'danger', text: `Test failed: ${p.error}` }
  if (p.kind === 'model') {
    return { tone: 'success', text: `${p.model} answered in ${Math.max(p.elapsedMs, 1)} ms` }
  }
  const found = p.models?.length ?? 0
  return {
    tone: 'success' as const,
    // An endpoint that lists nothing is reachable and usable — GET /models
    // is not universally implemented — so the sentence says what happened
    // rather than implying something is missing.
    text: found > 0 ? `Connected — ${found} models offered` : 'Connected — it lists no models',
  }
}

/** The row's credential state, in the agent.status vocabulary (nocx-y7fg):
 *  what the frontend can know without a vault read that raises the unlock
 *  prompt.
 *
 *  - none — the endpoint has no reference at all.
 *  - sealed — the vault is locked right now (its own status); the check
 *    cannot pass, and the list is not the place to raise the unlock prompt.
 *  - deleted — the referenced secret is gone: no vault exists (never set
 *    up, or reset), or the unsealed vault's own inventory no longer lists
 *    the row the endpoint references. The deleted secret case is read from
 *    the SAME inventory the editor's secret pickers read (vault.inventory),
 *    loaded at list time and only while the vault is unsealed — a sealed
 *    vault is never asked, so the list cannot raise the prompt.
 *  - unavailable — the inventory read failed (a store failure that is none
 *    of the above): the honest sentence, and the check cannot pass.
 *  - resolvable — the vault is unsealed and the inventory lists the row, or
 *    the inventory has not been asked (no controller — the dev-web harness
 *    — or still loading): the backend resolves the stored credential at
 *    probe time, and the probe's own refusal is the truth if it does not. */
type RowCredentialState = 'resolvable' | 'none' | 'deleted' | 'sealed' | 'unavailable'

/** The inventory fact the row derivation reads: whether the unsealed vault
 *  has answered, and which rows it lists. 'idle'/'loading' mean not asked
 *  or in flight — treated optimistically, because the probe is the
 *  truth-teller when the credential really cannot resolve. */
interface RowInventoryFact {
  state: 'idle' | 'loading' | 'loaded' | 'failed'
  entries: InventoryEntry[]
}

function rowCredentialState(
  ref: string | null,
  vaultState: 'uninitialized' | 'sealed' | 'unsealed' | undefined,
  inventory: RowInventoryFact,
): RowCredentialState {
  if (ref === null) return 'none'
  if (vaultState === 'sealed') return 'sealed'
  if (vaultState === 'uninitialized') return 'deleted'
  if (inventory.state === 'loaded') {
    return inventory.entries.some((e) => e.id === ref) ? 'resolvable' : 'deleted'
  }
  if (inventory.state === 'failed') return 'unavailable'
  return 'resolvable'
}

/** One model row in the editor draft. Alias is '' while typing and becomes
 *  null on the wire when blank. */
interface ModelDraft {
  name: string
  alias: string
}

/** One custom header row in the draft (bead nocx-lyyk). The value's SOURCE
 *  is chosen with the same SecretSource control the endpoint's key uses: a
 *  literal typed fresh ('new'), or an existing vault secret ('secret' with
 *  the row handle). Exactly one source per row; on the wire the row becomes
 *  {name, value, secret} with the unused side null. */
interface HeaderDraft {
  name: string
  mode: SecretSourceMode
  value: string
  row: string
}

/** The dialog draft. The key's source is chosen (type a new one / use an
 *  existing vault secret, nocx-rzjw) instead of inferred from a caption; a
 *  typed key is an input that never crosses back, and a referenced key is a
 *  row handle that does (ADR-0030 §3). */
interface EndpointDraft {
  name: string
  baseUrl: string
  keyMode: SecretSourceMode
  /** A key typed fresh ('new' mode). Sent once, minted/rotated by the
   *  backend, never read back. */
  key: string
  /** The row handle of an existing vault secret ('secret' mode). */
  keyRow: string
  models: ModelDraft[]
  headers: HeaderDraft[]
}

const blankDraft = (): EndpointDraft => ({
  name: '',
  baseUrl: '',
  keyMode: 'new',
  key: '',
  keyRow: '',
  models: [],
  headers: [],
})
type LoadState = 'loading' | 'ready' | 'failed'
export interface EndpointsSectionProps {
  client: EndpointClient
  /** The assistant's control-plane client (nocx-edio). Kept because the
   *  editor's Test button probes through the same wire the ask uses; the
   *  page itself shows no assistant status — readiness belongs on the ask
   *  chip, where a person is actually asking, not as a badge floating above
   *  this page's frame. */
  agentClient?: AgentClient
  /** The vault layer's controller. A save that carries a key is minted into
   *  the vault (design §4.5.3), so it routes through the vault's own
   *  operation-first seam (saveSecretWithVault) — the same owner the
   *  connections path uses at the moment a secret is created (nocx-v64o).
   *  Absent in the dev-web harness and bare embeds. */
  vaultController?: VaultController
  /** Vault inventory for the secret pickers (nocx-rzjw: the endpoint's key
   *  and header values may reference an existing vault secret). Optional:
   *  the dev-web harness has no vault, and the pickers then offer nothing,
   *  exactly like the connections editor's vaultClient. */
  vaultClient?: VaultClient
  /** An outside request to open the editor on a blank endpoint, as a
   *  counter: the ask refused with "no endpoint configured" and the
   *  surface is offering the repair. A counter rather than a boolean for
   *  the reason the secrets page uses one — asking twice must open it
   *  twice, and a boolean that is already true is silence. */
  addEndpointRequest?: number
}
export function EndpointsSection(props: EndpointsSectionProps) {
  const [endpoints, setEndpoints] = createSignal<Endpoint[]>([])
  const [loadState, setLoadState] = createSignal<LoadState>('loading')
  const [loadError, setLoadError] = createSignal('')
  const [searchQuery, setSearchQuery] = createSignal('')
  const [dialogOpen, setDialogOpen] = createSignal(false)
  // The outside request (a refused ask → "configure an endpoint"). Same
  // shape as the secrets page's add-secret request.
  createEffect(
    on(
      () => props.addEndpointRequest ?? 0,
      (n) => {
        if (n > 0) openNew()
      },
    ),
  )
  /** The vault's password rows for the pickers (ADR-0017). Empty when the
   *  vault is sealed or absent (dev harness). */
  const [secretRows, setSecretRows] = createSignal<InventoryEntry[]>([])
  /** The pickers only ever offer live vault data: while the vault is sealed
   *  or uninitialized, stale rows from an earlier load must not be offered. */
  const vaultOffersSecrets = createMemo(() => {
    const state = props.vaultController?.status()?.state
    return state === undefined || state === 'unsealed'
  })
  /** The pickers' rows, filtered to the kind a secret-valued header may
   *  reference — an API key is a password-kind secret (ADR-0030's mint is
   *  KindPassword). */
  const passwordRows = createMemo(() =>
    vaultOffersSecrets() ? secretRows().filter((e) => e.kind === 'password') : [],
  )
  /** Whether the unsealed vault's inventory has answered, for the row
   *  credential state (nocx-9bx0m). Loaded at list time ONLY while the
   *  vault is unsealed: a sealed vault is never asked, so the list cannot
   *  raise the unlock prompt it would otherwise (ADR-0032). 'failed' means
   *  the store did not answer — the honest 'unavailable' sentence, never a
   *  fabricated 'deleted'. */
  const [inventoryState, setInventoryState] = createSignal<
    'idle' | 'loading' | 'loaded' | 'failed'
  >('idle')
  const inventoryFact = (): RowInventoryFact => ({
    state: inventoryState(),
    entries: secretRows(),
  })
  // Ask the vault for its rows the moment it is (or becomes) unsealed. The
  // row renders a per-endpoint credential fact, so the list now needs the
  // inventory — the same read the editor's pickers already make, through
  // the same loader. A failed read stays 'failed' ('unavailable' on the
  // row): retrying here would loop on a store that keeps refusing, and the
  // next editor open already retries through the shared loader.
  createEffect(() => {
    const state = props.vaultController?.status()?.state
    if (state === 'unsealed' && inventoryState() === 'idle') void loadInventory()
  })
  const [discovered, setDiscovered] = createSignal<string[]>([])
  /** The endpoint being edited, or null for a new one. */
  const [editing, setEditing] = createSignal<Endpoint | null>(null)
  const [draft, setDraft] = createSignal<EndpointDraft>(blankDraft())
  /** The Test button's state: idle, running, or the probe result. */
  const [probeResult, setProbeResult] = createSignal<EndpointsProbeResult | null>(null)
  const [probing, setProbing] = createSignal(false)
  /** Models the endpoint says it offers — filled by an explicit connection
   *  test OR by the silent discovery on focus. One owner, so the two paths
   *  cannot disagree about what an endpoint offers. */
  /** The (base URL, key, endpoint) the discovered list belongs to, so
   *  re-focusing does not re-dial and changing the URL does. */
  const [discoveryKey, setDiscoveryKey] = createSignal('')
  /** One probe outcome per saved endpoint, rendered on its row
   *  (nocx-9bx0m). The editor's Test verdict stays in probeResult; the
   *  row's Test verdicts live here, keyed by endpoint id. */
  const [rowProbes, setRowProbes] = createSignal<Record<string, EndpointsProbeResult>>({})
  /** Rows whose probe is in flight — the row's Test is disabled while it
   *  runs, exactly like the editor's. */
  const [rowProbing, setRowProbing] = createSignal<Set<string>>(new Set())

  // ── Data loading ─────────────────────────────────────────────────────

  async function load() {
    try {
      const eps = await props.client.listEndpoints()
      setEndpoints(eps ?? [])
      setLoadState('ready')
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to load endpoints', { message })
      setEndpoints([])
      setLoadError(message)
      setLoadState('failed')
    }
  }

  /** The Test button: probe the form's DRAFT values with a real streaming
   *  completion — what will actually be used, not one cheap completion
   *  (design §4.5). The first model is the one the picker would use.
   *
   *  When editing a SAVED endpoint the probe NAMES the record (endpointId)
   *  and the backend resolves the credential it owns — exactly how
   *  connections.test names a profile. A key typed into the form is sent
   *  too and WINS on the backend: testing a new key before saving it is
   *  the other half of what this button is for. The renderer never reads a
   *  key back (ADR-0030 §3), so it cannot send a stored one. */
  async function runProbe() {
    const d = draft()
    // An empty model is not a reason to do nothing — it is the connection
    // check. There used to be an early return here, left behind when the
    // button stopped requiring a model, which made an enabled button do
    // nothing at all: the same defect one layer down.
    const model = d.models[0]?.name.trim() ?? ''
    setProbing(true)
    setProbeResult(null)
    try {
      const res = await props.client.probeEndpoint({
        name: d.name.trim(),
        baseUrl: d.baseUrl.trim(),
        key: draftKey(d),
        model,
        headers: draftHeaders(d),
        ...(editing() ? { endpointId: editing()!.id } : {}),
      })
      setProbeResult(res)
      if (res.ok && res.kind === 'connection') setDiscovered(res.models ?? [])
    } catch (err) {
      if (err instanceof VaultOperationCancelledError) {
        // The person chose not to unlock: the test did not run, and nothing
        // failed — leave the badge exactly as it was rather than painting a
        // failure they did not cause (ADR-0032).
        return
      }
      const message = (err as Error).message
      log.error('Endpoint test failed', { message })
      setProbeResult({
        name: d.name.trim(),
        model,
        // The check the call WOULD have been: a refusal must not be
        // reported as a model answer when no model was named.
        kind: model === '' ? 'connection' : 'model',
        ok: false,
        error: message,
        elapsedMs: 0,
        at: new Date().toISOString(),
      })
    } finally {
      setProbing(false)
    }
  }

  onMount(() => {
    void load()
  })

  // ── Draft editing ────────────────────────────────────────────────────
  /** Load the vault inventory — the ONE owner of the inventory fetch and
   *  the secretRows state (AD-8). Two consumers share it: the pickers
   *  (called when an editor opens) and the row credential state (called
   *  at list time once the vault is unsealed — the rows render a
   *  per-endpoint credential fact, so the list now needs the same read
   *  the pickers already make). The failure state ('failed') is what keeps
   *  a failed read from being misread as an empty vault — the rows say
   *  'unavailable', never a fabricated 'deleted'.
   *
   *  A sealed or uninitialized vault is SKIPPED, not probed (nocx-q27y's
   *  headers fix): the pickers offer nothing while the vault cannot answer,
   *  and neither the editor door nor the list may raise the unlock prompt —
   *  the vault-backed OPERATION (the Test button resolving a credential)
   *  is what raises it (ADR-0032). The skip resets the state to 'idle', so
   *  the list's unseal-triggered effect still loads once the vault can
   *  answer — a stale 'loaded' from before a seal would otherwise be
   *  misread after it. */
  async function loadInventory() {
    if (!props.vaultClient) return
    if (inventoryState() === 'loading') return // one fetch owner, one in flight
    const state = props.vaultController?.status()?.state
    if (state === 'sealed' || state === 'uninitialized') {
      setSecretRows([])
      setInventoryState('idle')
      return
    }
    setInventoryState('loading')
    try {
      const inv = await props.vaultClient.inventory()
      setSecretRows(inv?.entries ?? [])
      setInventoryState('loaded')
    } catch {
      setSecretRows([])
      setInventoryState('failed')
    }
  }

  function openNew() {
    setEditing(null)
    setDraft(blankDraft())
    setProbeResult(null)
    setDiscovered([])
    setDiscoveryKey('')
    validation.reset()
    void loadInventory()
    setDialogOpen(true)
  }

  function openEdit(ep: Endpoint) {
    setEditing(ep)
    // The key is never pre-filled with material: a typed key is an input,
    // and the record cannot be read back (ADR-0030 §3). What CAN be
    // pre-filled is the SOURCE: a saved credential is the row handle the
    // result carried, so the form opens on "Use existing secret" with the
    // bound row — keeping the key is now a choice, not a caption.
    setDraft({
      name: ep.name,
      baseUrl: ep.baseUrl,
      keyMode: ep.credential !== null ? 'secret' : 'new',
      key: '',
      keyRow: ep.credential ?? '',
      models: ep.models.map((m) => ({ name: m.name, alias: m.alias ?? '' })),
      headers: ep.headers.map((h) =>
        h.value !== null
          ? { name: h.name, mode: 'new' as const, value: h.value, row: '' }
          : { name: h.name, mode: 'secret' as const, value: '', row: h.secret ?? '' },
      ),
    })
    validation.reset()
    setProbeResult(null)
    setDiscovered([])
    setDiscoveryKey('')
    void loadInventory()
    setDialogOpen(true)
  }

  function closeDialog() {
    setDialogOpen(false)
    setEditing(null)
  }

  function setDraftField(field: 'name' | 'baseUrl' | 'key', value: string) {
    setDraft((d) => ({ ...d, [field]: value }))
  }

  function updateModel(index: number, patch: Partial<ModelDraft>) {
    setDraft((d) => ({
      ...d,
      models: d.models.map((m, i) => (i === index ? { ...m, ...patch } : m)),
    }))
  }

  function addModel() {
    setDraft((d) => ({ ...d, models: [...d.models, { name: '', alias: '' }] }))
  }

  function removeModel(index: number) {
    setDraft((d) => ({ ...d, models: d.models.filter((_, i) => i !== index) }))
  }

  // ── Custom headers (nocx-lyyk) ───────────────────────────────────────
  function updateHeader(index: number, patch: Partial<HeaderDraft>) {
    setDraft((d) => ({
      ...d,
      headers: d.headers.map((h, i) => (i === index ? { ...h, ...patch } : h)),
    }))
  }

  function addHeader() {
    setDraft((d) => ({
      ...d,
      headers: [...d.headers, { name: '', mode: 'new', value: '', row: '' }],
    }))
  }

  function removeHeader(index: number) {
    setDraft((d) => ({ ...d, headers: d.headers.filter((_, i) => i !== index) }))
  }

  /** The key the WIRE carries: only a fresh 'new' mode key is material. In
   *  'secret' mode the wire carries the row handle instead (draftKeyRow),
   *  and a blank 'new' key keeps the existing material on update (design
   *  §4.5.4) — the source control has made "keep" a visible choice. */
  const draftKey = (d: EndpointDraft): string => (d.keyMode === 'new' ? d.key : '')

  /** The row handle the WIRE carries in 'secret' mode, or '' to keep the
   *  existing reference (or stay keyless on create). */
  const draftKeyRow = (d: EndpointDraft): string => (d.keyMode === 'secret' ? d.keyRow : '')

  /** The wire form of the draft's header rows: exactly one source per row,
   *  the unused side null. A 'secret' row with no picker selection is
   *  dropped — it is an empty row, not a claim about material. */
  function draftHeaders(
    d: EndpointDraft,
  ): { name: string; value: string | null; secret: string | null }[] {
    return d.headers
      .map((h) => ({
        name: h.name.trim(),
        value: h.mode === 'new' ? h.value : null,
        secret: h.mode === 'secret' && h.row !== '' ? h.row : null,
      }))
      .filter((h) => h.name !== '' || h.value !== null || h.secret !== null)
  }

  // ── Validation (the kit's one answer to "how a form refuses") ─────────

  const validation = createFormValidation(
    {
      name: () => required('Name')(draft().name),
      baseUrl: () => combine(required('Base URL'), absoluteHttpUrl())(draft().baseUrl),
      models: () => {
        const rows = draft().models
        if (rows.length === 0) return 'Add at least one model'
        return rows.some((m) => m.name.trim() === '') ? 'Model name is required' : undefined
      },
      // A header row needs a name; the refused-name and control-character
      // rules are the BACKEND's (profile.ValidateEndpointHeaders, one
      // owner) and surface as the save's toast — the form refuses what it
      // can see, the backend refuses what it must.
      headers: () => {
        const rows = draft().headers
        return rows.some((h) => h.name.trim() === '') ? 'Header name is required' : undefined
      },
    },
    {
      controlId: (field) => {
        if (field === 'name') return 'endpoint-name'
        if (field === 'baseUrl') return 'endpoint-base-url'
        if (field === 'models') {
          const first = draft().models.findIndex((m) => m.name.trim() === '')
          return first >= 0 ? `endpoint-model-${first}-name` : undefined
        }
        if (field === 'headers') {
          const first = draft().headers.findIndex((h) => h.name.trim() === '')
          return first >= 0 ? `endpoint-header-${first}-name` : undefined
        }
        return field
      },
    },
  )
  const gate = createSubmitGate(validation)

  // ── Save / delete ────────────────────────────────────────────────────

  async function save() {
    if (!(await gate())) return
    const d = draft()
    const input = {
      name: d.name.trim(),
      baseUrl: d.baseUrl.trim(),
      key: draftKey(d),
      credential: draftKeyRow(d),
      headers: draftHeaders(d),
      models: d.models.map((m) => ({
        name: m.name.trim(),
        alias: m.alias.trim() === '' ? null : m.alias.trim(),
      })),
    }
    const editingId = editing()?.id
    const persist = async (): Promise<void> => {
      if (editingId) {
        await props.client.updateEndpoint(editingId, input)
      } else {
        await props.client.createEndpoint(input)
      }
    }
    try {
      // The vault seam guards the MINT path only: a referenced key needs no
      // mint, so a save that references an existing secret proceeds without
      // raising the setup sheet.
      if (props.vaultController && draftKey(d) !== '') {
        // The key is about to be minted into the vault (design §4.5.3), so
        // the save goes through the vault layer's own operation-first seam —
        // the same owner the connections path uses at the moment a secret is
        // created ("save this key", nocx-v64o). A missing or sealed vault
        // raises the vault's setup/unlock sheet and retries THIS save when
        // it completes; cancelling rejects with VaultOperationCancelledError,
        // nothing ran, and the editor stays open with the draft intact so
        // the person does not retype it.
        await props.vaultController.saveSecretWithVault(persist, 'save this endpoint key')
      } else {
        await persist()
      }
      closeDialog()
      await load()
      // A save that carried a key MINTED one: the row's credential fact is
      // derived from the vault inventory this page holds, so an inventory
      // loaded before the mint says the row's brand-new key is deleted —
      // which is what a person saw one second after saving it. Reload it
      // beside the endpoints, from the same success.
      await loadInventory()
      showToast({ level: 'success', message: `Saved "${input.name}"` })
    } catch (err) {
      // A cancelled setup/unlock is not an error: the sheet is the surface
      // while it is up, and the editor behind it still holds the draft.
      if (err instanceof VaultOperationCancelledError) return
      const message = (err as Error).message
      log.error('Failed to save endpoint', { message })
      showToast({ level: 'danger', message: `Could not save the endpoint: ${message}` })
    }
  }

  async function remove(ep: Endpoint) {
    if (!(await showConfirm(`Delete "${ep.name}"?`))) return
    try {
      await props.client.deleteEndpoint(ep.id)
      await load()
      showToast({ level: 'success', message: `Deleted "${ep.name}"` })
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to delete endpoint', { message })
      showToast({ level: 'danger', message: `Could not delete "${ep.name}": ${message}` })
    }
  }

  // ── Derived ──────────────────────────────────────────────────────────

  const filteredEndpoints = createMemo(() => {
    const q = searchQuery().trim().toLowerCase()
    if (q === '') return endpoints()
    return endpoints().filter(
      (ep) => ep.name.toLowerCase().includes(q) || ep.baseUrl.toLowerCase().includes(q),
    )
  })

  function modelSummary(models: Endpoint['models']): string {
    const n = models.length
    return `${n} model${n === 1 ? '' : 's'}`
  }

  // ── Rows ─────────────────────────────────────────────────────────────

  // ── Assistant status + probe display ───────────────────────────────

  /** The editor's Test verdict — the shared sentence mapping, so the
   *  editor and the row's Test cannot drift (probeOutcomeLine is the ONE
   *  owner of the words nocx-q27y decided). */
  const probeLine = () => probeOutcomeLine(probeResult())

  /** The models a connection check found, for the picker below. Additive:
   *  an endpoint that lists none is configured by hand exactly as before. */
  const discoveredModels = () => discovered()

  /**
   * Ask the endpoint what models it offers, WITHOUT being asked to test.
   *
   * A person opening the model field is about to need this list, and making
   * them press Test first is making them do the lookup by hand. So focus
   * triggers it — but silently, and that silence is the whole design:
   *
   *  - it never writes probeResult, so a background attempt cannot paint a
   *    red verdict nobody asked for. A failure leaves the suggestions empty
   *    and the field exactly as usable as it was;
   *  - it runs once per (base URL, key, endpoint) triple, so re-focusing the
   *    field does not re-dial, and changing the URL does;
   *  - it never runs while an explicit test is in flight.
   *
   * An explicit connection test fills the same list, so there is one owner of
   * "what models does this endpoint offer" and the two cannot disagree.
   */
  async function discoverModels() {
    const d = draft()
    const baseUrl = d.baseUrl.trim()
    if (baseUrl === '' || probing()) return
    const ep = editing()
    const key = `${baseUrl}${draftKey(d)}${draftKeyRow(d)}${ep?.id ?? ''}`
    if (discoveryKey() === key) return
    setDiscoveryKey(key)
    try {
      const res = await props.client.probeEndpoint({
        name: d.name.trim(),
        baseUrl,
        key: draftKey(d),
        model: '',
        headers: draftHeaders(d),
        ...(ep ? { endpointId: ep.id } : {}),
      })
      if (res.ok && res.kind === 'connection') setDiscovered(res.models ?? [])
    } catch {
      // Silent by design: nobody asked for a verdict. The field stays free
      // text, which is what it would have been anyway.
    }
  }

  /** The Test button needs a base URL and NOTHING else. It used to require a
   *  first model, which made it dead in the one state where a person most
   *  wants it — a new endpoint whose URL and key are typed and whose models
   *  are not, because the models are what the test is about to find
   *  (nocx-q27y). With no model it checks the connection; with one it asks
   *  that model to answer. */
  const testDisabled = () => probing() || draft().baseUrl.trim() === ''

  /** Why the Test button is unavailable, rendered beside it. A disabled
   *  control that does not say why is the half of this defect the owner
   *  actually hit: a grey button and silence. */
  const testDisabledReason = () => {
    if (probing()) return undefined
    if (draft().baseUrl.trim() === '') return 'Add a base URL to test the connection'
    return undefined
  }

  /** What pressing Test will do right now, so the label never promises the
   *  check it is not about to run. */
  const testLabel = () => {
    if (probing()) return 'Testing…'
    return (draft().models[0]?.name.trim() ?? '') === '' ? 'Test connection' : 'Test endpoint'
  }

  /** The row's Test: the SAME probe the editor runs on a saved endpoint
   *  (nocx-9bx0m) — the record is named, the key stays blank so the
   *  backend resolves the stored credential (nocx-reu5), and the model
   *  stays blank because this is the connection check, which needs no
   *  model (nocx-q27y). One call path, watched through the client method
   *  the editor already uses. */
  async function runRowProbe(ep: Endpoint) {
    if (rowTestDisabled(ep)) return // the disabled state says why — never a doomed dial
    setRowProbing((prev) => new Set(prev).add(ep.id))
    // A new attempt clears the previous verdict, exactly like the editor's.
    setRowProbes((prev) => {
      const rest = { ...prev }
      delete rest[ep.id]
      return rest
    })
    try {
      const res = await props.client.probeEndpoint({
        name: ep.name,
        baseUrl: ep.baseUrl,
        key: '',
        model: '',
        endpointId: ep.id,
        headers: ep.headers,
      })
      setRowProbes((prev) => ({ ...prev, [ep.id]: res }))
    } catch (err) {
      if (err instanceof VaultOperationCancelledError) {
        // The person chose not to unlock: the test did not run, and nothing
        // failed — the row keeps the sentence it had (ADR-0032).
        return
      }
      const message = (err as Error).message
      log.error('Endpoint test failed', { endpointId: ep.id, message })
      setRowProbes((prev) => ({
        ...prev,
        [ep.id]: {
          name: ep.name,
          model: '',
          kind: 'connection' as const,
          ok: false,
          error: message,
          elapsedMs: 0,
          at: new Date().toISOString(),
        },
      }))
    } finally {
      setRowProbing((prev) => {
        const next = new Set(prev)
        next.delete(ep.id)
        return next
      })
    }
  }

  /** The row's Test is refused only when NOTHING can make the check
   *  meaningful: the referenced secret is gone (no vault, or the unsealed
   *  vault's inventory no longer lists it), or the store did not answer.
   *
   *  A SEALED vault is not one of those. endpoints.probe raises
   *  vault.ErrVaultSealed for it, the backend's sealedNormalizer rewrites
   *  that to the canonical -32001, and the renderer's dispatcher raises the
   *  unlock and re-sends the request verbatim (ADR-0032; the rationale is
   *  written out at ws_assistant.go resolveProbeCredential, which calls a
   *  probe RESULT naming the sealed state "the dead end this bead exists to
   *  delete"). Greying the button out was this surface refusing a path the
   *  backend had deliberately kept open — so pressing Test on a locked
   *  vault asks for the passphrase and then runs the check, which is what a
   *  person means by pressing it.
   *
   *  A no-key endpoint stays testable too — the connection check can still
   *  pass against a public endpoint. */
  function rowTestDisabled(ep: Endpoint): boolean {
    const state = rowCredentialState(
      ep.credential,
      props.vaultController?.status()?.state,
      inventoryFact(),
    )
    return state === 'deleted' || state === 'unavailable'
  }

  /** The row's status, or NULL for silence.
   *
   *  The row speaks only to REFUSE, or to answer a check the person asked
   *  for. A resolvable credential is the absence of a problem and says
   *  nothing; so is a sealed vault, now that Test raises the unlock rather
   *  than being blocked by it. Both used to render — a green "Key saved"
   *  under every healthy endpoint and a full sentence about the vault on
   *  every row of a list the vault is not about — and the owner struck them
   *  in the same pass that struck the Roles page's green line. The rule the
   *  two share: a healthy state is silent.
   *
   *  The probe outcome is the exception and not one: it is the answer to a
   *  button, not an unsolicited reassurance. */
  function rowStatus(ep: Endpoint): { tone: StatusDotTone; text: string } | undefined {
    const probe = rowProbes()[ep.id]
    if (probe) {
      const line = probeOutcomeLine(probe)!
      return { tone: line.tone === 'danger' ? 'error' : 'ok', text: line.text }
    }
    const state = rowCredentialState(
      ep.credential,
      props.vaultController?.status()?.state,
      inventoryFact(),
    )
    if (state === 'none') return { tone: 'neutral', text: 'No key' }
    if (state === 'resolvable' || state === 'sealed') return undefined
    // deleted / unavailable: the sentences agent-status-line owns, always in
    // the StatusDot's warning — the Badge tone that mapping speaks (warning)
    // is the same meaning, spelled for the dot.
    return { tone: 'warning', text: credentialLine(state)!.text }
  }

  function renderRow(ep: Endpoint) {
    return (
      <RecordRow
        title={ep.name}
        kind={{ label: SCHEMA_LABEL[ep.schema] }}
        meta={modelSummary(ep.models)}
        status={rowStatus(ep)}
        onActivate={() => openEdit(ep)}
        actions={
          <>
            <IconButton
              size="sm"
              title="Edit"
              ariaLabel={`Edit ${ep.name}`}
              onClick={() => openEdit(ep)}
            >
              <PencilIcon />
            </IconButton>
            <IconButton
              size="sm"
              title="Test connection"
              ariaLabel={`Test ${ep.name}`}
              disabled={rowTestDisabled(ep) || rowProbing().has(ep.id)}
              onClick={() => void runRowProbe(ep)}
            >
              <CheckCircleIcon />
            </IconButton>
            <IconButton
              size="sm"
              title="Delete"
              ariaLabel={`Delete ${ep.name}`}
              onClick={() => void remove(ep)}
            >
              <TrashIcon />
            </IconButton>
          </>
        }
      />
    )
  }

  function renderModelRow(row: () => ModelDraft, index: number) {
    return (
      <div class="ep-model-row">
        <SuggestionField
          id={`endpoint-model-${index}-name`}
          label="Model id"
          required
          value={row().name}
          onInput={(v) => updateModel(index, { name: v })}
          onBlur={() => validation.touch('models')}
          onFocus={() => void discoverModels()}
          placeholder="gpt-4o"
          // What a successful connection test found the endpoint offering.
          // Free text still: an endpoint that lists nothing — GET /models is
          // not universally implemented — is configured by hand exactly as
          // before, and a model the list omits is still typeable.
          suggestions={discoveredModels()}
        />
        <TextField
          id={`endpoint-model-${index}-alias`}
          label="Picker label"
          value={row().alias}
          onInput={(v) => updateModel(index, { alias: v })}
          placeholder="Optional"
        />
      </div>
    )
  }

  /** One custom-header row: a name, and a value whose SOURCE is chosen with
   *  the same control the key uses (nocx-lyyk) — a literal typed fresh, or a
   *  reference to an existing vault secret (Azure's api-key header is the
   *  key). The rows are EditableRowList's, so a keystroke never rebuilds the
   *  row's DOM (nocx-fngd). */
  function renderHeaderRow(row: () => HeaderDraft, index: number) {
    return (
      <div class="ep-header-row">
        <TextField
          id={`endpoint-header-${index}-name`}
          label="Header"
          value={row().name}
          onInput={(v) => updateHeader(index, { name: v })}
          onBlur={() => validation.touch('headers')}
          placeholder="X-Title"
        />
        <SecretSource
          id={`endpoint-header-${index}`}
          label="Value"
          mode={row().mode}
          onModeChange={(mode) => updateHeader(index, { mode })}
          newLabel="Type a value"
          secretLabel="Use existing secret"
          ariaLabel={`Header ${row().name || index + 1} value source`}
          newControl={
            <TextField
              id={`endpoint-header-${index}-value`}
              label="Value"
              value={row().value}
              onInput={(v) => updateHeader(index, { value: v })}
              placeholder="nocx"
            />
          }
          secrets={passwordRows()}
          value={row().row}
          onValueChange={(v) => updateHeader(index, { row: v ?? '' })}
          placeholder="\u2014 None \u2014"
        />
      </div>
    )
  }

  // ── Empty / failure states ───────────────────────────────────────────

  const emptyContent = () => {
    if (loadState() === 'loading') {
      return <EmptyState title="Loading endpoints" />
    }
    if (loadState() === 'failed') {
      return (
        <EmptyState
          title="Couldn't load endpoints"
          description={loadError()}
          action={
            <Button variant="default" onClick={() => void load()}>
              Retry
            </Button>
          }
        />
      )
    }
    return (
      <EmptyState
        title="No endpoints yet"
        description="Add an AI endpoint to configure the assistant's model provider."
        action={
          <Button variant="primary" onClick={openNew}>
            + New endpoint
          </Button>
        }
      />
    )
  }

  // ── Render ───────────────────────────────────────────────────────────

  return (
    <div class="ep-root">
      <CollectionView
        searchValue={searchQuery()}
        onSearch={setSearchQuery}
        searchPlaceholder="Filter endpoints"
        searchLabel="Filter endpoints"
        actions={
          <Button variant="primary" onClick={openNew}>
            + New endpoint
          </Button>
        }
        hasItems={endpoints().length > 0}
        empty={emptyContent()}
      >
        <div role="list" aria-label="Endpoint list">
          <For each={filteredEndpoints()}>{(ep) => renderRow(ep)}</For>
        </div>
        {/* A filter that matches nothing hid every row and said nothing,
            which is indistinguishable from the list failing to load. */}
        <Show when={searchQuery().trim() !== '' && filteredEndpoints().length === 0}>
          <EmptyState
            title="Nothing matches this filter"
            description={`No endpoint's name or base URL contains "${searchQuery().trim()}".`}
          />
        </Show>
      </CollectionView>

      <Dialog
        open={dialogOpen()}
        onClose={closeDialog}
        onSubmit={() => void save()}
        title={editing() ? `Edit Endpoint: ${editing()!.name}` : 'New Endpoint'}
        size="lg"
        footer={
          <>
            <Button variant="default" onClick={closeDialog}>
              Cancel
            </Button>
            <Button variant="primary" onClick={() => void save()}>
              {editing() ? 'Save Endpoint' : 'Create Endpoint'}
            </Button>
          </>
        }
      >
        <Stack>
          <TextField
            id="endpoint-name"
            label="Name"
            required
            value={draft().name}
            onInput={(v) => setDraftField('name', v)}
            onBlur={() => validation.touch('name')}
            error={validation.error('name')}
            placeholder="My provider"
          />
          <TextField
            id="endpoint-base-url"
            label="Base URL"
            required
            value={draft().baseUrl}
            onInput={(v) => setDraftField('baseUrl', v)}
            onBlur={() => validation.touch('baseUrl')}
            error={validation.error('baseUrl')}
            placeholder="https://api.example.com/v1"
          />
          <SecretSource
            id="endpoint-key"
            label="API key"
            mode={draft().keyMode}
            onModeChange={(mode) => setDraft((d) => ({ ...d, keyMode: mode }))}
            newLabel="Type a new one"
            secretLabel="Use existing secret"
            ariaLabel="API key source"
            newControl={
              <TextField
                id="endpoint-key"
                label="API key"
                type="password"
                value={draft().key}
                onInput={(v) => setDraftField('key', v)}
                description="The key is stored in your vault, never in the record. Choosing an existing secret references it instead of minting a second copy."
              />
            }
            secrets={passwordRows()}
            value={draft().keyRow}
            onValueChange={(v) => setDraft((d) => ({ ...d, keyRow: v ?? '' }))}
          />
          {/* Custom headers belong to the CONNECTION, so they come before the
              button that checks it: the probe sends them on every request it
              makes (ws_assistant.go resolveProbeHeaders), and a header typed
              after a green test was never part of what went green. Models
              come after, because they are what the connection then offers and
              the model field discovers them from a successful test. */}
          <EditableRowList
            rows={draft().headers}
            ariaLabel="Custom headers"
            addLabel="Add header"
            emptyLabel="No custom headers — requests go out with just the credential."
            removeLabel={(i) => `Remove header ${i + 1}`}
            onRemove={removeHeader}
            onAdd={addHeader}
            error={validation.error('headers')}
            renderRow={renderHeaderRow}
          />
          <div class="ep-test-row">
            <Button
              variant="default"
              size="sm"
              disabled={testDisabled()}
              onClick={() => void runProbe()}
            >
              {testLabel()}
            </Button>
            <Show when={probing()}>
              <Spinner size="sm" label="Testing endpoint" />
            </Show>
            <Show when={testDisabledReason()}>
              <span class="ep-test-reason">{testDisabledReason()}</span>
            </Show>
            <Show when={probeLine()}>
              <Badge tone={probeLine()!.tone}>{probeLine()!.text}</Badge>
            </Show>
          </div>
          <EditableRowList
            rows={draft().models}
            ariaLabel="Endpoint models"
            addLabel="Add model"
            emptyLabel="No models — add the model id the API understands."
            removeLabel={(i) => `Remove model ${i + 1}`}
            onRemove={removeModel}
            onAdd={addModel}
            error={validation.error('models')}
            renderRow={renderModelRow}
          />
        </Stack>
      </Dialog>
    </div>
  )
}
