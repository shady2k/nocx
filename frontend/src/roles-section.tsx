/**
 * Model Roles surface — the Settings page where a person assigns one
 * (endpoint, model) pair to each model role (bead nocx-e6kn2), and names
 * the ONE default pair every role without an assignment of its own answers
 * with (bead nocx-rikz5). A feature asks for a role — the assistant asks
 * for `answering`, the classifier bead will ask for `classifier` — and
 * NEVER names a model id; the pair is written HERE and every feature picks
 * it up at its next call, resolved in exactly one place on the backend.
 *
 * The role set is CLOSED and defined by the product. The wire's roles.list
 * sends every role (an unassigned role is a row with nulls, never an absent
 * row), so a role resolving through the default — and a role whose endpoint
 * or model no longer exists — is a first-class VISIBLE state on this page,
 * the same failure the ask transaction refuses on: a role is never silently
 * re-pointed at another model, because then nobody could tell which model
 * answered. The default is never invented by the product either: a fresh
 * profile has none, and says so.
 *
 * Kit contract: every control is the kit's native `Select`; the state
 * sentence is the kit's StatusDot + text vocabulary (the tones the endpoint
 * rows already use); rows are spaced by `Stack` (surface-spacing-kit).
 * Nothing here is a hand-rolled control or a repainted kit component.
 */
import { For, Show, createSignal, onMount } from 'solid-js'
import { Select } from './ui/select'
import { Stack } from './ui/stack'
import { StatusDot, type StatusDotTone } from './ui/status-dot'
import { EmptyState } from './ui/empty-state'
import { Spinner } from './ui/spinner'
import { Button } from './ui/button'
import { showToast } from './ui/toast'
import { log } from './log'
import { EndpointClient, type Endpoint, type WireRole } from './endpoints'
import type { RolesListResult } from './generated/roles.list'

/** The default pair as the wire declares it — never hand-written here. A
 *  renderer type carrying a field the backend does not send is the
 *  `vault.status` defect AGENTS.md names. */
type WireDefault = RolesListResult['default']

/** The table every read and every write of this page adopts whole: the
 *  roles and the default from the SAME moment. Two accessors would let the
 *  page render a default and a role table that never coexisted. */
interface RolesTable {
  roles: WireRole[]
  default: WireDefault
}

export interface RolesSectionProps {
  /** The endpoint client, which is also the roles client (one backend
   *  domain). Absent in the dev-web harness; the section then renders
   *  nothing rather than offering controls that cannot run. */
  client?: EndpointClient
}

/** What each role is FOR, rendered under its name. The set is closed: every
 *  role the wire sends must be describable here, and an unknown role from a
 *  newer backend renders its value with no description rather than crash. */
const ROLE_NAME: Record<string, string> = {
  answering: 'Answering',
  classifier: 'Classifier',
}

const ROLE_DESCRIPTION: Record<string, string> = {
  answering: 'The model the assistant speaks with — the one that answers your questions.',
  classifier:
    'The second model that will judge proposed tool calls. No feature uses it yet; it is assignable so the classifier task (its own bead) has a role to ask for.',
}

/** The label the endpoint select shows for "no assignment of my own" — the
 *  role reads through the default, which is a choice, not an absence. One
 *  constant so the option and any future copy cannot drift apart. */
const AS_DEFAULT = 'As default'

/**
 * The tone + sentence of one role's REFUSAL, or NULL for silence.
 *
 * The line speaks only when the role does not resolve (bead nocx-rikz5).
 * Everything a working role could say, the page already says: an own pair
 * is shown by the role's two selects, and the pair "As default" reads
 * through is named once by the Default model control at the top. A line
 * repeating either is noise — the owner struck a green "As default: <ep> ·
 * <model>" that restated the control above it under every role.
 *
 * So there are exactly three sentences left and all three are failures:
 * nothing assigned anywhere, an endpoint that is gone, a model that is no
 * longer offered.
 *
 * This EXTENDS the one resolver rather than adding a second beside it: the
 * page and the ask may never grow two answers to "what does this role
 * mean", which is why this function was written pure and unit-tested in the
 * first place. The return type is `| null` because the silence is a value,
 * not an empty string — an empty string still renders a dot.
 */
export function roleStateLine(
  row: WireRole,
  def: WireDefault,
  endpoints: Endpoint[],
): { tone: StatusDotTone; text: string } | null {
  // An explicit assignment: the two selects already show it, so a healthy
  // row says nothing and a broken one keeps its refusal verbatim.
  if (row.endpointId !== null && row.model !== null) {
    return brokenLine(row.endpointId, row.model, endpoints)
  }
  if (!def) {
    return { tone: 'warning', text: 'No model assigned — the role cannot be used until it is' }
  }
  // Resolves through the default, or names the rung of it that failed. A
  // healthy default is silent here: the control above it already named the
  // pair, and saying it again under every role was what got struck.
  return brokenLine(def.endpointId, def.model, endpoints)
}

/** The two refusals a stored pair can carry — the same two profile.ResolveRole
 *  raises, so the page and the ask can never disagree about what is broken.
 *  Null when the pair resolves. */
