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

import { For, Show, type JSX } from 'solid-js'
import { Button } from '../ui/button'
import { EmptyState } from '../ui/empty-state'
import { RecordRow } from '../ui/record-row'
import { FolderIcon, PlusIcon } from '../ui/icons'

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
  /** Make a request here — the door the empty state offers, because an empty
   *  folder is exactly where a person needs it and the trail's own plus is
   *  at the other end of the line. */
  onNewRequest: () => void
}

export function FolderView(props: FolderViewProps) {
  return (
    <div class="api-folder">
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
    </div>
  )
}
