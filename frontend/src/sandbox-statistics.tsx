/**
 * SandboxStatistics — Solid view with four independent sections:
 * enforcement/observer status, source-tab grant, workspace profile editor,
 * and denied inbox.
 *
 * Design 2026-08-23 §4–7: the surface is a singleton, non-restorable
 * pane bound to the terminal from which it was opened.
 */
import { Show, createMemo, createEffect, createSignal, onCleanup, onMount, untrack } from 'solid-js'
import type {
  SandboxAccessChanged,
  SandboxAccessList,
  SandboxAccessResolve,
  SandboxAccessStatus,
  SandboxGrantGet,
  SandboxProfileGet,
  SandboxProfileSet,
  SandboxProfileDelete,
  SandboxStatus,
} from './ipc'
import { Button, EditableRowList, EmptyState, PageSection, Stack, StatusCard } from './ui'
import { SandboxDeniedAccessSection, type SandboxAccessClient } from './sandbox-denied-access'
import { classConflict } from './sandbox-path-classes'

// ── Client interface ──────────────────────────────────────────────────

export interface SandboxStatisticsClient {
  sandboxStatus(): Promise<SandboxStatus | null>
  sandboxAccessStatus(): Promise<SandboxAccessStatus | null>
  sandboxAccessList(limit?: number): Promise<SandboxAccessList>
  sandboxAccessResolve(
    eventId: string,
    decision: 'dismiss' | 'workspaceReadOnly' | 'workspaceReadWrite',
  ): Promise<SandboxAccessResolve>
  onSandboxAccessChanged(callback: (change: SandboxAccessChanged) => void): () => void
  sandboxProfileGet(paneId: string): Promise<SandboxProfileGet>
  sandboxProfileSet(
    workspaceId: string,
    expectedRevision: number,
    writablePaths: string[],
    readOnlyPaths: string[],
  ): Promise<SandboxProfileSet>
  sandboxProfileDelete(workspaceId: string, expectedRevision: number): Promise<SandboxProfileDelete>
  sandboxGrantGet(paneId: string): Promise<SandboxGrantGet>
}

// ── External deps ─────────────────────────────────────────────────────

export interface SandboxStatisticsDeps {
  /** The pane id whose sandbox state is observed. */
  activePaneId(): string | null
  /** Relay a relaunch when the grant revision has changed. */
  relaunch(): void
  /** Native directory picker for profile path additions. */
  openDirectory(): Promise<{ path: string }>
}

// ── Props ─────────────────────────────────────────────────────────────

export interface SandboxStatisticsProps {
  client: SandboxStatisticsClient
  deps: SandboxStatisticsDeps
}

// ── Load state ────────────────────────────────────────────────────────

type LoadState = 'loading' | 'ready' | 'failed'

// ── Grant provenance label ────────────────────────────────────────────

function grantSourceLabel(source: string): string {
  switch (source) {
    case 'standard':
      return 'Standard profile'
    case 'workspace':
      return 'Workspace profile'
    case 'legacy':
      return 'Legacy (pre-grant)'
    default:
      return source
  }
}

// ── Component ─────────────────────────────────────────────────────────

