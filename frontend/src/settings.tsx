/**
 * SettingsComponent — Solid rewrite of the settings surface.
 *
 * Replaces the imperative SettingsViewImpl + SettingsContent rendering
 * (both deleted in this commit).  Domain logic stays in settings-domain.ts.
 * ExportSection is rendered as a child component — no mountExportSection.
 *
 * Two defects fixed (not preserved):
 *   - nocx-x6w9: exactly ONE search box and ONE modified filter (both in rail)
 *   - nocx-ucxl: clicking a rail section always changes the content pane
 *
 * Uses the Page layout primitives (nocx-imkb.1) and the UI kit (nocx-vxqj.4).
 */

import type { JSX } from 'solid-js'
import {
  For,
  Show,
  untrack,
  createSignal,
  createMemo,
  createEffect,
  onMount,
  onCleanup,
} from 'solid-js'
import { createStore } from 'solid-js/store'
import { ConnectionsView } from './connections'
import { SecretsSection } from './secrets'
import { EndpointsSection } from './endpoints-section'
import { SnippetsSection } from './snippets/snippets-settings'
import { classConflict } from './sandbox-path-classes'
import { SANDBOX_READ_ONLY_PATHS_KEY, SANDBOX_WRITABLE_PATHS_KEY } from './sandbox-open'
import type { SnippetsStore } from './snippets/snippets-store'
import { RolesSection } from './roles-section'
import { AgentPolicySection } from './agent-policy-section'
import type { PolicyClient } from './policy-client'
import type { FootprintClient } from './footprint-client'
import type { AgentClient } from './agent'
import type { EndpointClient } from './endpoints'
import type { ProfileClient, SSHProfile } from './profiles'
import type { DialogClient } from './dialog-client'
import { SettingsObserver } from './settings-observer'
import {
  AcceptedSnapshot,
  applyAcceptedSnapshot,
  canResetSetting,
  fieldSaveError,
  isSettingModified,
  monotonicRevisionPolicy,
  numberRangeCaption,
  numberRangeError,
  textLengthCaption,
  textLengthError,
  reconnectRevisionPolicy,
  recordSaveOutcome,
  type Declaration,
  type RevisionPolicy,
  type SaveOutcome,
  type SettingsMirror,
  type SettingsGroup,
  type SettingsSnapshot,
} from './settings-domain'
import { BackupRestoreSection } from './backup-restore-section'
import { AboutSection } from './about-section'
import type { AboutClient } from './about-client'
import type { ClipboardAccess } from './clipboard'
import { VaultSection } from './vault'
import { log } from './log'
import {
  Page,
  PageSection,
  type PageScrollerHandle,
  SearchField,
  Checkbox,
  Select,
  TextField,
  Button,
  Badge,
  Field,
  GroupedRail,
  type GroupedRailItem,
  IconButton,
  StatusCard,
  EditableRowList,
  CodeBlock,
  Section,
} from './ui'
import { showToast } from './ui/toast'
import { ResetIcon } from './ui/icons'
import { systemPromptText } from './systemprompt'
import {
  historyDiscardSentence,
  historyUnavailableSentence,
  type HistoryStatus,
  type HistoryStatusStore,
} from './history-status'

/** The generated settings page that explains the standing prompt. */
const INSTRUCTIONS_SECTION = 'Instructions'

/** The section whose controls the history degrade contradicts. It is the
 *  Go-declared section string (internal/settings/settings.go), matched here
 *  rather than carried on settings.describe: the wire has no section object
 *  at all, and inventing one so that one section can carry one notice would
 *  make every future section-level fact a schema change. */
const HISTORY_SECTION = 'History'

const unavailableClipboard: ClipboardAccess = {
  readText: () => Promise.reject(new Error('no clipboard in this window')),
  writeText: () => Promise.reject(new Error('no clipboard in this window')),
}

export type SettingsPage =
  | { kind: 'generated'; id: string; title: string; groupId?: string }
  // A component page renders itself. It is a thunk rather than a bare
  // Component because such a page needs context the registry does not have —
  // Connections needs the ProfileClient and the connect callback — and binding
  // that at registration is what keeps the registry from having to know it.
  // scrollMode (design spec §3.8): 'page' — PageScroller owns vertical scroll;
  // 'contained' — Page provides a bounded content area and the surface assigns
  // its own scroll owners (e.g. Connections' two-column panels).
  // groupId names a group from the Go-declared catalogue (settings.describe);
  // undefined means the page renders at top level beside the groups.
  | {
      kind: 'component'
      id: string
      title: string
      groupId?: string
      description?: string
      actions?: JSX.Element
      scrollMode: 'page' | 'contained'
      renderContent: () => JSX.Element
    }
/** Stable DOM id for a setting row, derived from the declaration key. */
export function keyToDomId(key: string): string {
  return 'st-setting-' + encodeURIComponent(key)
}

/**
 * The backend's exact save refusal, in a person's words.
 *
 * The wire carries the registry's error verbatim — `settings: "key"
 * validation failed: …` and the Go sentinels beneath it — because a
 * developer wants that text in a log. A person watching a save fail does
 * not: the prefix names a package, and the sentinel answers a question they
 * never asked. The same rule as malformed-reason.ts: the wire keeps the
 * precise reason and the renderer says what it means.
 *
 * The validation shape carries its own message after the prefix
 * (`value must not be empty`, `"x" is not a valid option`) — stripping the
 * machine prefix IS the mapping. Everything else collapses to one honest
 * sentence rather than surfacing a transport string.
 */
function settingSaveErrorSentence(raw: string): string {
  const validation = /^settings: "[^"]*" validation failed: (.+)$/.exec(raw)
  if (validation) return validation[1]
  if (raw === 'settings: store is read-only') {
    return 'This setting could not be saved — the store is read-only'
  }
  return 'This setting could not be saved'
}
// ── Types ──────────────────────────────────────────────────────────────

// The wire declaration (with its Min/Max/unit validation) lives in the
// settings domain; the screen renders from it and never validates itself.

type LoadState = 'loading' | 'ready' | 'failed' | 'empty'

export interface SettingsComponentHandle {
  focus(): void
  scrollToKey(key: string): void
  /**
   * Show the Connections page with a blank profile open for editing — the
   * quick-connect palette's "New connection" entry point.
   */
  newConnection(): void
  /**
   * Show the Secrets page with the add dialog open — the prompt's '@'
   * picker offering to create a secret when the one you want is not there.
   */
  newSecret(name?: string): void
  /**
   * Show the Endpoints page with the editor open on a blank endpoint — the
   * ask surface's repair for "no endpoint configured".
   */
  newEndpoint(): void
  /**
   * Show a component page by its registry id, with nothing else asked for —
   * the general form of the three above, for a surface that only wants the
   * page ("Manage snippets…", nocx-d346). An id no page carries shows the
   * generated sections, which is where an unrouted Settings tab already
   * lands.
   */
  openPage(id: string): void
  /** Resolves when the initial data load completes. */
  ready(): Promise<void>
}

