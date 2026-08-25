// What is in a folder, as a PAGE — the third thing the right half can show,
// beside a request and the environments.
//
// A folder used to be a row that folded away and nothing else. Clicking one
// answered with the absence of its children, which is not an answer to "what
// is in here" and is no answer at all to "where am I": the crumb trail above
// went on naming a request from somewhere else, and the plus beside it made
// its request somewhere else too. Postman and Bruno both settle this by
// making a folder something you OPEN — Postman gives it an overview tab,
// Bruno a settings tab — and this is that, in the shape this surface already
// has for the environments page: it takes the half, the tree beside it goes
// on working, and the request underneath is untouched.
//
// IT RENDERS, IT DOES NOT DECIDE. What is in a folder is answered once, by
// `contentsOf` in api-tree.ts, which is the same index the tree walks — a
// page that re-derived "what hangs under here" would be the second answer
// that disagrees with the column beside it on the day the backend lists a
// folder whose parent it did not. What a row can BE is answered once too, by
// the menus in api-pane.tsx that the tree's rows already open: this page is a
// third door onto those lists and never a second copy of them.

import { For, Show, createEffect, createSignal, type JSX } from 'solid-js'
import { showToast } from '../ui/toast'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { EditableRowList } from '../ui/row-list'
import { EmptyState } from '../ui/empty-state'
import { RecordRow } from '../ui/record-row'
import { FolderIcon, PlusIcon } from '../ui/icons'
import { StatusCard } from '../ui/status-card'
import { Tabs } from '../ui/tabs'
import { TextField } from '../ui/text-field'
import type { ApiParam } from './api-model'
// The label rule for a table's tab is stated once, by the surface that first
// stated it, rather than restated here (nocx-x3cax.6).
import { counted } from './request-form'

/**
 * One line of the page — a folder or a request, in the vocabulary the row
 * renders rather than the one the store holds.
 *
 * The WORDS are decided by the caller, which is the level that can see the
 * collection: how many things a folder holds is a question about the listing,
 * and a page that answered it itself would be reading the tree a second time.
 */
export interface FolderEntry {
  /** The path within the collection — the identity every act takes. */
  relPath: string
  /** What it is called: the name a request declares, a folder's last
   *  segment. */
  name: string
  /** The badge: a request's verb, or the word for a folder. */
  kind: string
  /** The line under the name — the file this row is, or what a folder holds.
   *  '' draws no line at all. */
  meta: string
  /** Whether opening this goes INTO it rather than into the form. */
  folder: boolean
}

export interface FolderViewProps {
  /** The folder's path inside the collection, '' at the collection's root.
   *  Only used to tell those two apart in the empty state — the trail above
   *  the page is what names the place, and a page that named it a second
   *  time would be two answers to where a person is. */
  folder: string
  /** Folders first, then the requests beside them: the order the tree draws,
   *  because a page that sorted its own way would read as a different folder
   *  from the one in the column. */
  entries: readonly FolderEntry[]
  onOpen: (entry: FolderEntry) => void
  /**
   * What this row can be, as controls standing on it.
   *
   * BUILT BY THE CALLER, not by a menu this page owns. A page-full of rows
   * is not the narrow column the tree is, so the acts stand on the row the
   * way the Snippets and Endpoints lists put theirs — a ⋮ here would be a
   * click spent opening a list to press one of three things that fit. The
   * ACTS themselves live one level up, where their one owner is; this only
   * says where they are drawn.
   */
  actions: (entry: FolderEntry) => JSX.Element
  /** Rows from the folder's reserved variables document. */
  variables: readonly ApiParam[] | null
  loading: boolean
  /** A write is in flight. */
  busy: boolean
  /** What is on screen has been written since it was last edited — NOT "there
   *  is nothing to save", which is also true of a folder nobody has touched. */
  written: boolean
  /** A refused read belongs to the on-page card. */
  error: string
  /** A refused save belongs to the notification channel. */
  saveError: string
  onVariables: (variables: readonly ApiParam[]) => void
  /** Make a request here — the door the empty state offers, because an empty
   *  folder is exactly where a person needs it and the trail's own plus is
   *  at the other end of the line. */
  onNewRequest: () => void
}