function brokenLine(
  endpointId: string,
  modelName: string,
  endpoints: Endpoint[],
): { tone: StatusDotTone; text: string } | null {
  const ep = endpoints.find((e) => e.id === endpointId)
  if (!ep) {
    return { tone: 'error', text: 'The assigned endpoint no longer exists — reassign this role' }
  }
  const model = ep.models.find((m) => m.name === modelName)
  if (!model) {
    return {
      tone: 'error',
      text: `The assigned model "${modelName}" is no longer offered by ${ep.name} — reassign this role`,
    }
  }
  return null
}

interface RoleDraft {
  endpointId: string
  modelId: string
}

function blankDraft(): RoleDraft {
  return { endpointId: '', modelId: '' }
}

/** Re-derive the per-role drafts from the wire table — the one source of
 *  truth after every load and every write. */
function draftFromWire(roles: WireRole[]): Record<string, RoleDraft> {
  const d: Record<string, RoleDraft> = {}
  for (const r of roles) {
    d[r.role] = { endpointId: r.endpointId ?? '', modelId: r.model ?? '' }
  }
  return d
}

/** The default control's draft, re-derived from the wire the same way. */
function defaultDraftFromWire(def: WireDefault): RoleDraft {
  return def ? { endpointId: def.endpointId, modelId: def.model } : blankDraft()
}