export interface SettingsComponentProps {
  profileClient: ProfileClient
  observer?: SettingsObserver
  onConnect?: (profile: SSHProfile) => void
  vaultController?: import('./vault').VaultController
  vaultClient?: import('./vault-client').VaultClient
  dialogClient?: DialogClient
  /** Remote footprint (nocx-mlm7 P10) for the Connections page. Absent in
   *  the dev-web harness; the section then renders nothing. */
  footprintClient?: FootprintClient
  endpointsClient?: EndpointClient
  /** The assistant's control-plane client (nocx-edio). Absent in the
   *  dev-web harness; the endpoints section then shows no status line. */
  agentClient?: AgentClient
  /** The snippet library (nocx-gjnr) — the SAME store the palette reads, so
   *  a snippet saved here is in the next fire without a notification on the
   *  wire (design §6). Absent in an embedding with no snippets service. */
  snippetsStore?: SnippetsStore
  /** Whether durable command history is actually running (nocx-rtg0.15).
   *  The History section's five controls all describe a store, so when
   *  there is no store the section has to say so where the user is looking
   *  — a soft degrade visible only in a log is how a feature that does not
   *  exist survives a release. Absent in an embedding with no backend; the
   *  section then makes no claim either way. */
  historyStatus?: HistoryStatusStore
  aboutClient?: AboutClient
  clipboard?: ClipboardAccess
  /** The agent policy client (ADR-0020 §7 as amended). Absent in
   *  embeddings that never configure the agent; the page then says so. */
  policyClient?: PolicyClient
  ref?: { current: SettingsComponentHandle | null }
}

// ── Root component ─────────────────────────────────────────────────────

