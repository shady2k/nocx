/**
 * FolderPicker — our own folder browser, so choosing a folder never depends on
 * a native dialog.
 *
 * `dialog.openFile` and `dialog.openDirectory` are NATIVE dialogs reached
 * through Wails. The dev-web harness has no Wails at all, and a backend served
 * over the network would have none either — so a product whose only way to
 * choose a folder is native has no way to choose a folder in two of the three
 * configurations it runs in. This is the other half: a routed browser that
 * works wherever a directory can be listed.
 *
 * ## The `list` callback is the whole design
 *
 * The component does not import a client, knows nothing about JSON-RPC or a
 * `bindingId`, and cannot tell a local filesystem from a remote one. THE
 * CALLER decides which machine to look at. That is what keeps the routing
 * decision where it belongs, and what makes this testable with a stub — no
 * client, no dispatcher, no backend. If a test of this component ever needs
 * one, the seam has moved to the wrong place.
 *
 * It therefore supports every filesystem nocx can reach, deliberately: it is a
 * general dialog, not part of any one feature. A caller that is choosing a
 * folder for a collection passes the BACKEND'S OWN filesystem — not because a
 * remote one is hard, but because of availability: a collection that lives on
 * a remote host stops opening the moment that host is unreachable, and a
 * collection must open always. That is a property being chosen, not a
 * limitation waiting to be lifted.
 *
 * ## What the dialog guarantees
 *
 * - **Only a directory can be the answer.** Files are listed — greyed and
 *   inert — because hiding them makes a directory full of files look exactly
 *   like an empty one, and recognising the folder you are after is most of
 *   what browsing is for. A file row is `aria-disabled`, is not a tab stop,
 *   and no click on it changes the answer.
 * - **Typing always works.** The path field is the answer, and it is editable
 *   whatever the listing did. It is the path that survives when a listing
 *   fails, so `Choose` resolves the field's text — not the last directory the
 *   backend agreed to list.
 * - **A failed listing shows its reason in place.** The dialog does not close
 *   and the listing on screen is not emptied: the entries and the directory
 *   they came from are replaced only by a listing that SUCCEEDED. A later
 *   success clears the reason.
 * - **Entries are never re-sorted.** `files.list`'s contract says ordering is
 *   backend-owned and deterministic; re-sorting here would be a second owner
 *   of the order, which is the shape that agrees everywhere anyone looks and
 *   disagrees somewhere nobody did.
 *
 * ## Why the rows are not `TreeRow`
 *
 * TreeRow is the file TREE's row: a `treeitem` with a depth, a disclosure and
 * an `aria-expanded` state. This dialog shows one directory at a time and its
 * rows are choices, not branches — a `listbox` of `option`s. Nesting a
 * `treeitem` inside an `option` is not a layout compromise, it is a broken
 * accessibility tree, and giving a row an `aria-expanded` it never honours is
 * worse than not having one. The glyphs are still the kit's single answer to
 * "what does a folder look like" (`./icons`), so the vocabulary is shared even
 * though the row is not.
 *
 * @example
 * ```ts
 * const chosen = await showFolderPicker({
 *   initialPath: home,
 *   list: (path) => files.list(bindingId, path),
 * })
 * ```
 */

import { For, Show, createSignal, onMount, type Component } from 'solid-js'
import { render } from 'solid-js/web'
import { Dialog } from './dialog'
import { Button } from './button'
import { TextField } from './text-field'
import { StatusCard } from './status-card'
import { EmptyState } from './empty-state'
import { ArrowUpIcon, FileIcon, FolderIcon } from './icons'

/** One entry of one directory. Two facts, because two facts are all the
 *  dialog acts on: what to show, and whether it can be the answer. */
export interface FolderEntry {
  name: string
  isDirectory: boolean
}

export interface FolderPickerProps {
  /** Where to start. */
  initialPath: string
  /** One page of one directory. The caller supplies this — the component
   *  knows nothing about JSON-RPC, bindings or which machine it is reading. */
  list: (path: string) => Promise<{ path: string; entries: FolderEntry[] }>
  onResolve: (chosen: string | null) => void
}

