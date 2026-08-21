// The header of the right half: where this request LIVES, what it is called,
// and the one action that is about the file rather than about the exchange.
//
// The name is edited HERE, in place, and that is the whole reason this
// module exists. A request arrives called "Untitled request" — creating one
// asks nothing, because a person pressing "new request" has already said
// what they want and a naming dialog puts a decision before the thing they
// came to do (api-store.ts). The name is then given where it is READ: in
// the line that says which collection this is in, the way the owner's
// reference does it.
//
// WHAT RENAMING DOES NOT DO: move the file. The tree row shows this name
// (api-tree.ts prefers it), and the file keeps the path it was created
// under. Renaming a file is a different act — it is a move, with a
// collision to resolve and a git rename behind it — and there is no method
// for it yet. Doing it quietly here would be that act performed by
// somebody who asked for a different one.

import { Show, createSignal } from 'solid-js'
import { Button } from '../ui/button'
import { IconButton } from '../ui/icon-button'
import { MoreIcon, SaveIcon } from '../ui/icons'
import { TextField } from '../ui/text-field'

export interface RequestCrumbsProps {
  /** The collection the workbench is pointed at. '' when none is open. */
  collection: string
  /** The request in the form, or null when nothing is open. */
  name: string | null
  onRename: (name: string) => void
  /** True when the draft differs from its file, or has no file yet. */
  savable: boolean
  onSave: () => void
  /**
   * Everything else this request can be — deleted, today.
   *
   * BESIDE SAVE, not on the tree row. It sat on the row first and looked
   * wrong there, and the reason is what the two lines are for: the row is a
   * list of what EXISTS and the header is about the one request a person is
   * working on. An action that acts on the open request belongs where the
   * open request is named. Optional, and absent while nothing is open —
   * there is then no request for it to be about.
   */
  onMore?: (e: MouseEvent) => void
}

export function RequestCrumbs(props: RequestCrumbsProps) {
  const [editing, setEditing] = createSignal(false)
  const [typed, setTyped] = createSignal('')

  const start = (): void => {
    setTyped(props.name ?? '')
    setEditing(true)
  }

  const commit = (): void => {
    const next = typed().trim()
    setEditing(false)
    // A blank name is not a rename — it is a field somebody cleared and
    // walked away from. The tree would then show the file's own basename,
    // which is a name nobody chose.
    if (next === '' || next === props.name) return
    props.onRename(next)
  }

  return (
    <header class="api-crumbs">
      <Show when={props.collection !== ''}>
        <span class="api-crumbs__collection">{props.collection}</span>
        <Show when={props.name !== null}>
          <span class="api-crumbs__sep" aria-hidden="true">
            ›
          </span>
        </Show>
      </Show>
      <Show when={props.name !== null}>
        <Show
          when={editing()}
          fallback={
            // The kit's ghost Button: this is a control, so it is one — it
            // takes focus, answers Enter and Space and says what it does.
            // The surface PLACES it (the name reads as text in a crumb
            // trail) and does not repaint it.
            <span class="api-crumbs__name">
              <Button variant="ghost" title="Rename" onClick={start}>
                {props.name}
              </Button>
            </span>
          }
        >
          {/* A FORM, so Enter commits. The kit's field has no key handler and
              should not grow one for this: a single-input form submitting on
              Enter is the platform's own answer, and it costs no prop. */}
          <form
            class="api-crumbs__field"
            onSubmit={(e: SubmitEvent) => {
              e.preventDefault()
              commit()
            }}
          >
            <TextField
              id="api-request-name"
              ariaLabel="Request name"
              value={typed()}
              onInput={setTyped}
              onBlur={commit}
              autoFocus
              selectOnFocus
            />
          </form>
        </Show>
      </Show>
      <Show when={props.name !== null}>
        <div class="api-crumbs__save">
          <Button
            id="api-save-request"
            disabled={!props.savable}
            title={props.savable ? 'Write this request to its file' : 'Nothing to save'}
            onClick={props.onSave}
          >
            <SaveIcon />
            Save
          </Button>
          <Show when={props.onMore}>
            <IconButton
              id="api-request-menu"
              size="sm"
              title="More actions for this request"
              ariaLabel="More actions for this request"
              onClick={(e: MouseEvent) => props.onMore?.(e)}
            >
              <MoreIcon />
            </IconButton>
          </Show>
        </div>
      </Show>
    </header>
  )
}
