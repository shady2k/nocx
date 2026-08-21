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
import { CollectionDialog } from './collection-dialog'
import { RequestForm } from './request-form'
import { RunList } from './run-list'
import type { ApiStore } from './api-store'
import type { DirectoryPicker } from './api-client'
import type { ApiOpenCollection } from './api-model'

export interface ApiPaneProps {
  store: ApiStore
  /**
   * The native directory picker, when the backend offers one.
   *
   * It is not on the store: the store owns api.* state, and `dialog.*` is
   * another domain's method reached through another client (AD-8). It
   * arrives here for the same reason `openFileDialog` arrives at
   * KeyMaterialInput — a capability the surface offers only where it exists.
   */
  openDirectory?: DirectoryPicker
}

export function ApiPane(props: ApiPaneProps) {
  // The store is constructed once by ApiContent and handed in; it is a
  // dependency, not a reactive value, and nothing ever swaps one workbench's
  // store for another's. Reading it here rather than at every call site keeps
  // the surface readable — the reactivity that matters is inside the store's
  // own signals, which ARE read in tracked scopes below.
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const store = props.store
  // eslint-disable-next-line solid/reactivity -- injected dependency, never replaced
  const picker = props.openDirectory
  const [collapsed, setCollapsed] = createSignal<ReadonlySet<string>>(new Set())
  const [curlLine, setCurlLine] = createSignal('')
  const [postmanFile, setPostmanFile] = createSignal('')
  const [postmanDest, setPostmanDest] = createSignal('')

  // The two asks. Each owns what is typed into it, the reason its last
  // attempt was refused, and whether a call is in flight.
  const [naming, setNaming] = createSignal(false)
  const [name, setName] = createSignal('')
  const [creating, setCreating] = createSignal(false)
  // The reason the LAST CREATE was refused, in the backend's words — read off
  // the store the moment the call settles rather than tracked reactively,
  // because `store.error()` is the last failure of ANY call and a listing that
  // failed an hour ago is not a sentence about the name being typed now.
  const [nameRefused, setNameRefused] = createSignal('')

  const [opening, setOpening] = createSignal(false)
  const [folderPath, setFolderPath] = createSignal('')
  const [openingFolder, setOpeningFolder] = createSignal(false)
  const [pathRefused, setPathRefused] = createSignal('')

  /**
   * Whether the folder ask still offers Browse.
   *
   * Both ends of the interval: it is true from the moment the surface is
   * built with a picker wired until that picker reports itself unavailable —
   * `-32601`, which is every `make dev-web` run and any build whose
   * `dialog.openDirectory` is not wired — and it never returns for the life
   * of the surface. A control that has refused once and stays on screen is
   * the broken-looking fallback this whole capability check exists to avoid;
   * the reason it gave is shown in the ask, so the control does not simply
   * vanish without a word.
   */
  const [pickerLive, setPickerLive] = createSignal(picker !== undefined)

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

  // A FRESH ASK STARTS EMPTY. Both dialogs are mounted for the life of the
  // surface, so without this the field still holds the last answer — an
  // offer nobody wrote, and one that Enter would submit straight back to a
  // backend that has just refused it as already there. A refusal does not
  // close the ask, so what was typed survives it.
  const askForName = (): void => {
    setName('')
    setNameRefused('')
    setNaming(true)
  }

  const askForFolder = (): void => {
    setFolderPath('')
    setPathRefused('')
    setOpening(true)
  }

  const createCollection = (typed: string): void => {
    setCreating(true)
    void store.createCollection(typed).then(() => {
      setCreating(false)
      setNameRefused(store.error())
      // The store has already put the collection in the list, open and
      // pointed at — `api.collections.create` answered an open's shape — so
      // there is nothing to fetch here and nothing to select. On a refusal
      // the dialog stays, holding the name and the reason.
      if (store.error() !== '') return
      setNaming(false)
      showToast({ level: 'success', message: `Created ${typed}` })
    })
  }

  const openFolder = (path: string): void => {
    setOpeningFolder(true)
    void store.openFolder(path).then(() => {
      setOpeningFolder(false)
      setPathRefused(store.error())
      if (store.error() !== '') return
      setOpening(false)
      showToast({ level: 'success', message: `Opened ${path}` })
    })
  }

  /**
   * Ask the platform for a folder and put it in the field.
   *
   * An EMPTY path is a cancellation, not an answer — the contract
   * `dialog.openFile` already keeps — and writing it into the field would
   * erase what the person typed as the price of changing their mind. A
   * rejection is the method reporting itself unavailable: the reason goes
   * where every other refusal goes and the control retires, because the next
   * click would refuse identically.
   */
  const browseForFolder = (): void => {
    if (!picker) return
    setPathRefused('')
    void picker().then(
      (chosen) => {
        if (chosen.path !== '') setFolderPath(chosen.path)
      },
      (err: unknown) => {
        setPickerLive(false)
        setPathRefused(err instanceof Error ? err.message : String(err))
      },
    )
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
            {/* TWO ACTIONS, AND EACH ASKS. Neither is a form the panel wears:
                the state a person with nothing open starts in should show
                them what they can do, not what they must fill in first
                (nocx-84shs). Making one is primary because it is the action
                a person with no collection at all needs — opening a folder
                somebody else made is the second door, not the first. */}
            <Button id="api-new-collection" variant="primary" onClick={askForName}>
              New collection
            </Button>
            <Button id="api-open-collection" onClick={askForFolder}>
              Open folder…
            </Button>
            <CollectionDialog
              open={naming()}
              title="New collection"
              submitLabel="Create"
              fieldId="api-new-collection-name"
              fieldLabel="Name"
              fieldDescription="A name, not a path — the folder is made where nocx keeps collections. It is safe to commit: no secret value is ever written into it."
              placeholder="orders-api"
              value={name()}
              onInput={setName}
              error={nameRefused()}
              busy={creating()}
              onCancel={() => setNaming(false)}
              onSubmit={createCollection}
            />
            <CollectionDialog
              open={opening()}
              title="Open a collection folder"
              submitLabel="Open"
              fieldId="api-collection-path"
              fieldLabel="Collection folder"
              fieldDescription="The folder you place. It is safe to commit: no secret value is ever written into it."
              placeholder="/work/acme-api"
              value={folderPath()}
              onInput={setFolderPath}
              error={pathRefused()}
              busy={openingFolder()}
              onBrowse={pickerLive() ? browseForFolder : undefined}
              onCancel={() => setOpening(false)}
              onSubmit={openFolder}
            />
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
                  description="Make one above, open a folder you already have, or import a Postman export — its requests appear here."
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
                      selected={
                        row.kind === 'collection' && row.handle === store.activeCollection()
                      }
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
                <Caption>{collectionLabel(open)}</Caption>
                <Show when={open.error !== ''}>
                  <p class="api-tree__reason">{open.error}</p>
                </Show>
                <IconButton
                  size="sm"
                  title={`Close ${collectionLabel(open)}`}
                  ariaLabel={`Close ${collectionLabel(open)}`}
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

/** What to call one open collection where its LOCATION belongs: the folder's
 *  path, and its name when there is no path to show. A row minted from
 *  `api.collections.create` has none — the backend decided where the folder
 *  went and the result does not carry it (§13.1) — so the caption and the
 *  Close button would otherwise be blank and "Close " respectively. The tree
 *  row above prefers the other way round (api-tree.ts), because a row is
 *  asking what the thing is CALLED and this is asking where it IS. */
function collectionLabel(open: ApiOpenCollection): string {
  return open.path !== '' ? open.path : open.collection.name
}

/** The kit's row vocabulary for a workbench row. A collection and a directory
 *  are both folders as far as the row is concerned; a file the format did not
 *  recognise is `unreadable`, which is the kit's own word for a row that
 *  exists and cannot be opened. */
function rowKind(row: ApiTreeRow): 'dir' | 'unreadable' {
  return row.kind === 'malformed' ? 'unreadable' : 'dir'
}