export function RolesSection(props: RolesSectionProps) {
  const [roles, setRoles] = createSignal<WireRole[]>([])
  const [wireDefault, setWireDefault] = createSignal<WireDefault>(null)
  const [endpoints, setEndpoints] = createSignal<Endpoint[]>([])
  const [loadState, setLoadState] = createSignal<'loading' | 'ready' | 'failed'>('loading')
  const [loadError, setLoadError] = createSignal('')
  /** Per-role draft: what the selects show while a change is mid-gesture
   *  (an endpoint without a model yet — half a pair is never written). The
   *  state line renders the WIRE: a draft never claims to be assigned. */
  const [drafts, setDrafts] = createSignal<Record<string, RoleDraft>>({})
  const [defaultDraft, setDefaultDraft] = createSignal<RoleDraft>(blankDraft())
  const [busyRole, setBusyRole] = createSignal<string | null>(null)
  const [defaultBusy, setDefaultBusy] = createSignal(false)

  /** Adopt a wire table whole. Every read and every write funnels through
   *  here so the roles and the default on screen always came from the same
   *  answer — and so nothing the page shows outlives what the store took. */
  function adopt(table: RolesTable) {
    setRoles(table.roles)
    setWireDefault(table.default)
    setDrafts(draftFromWire(table.roles))
    setDefaultDraft(defaultDraftFromWire(table.default))
  }

  async function load() {
    if (!props.client) return
    try {
      const [table, eps] = await Promise.all([
        props.client.listRoles(),
        props.client.listEndpoints(),
      ])
      setEndpoints(eps ?? [])
      adopt({ roles: table?.roles ?? [], default: table?.default ?? null })
      setLoadState('ready')
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to load model roles', { message })
      setLoadError(message)
      setLoadState('failed')
    }
  }

  onMount(() => {
    void load()
  })

  function setDraft(role: string, patch: Partial<RoleDraft>) {
    setDrafts((d) => ({ ...d, [role]: { ...blankDraft(), ...d[role], ...patch } }))
  }

  /** Writes the role's pair (or clears it) and adopts the returned table —
   *  the single shape the wire declares, so this page cannot disagree with
   *  itself about what is assigned. */
  async function commit(role: string, endpointId: string | null, modelId: string | null) {
    if (!props.client) return
    setBusyRole(role)
    try {
      adopt(await props.client.assignRole({ role, endpointId, model: modelId }))
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to assign a model role', { role, message })
      showToast({ level: 'danger', message: `Could not assign the role: ${message}` })
      // The wire may have changed under us; re-read rather than trust the
      // draft to still mean anything.
      void load()
    } finally {
      setBusyRole(null)
    }
  }

  /** Writes the default pair (or clears it: both halves empty) and adopts
   *  the returned table. On refusal the page re-reads rather than keeping
   *  the draft — it never shows a default the store did not take. */
  async function commitDefault(endpointId: string, modelId: string) {
    if (!props.client) return
    setDefaultBusy(true)
    try {
      adopt(await props.client.setDefault({ endpointId, model: modelId }))
    } catch (err) {
      const message = (err as Error).message
      log.error('Failed to set the default model', { message })
      showToast({ level: 'danger', message: `Could not set the default model: ${message}` })
      void load()
    } finally {
      setDefaultBusy(false)
    }
  }

  /** Endpoint select change: a real endpoint starts a new draft (no write
   *  yet — an (endpoint, model) pair needs both halves); "As default" is
   *  the CLEAR write that drops the role's own pair so it reads through the
   *  default again. */
  function onEndpointChange(row: WireRole, value: string) {
    if (value === '') {
      setDraft(row.role, blankDraft())
      void commit(row.role, null, null)
      return
    }
    setDraft(row.role, { endpointId: value, modelId: '' })
  }

  /** Model select: completes the pair and writes it. */
  function onModelChange(row: WireRole, value: string) {
    if (value === '') return // the placeholder is a no-op, never a half-pair
    const epId = draftEndpoint(drafts(), row.role)
    if (epId === '') return
    setDraft(row.role, { modelId: value })
    void commit(row.role, epId, value)
  }

  /** The same two rules on the default control: "— None —" is the CLEAR
   *  write (both halves empty), and an endpoint alone is never written. */
  function onDefaultEndpointChange(value: string) {
    if (value === '') {
      setDefaultDraft(blankDraft())
      void commitDefault('', '')
      return
    }
    setDefaultDraft({ endpointId: value, modelId: '' })
  }

  function onDefaultModelChange(value: string) {
    if (value === '') return
    const epId = defaultDraft().endpointId
    if (epId === '') return
    setDefaultDraft((d) => ({ ...d, modelId: value }))
    void commitDefault(epId, value)
  }

  const endpointOptions = () => endpoints().map((e) => ({ value: e.id, label: e.name }))

  /** The models one endpoint offers, by its id — '' offers none, which is
   *  what keeps the model select empty until an endpoint is chosen. */
  function modelOptionsFor(endpointId: string) {
    const ep = endpoints().find((e) => e.id === endpointId)
    return (ep?.models ?? []).map((m) => ({ value: m.name, label: m.alias ?? m.name }))
  }

  function renderDefault() {
    const draft = () => defaultDraft()
    return (
      <div class="roles-default">
        <Stack>
          <div>
            <div class="roles-default__title">Default model</div>
            <div class="roles-default__description">
              The pair every role without a model of its own answers with. Nothing is chosen until
              you choose it — the product never picks one for you.
            </div>
          </div>
          <div class="roles-default__controls">
            <label class="roles-default__field">
              <span class="roles-default__label">Endpoint</span>
              <Select
                value={draft().endpointId}
                disabled={defaultBusy()}
                placeholder="— None —"
                options={endpointOptions()}
                onChange={(v) => onDefaultEndpointChange(v)}
              />
            </label>
            <label class="roles-default__field">
              <span class="roles-default__label">Model</span>
              <Select
                value={draft().modelId}
                disabled={defaultBusy() || draft().endpointId === ''}
                placeholder="— pick a model —"
                options={modelOptionsFor(draft().endpointId)}
                onChange={(v) => onDefaultModelChange(v)}
              />
            </label>
          </div>
        </Stack>
      </div>
    )
  }

  function renderRow(row: WireRole) {
    const draft = () => drafts()[row.role] ?? blankDraft()
    const line = () => roleStateLine(row, wireDefault(), endpoints())
    const busy = () => busyRole() === row.role

    return (
      <div class="roles-role">
        <Stack>
          <div>
            <div class="roles-role__title">{ROLE_NAME[row.role] ?? row.role}</div>
            <div class="roles-role__description">{ROLE_DESCRIPTION[row.role] ?? ''}</div>
          </div>
          <div class="roles-role__controls">
            <label class="roles-role__field">
              <span class="roles-role__label">Endpoint</span>
              <Select
                value={draft().endpointId}
                disabled={busy()}
                placeholder={AS_DEFAULT}
                options={endpointOptions()}
                onChange={(v) => onEndpointChange(row, v)}
              />
            </label>
            <label class="roles-role__field">
              <span class="roles-role__label">Model</span>
              <Select
                value={draft().modelId}
                disabled={busy() || draft().endpointId === ''}
                placeholder="— pick a model —"
                options={modelOptionsFor(draft().endpointId)}
                onChange={(v) => onModelChange(row, v)}
              />
            </label>
          </div>
          {/* Silence is a state, and it is the normal one: a role that
              resolves — its own pair, or the default — says nothing here.
              This line only ever refuses. */}
          <Show when={line()}>
            {(l) => (
              <div class="roles-role__state" data-tone={l().tone}>
                <StatusDot tone={l().tone} accessibleName="Role state">
                  {l().text}
                </StatusDot>
              </div>
            )}
          </Show>
        </Stack>
      </div>
    )
  }

  const content = () => {
    if (loadState() === 'loading') {
      return <Spinner size="sm" label="Loading model roles" />
    }
    if (loadState() === 'failed') {
      return (
        <EmptyState
          title="Couldn't load model roles"
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
      <Stack divided>
        <Show when={endpoints().length === 0}>
          <EmptyState
            title="No endpoints yet"
            description="Add an AI endpoint on the Endpoints page first — a role assigns an endpoint's model."
          />
        </Show>
        {renderDefault()}
        <For each={roles()}>{(row) => renderRow(row)}</For>
      </Stack>
    )
  }

  return (
    <Show when={props.client}>
      <Stack>{content()}</Stack>
    </Show>
  )
}

/** The draft endpoint id for a role ('' when none). One owner of the
 *  lookup so the model select and the write share the same draft. */
function draftEndpoint(drafts: Record<string, RoleDraft>, role: string): string {
  return drafts[role]?.endpointId ?? ''
}