export function FolderView(props: FolderViewProps) {
  // WHICH SECTION IS OPEN. It starts on the contents, because that is what the
  // page is for: the variables editor is somewhere a person GOES, and it used
  // to be the first thing they met (nocx-x3cax.6).
  const [tab, setTab] = createSignal('contents')
  let lastRefused = ''
  createEffect(() => {
    const error = props.saveError
    if (error === '') {
      lastRefused = ''
      return
    }
    if (error === lastRefused) return
    lastRefused = error
    showToast({ level: 'danger', message: error })
  })

  const rows = () => props.variables ?? []
  const patchRow = (index: number, over: Partial<ApiParam>): void => {
    props.onVariables(rows().map((row, i) => (i === index ? { ...row, ...over } : row)))
  }

  const readDescription = (): string => {
    const reason = props.error || 'The folder variables file could not be read.'
    if (reason.includes('folder variables file is malformed')) {
      return `${reason} Correct .variables.json in this folder in an editor before this page can edit it.`
    }
    return reason
  }

  /** What is in the folder — the page's own subject. */
  const contents = () => (
    <Show
      when={props.entries.length > 0}
      fallback={
        <EmptyState
          icon={<FolderIcon />}
          title={props.folder === '' ? 'This collection is empty' : 'This folder is empty'}
          description="Make a request here, or import a curl command into it."
          action={
            <Button variant="primary" onClick={() => props.onNewRequest()}>
              <PlusIcon />
              New request
            </Button>
          }
        />
      }
    >
      <For each={props.entries}>
        {(entry) => (
          <RecordRow
            title={entry.name}
            kind={{ label: entry.kind, tone: 'neutral' }}
            meta={entry.meta !== '' ? entry.meta : undefined}
            onActivate={() => props.onOpen(entry)}
            actions={props.actions(entry)}
          />
        )}
      </For>
    </Show>
  )

  /** The scope this folder declares, with its own Save, its loading state and
   *  its refusal — all of it inside the section it belongs to. */
  const variables = () => (
    <>
      <p class="api-folder__note">
        A variable here answers for every request in this folder and below it, and a request's own
        variable wins over it.
      </p>
      <Show
        when={!props.loading}
        fallback={
          <StatusCard title="Loading folder variables" description="Reading this folder's file." />
        }
      >
        <Show
          when={props.variables !== null}
          fallback={
            <StatusCard
              title="Folder variables unavailable"
              description={readDescription()}
              tone="danger"
            />
          }
        >
          <EditableRowList
            variant="table"
            ariaLabel="Folder variables"
            columns={[{ label: 'Send', labelHidden: true }, { label: 'Name' }, { label: 'Value' }]}
            rows={rows()}
            addLabel="Add variable"
            emptyLabel="No variables declared in this folder."
            removeLabel={(i) => `Remove variable ${i + 1}`}
            renderRow={(row, i) => (
              <>
                <td>
                  <Checkbox
                    ariaLabel={`Use variable ${i + 1}`}
                    checked={row().enabled}
                    onChange={(enabled) => patchRow(i, { enabled })}
                  />
                </td>
                <td>
                  <TextField
                    id={`api-folder-var-name-${i}`}
                    ariaLabel={`Variable ${i + 1} name`}
                    placeholder="baseUrl"
                    value={row().name}
                    onInput={(value) => patchRow(i, { name: value })}
                  />
                </td>
                <td>
                  <TextField
                    id={`api-folder-var-value-${i}`}
                    ariaLabel={`Variable ${i + 1} value`}
                    placeholder="https://api.example.com"
                    value={row().value}
                    onInput={(value) => patchRow(i, { value })}
                  />
                </td>
              </>
            )}
            onRemove={(i) => props.onVariables(rows().filter((_, j) => j !== i))}
            onAdd={() => props.onVariables([...rows(), { name: '', value: '', enabled: true }])}
          />
          {/* NO SAVE. The rows write themselves once typing stops
              (nocx-x3cax.7), and this is the half that keeps that honest: a
              save nobody asked for and nobody is told about is a save a person
              cannot trust. It says the two states that exist and stays quiet
              about the third — a folder just opened has been written by
              nobody, and telling them "Saved" would be a claim about an act
              that did not happen. The refusal is a danger toast, which is the
              surface still there when the table is not. */}
          <Show when={props.busy || props.written}>
            <p class="api-folder__note" aria-live="polite">
              {props.busy ? 'Saving…' : 'Saved'}
            </p>
          </Show>
        </Show>
      </Show>
    </>
  )

  return (
    <div class="api-folder">
      {/* CONTENTS FIRST, AND THE KIT'S TABS RATHER THAN A SECOND VOCABULARY.
          The variables editor was appended ABOVE the listing because that was
          the smallest change that made the scope reachable (nocx-x3cax.2), and
          it left the page opening on an editor for something most folders do
          not have, with the requests pushed under it.

          `Tabs` is the component the request surface already uses for exactly
          this — sections that are views of ONE thing, where switching is not
          navigation — and its own doc names the two-section case as what
          `horizontal` is for. Its content closures read props live; nothing
          here captures a value, which is the identity contract that file
          states. */}
      <Tabs
        orientation="horizontal"
        ariaLabel="This folder"
        active={tab()}
        onChange={setTab}
        items={[
          { id: 'contents', label: 'Contents', content: contents },
          // The same words the request's own strip uses for the same fact, so
          // one convention is read across the panel.
          { id: 'variables', label: counted('Variables', rows()), content: variables },
        ]}
      />
    </div>
  )
}
