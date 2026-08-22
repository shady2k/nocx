import { For, Show, createMemo, createSignal, onCleanup, onMount, untrack } from 'solid-js'
import type {
  SandboxAccessChanged,
  SandboxAccessEvent,
  SandboxAccessList,
  SandboxAccessResolve,
  SandboxAccessStatus,
} from './ipc'
import {
  Button,
  CollectionView,
  EmptyState,
  PageSection,
  RecordRow,
  Select,
  Stack,
  StatusCard,
} from './ui'
import type { StatusDotTone } from './ui/status-dot'

/**
 * The inbox is a LIST OF RECORDS, which the kit already owns: CollectionView
 * for the searchable shell and RecordRow for the row grammar (nocx-pp3y.3).
 * This surface used to render its own <article> card with its own border,
 * background and dt/dd body — a fourth dialect beside the three the composite
 * was built to end — and check-row-grammar could not see it, because its rule
 * matches -(item|row)__(name|meta) and these classes were named otherwise
 * (nocx-a6yc7).
 */

/** The state of one attempt, in the kit's dot-and-text vocabulary. */
function stateStatus(state: string): { tone: StatusDotTone; text: string } {
  if (state === 'pending') return { tone: 'error', text: 'Pending' }
  return { tone: 'neutral', text: state.charAt(0).toUpperCase() + state.slice(1) }
}

/** Everything the row says under its meta line, in reading order. */
function eventDetail(event: SandboxAccessEvent): string[] {
  const attempts = `${event.count} attempt${event.count === 1 ? '' : 's'}`
  const lines = [
    event.shell ?? 'Unknown shell',
    `Last seen ${new Date(event.lastSeen).toLocaleString()} · ${attempts}`,
  ]
  if (event.directory !== '') lines.push(`Global rule directory: ${event.directory}`)
  if (event.access === 'readWrite' && event.state === 'pending') {
    lines.push('A read-only rule will not satisfy this write attempt.')
  }
  if (!event.canGrant && event.grantReason) lines.push(event.grantReason)
  return lines
}

export interface SandboxAccessClient {
  sandboxAccessStatus(): Promise<SandboxAccessStatus | null>
  sandboxAccessList(limit?: number): Promise<SandboxAccessList>
  sandboxAccessResolve(
    eventId: string,
    decision: 'dismiss' | 'globalReadOnly' | 'globalReadWrite',
  ): Promise<SandboxAccessResolve>
  onSandboxAccessChanged(callback: (change: SandboxAccessChanged) => void): () => void
}

export interface SandboxAccessSettingsProps {
  client?: SandboxAccessClient
}

type LoadState = 'loading' | 'ready' | 'failed'

const ALL_APPLICATIONS = 'all'

function applicationKey(event: SandboxAccessEvent): string {
  return JSON.stringify(event.executable ?? null)
}

function applicationOptions(events: SandboxAccessEvent[], selected: string) {
  const options = new Map<string, string>()
  for (const event of events) {
    options.set(applicationKey(event), event.executable ?? 'Unknown program')
  }
  if (selected !== ALL_APPLICATIONS && !options.has(selected)) {
    try {
      const executable: unknown = JSON.parse(selected)
      options.set(selected, typeof executable === 'string' ? executable : 'Unknown program')
    } catch {
      options.set(selected, 'Unknown program')
    }
  }
  return [...options]
    .map(([value, label]) => ({ value, label }))
    .sort((a, b) => {
      if (a.label !== b.label) return a.label < b.label ? -1 : 1
      return a.value < b.value ? -1 : a.value > b.value ? 1 : 0
    })
}

function matchesKeywords(event: SandboxAccessEvent, query: string): boolean {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (terms.length === 0) return true
  const access = event.access === 'readWrite' ? 'Read / write' : 'Read only'
  const haystack = [
    event.path,
    event.directory,
    event.executable,
    event.shell,
    event.operation,
    event.source,
    event.state,
    access,
  ]
    .filter((value): value is string => typeof value === 'string')
    .join('\n')
    .toLowerCase()
  return terms.every((term) => haystack.includes(term))
}

