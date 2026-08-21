// The API workbench, as one pane (design §9.1, §9.2): the collection tree,
// the request form, and the list of runs — left to right and top to bottom.
//
// ONE workbench, not a pane per request. Requests are switched between
// constantly, so a tab each would make the strip worse than a list is; the
// tree lives HERE and is not duplicated into a sidebar view, because two
// trees would be two owners of one selection.
//
// The environment is deliberately a statement rather than a control. Design
// §6.5 has an environment answer both "where" and "how to get there", and the
// api.* contract carries no environment method yet — `internal/transport`'s
// send handler says so in as many words: "the route is the direct one … until
// then every send goes out from this machine". A dropdown over nothing would
// be a control that governs nothing, which is how a feature that does not
// exist survives a release. The surface says what is true instead.

import { For, Show, createSignal } from 'solid-js'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Caption } from '../ui/caption'
import { EmptyState } from '../ui/empty-state'
import { IconButton } from '../ui/icon-button'
import { CloseIcon, RefreshIcon } from '../ui/icons'
import { MarkerList } from '../ui/marker-list'
import { Section } from '../ui/section'
import { Stack } from '../ui/stack'
import { StatusCard } from '../ui/status-card'
import { TextField } from '../ui/text-field'
import { TreeRow } from '../ui/tree-row'
import { showToast } from '../ui/toast'
import { flattenCollections, type ApiTreeRow } from './api-tree'
import { RequestForm } from './request-form'
import { RunList } from './run-list'
import type { ApiStore } from './api-store'

export interface ApiPaneProps {
  store: ApiStore
}