/* ── The one place this component reasons about the shape of a path ───────
   POSIX separator: nocx's desktop shell is macOS and its backend targets
   macOS and Linux, and a Windows story invented here would be a second,
   untested grammar for a platform nothing else in the tree serves.

   Both helpers build a REQUEST, never a fact. `list` answers with the
   canonical `path` for whatever it was asked, and that answer is what the
   dialog adopts — so a join that guessed wrong about a trailing slash is
   corrected by the reply rather than remembered. The only thing derived here
   and not re-asked is `parentOf(...) === null`, which is what disables the
   Up button at the root. */
const SEP = '/'

function childOf(dir: string, name: string): string {
  return dir.endsWith(SEP) ? `${dir}${name}` : `${dir}${SEP}${name}`
}

/** The parent of `dir`, or null when there is none to go to. */
function parentOf(dir: string): string | null {
  if (dir === '' || dir === SEP) return null
  const trimmed = dir.endsWith(SEP) ? dir.slice(0, -1) : dir
  const cut = trimmed.lastIndexOf(SEP)
  if (cut < 0) return null
  return cut === 0 ? SEP : trimmed.slice(0, cut)
}

/** What to put in front of the user when a listing fails. A rejection is
 *  whatever the caller's transport threw, so this narrows rather than
 *  stringifies: an unrecognisable rejection gets a sentence that is true
 *  instead of `[object Object]`. */
function reasonOf(e: unknown): string {
  if (e instanceof Error && e.message !== '') return e.message
  if (typeof e === 'string' && e !== '') return e
  return 'The directory could not be read.'
}

interface ListingFailure {
  path: string
  reason: string
}