export function SandboxAccessSettings(props: SandboxAccessSettingsProps) {
  const [state, setState] = createSignal<LoadState>('loading')
  const [status, setStatus] = createSignal<SandboxAccessStatus | null>(null)
  const [page, setPage] = createSignal<SandboxAccessList>({ events: [], revision: 0, lost: 0 })
  const [resolving, setResolving] = createSignal<string | null>(null)
  const [actionError, setActionError] = createSignal('')
  const [applicationFilter, setApplicationFilter] = createSignal(ALL_APPLICATIONS)
  const [keywordFilter, setKeywordFilter] = createSignal('')
  const applications = createMemo(() => applicationOptions(page().events, applicationFilter()))
  const visibleEvents = createMemo(() =>
    page().events.filter(
      (event) =>
        (applicationFilter() === ALL_APPLICATIONS ||
          applicationKey(event) === applicationFilter()) &&
        matchesKeywords(event, keywordFilter()),
    ),
  )

  const clearFilters = () => {
    setApplicationFilter(ALL_APPLICATIONS)
    setKeywordFilter('')
  }

  const load = async (): Promise<void> => {
    if (!props.client) {
      setStatus(null)
      setPage({ events: [], revision: 0, lost: 0 })
      setState('ready')
      return
    }
    try {
      const [nextStatus, nextPage] = await Promise.all([
        props.client.sandboxAccessStatus(),
        props.client.sandboxAccessList(200),
      ])
      setStatus(nextStatus)
      setPage(nextPage)
      setState('ready')
    } catch {
      setState('failed')
    }
  }

  onMount(() => {
    void load()
    const client = props.client
    const unsubscribe = client?.onSandboxAccessChanged(() => void untrack(load))
    onCleanup(() => unsubscribe?.())
  })

  const resolve = async (
    event: SandboxAccessEvent,
    decision: 'dismiss' | 'globalReadOnly' | 'globalReadWrite',
  ): Promise<void> => {
    if (!props.client || event.state !== 'pending') return
    setResolving(event.id)
    setActionError('')
    try {
      await props.client.sandboxAccessResolve(event.id, decision)
      await load()
    } catch {
      setActionError('The event changed or the global rule was rejected. Reload and try again.')
    } finally {
      setResolving(null)
    }
  }

  return (
    <div class="sandbox-access-settings">
      <PageSection
        title="Sandbox access"
        description="Denied filesystem attempts from sandboxed shells. This diagnostic inbox is bounded and kept only in memory; new global rules apply to future sandboxed tabs."
      >
        <Show when={state() === 'loading'}>
          <EmptyState title="Loading sandbox access" />
        </Show>
        <Show when={state() === 'failed'}>
          <EmptyState
            title="Couldn't load sandbox access"
            description="The backend did not return the denied-access inbox."
            action={<Button onClick={() => void load()}>Retry</Button>}
          />
        </Show>
        <Show when={state() === 'ready'}>
          <Show
            when={status() !== null}
            fallback={
              <EmptyState
                title="Denied-access monitoring unavailable"
                description="This window has no sandbox access observer. Sandbox enforcement status is reported separately."
              />
            }
          >
            <Stack>
              <StatusCard
                tone={status()!.available ? 'ok' : 'warning'}
                title={status()!.available ? 'Monitoring active' : 'Monitoring unavailable'}
                description={
                  status()!.detail ?? status()!.reason ?? 'Best-effort diagnostic observer.'
                }
              />
              <Show when={Math.max(status()!.lost, page().lost) > 0}>
                <StatusCard
                  tone="warning"
                  title={`${Math.max(status()!.lost, page().lost)} events were dropped`}
                  description="The bounded inbox or the observer could not keep up."
                />
              </Show>
              <Show when={actionError() !== ''}>
                <StatusCard tone="danger" title={actionError()} />
              </Show>
              <Show
                when={page().events.length > 0}
                fallback={
                  <EmptyState
                    title="No denied access attempts"
                    description="Attempts appear here only while nocx is running and the platform observer is available."
                  />
                }
              >
                <CollectionView
                  searchValue={keywordFilter()}
                  onSearch={setKeywordFilter}
                  searchPlaceholder="Filter by keywords"
                  searchLabel="Filter by keywords"
                  hasItems={visibleEvents().length > 0}
                  actions={
                    <Select
                      value={applicationFilter()}
                      onChange={setApplicationFilter}
                      options={applications()}
                      placeholder="All applications"
                      placeholderValue={ALL_APPLICATIONS}
                      ariaLabel="Filter by application"
                    />
                  }
                  empty={
                    <EmptyState
                      title="No access attempts match these filters"
                      description="Change or clear the application and keyword filters."
                      action={<Button onClick={clearFilters}>Clear filters</Button>}
                    />
                  }
                >
                  <For each={visibleEvents()}>
                    {(event) => (
                      <RecordRow
                        title={event.path}
                        kind={{
                          label: event.access === 'readWrite' ? 'Read / write' : 'Read only',
                          tone: event.access === 'readWrite' ? 'warning' : 'neutral',
                        }}
                        meta={event.executable ?? 'Unknown program'}
                        status={stateStatus(event.state)}
                        detail={eventDetail(event)}
                        actions={
                          <>
                            <Button
                              disabled={
                                event.state !== 'pending' ||
                                !event.canGrant ||
                                resolving() === event.id
                              }
                              onClick={() => void resolve(event, 'globalReadOnly')}
                            >
                              Add global read-only
                            </Button>
                            <Button
                              disabled={
                                event.state !== 'pending' ||
                                !event.canGrant ||
                                resolving() === event.id
                              }
                              onClick={() => void resolve(event, 'globalReadWrite')}
                            >
                              Add global read-write
                            </Button>
                            <Button
                              disabled={event.state !== 'pending' || resolving() === event.id}
                              onClick={() => void resolve(event, 'dismiss')}
                            >
                              Dismiss
                            </Button>
                          </>
                        }
                      />
                    )}
                  </For>
                </CollectionView>
              </Show>
            </Stack>
          </Show>
        </Show>
      </PageSection>
    </div>
  )
}
