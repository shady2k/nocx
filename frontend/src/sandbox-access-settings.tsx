import { For, Show, createMemo, createSignal, onCleanup, onMount, untrack } from 'solid-js'
import type {
  SandboxAccessChanged,
  SandboxAccessEvent,
  SandboxAccessList,
  SandboxAccessResolve,
  SandboxAccessStatus,
} from './ipc'
import { Badge, Button, EmptyState, PageSection, SearchField, Select, Stack } from './ui'

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
              <div class="sandbox-access-status" role="status">
                <Badge tone={status()!.available ? 'success' : 'warning'}>
                  {status()!.available ? 'Monitoring active' : 'Monitoring unavailable'}
                </Badge>
                <span>
                  {status()!.detail ?? status()!.reason ?? 'Best-effort diagnostic observer.'}
                </span>
              </div>
              <Show when={Math.max(status()!.lost, page().lost) > 0}>
                <div class="sandbox-access-loss" role="status">
                  {Math.max(status()!.lost, page().lost)} events were dropped because the bounded
                  inbox or observer could not keep up.
                </div>
              </Show>
              <Show when={actionError() !== ''}>
                <div class="sandbox-access-error" role="alert">
                  {actionError()}
                </div>
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
                <div
                  class="sandbox-access-filters"
                  role="search"
                  aria-label="Filter sandbox access"
                >
                  <Select
                    value={applicationFilter()}
                    onChange={setApplicationFilter}
                    options={applications()}
                    placeholder="All applications"
                    placeholderValue={ALL_APPLICATIONS}
                    ariaLabel="Filter by application"
                  />
                  <SearchField
                    value={keywordFilter()}
                    onInput={setKeywordFilter}
                    placeholder="Filter by keywords"
                    ariaLabel="Filter by keywords"
                  />
                </div>
                <Show
                  when={visibleEvents().length > 0}
                  fallback={
                    <EmptyState
                      title="No access attempts match these filters"
                      description="Change or clear the application and keyword filters."
                      action={<Button onClick={clearFilters}>Clear filters</Button>}
                    />
                  }
                >
                  <div class="sandbox-access-list">
                    <For each={visibleEvents()}>
                      {(event) => (
                        <article class="sandbox-access-event">
                          <div class="sandbox-access-event-heading">
                            <code>{event.path}</code>
                            <Badge tone={event.state === 'pending' ? 'warning' : 'neutral'}>
                              {event.state}
                            </Badge>
                          </div>
                          <dl class="sandbox-access-attribution">
                            <div>
                              <dt>Observed program (untrusted)</dt>
                              <dd>{event.executable ?? 'Unknown program'}</dd>
                            </div>
                            <div>
                              <dt>Shell</dt>
                              <dd>{event.shell ?? 'Unknown shell'}</dd>
                            </div>
                            <div>
                              <dt>Access</dt>
                              <dd>{event.access === 'readWrite' ? 'Read / write' : 'Read only'}</dd>
                            </div>
                            <div>
                              <dt>Last seen</dt>
                              <dd>
                                {new Date(event.lastSeen).toLocaleString()} · {event.count} attempt
                                {event.count === 1 ? '' : 's'}
                              </dd>
                            </div>
                          </dl>
                          <Show when={event.directory !== ''}>
                            <div class="sandbox-access-grant-root">
                              Global rule directory: <code>{event.directory}</code>
                            </div>
                          </Show>
                          <Show when={event.access === 'readWrite' && event.state === 'pending'}>
                            <div class="sandbox-access-caution">
                              A read-only rule will not satisfy this write attempt.
                            </div>
                          </Show>
                          <Show when={!event.canGrant && event.grantReason}>
                            <div class="sandbox-access-caution">{event.grantReason}</div>
                          </Show>
                          <div class="sandbox-access-actions">
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
                          </div>
                        </article>
                      )}
                    </For>
                  </div>
                </Show>
              </Show>
            </Stack>
          </Show>
        </Show>
      </PageSection>
    </div>
  )
}