export function ApiPane(props: ApiPaneProps) {
  // The store is constructed once by ApiContent and handed in; it is a
  // dependency, not a reactive value, and nothing ever swaps one workbench's
  // store for another's. Reading it here rather than at every call site keeps
  // the surface readable — the reactivity that matters is inside the store's
  // own signals, which ARE read in tracked scopes below.
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const store = props.store
  const [collapsed, setCollapsed] = createSignal<ReadonlySet<string>>(new Set())
  const [folderPath, setFolderPath] = createSignal('')
  const [curlLine, setCurlLine] = createSignal('')
  const [postmanFile, setPostmanFile] = createSignal('')
  const [postmanDest, setPostmanDest] = createSignal('')

  const rows = (): ApiTreeRow[] => flattenCollections(store.collections(), collapsed())

  const toggle = (key: string): void => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const activate = (row: ApiTreeRow): void => {
    if (row.kind === 'request') {
      void store.openRequest(row.handle, row.relPath)
      return
    }
    // A malformed file has nothing to open — the row's own text is the whole
    // answer — and a collection or directory row toggles, the way every file
    // tree in the product does (the disclosure is a 16px target and the row
    // is the whole width).
    if (row.expandable) toggle(row.key)
  }

  const openFolder = (): void => {
    const path = folderPath().trim()
    if (path === '') return
    void store.openFolder(path).then(() => setFolderPath(''))
  }

  const importCurl = (): void => {
    const line = curlLine().trim()
    if (line === '') return
    void store.importCurl(line)
  }

  const importPostman = (): void => {
    const file = postmanFile().trim()
    const dest = postmanDest().trim()
    if (file === '' || dest === '') return
    void store.importPostman(file, dest).then(() => {
      if (store.error() === '') {
        showToast({ level: 'success', message: `Imported into ${dest}` })
      }
    })
  }

  return (
    <div class="api-workbench">
      <aside class="api-workbench__tree">
        <Stack gap="loose">
          <Section title="Collections">
            <TextField
              id="api-collection-path"
              label="Collection folder"
              description="The folder you place. It is safe to commit: no secret value is ever written into it."
              placeholder="/work/acme-api"
              value={folderPath()}
              onInput={setFolderPath}
            />
            <Button variant="primary" disabled={folderPath().trim() === ''} onClick={openFolder}>
              Open folder
            </Button>
            <Show when={store.error() !== ''}>
              <StatusCard
                tone="danger"
                title="That did not work"
                description={store.error()}
                action={
                  <Button onClick={() => void store.refresh()}>Re-read the open folders</Button>
                }
              />
            </Show>
          </Section>

          <div class="api-tree" role="tree" aria-label="Collections">
            <Show
              when={rows().length > 0}
              fallback={
                <EmptyState
                  title="No collections open"
                  description="Open a folder above, or import a Postman export, and its requests appear here."
                />
              }
            >
              <For each={rows()}>
                {(row) => (
                  <div
                    class="api-tree__row"
                    data-rel-path={row.kind === 'request' ? row.relPath : undefined}
                    data-row-key={row.key}
                    onClick={() => activate(row)}
                  >
                    <TreeRow
                      name={row.name}
                      depth={row.depth}
                      kind={row.kind === 'request' ? 'regular' : rowKind(row)}
                      disabled={row.kind === 'malformed'}
                      expanded={row.expanded}
                      onToggle={() => toggle(row.key)}
                      badge={
                        <Show when={row.method !== ''}>
                          <Badge tone="neutral">{row.method}</Badge>
                        </Show>
                      }
                    />
                    <Show when={row.reason !== ''}>
                      <p class="api-tree__reason">{row.reason}</p>
                    </Show>
                  </div>
                )}
              </For>
            </Show>
          </div>

          <For each={store.collections()}>
            {(open) => (
              <div class="api-tree__folder">
                <Caption>{open.path}</Caption>
                <Show when={open.error !== ''}>
                  <p class="api-tree__reason">{open.error}</p>
                </Show>
                <IconButton
                  size="sm"
                  title={`Close ${open.path}`}
                  ariaLabel={`Close ${open.path}`}
                  onClick={() => void store.closeFolder(open.handle)}
                >
                  <CloseIcon />
                </IconButton>
              </div>
            )}
          </For>

          <Section title="Environment">
            <p class="api-workbench__note">
              An environment answers where a request goes and how to reach it, and the api.* control
              plane does not carry one yet. Until it does, every send goes out from this machine,
              direct — there is no route to choose and nothing here pretends otherwise.
            </p>
          </Section>

          <Section title="Import" collapsible open={false} onToggle={() => undefined}>
            <TextField
              id="api-import-curl"
              label="curl command line"
              description="Parsed, never executed — there is no shell behind this field."
              multiline
              value={curlLine()}
              onInput={setCurlLine}
            />
            <Button disabled={curlLine().trim() === ''} onClick={importCurl}>
              Convert to a request
            </Button>
            <TextField
              id="api-import-postman-file"
              label="Postman v2.1 export"
              placeholder="/work/acme.postman_collection.json"
              value={postmanFile()}
              onInput={setPostmanFile}
            />
            <TextField
              id="api-import-postman-dest"
              label="New collection folder"
              placeholder="/work/acme-api"
              value={postmanDest()}
              onInput={setPostmanDest}
            />
            <Button
              disabled={postmanFile().trim() === '' || postmanDest().trim() === ''}
              onClick={importPostman}
            >
              Import Postman export
            </Button>
            <Show when={store.notes().length > 0}>
              <MarkerList
                items={store.notes().map((n) => ({
                  tone: 'excluded' as const,
                  text: `${n.what} — ${n.why}`,
                }))}
              />
            </Show>
          </Section>

          <IconButton
            size="sm"
            title="Re-read the open folders"
            ariaLabel="Re-read the open folders"
            onClick={() => void store.refresh()}
          >
            <RefreshIcon />
          </IconButton>
        </Stack>
      </aside>

      <section class="api-workbench__request" aria-label="Request">
        <RequestForm
          request={store.draft()}
          dirty={store.dirty()}
          sendable={store.selected() !== null}
          sending={store.sending()}
          onEdit={(next) => store.editDraft(next)}
          onSend={() => void store.send()}
        />
      </section>

      <section class="api-workbench__runs" aria-label="Runs">
        <RunList runs={store.runs()} onView={(id, view) => store.setRunView(id, view)} />
      </section>
    </div>
  )
}

/** The kit's row vocabulary for a workbench row. A collection and a directory
 *  are both folders as far as the row is concerned; a file the format did not
 *  recognise is `unreadable`, which is the kit's own word for a row that
 *  exists and cannot be opened. */
function rowKind(row: ApiTreeRow): 'dir' | 'unreadable' {
  return row.kind === 'malformed' ? 'unreadable' : 'dir'
}
