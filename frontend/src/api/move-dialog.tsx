// The "Move to folder…" chooser — one request, and every folder of THIS
// collection plus its root, plus a way to make a folder from the same
// place (nocx-8aczn.2).
//
// It is a dialog, not a second context menu, for the same reason the
// folder ask is one: a move has exactly one answer to get right, the
// destination, and a menu is a list of ACTS. The kit's Dialog owns the
// modal surface, the Radio group owns "one of N", the TextField owns the
// new-folder name and the Buttons own Move/Cancel — this component places
// them and owns nothing but the choice itself.
//
// A destination folder that does not exist is a refusal this surface
// anticipates rather than waits for: the chooser offers to make one from
// the same place, because api.collections.createFolder exists and a young
// collection is exactly where somebody moves something into a folder that
// is not there yet. The caller composes the two acts (create, then move);
// the dialog only says which was asked for.

import { For, Show, createEffect, createSignal } from 'solid-js'
import { showToast } from '../ui/toast'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Radio } from '../ui/radio'
import { TextField } from '../ui/text-field'

export interface MoveToFolderDialogProps {
  open: boolean
  /** The request being moved, read into the title so the ask says WHAT. */
  requestName: string
  /** The collection the move is inside, for the root row's label. */
  collectionName: string
  /** Every folder THIS collection lists, each a path relative to its
   *  root. The store already holds them (`collection.folders`); the chooser
   *  never derives a second list from the requests' paths. */
  folders: readonly string[]
  /** The backend's reason the last attempt was refused, or ''. */
  error: string
  /** True while a move or a create-then-move is in flight. */
  busy: boolean
  onCancel: () => void
  /** Move into an EXISTING folder; '' is the collection's root. */
  onMove: (toRelPath: string) => void
  /** Make a folder at the collection's root, then move into it. */
  onNewFolderAndMove: (name: string) => void
}

/** The radio value that means "make a new folder". It is a sentinel, not a
 *  path: no folder this backend would accept is spelled exactly this way
 *  (a name is one component and `__new__` is not what a person chose), and
 *  it is never SENT — the caller translates it into a create first, so the
 *  wire only ever sees real folder paths. */
const NEW_FOLDER_VALUE = '__new__'

export function MoveToFolderDialog(props: MoveToFolderDialogProps) {
  // WHICH PLACE is chosen: '' the root, a folder's path, or NEW_FOLDER.
  const [chosen, setChosen] = createSignal('')
  const [newName, setNewName] = createSignal('')

  // A FRESH ASK STARTS EMPTY, exactly as the surface's other asks do
  // (api-pane says it of every dialog it keeps mounted): this dialog lives
  // for the life of the workbench, so without this the next request would
  // open with the LAST answer — the folder somebody picked for the one
  // before, or a half-typed "New folder" name — and the primary action
  // would move the new request to a place nobody chose this time. Reset
  // happens on the OPEN transition, so a refusal that keeps the dialog
  // open (error retry) preserves what was chosen.
  createEffect(() => {
    if (props.open) {
      setChosen('')
      setNewName('')
    }
  })
  const refusal = (): string | undefined => (props.error !== '' ? props.error : undefined)
  // The outcome of a Move, in a toast. The refusal sits on the new-folder
  // field when THAT is the destination (the box to fix); a move into an
  // existing folder or the root has no field, so it is said where the kit
  // says outcomes are said. Edge-triggered on the refusal itself, so a
  // re-render cannot stack a second sticky toast for the same error.
  let lastRefused = ''
  createEffect(() => {
    const err = props.error
    if (err === '') {
      lastRefused = ''
      return
    }
    if (chosen() === NEW_FOLDER_VALUE) return
    if (err === lastRefused) return
    lastRefused = err
    showToast({ level: 'danger', message: err })
  })

  const ready = (): boolean => {
    if (chosen() === NEW_FOLDER_VALUE) return newName().trim() !== ''
    return true
  }

  const submit = (): void => {
    if (!ready() || props.busy) return
    if (chosen() === NEW_FOLDER_VALUE) {
      props.onNewFolderAndMove(newName().trim())
      return
    }
    props.onMove(chosen())
  }

  return (
    <Dialog
      open={props.open}
      title={`Move ${props.requestName} to…`}
      onClose={props.onCancel}
      onSubmit={submit}
      footer={
        <>
          <Button variant="default" onClick={props.onCancel}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!ready() || props.busy} onClick={submit}>
            {chosen() === NEW_FOLDER_VALUE ? 'Create and move' : 'Move here'}
          </Button>
        </>
      }
    >
      {/* The folder list, ONE choice in a group. The kit's Radio is the
          one-of-N primitive; the group container owns the layout and the
          role. Root first, then the collection's folders in the backend's
          own order (parents before their children — api-tree depends on
          it, and so does this list). */}
      <div class="api-move__folders" role="radiogroup" aria-label="Move to folder">
        <Radio
          name="api-move-folder"
          value=""
          checked={chosen() === ''}
          onChange={setChosen}
          label={`Root of ${props.collectionName}`}
          ariaLabel={`Root of ${props.collectionName}`}
        />
        <For each={props.folders}>
          {(folder) => (
            <Radio
              name="api-move-folder"
              value={folder}
              checked={chosen() === folder}
              onChange={setChosen}
              label={folder}
              ariaLabel={folder}
            />
          )}
        </For>
        <Radio
          name="api-move-folder"
          value={NEW_FOLDER_VALUE}
          checked={chosen() === NEW_FOLDER_VALUE}
          onChange={setChosen}
          label="New folder…"
        />
      </div>
      {/* The new-folder half of the same decision: only when it is the
          decision. It is made AT THE ROOT — the chooser's other rows are
          the collection's own, and making one somewhere else would need to
          put this control inside a choice, which is a second chooser. */}
      <Show when={chosen() === NEW_FOLDER_VALUE}>
        <TextField
          id="api-move-new-folder-name"
          ariaLabel="New folder name"
          label="New folder name"
          description={`A folder at the root of ${props.collectionName}; ${props.requestName} will be moved into it.`}
          value={newName()}
          onInput={setNewName}
          error={refusal()}
          autoFocus
          required
        />
      </Show>
    </Dialog>
  )
}