export function SandboxStatistics(props: SandboxStatisticsProps) {
  // ── Enforcement status ──────────────────────────────────────────────
  const [enforcementLoad, setEnforcementLoad] = createSignal<LoadState>('loading')
  const [enforcementStatus, setEnforcementStatus] = createSignal<SandboxStatus | null>(null)
  const [observerStatus, setObserverStatus] = createSignal<SandboxAccessStatus | null>(null)

  const loadEnforcement = async (): Promise<void> => {
    try {
      const [enf, obs] = await Promise.all([
        props.client.sandboxStatus(),
        props.client.sandboxAccessStatus(),
      ])
      setEnforcementStatus(enf)
      setObserverStatus(obs)
      setEnforcementLoad('ready')
    } catch {
      setEnforcementLoad('failed')
    }
  }

  // ── Source tab grant ────────────────────────────────────────────────
  const [grantLoad, setGrantLoad] = createSignal<LoadState>('loading')
  const [grant, setGrant] = createSignal<SandboxGrantGet | null>(null)

  const loadGrant = async (paneId = props.deps.activePaneId()): Promise<void> => {
    if (!paneId) {
      setGrant(null)
      setGrantLoad('ready')
      return
    }
    try {
      const g = await props.client.sandboxGrantGet(paneId)
      setGrant(g)
      setGrantLoad('ready')
    } catch {
      setGrantLoad('failed')
    }
  }

  // ── Workspace profile ───────────────────────────────────────────────
  const [profileLoad, setProfileLoad] = createSignal<LoadState>('loading')
  const [profile, setProfile] = createSignal<SandboxProfileGet | null>(null)
  const [profileError, setProfileError] = createSignal('')
  const [writablePaths, setWritablePaths] = createSignal<string[]>([])
  const [readOnlyPaths, setReadOnlyPaths] = createSignal<string[]>([])
  const [pathErrors, setPathErrors] = createSignal<Record<string, string>>({})
  const [profileSaving, setProfileSaving] = createSignal(false)
  const [profileDirty, setProfileDirty] = createSignal(false)

  const applyProfile = (p: SandboxProfileGet): void => {
    setProfile(p)
    setWritablePaths([...p.writablePaths])
    setReadOnlyPaths([...p.readOnlyPaths])
    setPathErrors({})
    setProfileError('')
    setProfileDirty(false)
  }

  const loadProfile = async (paneId = props.deps.activePaneId()): Promise<void> => {
    if (!paneId) {
      setProfile(null)
      setProfileLoad('ready')
      return
    }
    try {
      const p = await props.client.sandboxProfileGet(paneId)
      applyProfile(p)
      setProfileLoad('ready')
    } catch (err) {
      setProfileError(
        err instanceof Error ? err.message : 'The backend did not return profile information.',
      )
      setProfileLoad('failed')
    }
  }

  const saveProfile = async (): Promise<void> => {
    const p = profile()
    if (!p || profileSaving()) return
    setProfileSaving(true)
    setProfileError('')
    try {
      const result = await props.client.sandboxProfileSet(
        p.workspaceId,
        p.inherited ? 0 : p.revision,
        writablePaths(),
        readOnlyPaths(),
      )
      setProfile({
        ...p,
        source: 'workspace' as const,
        inherited: false,
        revision: result.revision,
        writablePaths: result.writablePaths,
        readOnlyPaths: result.readOnlyPaths,
      })
      setWritablePaths([...result.writablePaths])
      setReadOnlyPaths([...result.readOnlyPaths])
      setProfileDirty(false)
    } catch (err) {
      setProfileError(
        err instanceof Error
          ? err.message
          : 'Could not save the profile. The revision may have changed — reload and try again.',
      )
    } finally {
      setProfileSaving(false)
    }
  }

  const deleteProfile = async (): Promise<void> => {
    const p = profile()
    if (!p || profileSaving()) return
    setProfileSaving(true)
    setProfileError('')
    try {
      await props.client.sandboxProfileDelete(p.workspaceId, p.revision)
      const reloaded = await props.client.sandboxProfileGet(props.deps.activePaneId() ?? '')
      applyProfile(reloaded)
    } catch (err) {
      setProfileError(
        err instanceof Error ? err.message : 'Could not delete the profile. Reload and try again.',
      )
    } finally {
      setProfileSaving(false)
    }
  }

  const addWritablePath = async (): Promise<void> => {
    try {
      const picked = await props.deps.openDirectory()
      if (!picked.path) return
      const err = classConflict('readWrite', picked.path, readOnlyPaths(), writablePaths())
      if (err) {
        setPathErrors({ ...pathErrors(), ['writable:' + picked.path]: err })
        return
      }
      setWritablePaths([...writablePaths(), picked.path])
      setProfileDirty(true)
    } catch {
      // User cancelled or picker failed — no-op.
    }
  }

  const addReadOnlyPath = async (): Promise<void> => {
    try {
      const picked = await props.deps.openDirectory()
      if (!picked.path) return
      const err = classConflict('readOnly', picked.path, readOnlyPaths(), writablePaths())
      if (err) {
        setPathErrors({ ...pathErrors(), ['readonly:' + picked.path]: err })
        return
      }
      setReadOnlyPaths([...readOnlyPaths(), picked.path])
      setProfileDirty(true)
    } catch {
      // User cancelled or picker failed — no-op.
    }
  }

  const removeWritablePath = (index: number): void => {
    const next = [...writablePaths()]
    next.splice(index, 1)
    setWritablePaths(next)
    setProfileDirty(true)
  }

  const removeReadOnlyPath = (index: number): void => {
    const next = [...readOnlyPaths()]
    next.splice(index, 1)
    setReadOnlyPaths(next)
    setProfileDirty(true)
  }

  // ── Relaunch detection ──────────────────────────────────────────────
  const staleGrant = createMemo(() => {
    const g = grant()
    const p = profile()
    if (!g || !p) return false
    if (g.provenance.profileSource === 'legacy') return false
    if (g.provenance.profileSource !== p.source) return true
    return g.provenance.profileRevision !== null && g.provenance.profileRevision !== p.revision
  })

  createEffect(() => {
    const paneId = props.deps.activePaneId()
    setGrantLoad('loading')
    setProfileLoad('loading')
    void Promise.all([loadGrant(paneId), loadProfile(paneId)])
  })

  onMount(() => {
    void loadEnforcement()
    const unsubscribe = props.client.onSandboxAccessChanged(() => {
      void untrack(loadProfile)
    })
    onCleanup(unsubscribe)
  })

  // ── Denied access client adapter ────────────────────────────────────
  const accessClient: SandboxAccessClient = {
    sandboxAccessStatus: () => props.client.sandboxAccessStatus(),
    sandboxAccessList: (limit?) => props.client.sandboxAccessList(limit),
    sandboxAccessResolve: (eventId, decision) =>
      props.client.sandboxAccessResolve(eventId, decision),
    onSandboxAccessChanged: (callback) => props.client.onSandboxAccessChanged(callback),
  }

  // ── Render ──────────────────────────────────────────────────────────
  return (
    <div class="sandbox-statistics">
      <Stack gap="loose">
        {/* ── Section 1: Enforcement / Observer status ── */}
        <PageSection
          title="Enforcement status"
          description="Sandbox enforcement and denied-access observer availability."
        >
          <Show when={enforcementLoad() === 'loading'}>
            <EmptyState title="Loading enforcement status" />
          </Show>
          <Show when={enforcementLoad() === 'failed'}>
            <EmptyState
              title="Couldn't load enforcement status"
              description="The backend did not return sandbox status."
              action={<Button onClick={() => void loadEnforcement()}>Retry</Button>}
            />
          </Show>
          <Show when={enforcementLoad() === 'ready'}>
            <Stack>
              <Show
                when={enforcementStatus() !== null}
                fallback={
                  <StatusCard
                    tone="neutral"
                    title="Enforcement status unavailable"
                    description="Sandbox enforcement is not available in this window."
                  />
                }
              >
                <StatusCard
                  tone={enforcementStatus()!.available ? 'ok' : 'warning'}
                  title={
                    enforcementStatus()!.available
                      ? `Sandbox enforcement active (${enforcementStatus()!.backend})`
                      : `Sandbox enforcement unavailable (${enforcementStatus()!.backend})`
                  }
                  description={
                    enforcementStatus()!.detail ??
                    enforcementStatus()!.reason ??
                    (enforcementStatus()!.available
                      ? 'The platform enforces filesystem restrictions for sandboxed shells.'
                      : 'Sandbox enforcement is not available on this system.')
                  }
                />
              </Show>
              <StatusCard
                tone={
                  observerStatus() === null
                    ? 'neutral'
                    : observerStatus()!.available
                      ? 'ok'
                      : 'warning'
                }
                title={
                  observerStatus() === null
                    ? 'Observer status unavailable'
                    : observerStatus()!.available
                      ? 'Denied-access observer active'
                      : 'Denied-access observer unavailable'
                }
                description={
                  observerStatus()?.detail ??
                  observerStatus()?.reason ??
                  (observerStatus() === null
                    ? 'The denied-access observer is not available in this window.'
                    : observerStatus()!.available
                      ? 'Best-effort diagnostic observer is running.'
                      : 'Denied-access observation is not available.')
                }
              />
              <Show when={(observerStatus()?.lost ?? 0) > 0}>
                <StatusCard
                  tone="warning"
                  title={`${observerStatus()!.lost} events were dropped`}
                  description="The observer could not keep up with the event rate."
                />
              </Show>
            </Stack>
          </Show>
        </PageSection>

        {/* ── Section 2: Source tab grant ── */}
        <PageSection
          title="Source tab grant"
          description="The immutable grant minted for the terminal that opened this surface, if any."
        >
          <Show when={grantLoad() === 'loading'}>
            <EmptyState title="Loading grant" />
          </Show>
          <Show when={grantLoad() === 'failed'}>
            <EmptyState
              title="Couldn't load grant"
              description="The backend did not return grant information."
              action={<Button onClick={() => void loadGrant()}>Retry</Button>}
            />
          </Show>
          <Show when={grantLoad() === 'ready'}>
            <Show
              when={grant() !== null}
              fallback={
                <EmptyState
                  title="No sandbox grant"
                  description="The source terminal is not sandboxed. Convert it to see its grant here."
                />
              }
            >
              <Stack>
                <StatusCard
                  tone="ok"
                  title={`Workspace: ${grant()!.realized.workspace}`}
                  description={`Issued ${new Date(grant()!.issuedAt).toLocaleString()} · Backend ${grant()!.realized.backend}`}
                />
                <Show when={grant()!.realized.writableRoots.length > 0}>
                  <StatusCard
                    tone="neutral"
                    title="Writable roots"
                    description={grant()!.realized.writableRoots.join(', ')}
                  />
                </Show>
                <Show when={grant()!.realized.readOnlyRoots.length > 0}>
                  <StatusCard
                    tone="neutral"
                    title="Read-only roots"
                    description={grant()!.realized.readOnlyRoots.join(', ')}
                  />
                </Show>
                <Show when={grant()!.realized.homeProjections.length > 0}>
                  <StatusCard
                    tone="neutral"
                    title="Home projections"
                    description={grant()!
                      .realized.homeProjections.map((h) => `${h.relativePath} → ${h.hostPath}`)
                      .join(', ')}
                  />
                </Show>
                <StatusCard
                  tone="neutral"
                  title="Provenance"
                  description={`${grantSourceLabel(grant()!.provenance.profileSource)}${grant()!.provenance.profileRevision !== null ? ` (revision ${grant()!.provenance.profileRevision})` : ''}`}
                />
                <Show when={staleGrant()}>
                  <StatusCard
                    tone="warning"
                    title="Grant is stale"
                    description="The workspace profile has been updated since this grant was minted. The running tab still uses the old policy."
                    action={
                      <Button variant="primary" onClick={() => props.deps.relaunch()}>
                        Relaunch with updated profile
                      </Button>
                    }
                  />
                </Show>
              </Stack>
            </Show>
          </Show>
        </PageSection>

        {/* ── Section 3: Workspace profile ── */}
        <PageSection
          title="Source workspace profile"
          description="The effective sandbox profile for the source terminal's workspace."
        >
          <Show when={profileLoad() === 'loading'}>
            <EmptyState title="Loading profile" />
          </Show>
          <Show when={profileLoad() === 'failed'}>
            <EmptyState
              title="Couldn't load profile"
              description={profileError() || 'The backend did not return profile information.'}
              action={<Button onClick={() => void loadProfile()}>Retry</Button>}
            />
          </Show>
          <Show when={profileLoad() === 'ready'}>
            <Show
              when={profile() !== null}
              fallback={
                <EmptyState
                  title="No source terminal"
                  description="Open Sandbox statistics from a terminal to see its profile."
                />
              }
            >
              <Stack>
                <StatusCard
                  tone={profile()!.inherited ? 'neutral' : 'ok'}
                  title={
                    profile()!.inherited
                      ? 'Inheriting standard profile'
                      : `Explicit workspace profile for ${profile()!.workspaceId}`
                  }
                  description={
                    profile()!.inherited
                      ? 'This workspace uses the standard sandbox profile. Edit the paths below to create an explicit workspace profile.'
                      : 'This workspace has an explicit profile. Delete it to return to the standard profile.'
                  }
                />

                <Show
                  when={profile()!.workspaceId !== 'workspace:default'}
                  fallback={
                    <StatusCard
                      tone="neutral"
                      title="Standard profile"
                      description="The default workspace uses the standard sandbox profile. Edit it in Settings; changes affect future grants only."
                    />
                  }
                >
                  {/* Writable paths */}
                  <div class="sandbox-statistics-path-list">
                    <EditableRowList
                      rows={writablePaths()}
                      ariaLabel="Writable paths"
                      addLabel="Add writable folder"
                      emptyLabel="No writable folders beyond the workspace."
                      removeLabel={(index) => `Remove writable folder ${index + 1}`}
                      onRemove={(index) => removeWritablePath(index)}
                      onAdd={() => void addWritablePath()}
                      renderRow={(path) => <span class="sandbox-statistics-path">{path()}</span>}
                    />
                  </div>

                  {/* Read-only paths */}
                  <div class="sandbox-statistics-path-list">
                    <EditableRowList
                      rows={readOnlyPaths()}
                      ariaLabel="Read-only paths"
                      addLabel="Add read-only folder"
                      emptyLabel="No read-only folders."
                      removeLabel={(index) => `Remove read-only folder ${index + 1}`}
                      onRemove={(index) => removeReadOnlyPath(index)}
                      onAdd={() => void addReadOnlyPath()}
                      renderRow={(path) => <span class="sandbox-statistics-path">{path()}</span>}
                    />
                  </div>

                  <Show when={profileError() !== ''}>
                    <StatusCard tone="danger" title={profileError()} />
                  </Show>

                  <Show when={profileDirty()}>
                    <div class="sandbox-statistics-profile-actions">
                      <Button
                        variant="primary"
                        disabled={profileSaving()}
                        onClick={() => void saveProfile()}
                      >
                        Save profile
                      </Button>
                      <Button
                        disabled={profileSaving()}
                        onClick={() => {
                          const p = profile()
                          if (p) applyProfile(p)
                        }}
                      >
                        Discard changes
                      </Button>
                    </div>
                  </Show>

                  <Show when={!profile()!.inherited}>
                    <div class="sandbox-statistics-profile-actions">
                      <Button
                        variant="danger"
                        disabled={profileSaving()}
                        onClick={() => void deleteProfile()}
                      >
                        Delete profile (inherit standard)
                      </Button>
                    </div>
                  </Show>
                </Show>
              </Stack>
            </Show>
          </Show>
        </PageSection>

        {/* ── Section 4: Denied inbox ── */}
        <SandboxDeniedAccessSection client={accessClient} />
      </Stack>
    </div>
  )
}