export function SettingsComponent(props: SettingsComponentProps) {
  // ── State ──────────────────────────────────────────────────────────
  const [declarations, setDeclarations] = createSignal<Declaration[]>([])
  const [values, setValues] = createStore<Record<string, unknown>>({})
  const [draftValues, setDraftValues] = createStore<Record<string, unknown>>({})
  const [overridden, setOverridden] = createSignal<Set<string>>(new Set())
  const [errors, setErrors] = createStore<Record<string, string>>({})
  const [revision, setRevision] = createSignal(0)
  const [secretStates, setSecretStates] = createStore<Record<string, boolean>>({})
  const [loadState, setLoadState] = createSignal<LoadState>('loading')
  const [searchQuery, setSearchQuery] = createSignal('')
  const [modifiedOnly, setModifiedOnly] = createSignal(false)
  const [activeComponentPage, setActiveComponentPage] = createSignal<string | null>(null)
  // A counter rather than a flag: the Connections page has to start a blank
  // profile every time the palette asks, including the second time, and a
  // boolean would need a reset that both sides have to remember to do. Zero
  // means "nobody asked", which is what a normally-opened Settings tab reads.
  const [newConnectionRequest, setNewConnectionRequest] = createSignal(0)
  const [newSecretRequest, setNewSecretRequest] = createSignal(0)
  const [newEndpointRequest, setNewEndpointRequest] = createSignal(0)
  const [newSecretName, setNewSecretName] = createSignal('')
  const [sectionFilter, setSectionFilter] = createSignal<string | null>(null)
  // The rail's group catalogue and the section→group mapping, straight from
  // the settings.describe snapshot. The rail renders from these; there is no
  // lookup table in the frontend (nocx-dgsp).
  const [groups, setGroups] = createSignal<SettingsGroup[]>([])
  const [sectionGroups, setSectionGroups] = createSignal<Record<string, string>>({})
  // Durable-history availability, mirrored from the store so the section can
  // render it. null is "not read yet" and renders nothing — a surface shows
  // its placeholder rather than a lie in either direction.
  const [historyStatus, setHistoryStatus] = createSignal<HistoryStatus | null>(null)
  onMount(() => {
    const store = props.historyStatus
    if (store === undefined) return
    setHistoryStatus(store.status())
    onCleanup(store.subscribe((s) => setHistoryStatus(s)))
  })
  /** The two lines of the History notice, or null when there is nothing to
   *  say. One owner for the words (history-status.ts) — the recall panel
   *  tells the same person the same thing a moment later. */
  const historyNotice = createMemo(() => historyUnavailableSentence(historyStatus()))
  /** And the other thing the History section may have to say: history is
   *  running and starts from nothing, because the storage format changed.
   *  A separate memo because it is a separate fact — the notice above says a
   *  feature is down, this says a working one lost what it had. */
  const discardNotice = createMemo(() => historyDiscardSentence(historyStatus()))

  // Promise that resolves when the initial data load finishes.
  let resolveReady: () => void
  const readyPromise = new Promise<void>((resolve) => {
    resolveReady = resolve
  })

  // PageScroller handle (received via Page's scrollerRef).
  let scrollerHandle: PageScrollerHandle = { scrollToElement: () => {} }

  // ── Observer ───────────────────────────────────────────────────────
  let cleanupObserver: (() => void) | null = null

  onCleanup(() => {
    cleanupObserver?.()
  })

  function startObserver(): void {
    if (!props.observer) return
    cleanupObserver = props.observer.start(() => {
      void refresh(reconnectRevisionPolicy)
    })
    props.observer.setRevision(revision())
  }

  // ── Data loading ───────────────────────────────────────────────────

  async function refresh(accept: RevisionPolicy = monotonicRevisionPolicy): Promise<void> {
    setLoadState('loading')
    try {
      const [desc, snap] = await Promise.all([
        props.profileClient.describeSettings(),
        props.profileClient.getSnapshot(),
      ])
      const decls = desc.declarations ?? []
      setDeclarations(decls)
      setGroups(desc.groups ?? [])
      setSectionGroups(desc.sectionGroups ?? {})

      const rawSnap: SettingsSnapshot = {
        values: snap.values ?? {},
        overridden: snap.overridden ?? [],
        revision: snap.revision ?? 0,
      }
      const accepted = accept(revision(), rawSnap)
      if (accepted) {
        const nextState = applyAcceptedSnapshot(accepted)
        applyMirror(nextState)
        props.observer?.setRevision(nextState.revision)
      }

      // Parallel secret-existence probes.
      const secretDecls = decls.filter((d) => d.control === 'secret')
      if (secretDecls.length > 0) {
        const results = await Promise.allSettled(
          secretDecls.map((d) => props.profileClient.secretExists(d.key)),
        )
        for (let i = 0; i < secretDecls.length; i++) {
          const r = results[i]
          setSecretStates(secretDecls[i].key, r.status === 'fulfilled' ? r.value.exists : false)
        }
      } else {
        for (const k of Object.keys(secretStates)) {
          setSecretStates(k, false as never)
        }
      }

      setLoadState(decls.length === 0 ? 'empty' : 'ready')
    } catch {
      setLoadState('failed')
    }
  }

  function applyMirror(m: SettingsMirror): void {
    for (const [k, v] of Object.entries(m.values)) setValues(k, v as never)
    for (const k of Object.keys(values)) {
      if (!(k in m.values)) setValues(k, undefined as never)
    }
    for (const [k, v] of Object.entries(m.draftValues)) setDraftValues(k, v as never)
    for (const k of Object.keys(draftValues)) {
      if (!(k in m.draftValues)) setDraftValues(k, undefined as never)
    }
    setOverridden(new Set(m.overridden))
    for (const [k, v] of Object.entries(m.errors)) setErrors(k, v)
    for (const k of Object.keys(errors)) {
      if (!(k in m.errors)) setErrors(k, undefined as never)
    }
    setRevision(m.revision)
  }

  function toMirror(): SettingsMirror {
    const v: Record<string, unknown> = {}
    for (const k of Object.keys(values)) v[k] = values[k]
    const dv: Record<string, unknown> = {}
    for (const k of Object.keys(draftValues)) dv[k] = draftValues[k]
    const e: Record<string, string> = {}
    for (const k of Object.keys(errors)) e[k] = errors[k]
    return {
      values: v,
      draftValues: dv,
      overridden: overridden(),
      errors: e,
      revision: revision(),
    }
  }

  onMount(() => {
    startObserver()
    void refresh().then(() => resolveReady())
  })

  // ── Derived: filtered declarations ─────────────────────────────────

  const filteredDeclarations = createMemo(() => {
    let filtered = declarations()
    const q = searchQuery().toLowerCase()
    const sf = sectionFilter()

    // Section filter (nocx-ucxl): clicking a nav item always changes content.
    // A search overrides it — searching within one section would silently hide
    // the setting the user is looking for and give no hint that it did.
    if (sf !== null && q === '') {
      filtered = filtered.filter((d) => d.section === sf)
    }

    // Modified-only filter.
    if (modifiedOnly()) {
      filtered = filtered.filter((d) => d.control !== 'secret' && isModified(d))
    }

    // Search filter.
    if (q) {
      type Scored = { decl: Declaration; score: number }
      const scored: Scored[] = []
      for (const d of filtered) {
        const score = searchScore(d, q)
        if (score > 0) scored.push({ decl: d, score })
      }
      scored.sort((a, b) => b.score - a.score)
      filtered = scored.map((s) => s.decl)
    }

    return filtered
  })

  const isSearching = createMemo(() => searchQuery().length > 0)

  // ── Derived: sections (registry pages) ─────────────────────────────

  const sections: () => string[] = createMemo(() => {
    const seen = new Set<string>()
    const result: string[] = []
    for (const d of declarations()) {
      if (!seen.has(d.section)) {
        seen.add(d.section)
        result.push(d.section)
      }
    }
    return result
  })

  /** The typed page registry — generated sections + component pages. */
  const settingsPages = createMemo<SettingsPage[]>(() => {
    const generated: SettingsPage[] = sections().map((s) => ({
      kind: 'generated' as const,
      id: s,
      title: s,
      groupId: sectionGroups()[s],
    }))
    const backupPage: SettingsPage = {
      kind: 'component',
      id: 'backup',
      title: 'Backup & Restore',
      groupId: 'application',
      scrollMode: 'page',
      renderContent: () => <BackupRestoreSection profileClient={props.profileClient} />,
    }
    const connectionPage: SettingsPage = {
      kind: 'component',
      id: 'connections',
      scrollMode: 'contained',
      title: 'Connections',
      renderContent: () => (
        <ConnectionsView
          client={props.profileClient}
          vaultController={props.vaultController}
          vaultClient={props.vaultClient}
          dialogClient={props.dialogClient}
          footprintClient={props.footprintClient}
          onConnect={props.onConnect}
          newProfileRequest={newConnectionRequest()}
          onNavigateToSecrets={() => setActiveComponentPage('secrets')}
        />
      ),
    }
    const secretsPage: SettingsPage = {
      kind: 'component',
      id: 'secrets',
      title: 'Secrets',
      groupId: 'vault',
      scrollMode: 'contained',
      renderContent: () => (
        <Show
          when={props.vaultClient && props.vaultController}
          fallback={
            <PageSection title="Secrets">
              Vault secrets are not available in this window.
            </PageSection>
          }
        >
          <SecretsSection
            vaultClient={props.vaultClient!}
            vaultController={props.vaultController!}
            dialogClient={props.dialogClient}
            profileClient={props.profileClient}
            addSecretRequest={newSecretRequest()}
            addSecretName={newSecretName()}
          />
        </Show>
      ),
    }
    const vaultPage: SettingsPage = {
      kind: 'component',
      id: 'vault',
      title: 'Protection',
      groupId: 'vault',
      scrollMode: 'page',
      // The section is listed unconditionally — a surface that appears only
      // once some other state exists is how a feature ships unreachable. The
      // guard is for the client being absent, which the composition root never
      // does and a bare-bones embedding might; it renders a sentence rather
      // than throwing on a non-null assertion.
      renderContent: () => (
        <Show
          when={props.vaultClient && props.vaultController}
          fallback={
            <PageSection title="Protection">Vault is not available in this window.</PageSection>
          }
        >
          <VaultSection vaultClient={props.vaultClient!} vaultController={props.vaultController!} />
        </Show>
      ),
    }
    const endpointsPage: SettingsPage = {
      kind: 'component',
      id: 'endpoints',
      title: 'Endpoints',
      groupId: 'assistant',
      scrollMode: 'contained',
      // Registered unconditionally for the same reason vaultPage is: a
      // surface that appears only once some other state exists is how a
      // feature ships unreachable. The guard is the client being absent.
      renderContent: () => (
        <Show
          when={props.endpointsClient}
          fallback={
            <PageSection title="Endpoints">
              AI endpoints are not available in this window.
            </PageSection>
          }
        >
          <EndpointsSection
            client={props.endpointsClient!}
            agentClient={props.agentClient}
            vaultController={props.vaultController}
            vaultClient={props.vaultClient}
            addEndpointRequest={newEndpointRequest()}
          />
        </Show>
      ),
    }
    const snippetsPage: SettingsPage = {
      kind: 'component',
      id: 'snippets',
      title: 'Snippets',
      groupId: 'application',
      scrollMode: 'contained',
      // Registered unconditionally for the same reason vaultPage is: a
      // surface that appears only once some other state exists is how a
      // feature ships unreachable — and until this page existed, the
      // library's create/update/delete/reorder had no caller at all.
      renderContent: () => (
        <Show
          when={props.snippetsStore}
          fallback={
            <PageSection title="Snippets">Snippets are not available in this window.</PageSection>
          }
        >
          <SnippetsSection store={props.snippetsStore!} />
        </Show>
      ),
    }

    const rolesPage: SettingsPage = {
      kind: 'component',
      id: 'roles',
      title: 'Roles',
      groupId: 'assistant',
      scrollMode: 'page',
      // Registered unconditionally for the same reason endpointsPage is: a
      // surface that appears only once some other state exists is how a
      // feature ships unreachable. The guard is the client being absent.
      renderContent: () => (
        <Show
          when={props.endpointsClient}
          fallback={
            <PageSection title="Roles">Model roles are not available in this window.</PageSection>
          }
        >
          <RolesSection client={props.endpointsClient} />
        </Show>
      ),
    }

    const policyPage: SettingsPage = {
      kind: 'component',
      id: 'policy',
      title: 'Agent policy',
      groupId: 'assistant',
      scrollMode: 'page',
      renderContent: () => (
        <Show
          when={props.policyClient}
          fallback={
            <PageSection title="Agent policy">
              The agent policy is not available in this window.
            </PageSection>
          }
        >
          <AgentPolicySection client={props.policyClient!} />
        </Show>
      ),
    }

    // LAST IN THE RAIL, and in the 'application' group with Backup and
    // Snippets. It is the page nobody navigates to on purpose until something
    // has gone wrong, which is exactly why it must be findable in the obvious
    // place rather than clever about where it sits.
    const aboutPage: SettingsPage = {
      kind: 'component',
      id: 'about',
      title: 'About',
      groupId: 'application',
      scrollMode: 'page',
      renderContent: () => (
        <AboutSection
          load={() =>
            props.aboutClient
              ? props.aboutClient.load()
              : Promise.reject(new Error('the build description is not available in this window'))
          }
          clipboard={props.clipboard ?? unavailableClipboard}
        />
      ),
    }
    return [
      connectionPage,
      ...generated,
      backupPage,
      vaultPage,
      secretsPage,
      endpointsPage,
      snippetsPage,
      rolesPage,
      policyPage,
      aboutPage,
    ]
  })

  /** The rail rows the grouped rail renders: every page resolved to a group
   *  (or top level), its active state and per-section modified count. */
  const railItems = createMemo<GroupedRailItem[]>(() =>
    settingsPages().map((page) => ({
      id: page.id,
      title: page.title,
      groupId: page.groupId,
      // Accessors, not values: the row objects stay stable across navigation
      // and the rail updates them in place (Solid fine-grained updates).
      count: () => (page.kind === 'generated' ? modifiedBySection().get(page.id) : undefined),
      active: () =>
        page.kind === 'component'
          ? activeComponentPage() === page.id
          : activeComponentPage() === null && sectionFilter() === page.title,
      onSelect: () => handleNavClick(page),
    })),
  )

  /** The active component page, or null when a generated section is showing. */
  const activePage = createMemo<Extract<SettingsPage, { kind: 'component' }> | null>(() => {
    const id = activeComponentPage()
    if (id === null) return null
    const page = settingsPages().find((p) => p.id === id)
    return page !== undefined && page.kind === 'component' ? page : null
  })

  /** The active scroll mode — derived from the active component page,
   *  falling back to 'page' for generated sections. */
  const scrollMode = createMemo(() => {
    const page = activePage()
    return page !== null && page.kind === 'component' ? page.scrollMode : 'page'
  })

  /**
   * Open on the first page rather than on everything at once.
   *
   * With no selection the body listed every section end to end and the rail
   * showed nothing as current, so the rail read as decoration. The first page
   * is the first REGISTRY page (settingsPages()[0]) — the top-level
   * Connections page — not sections()[0], which is the first generated
   * section and can sit deep inside a group (History under Application).
   * Runs once, when the sections first arrive, and only while the user has
   * not already chosen — a later re-render must not yank them back to the
   * top of the list.
   */
  createEffect(() => {
    if (sections().length === 0) return
    const first = settingsPages()[0]
    if (first === undefined) return
    // The guard reads are untracked: the effect must fire only when the data
    // arrives, never in reaction to the user's own navigation. Tracking
    // activeComponentPage here let a click's first write (acp → null) re-run
    // the effect BEFORE sectionFilter was set, re-selecting Connections and
    // yanking the click back to the top of the rail.
    if (untrack(() => sectionFilter() !== null || activeComponentPage() !== null)) return
    if (untrack(() => searchQuery() !== '')) return
    handleNavClick(first)
  })

  const modifiedCount = createMemo(() => {
    let count = 0
    for (const d of declarations()) {
      if (d.control !== 'secret' && isModified(d)) count++
    }
    return count
  })

  /**
   * Whether a refusal to save a setting should be raised as a toast.
   *
   * One refusal is NOT toasted, and the reason is not that it is routed
   * somewhere else — the element that used to render it is gone. For a
   * number whose value the declaration's range rules reject, the number
   * field's caption already states the range fact ("Must be at least 128
   * MiB" sits under the box the moment the value leaves the bounds), so the
   * person is already looking at the news. `fieldSaveError` returns
   * `undefined` for exactly that case — its comment records the defect it
   * prevents: the backend's sentence rendered directly under the caption
   * that already said the same thing, in the backend's language and wider
   * than the field's column. Toasting the same fact would be that defect
   * again. Every other refusal — an unpredicted validation, a transport
   * failure, a read-only store — is the outcome of the save call the user
   * triggered and belongs in a toast (ui/README.md "Toast"), never in the
   * document flow.
   */
  function rejectionNeedsToast(key: string, value: unknown, message: string | undefined): boolean {
    if (message === undefined) return false
    const decl = declarations().find((d) => d.key === key)
    if (decl && decl.control === 'number') {
      const numericValue = Number(value)
      if (
        !Number.isNaN(numericValue) &&
        fieldSaveError(decl, numericValue, message) === undefined
      ) {
        return false
      }
    }
    // The same reasoning one control over. A paragraph past its declared
    // length carries the bound in its own caption, permanently on screen, so
    // the backend refusing it is not news either — and `fieldSaveError` is
    // where that judgement is made for both controls rather than here for one
    // and there for the other.
    if (decl && decl.control === 'text') {
      const text = typeof value === 'string' ? value : ''
      if (fieldSaveError(decl, 0, message, text) === undefined) return false
    }
    return true
  }

  const modifiedBySection = createMemo(() => {
    const counts = new Map<string, number>()
    for (const d of declarations()) {
      if (d.control !== 'secret' && isModified(d)) {
        counts.set(d.section, (counts.get(d.section) ?? 0) + 1)
      }
    }
    return counts
  })

  // ── Visible keys set for style.display hiding ─────────────────────

  const visibleKeys: () => Set<string> = createMemo(
    () => new Set(filteredDeclarations().map((d: Declaration) => d.key)),
  )

  /**
   * The sections with something to show — the ones the body renders.
   *
   * A section with no visible row is not rendered at all rather than
   * rendered and hidden. The surface already answers "is this page
   * showing?" by unmounting: a component page's content is behind a keyed
   * `Show`, and the whole generated block is behind another. A generated
   * section is the same question, and answering it a second way left the
   * open page trailed by every other section's rows at `display: none`.
   *
   * That is not merely waste. The rows of a page nobody is on are the tail
   * of the scroller's content, so "the last setting row" — which is what
   * the browser proofs of the scroll chain measure — meant whichever row
   * happened to be declared last in the whole registry. It belonged to the
   * open section only for as long as the last-registered section happened
   * to be the one under test (nocx-avogl.4 registered one after it and the
   * proofs began measuring a row nobody can see).
   *
   * Row-level hiding stays: within a section that IS showing, search hides
   * individual rows, and Stack's `divided` variant is built for exactly
   * that (`:not(.st-vis-hidden)`).
   */
  const visibleSections: () => string[] = createMemo(() => {
    const shown = visibleKeys()
    const withRows = new Set(
      declarations()
        .filter((d) => shown.has(d.key))
        .map((d) => d.section),
    )
    return sections().filter((s) => withRows.has(s))
  })

  // ── Actions ────────────────────────────────────────────────────────

  async function saveSetting(key: string, value: unknown): Promise<void> {
    let outcome: SaveOutcome
    try {
      await props.profileClient.setSetting(key, value)
      outcome = { kind: 'accepted', value }
    } catch (err) {
      outcome = { kind: 'rejected', error: (err as Error).message, attemptedValue: value }
    }
    const nextState = recordSaveOutcome(toMirror(), key, outcome)
    applyMirror(nextState)
    if (outcome.kind === 'rejected') {
      const label = declarations().find((d) => d.key === key)?.label ?? key
      if (rejectionNeedsToast(key, outcome.attemptedValue, outcome.error)) {
        showToast({
          level: 'danger',
          message: `Could not save "${label}": ${settingSaveErrorSentence(outcome.error)}`,
        })
      }
    }
  }

  async function addPath(decl: Declaration): Promise<void> {
    if (!props.dialogClient) return
    try {
      const picked = await props.dialogClient.openDirectoryDialog()
      if (!picked.path) return
      // The two-class rule is stated once, in sandbox-path-classes, and the
      // backend is still the authority. This surface used to compare exact
      // strings in both directions, which refused a writable folder inside a
      // read-only one — the exception ADR-0039 exists for — and accepted a
      // read-only folder inside a writable one, which the backend then
      // refused in different words (nocx-61alt).
      const conflict = sandboxClassConflictFor(decl.key, picked.path)
      if (conflict !== null) {
        setErrors(decl.key, conflict)
        return
      }
      setErrors(decl.key, undefined as never)
      const current = pathsValue(decl.key)
      await saveSetting(
        decl.key,
        current.includes(picked.path) ? current : [...current, picked.path],
      )
    } catch (err) {
      setErrors(decl.key, (err as Error).message)
    }
  }

  async function removePath(decl: Declaration, index: number): Promise<void> {
    await saveSetting(
      decl.key,
      pathsValue(decl.key).filter((_, itemIndex) => itemIndex !== index),
    )
  }

  async function resetSetting(key: string): Promise<void> {
    setErrors(key, undefined as never)
    try {
      await props.profileClient.resetSetting(key)
      const snap = await props.profileClient.getSnapshot()
      const rawSnap: SettingsSnapshot = {
        values: snap.values ?? {},
        overridden: snap.overridden ?? [],
        revision: snap.revision ?? 0,
      }
      const accepted = AcceptedSnapshot.accept(revision(), rawSnap)
      if (accepted) {
        const nextState = applyAcceptedSnapshot(accepted)
        applyMirror(nextState)
      }
    } catch (err) {
      const message = (err as Error).message
      setErrors(key, message)
      const label = declarations().find((d) => d.key === key)?.label ?? key
      showToast({
        level: 'danger',
        message: `Could not reset "${label}": ${settingSaveErrorSentence(message)}`,
      })
    }
  }

  async function saveSecret(key: string, value: string): Promise<void> {
    setErrors(key, undefined as never)
    try {
      await props.profileClient.secretSet(key, value)
      setSecretStates(key, true)
    } catch (err) {
      const message = (err as Error).message
      setErrors(key, message)
      const label = declarations().find((d) => d.key === key)?.label ?? key
      showToast({
        level: 'danger',
        message: `Could not save "${label}": ${settingSaveErrorSentence(message)}`,
      })
    }
  }

  async function deleteSecret(key: string): Promise<void> {
    setErrors(key, undefined as never)
    try {
      await props.profileClient.secretDelete(key)
      setSecretStates(key, false)
    } catch (err) {
      const message = (err as Error).message
      setErrors(key, message)
      const label = declarations().find((d) => d.key === key)?.label ?? key
      showToast({
        level: 'danger',
        message: `Could not clear "${label}": ${settingSaveErrorSentence(message)}`,
      })
    }
  }

  function handleSearchInput(value: string): void {
    setSearchQuery(value)
    // A component page owns the whole body, so typing into the search box while
    // Connections or Export was open changed the results nobody could see and
    // the box looked broken. Search is over the settings, so searching leaves
    // the page — and clearing the box returns to the section that was selected,
    // which sectionFilter has been holding all along.
    if (value !== '') setActiveComponentPage(null)
  }

  function handleNavClick(page: SettingsPage): void {
    if (page.kind === 'component') {
      setActiveComponentPage(page.id)
      setSectionFilter(null)
      setSearchQuery('')
    } else {
      setActiveComponentPage(null)
      // Selects, never toggles. Clicking the current page used to deselect it
      // and drop the user back into the everything-at-once list, which is not a
      // state the rail can show and not one anybody asked for.
      setSectionFilter(page.title)
      setSearchQuery('')
    }
  }

  // ── Keyboard handler ───────────────────────────────────────────────

  function handleKeydown(e: KeyboardEvent): void {
    if (
      e.key === '/' &&
      !(
        e.target instanceof HTMLInputElement ||
        e.target instanceof HTMLTextAreaElement ||
        e.target instanceof HTMLSelectElement
      )
    ) {
      e.preventDefault()
      document.querySelector<HTMLInputElement>('[aria-label="Search settings"]')?.focus()
    }
    if (e.key === 'Escape' && searchQuery()) {
      setSearchQuery('')
    }
  }

  // ── Expose handle ──────────────────────────────────────────────────

  const handle: SettingsComponentHandle = {
    focus(): void {
      document.querySelector<HTMLInputElement>('[aria-label="Search settings"]')?.focus()
    },
    scrollToKey(key: string): void {
      // Clear search and section filter so the target row is visible.
      setSearchQuery('')
      setSectionFilter(null)

      const row = document.getElementById(keyToDomId(key))
      if (!row) return

      scrollerHandle.scrollToElement(row)
      const control = row.querySelector<HTMLElement>('input, select, button')
      control?.focus()

      row.classList.add('ui-settings-row-highlight')
      row.addEventListener(
        'animationend',
        () => row.classList.remove('ui-settings-row-highlight'),
        { once: true },
      )
    },
    newConnection(): void {
      // Clear the filters first: with a search still active the Connections page
      // is not among the pages the rail offers, and the request would land on a
      // page nobody can see.
      setSearchQuery('')
      setSectionFilter(null)
      setActiveComponentPage('connections')
      setNewConnectionRequest((n) => n + 1)
    },
    newSecret(name?: string): void {
      // Same reason as newConnection: an active search hides the page the
      // request is addressed to.
      setSearchQuery('')
      setSectionFilter(null)
      setActiveComponentPage('secrets')
      // The name BEFORE the counter: the effect that opens the dialog runs
      // on the counter and reads the name as it stands then.
      setNewSecretName(name ?? '')
      setNewSecretRequest((n) => n + 1)
    },
    newEndpoint(): void {
      // Same reason as newConnection: an active search hides the page the
      // request is addressed to.
      setSearchQuery('')
      setSectionFilter(null)
      setActiveComponentPage('endpoints')
      setNewEndpointRequest((n) => n + 1)
    },
    openPage(id: string): void {
      // Same reason as newConnection: an active search or section filter
      // hides the page the request is addressed to.
      setSearchQuery('')
      setSectionFilter(null)
      setActiveComponentPage(id)
    },
    ready(): Promise<void> {
      return readyPromise
    },
  }

  // eslint-disable-next-line solid/reactivity
  if (props.ref) {
    // eslint-disable-next-line solid/reactivity
    props.ref.current = handle
  }

  // ── Search scoring ─────────────────────────────────────────────────

  function searchScore(decl: Declaration, query: string): number {
    const q = query.toLowerCase()
    if (decl.label.toLowerCase() === q || decl.key.toLowerCase() === q) return 2
    if (decl.label.toLowerCase().includes(q)) return 1
    if (decl.description.toLowerCase().includes(q)) return 1
    if (decl.section.toLowerCase().includes(q)) return 1
    if (decl.key.toLowerCase().includes(q)) return 1
    if (decl.options) {
      for (const opt of decl.options) {
        if (opt.label.toLowerCase().includes(q) || opt.value.toLowerCase().includes(q)) return 1
      }
    }
    return 0
  }

  // ── Value helpers ──────────────────────────────────────────────────

  function effectiveValue(key: string): unknown {
    if (key in draftValues) return draftValues[key]
    return values[key]
  }

  function pathsValue(key: string): string[] {
    const value = effectiveValue(key)
    return Array.isArray(value)
      ? value.filter((item): item is string => typeof item === 'string')
      : []
  }

  /** The class a sandbox path list grants, or null for any other setting.
   *  Keyed by declaration id because that is what the wire carries; moving it
   *  onto the declaration itself is nocx-lt8su. */
  function sandboxPathClass(key: string): 'readOnly' | 'readWrite' | null {
    if (key === SANDBOX_WRITABLE_PATHS_KEY) return 'readWrite'
    if (key === SANDBOX_READ_ONLY_PATHS_KEY) return 'readOnly'
    return null
  }

  /** What this surface may say about a pick, before the backend decides. */
  function sandboxClassConflictFor(key: string, path: string): string | null {
    const target = sandboxPathClass(key)
    if (target === null) return null
    return classConflict(
      target,
      path,
      pathsValue(SANDBOX_READ_ONLY_PATHS_KEY),
      pathsValue(SANDBOX_WRITABLE_PATHS_KEY),
    )
  }

  /**
   * Has the user actually changed this setting away from its default?
   *
   * The single answer behind the row marker, the rail counts, the Modified-only
   * filter and whether Reset is offered — they were four reads of the raw
   * override set, which is why picking a value, changing your mind and picking
   * the default again left the row flagged as modified with a Reset that would
   * change nothing.
   */
  function isModified(decl: Declaration): boolean {
    return isSettingModified(overridden(), decl.key, effectiveValue(decl.key), decl.default)
  }

  function displayValue(value: unknown, decl: Declaration): string {
    const def = decl.default

    if (decl.control === 'number') {
      if (typeof value === 'number' && !isNaN(value)) return String(value)
      if (typeof def === 'number' && !isNaN(def)) {
        if (value !== undefined && value !== null) {
          log.warn('nocx: unexpected type for setting', {
            key: decl.key,
            got: typeof value,
            expected: 'number',
          })
        }
        return String(def)
      }
      if (value !== undefined && value !== null) {
        log.warn('nocx: unusable value and default for setting', {
          key: decl.key,
          got: typeof value,
          defaultType: typeof def,
        })
      }
      return '0'
    }

    if (typeof value === 'string') return value
    if (typeof def === 'string') {
      if (value !== undefined && value !== null) {
        log.warn('nocx: unexpected type for setting', {
          key: decl.key,
          got: typeof value,
          expected: 'string',
        })
      }
      return def
    }
    if (value !== undefined && value !== null) {
      log.warn('nocx: unusable value and default for setting', {
        key: decl.key,
        got: typeof value,
        defaultType: typeof def,
      })
    }
    return ''
  }

  // ── Render helpers ─────────────────────────────────────────────────

  // ── Sub-components ─────────────────────────────────────────────────

  /**
   * The reset affordance for one setting.
   *
   * There used to be a "Customized"/"Default" badge beside the control as well.
   * It is gone: "Default" was printed on every untouched row, which is the
   * overwhelmingly common case, and "Customized" said nothing that the Reset
   * button beside it did not already say. What a changed row now carries is a
   * dot on its label (`.ui-settings-row--modified`) — scannable down the
   * column, and out of the control's way.
   *
   * The affordance itself is an icon at the right end of the row, in a gutter the
   * row reserves whether or not it is showing one. It used to be a full "Reset to
   * default" button sitting directly BELOW the control — which is where the
   * pointer already is after operating that control, so the two were one slip
   * apart. Sideways with a gap costs the same pixels and is not on that path.
   * Icon-only, so `ariaLabel` is not optional; `title` carries the same words the
   * button used to spell out.
   */
  function ProvenanceBadge(props: { decl: Declaration }) {
    // eslint-disable-next-line solid/reactivity
    const decl = props.decl
    const decision = () =>
      isModified(decl)
        ? canResetSetting(overridden(), decl.key)
        : ({ canReset: false, reason: 'notOverridden' } as const)

    // The SLOT is unconditional and the icon inside it is not. With the `Show`
    // around the span, the slot itself came and went and the control shifted
    // sideways the moment a value changed — the control moving as a side effect
    // of being used, which is the same defect the modified dot had on the label
    // side. Its width is reserved in settings.css.
    return (
      <span class="ui-settings-provenance">
        <Show when={decl.default !== undefined && decision().canReset}>
          <IconButton
            size="xs"
            ariaLabel={'Reset "' + decl.label + '" to default'}
            title="Reset to default"
            onClick={() => void resetSetting(decl.key)}
          >
            <ResetIcon />
          </IconButton>
        </Show>
      </span>
    )
  }

  function SettingRow(props: { decl: Declaration; visible: boolean }) {
    // eslint-disable-next-line solid/reactivity
    const decl = props.decl
    const eff = () => effectiveValue(decl.key)
    // The number this row shows, for the two consumers that reason about
    // bounds. Guarded by the control kind: displayValue coerces to a string
    // and warns when it can find neither a usable value nor a usable
    // default, which is exactly what a boolean looks like to it — so
    // calling it for every row filled the console with "unusable value and
    // default for setting history.enabled, got boolean, defaultType
    // boolean". NaN for a row that is not a number; every caller of a range
    // check treats NaN as "no opinion".
    const numeric = () => (decl.control === 'number' ? Number(displayValue(eff(), decl)) : NaN)
    // The text this row shows, for the length caption and its error. Empty
    // for a row that is not text, for the same reason numeric() is NaN
    // there: displayValue warns when it can find neither a usable value nor
    // a usable default, which is what a boolean looks like to it.
    const textValue = () => (decl.control === 'text' ? displayValue(eff(), decl) : '')
    const err = () => errors[decl.key]
    const showBreadcrumb = () => isSearching() && sectionFilter() === null

    return (
      <div
        class="ui-settings-row"
        id={keyToDomId(decl.key)}
        data-key={decl.key}
        classList={{
          'st-vis-hidden': !props.visible,
          'ui-settings-row--modified': isModified(decl),
        }}
      >
        <Field
          for={keyToDomId(decl.key)}
          label={decl.label}
          labelMarker={
            // Always rendered, coloured only when modified. Under a `Show` the
            // dot's 6px box and its margin entered the flow the moment a value
            // was changed, so the label jumped 14px sideways — the row moved to
            // report that it had not moved. Reserving the space costs nothing on
            // an unmodified row and keeps the labels in one column.
            <span
              class="ui-settings-modified-dot"
              data-modified={isModified(decl) ? 'true' : undefined}
              aria-hidden="true"
            />
          }
          description={decl.description || undefined}
          orientation="horizontal"
          labelAdornment={
            // The storage-class tag (Public / Private / Secret) used to sit here.
            // It is gone: nothing in the product consulted `dataClass` — export
            // eligibility is decided by the control kind in Registry.GetSnapshot,
            // not by the class — so the tag printed one of three words on every
            // row with no consequence attached to any of them.
            <Show when={showBreadcrumb()}>
              <span class="ui-settings-breadcrumb">{decl.section}</span>
            </Show>
          }
        >
          {/* One line: the control and its reset affordance, side by side. The
              wrapper is the surface's own, so the reset sits level with the
              control without the surface reaching into Field's column. */}
          <Show when={decl.control !== 'paths'}>
            <div class="ui-settings-control-line">
              <Show when={decl.control === 'toggle'}>
                <Checkbox
                  variant="switch"
                  checked={!!eff()}
                  ariaLabel={decl.label}
                  onChange={(c) => void saveSetting(decl.key, c)}
                />
              </Show>

              <Show when={decl.control === 'text'}>
                {/* multiline is a VARIANT of the same kit component, declared
                  by the setting (Declaration.multiline) — the kit answers
                  "a paragraph rather than a value" with a prop and has no
                  second component, so neither does this. The caption is the
                  declared bound, permanently on screen: the criterion is
                  that a length limit is stated, never discovered by losing
                  text to it. */}
                <TextField
                  multiline={decl.multiline === true}
                  // A setting's paragraph is always prose. The kit's default
                  // is verbatim because its first caller pastes a private
                  // key; nothing on this screen ever does — a secret-class
                  // setting is a `secret` control, not a text one.
                  wrap
                  value={textValue()}
                  caption={textLengthCaption(decl, textValue())}
                  captionAlign="end"
                  error={textLengthError(decl, textValue())}
                  onInput={(v) => void saveSetting(decl.key, v)}
                />
              </Show>

              <Show when={decl.control === 'number'}>
                <TextField
                  type="number"
                  value={displayValue(eff(), decl)}
                  min={decl.min}
                  max={decl.max}
                  unit={decl.unit}
                  caption={numberRangeCaption(decl, numeric())}
                  captionAlign="end"
                  error={numberRangeError(decl, numeric())}
                  onInput={(v) => {
                    const n = Number(v)
                    void saveSetting(decl.key, isNaN(n) ? Number(displayValue(eff(), decl)) : n)
                  }}
                />
              </Show>

              <Show when={decl.control === 'select'}>
                <Select
                  value={displayValue(eff(), decl)}
                  onChange={(v) => void saveSetting(decl.key, v)}
                  options={decl.options ?? []}
                />
              </Show>

              <Show when={decl.control === 'secret'}>
                <div class="ui-settings-secret">
                  <span class="ui-settings-secret-status">
                    {secretStates[decl.key] ? 'Configured' : 'Not configured'}
                  </span>
                  <Button
                    variant="default"
                    onClick={() => {
                      const value = prompt('Enter new value for "' + decl.label + '":')
                      if (value === null) return
                      void saveSecret(decl.key, value)
                    }}
                  >
                    Replace
                  </Button>
                  <Button variant="danger" onClick={() => void deleteSecret(decl.key)}>
                    Clear
                  </Button>
                </div>
              </Show>

              <ProvenanceBadge decl={decl} />
            </div>
          </Show>

          <Show when={decl.control === 'paths'}>
            <div class="ui-settings-paths">
              <EditableRowList
                rows={pathsValue(decl.key)}
                ariaLabel={decl.label}
                addLabel="Add folder"
                emptyLabel="No folders — the sandboxed pane is limited to its workspace."
                removeLabel={(index) => `Remove folder ${index + 1}`}
                onRemove={(index) => void removePath(decl, index)}
                onAdd={() => void addPath(decl)}
                renderRow={(path) => <span class="ui-settings-paths-row">{path()}</span>}
                error={err()}
              />
              <ProvenanceBadge decl={decl} />
            </div>
          </Show>

          <Show when={fieldSaveError(decl, numeric(), err())}>
            <div class="ui-settings-error">{err()}</div>
          </Show>
        </Field>
      </div>
    )
  }

  // ── Main render ────────────────────────────────────────────────────

  return (
    <div class="ui-settings" onKeyDown={handleKeydown}>
      <Page
        title="Settings"
        titleHidden
        leading={
          <div class="ui-settings-rail">
            {/* ONE search box (nocx-x6w9) — only in the rail. */}
            <div class="ui-settings-search">
              <SearchField
                value={searchQuery()}
                onInput={handleSearchInput}
                placeholder="Search settings…"
                ariaLabel="Search settings"
              />
            </div>

            {/* ONE modified-only filter (nocx-x6w9) — only in the rail. */}
            <div class="ui-settings-filter">
              <Checkbox
                checked={modifiedOnly()}
                onChange={(c) => setModifiedOnly(c)}
                label={' Modified'}
              />
              <Show when={modifiedCount() > 0}>
                <Badge tone="warning">{String(modifiedCount())}</Badge>
              </Show>
            </div>

            {/* The grouped rail is kit work (nocx-dgsp): the surface places it
                and never repaints it. Grouping comes from the Go-declared
                catalogue in the settings.describe snapshot, resolved per page
                by railItems. It renders only once the catalogue has arrived —
                the guard refuses an empty catalogue with named pages. */}
            <Show when={loadState() === 'ready'}>
              <GroupedRail label="Settings sections" groups={groups()} items={railItems()} />
            </Show>
          </div>
        }
        scrollerRef={(h) => {
          scrollerHandle = h
        }}
        scrollMode={scrollMode()}
      >
        {/* A component page takes over the body when active. Resolved through
              the registry rather than a chain of id comparisons, so adding a
              page is one registry entry. */}
        {/* `keyed` is load-bearing: a plain Show only re-runs its callback
              when `when` crosses falsy→truthy, so switching Export → Connections
              (truthy → different truthy) left the previous page on screen.
              Keying re-creates the subtree whenever the page identity changes. */}
        <Show when={activePage()} keyed>
          {(page) => page.renderContent()}
        </Show>

        {/* Generated settings sections — hidden when a component page is active. */}
        <Show when={activeComponentPage() === null}>
          <Show when={loadState() === 'loading'}>
            <div class="ui-settings-status ui-settings-loading">Loading settings…</div>
          </Show>

          <Show when={loadState() === 'failed'}>
            <div class="ui-settings-status ui-settings-failed">
              <span>Failed to load settings.</span>
              <Button variant="default" onClick={() => void refresh()}>
                Retry
              </Button>
            </div>
          </Show>

          <Show
            when={
              loadState() === 'ready' &&
              filteredDeclarations().length === 0 &&
              declarations().length > 0
            }
          >
            <div class="ui-settings-status ui-settings-nomatch">No settings match your search.</div>
          </Show>

          {/* The sections with something to show, and no others — see
              visibleSections. Within one of them, search still hides
              individual rows via `st-vis-hidden`. */}
          <Show when={loadState() === 'ready'}>
            <For each={visibleSections()}>
              {(section) => {
                const sectionDecls = () => declarations().filter((d) => d.section === section)
                return (
                  <PageSection
                    id={'st-section-' + encodeURIComponent(section)}
                    title={section}
                    divided
                  >
                    {/* The prompt artifact is generated from the Go renderer and
                        contains placeholders instead of any focused pane facts.
                        CodeBlock owns the fixed scroll cap, so this long
                        read-only text cannot push the person's field away. */}
                    <Show when={section === INSTRUCTIONS_SECTION}>
                      <CodeBlock ariaLabel="nocx system prompt">{systemPromptText}</CodeBlock>
                    </Show>
                    {/* The degrade notice, above the controls it
                        contradicts. A kit StatusCard, placed and never
                        repainted: a state plus what to do about it is
                        exactly what it is for, and hand-rolling a
                        coloured div here is the defect two epics spent
                        themselves unwinding (ui/README.md). */}
                    <Show when={section === HISTORY_SECTION && historyNotice() !== null}>
                      <StatusCard
                        tone="warning"
                        title={historyNotice()!.title}
                        description={historyNotice()!.description}
                      />
                    </Show>
                    {/* The discard is `neutral`, not `warning`: nothing is
                        wrong and there is nothing to fix — it is a thing
                        that happened, which the person is entitled to know
                        because an empty history after an update is
                        otherwise indistinguishable from a fresh install.
                        The kit has no `info` tone and does not need one;
                        neutral is what "a fact, not a fault" already
                        means here. */}
                    <Show when={section === HISTORY_SECTION && discardNotice() !== null}>
                      <StatusCard
                        tone="neutral"
                        title={discardNotice()!.title}
                        description={discardNotice()!.description}
                      />
                    </Show>
                    <Show
                      when={section === INSTRUCTIONS_SECTION}
                      fallback={
                        <For each={sectionDecls()}>
                          {(decl) => (
                            <SettingRow decl={decl} visible={visibleKeys().has(decl.key)} />
                          )}
                        </For>
                      }
                    >
                      <Section title="What the person added" divided>
                        <For each={sectionDecls()}>
                          {(decl) => (
                            <SettingRow decl={decl} visible={visibleKeys().has(decl.key)} />
                          )}
                        </For>
                      </Section>
                    </Show>
                  </PageSection>
                )
              }}
            </For>
          </Show>
        </Show>
      </Page>
    </div>
  )
}