export const FolderPicker: Component<FolderPickerProps> = (props) => {
  /** The directory whose entries are on screen — only ever a path a listing
   *  SUCCEEDED for, which is what keeps a failure from emptying the tree. */
  const [dir, setDir] = createSignal(props.initialPath)
  const [entries, setEntries] = createSignal<FolderEntry[]>([])
  const [failure, setFailure] = createSignal<ListingFailure | null>(null)
  const [busy, setBusy] = createSignal(false)

  /** THE ANSWER, and its single owner: the path field's text is what `Choose`
   *  resolves. Navigating writes the listing's canonical path into it,
   *  selecting a row writes that row's path into it, and the user may type
   *  over either. One input, one owner — a selection model beside a text
   *  field would be two surfaces claiming one value, and the loser would go
   *  on advertising an answer it could no longer give. */
  const [answer, setAnswer] = createSignal(props.initialPath)
  const chosen = () => answer().trim()

  /* Which listing is the current one. A directory that answers slowly must
     not overwrite the directory the user has moved on to; the counter is
     compared on the way out, so a late reply is dropped rather than
     rendered. */
  let seq = 0

  const navigate = async (to: string): Promise<void> => {
    if (to === '') return
    const req = ++seq
    setBusy(true)
    try {
      const listing = await props.list(to)
      if (req !== seq) return
      setDir(listing.path)
      setEntries(listing.entries)
      setFailure(null)
      setAnswer(listing.path)
    } catch (e) {
      if (req !== seq) return
      // The listing on screen is left exactly as it was: the reason appears
      // above it, and everything else in the dialog goes on working.
      setFailure({ path: to, reason: reasonOf(e) })
    } finally {
      if (req === seq) setBusy(false)
    }
  }

  onMount(() => {
    void navigate(props.initialPath)
  })

  const cancel = () => props.onResolve(null)
  const choose = () => {
    if (chosen() === '') return
    props.onResolve(chosen())
  }

  const parent = () => parentOf(dir())

  return (
    <Dialog
      open
      size="lg"
      title="Choose a folder"
      onClose={cancel}
      onSubmit={choose}
      footer={
        <>
          <Button variant="default" onClick={cancel}>
            Cancel
          </Button>
          <Button variant="primary" disabled={chosen() === ''} onClick={choose}>
            Choose
          </Button>
        </>
      }
    >
      <div class="ui-folder-picker">
        <div class="ui-folder-picker__path">
          <TextField
            label="Folder"
            value={answer()}
            onInput={setAnswer}
            autoFocus
            placeholder="/path/to/folder"
          />
          <Button
            variant="default"
            disabled={chosen() === ''}
            onClick={() => void navigate(chosen())}
          >
            Go
          </Button>
          <Button
            variant="default"
            ariaLabel="Up one level"
            title="Up one level"
            disabled={parent() === null}
            onClick={() => {
              const up = parent()
              if (up !== null) void navigate(up)
            }}
          >
            <ArrowUpIcon />
          </Button>
        </div>

        <Show when={failure()}>
          {(f) => (
            <StatusCard tone="danger" title={`Cannot open ${f().path}`} description={f().reason} />
          )}
        </Show>

        <div
          class="ui-folder-picker__list"
          role="listbox"
          aria-label="Folder contents"
          aria-busy={busy() ? 'true' : undefined}
        >
          <Show
            when={entries().length > 0}
            fallback={
              <Show when={!busy() && failure() === null}>
                <EmptyState title="This folder is empty" />
              </Show>
            }
          >
            <For each={entries()}>
              {(entry) => {
                const path = () => childOf(dir(), entry.name)
                const selected = () => entry.isDirectory && answer() === path()
                const take = () => {
                  if (entry.isDirectory) setAnswer(path())
                }
                const enter = () => {
                  if (entry.isDirectory) void navigate(path())
                }
                return (
                  <div
                    class="ui-folder-picker__row"
                    role="option"
                    data-kind={entry.isDirectory ? 'dir' : 'file'}
                    data-selected={selected() ? 'true' : undefined}
                    aria-selected={selected()}
                    aria-disabled={entry.isDirectory ? undefined : 'true'}
                    tabIndex={entry.isDirectory ? 0 : -1}
                    onClick={take}
                    onDblClick={enter}
                    onKeyDown={(e: KeyboardEvent) => {
                      if (e.key === 'Enter') {
                        e.preventDefault()
                        enter()
                      } else if (e.key === ' ') {
                        e.preventDefault() // Space must not scroll the list
                        take()
                      }
                    }}
                  >
                    <span class="ui-folder-picker__icon" aria-hidden="true">
                      <Show when={entry.isDirectory} fallback={<FileIcon />}>
                        <FolderIcon />
                      </Show>
                    </span>
                    <span class="ui-folder-picker__label" title={entry.name}>
                      {entry.name}
                    </span>
                  </div>
                )
              }}
            </For>
          </Show>
        </div>
      </div>
    </Dialog>
  )
}

/**
 * Put the folder browser on screen and resolve with the absolute path the
 * person chose, or with null when they cancelled.
 *
 * `onResolve` is supplied by this function rather than by the caller — that is
 * what the promise IS — so the argument is the rest of the props. A caller
 * that passed its own would be a second owner of the answer, which is the one
 * thing the component is careful not to have.
 *
 * The teardown is deferred for the same reason `showConfirm` and the
 * name-and-colour dialog defer theirs: Dialog's own cleanup — popOverlay and
 * the focus restore — must run against a live root.
 */
export function showFolderPicker(
  props: Omit<FolderPickerProps, 'onResolve'>,
): Promise<string | null> {
  return new Promise<string | null>((resolve) => {
    const host = document.createElement('div')
    document.body.appendChild(host)

    let dispose: (() => void) | null = null
    let settled = false

    const finish = (result: string | null) => {
      // Escape fires the cancel path and the disposer can run again on
      // unmount; the promise must resolve exactly once.
      if (settled) return
      settled = true
      queueMicrotask(() => {
        dispose?.()
        host.remove()
      })
      resolve(result)
    }

    dispose = render(
      () => <FolderPicker initialPath={props.initialPath} list={props.list} onResolve={finish} />,
      host,
    )
  })
}
