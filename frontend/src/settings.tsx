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
import { For, Show, createSignal, createMemo, onMount, onCleanup } from 'solid-js'
import { createStore } from 'solid-js/store'
import { ConnectionsView } from './connections'
import type { ProfileClient, SSHProfile } from './profiles'
import { SettingsObserver } from './settings-observer'
import {
  AcceptedSnapshot,
  applyAcceptedSnapshot,
  canResetSetting,
  monotonicRevisionPolicy,
  reconnectRevisionPolicy,
  recordSaveOutcome,
  type RevisionPolicy,
  type SaveOutcome,
  type SettingsMirror,
  type SettingsSnapshot,
} from './settings-domain'
import { ExportSection } from './export-section'
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
} from './ui'

// ── Settings page registry type (Deliverable 2) ────────────────────────

export type SettingsPage =
  | { kind: 'generated'; id: string; title: string }
  // A component page renders itself. It is a thunk rather than a bare
  // Component because such a page needs context the registry does not have —
  // Connections needs the ProfileClient and the connect callback — and binding
  // that at registration is what keeps the registry from having to know it.
  | { kind: 'component'; id: string; title: string; render: () => JSX.Element }

// ── Stable DOM id ──────────────────────────────────────────────────────

/** Stable DOM id for a setting row, derived from the declaration key. */
export function keyToDomId(key: string): string {
  return 'st-setting-' + encodeURIComponent(key)
}

// ── Types ──────────────────────────────────────────────────────────────

export interface Declaration {
  key: string
  section: string
  label: string
  description: string
  control: 'toggle' | 'text' | 'number' | 'select' | 'secret'
  dataClass: 'publicConfig' | 'privateMetadata' | 'privateContent' | 'secretAuthenticator'
  default?: unknown
  options?: { value: string; label: string }[]
  min?: number
  max?: number
}

type LoadState = 'loading' | 'ready' | 'failed' | 'empty'

export interface SettingsComponentHandle {
  focus(): void
  scrollToKey(key: string): void
  /** Resolves when the initial data load completes. */
  ready(): Promise<void>
}

export interface SettingsComponentProps {
  profileClient: ProfileClient
  observer?: SettingsObserver
  onConnect?: (profile: SSHProfile) => void
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
  const [sectionFilter, setSectionFilter] = createSignal<string | null>(null)

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
    props.observer.start(() => {
      void refresh(reconnectRevisionPolicy)
    })
    props.observer.setRevision(revision())
    // eslint-disable-next-line solid/reactivity
    cleanupObserver = () => props.observer!.stop()
  }

  // ── Data loading ───────────────────────────────────────────────────

  async function refresh(accept: RevisionPolicy = monotonicRevisionPolicy): Promise<void> {
    setLoadState('loading')
    try {
      const [desc, snap] = await Promise.all([
        props.profileClient.describeSettings(),
        props.profileClient.getSnapshot(),
      ])
      const decls = (desc.declarations as Declaration[]) ?? []
      setDeclarations(decls)

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
    if (sf !== null) {
      filtered = filtered.filter((d) => d.section === sf)
    }

    // Modified-only filter.
    if (modifiedOnly()) {
      filtered = filtered.filter((d) => d.control !== 'secret' && overridden().has(d.key))
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
    }))
    const connectionPage: SettingsPage = {
      kind: 'component',
      id: 'connections',
      title: 'Connections',
      render: () => <ConnectionsView client={props.profileClient} onConnect={props.onConnect} />,
    }
    return [...generated, connectionPage]
  })

  const modifiedCount = createMemo(() => {
    let count = 0
    for (const d of declarations()) {
      if (d.control !== 'secret' && overridden().has(d.key)) count++
    }
    return count
  })

  const modifiedBySection = createMemo(() => {
    const counts = new Map<string, number>()
    for (const d of declarations()) {
      if (d.control !== 'secret' && overridden().has(d.key)) {
        counts.set(d.section, (counts.get(d.section) ?? 0) + 1)
      }
    }
    return counts
  })

  // ── Visible keys set for style.display hiding ─────────────────────

  const visibleKeys: () => Set<string> = createMemo(
    () => new Set(filteredDeclarations().map((d: Declaration) => d.key)),
  )

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
      setErrors(key, (err as Error).message)
    }
  }

  async function saveSecret(key: string, value: string): Promise<void> {
    setErrors(key, undefined as never)
    try {
      await props.profileClient.secretSet(key, value)
      setSecretStates(key, true)
    } catch (err) {
      setErrors(key, (err as Error).message)
    }
  }

  async function deleteSecret(key: string): Promise<void> {
    setErrors(key, undefined as never)
    try {
      await props.profileClient.secretDelete(key)
      setSecretStates(key, false)
    } catch (err) {
      setErrors(key, (err as Error).message)
    }
  }

  function handleSearchInput(value: string): void {
    setSearchQuery(value)
  }

  function handleNavClick(page: SettingsPage): void {
    if (page.kind === 'component') {
      setActiveComponentPage(page.id)
      setSectionFilter(null)
      setSearchQuery('')
    } else {
      setActiveComponentPage(null)
      setSectionFilter((prev) => (prev === page.title ? null : page.title))
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

  function renderDataClassIndicator(dataClass: Declaration['dataClass']): string {
    switch (dataClass) {
      case 'publicConfig':
        return 'Public'
      case 'privateMetadata':
      case 'privateContent':
        return 'Private'
      case 'secretAuthenticator':
        return 'Secret'
    }
  }

  // ── Sub-components ─────────────────────────────────────────────────

  function ProvenanceBadge(props: { decl: Declaration }) {
    // eslint-disable-next-line solid/reactivity
    const decl = props.decl
    const customized = () => overridden().has(decl.key)
    const decision = () => canResetSetting(overridden(), decl.key)

    return (
      <Show when={decl.default !== undefined}>
        <span class="ui-settings-provenance">
          <Badge variant={customized() ? 'warning' : 'default'}>
            {customized() ? 'Customized' : 'Default'}
          </Badge>
          <Show when={decision().canReset}>
            <Button class="ui-settings-reset-btn" onClick={() => void resetSetting(decl.key)}>
              Reset
            </Button>
          </Show>
        </span>
      </Show>
    )
  }

  function SettingRow(props: { decl: Declaration; visible: boolean }) {
    // eslint-disable-next-line solid/reactivity
    const decl = props.decl
    const eff = () => effectiveValue(decl.key)
    const err = () => errors[decl.key]
    const showBreadcrumb = () => isSearching() && sectionFilter() === null

    return (
      <div
        class="ui-settings-row"
        id={keyToDomId(decl.key)}
        data-key={decl.key}
        classList={{ 'st-vis-hidden': !props.visible }}
      >
        <Field
          for={keyToDomId(decl.key)}
          label={decl.label}
          description={decl.description || undefined}
          orientation="horizontal"
        >
          <Show when={showBreadcrumb()}>
            <span class="ui-settings-breadcrumb">{decl.section}</span>
          </Show>
          <span class="ui-settings-data-class" title={'Storage class: ' + decl.dataClass}>
            {renderDataClassIndicator(decl.dataClass)}
          </span>

          <Show when={decl.control === 'number'}>
            <span class="ui-settings-bounds">
              <Show when={decl.min !== undefined && decl.max !== undefined}>
                {String(decl.min) + ' – ' + String(decl.max)}
              </Show>
              <Show when={decl.min !== undefined && decl.max === undefined}>
                {'≥ ' + String(decl.min)}
              </Show>
              <Show when={decl.max !== undefined && decl.min === undefined}>
                {'≤ ' + String(decl.max)}
              </Show>
            </span>
          </Show>

          <Show when={decl.control === 'toggle'}>
            <Checkbox checked={!!eff()} onChange={(c) => void saveSetting(decl.key, c)} />
          </Show>

          <Show when={decl.control === 'text'}>
            <TextField
              value={displayValue(eff(), decl)}
              onInput={(v) => void saveSetting(decl.key, v)}
            />
          </Show>

          <Show when={decl.control === 'number'}>
            <TextField
              type="number"
              value={displayValue(eff(), decl)}
              min={decl.min}
              max={decl.max}
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
                onClick={() => {
                  const value = prompt('Enter new value for "' + decl.label + '":')
                  if (value === null) return
                  void saveSecret(decl.key, value)
                }}
              >
                Replace
              </Button>
              <Button
                class="ui-settings-secret-clear"
                variant="danger"
                onClick={() => void deleteSecret(decl.key)}
              >
                Clear
              </Button>
            </div>
          </Show>

          <ProvenanceBadge decl={decl} />

          <Show when={err()}>
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
        leading={
          <div class="kit-scope">
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
                <Badge variant="warning">{'(' + modifiedCount() + ')'}</Badge>
              </Show>
            </div>

            <nav aria-label="Settings sections">
              <ul class="ui-settings-section-nav">
                <For each={settingsPages()}>
                  {(page) => {
                    const active = () =>
                      page.kind === 'component'
                        ? activeComponentPage() === page.id
                        : activeComponentPage() === null && sectionFilter() === page.title
                    const count = () =>
                      page.kind === 'generated' ? modifiedBySection().get(page.id) : undefined
                    return (
                      <li
                        classList={{
                          'ui-settings-section-nav-item': true,
                          'ui-settings-section-nav-active': active(),
                        }}
                        data-section={page.title}
                      >
                        <Button
                          class="ui-settings-section-nav-link"
                          onClick={() => handleNavClick(page)}
                        >
                          {page.title}
                          <Show when={count() !== undefined && count()! > 0}>
                            <Badge variant="warning">{String(count())}</Badge>
                          </Show>
                        </Button>
                      </li>
                    )
                  }}
                </For>
              </ul>
            </nav>
          </div>
        }
        scrollerRef={(h) => {
          scrollerHandle = h
        }}
      >
        <div class="kit-scope">
          {/* Component page takes over the body when active. */}
          <Show when={activeComponentPage() === 'connections'}>
            <ConnectionsView client={props.profileClient} onConnect={props.onConnect} />
          </Show>

          {/* Generated settings sections — hidden when a component page is active. */}
          <Show when={activeComponentPage() === null}>
            <Show when={loadState() === 'loading'}>
              <div class="ui-settings-status ui-settings-loading">Loading settings…</div>
            </Show>

            <Show when={loadState() === 'failed'}>
              <div class="ui-settings-status ui-settings-failed">
                <span>Failed to load settings.</span>
                <Button onClick={() => void refresh()}>Retry</Button>
              </div>
            </Show>

            <Show
              when={
                loadState() === 'ready' &&
                filteredDeclarations().length === 0 &&
                declarations().length > 0
              }
            >
              <div class="ui-settings-status ui-settings-nomatch">
                No settings match your search.
              </div>
            </Show>

            {/* Render all sections; hide non-matching rows via inline style. */}
            <Show when={loadState() === 'ready'}>
              <For each={sections()}>
                {(section) => {
                  const sectionDecls = () => declarations().filter((d) => d.section === section)
                  const sectionVisible = () => sectionDecls().some((d) => visibleKeys().has(d.key))
                  return (
                    <div classList={{ 'st-vis-hidden': !sectionVisible() }}>
                      <PageSection id={'st-section-' + encodeURIComponent(section)} title={section}>
                        <For each={sectionDecls()}>
                          {(decl) => (
                            <SettingRow decl={decl} visible={visibleKeys().has(decl.key)} />
                          )}
                        </For>
                      </PageSection>
                    </div>
                  )
                }}
              </For>
            </Show>

            {/* ExportSection as a child component (no mountExportSection). */}
            <Show when={loadState() === 'ready'}>
              <ExportSection profileClient={props.profileClient} />
            </Show>
          </Show>
        </div>
      </Page>
    </div>
  )
}
