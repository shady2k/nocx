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
import { ArrowDownIcon, MoreIcon, PlusIcon } from '../ui/icons'
import { TextField } from '../ui/text-field'

export interface RequestCrumbsProps {
  /** The collection the workbench is pointed at. '' when none is open. */
  collection: string
  /** The request in the form, or null when nothing is open. */
  name: string | null
  /** The folder the open request lives in, INSIDE the collection — the
   *  path segment between the collection and the name. Absent (undefined)
   *  on a request at the collection's root, because there is nothing to
   *  name then. It is what makes a move's outcome visible in the header:
   *  after api.request.move re-points the form at the new path, this
   *  segment changes to the new place (nocx-8aczn.2). */
  folder?: string | null
  onRename: (name: string) => void
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
  /**
   * Make a new request in the collection the workbench is pointed at.
   *
   * A FIXED DOOR. The other two are on a collection row — a plus that is
   * only there while the pointer is over it, and that row's menu — so making
   * a request meant aiming at a line in a list that moves as folders are
   * expanded. This one is where the open request is already named, it needs
   * no aiming, and it asks nothing: the store writes into the ACTIVE
   * collection, which is the one this trail's first segment names.
   *
   * Optional, and that is the capability: a workbench with no collection
   * open hands nothing in, so there is no control rather than one that
   * refuses. It is present with no request open, though — an empty
   * collection is exactly where a person needs it.
   *
   * The row's plus and the row's menu stay. They answer a different
   * question: a request in a collection that is NOT the active one.
   */
  onNew?: () => void
  /**
   * Ask for a curl command line to convert into the form.
   *
   * BESIDE THE OTHER TWO DOORS, not on the line. It sat between the URL and
   * Send, which put a control about WHERE A REQUEST COMES FROM in the row a
   * person edits the request in — and the line is already the busiest thing
   * on the surface. Making one, importing one and acting on the open one are
   * the same kind of act on the same subject, so they read as one group at
   * the trailing end: Save, then new, then imported, then the rest.
   *
   * Optional, like the other two, and for the same reason: a crumb trail
   * mounted without an owner for the ask offers nothing rather than a
   * control that swallows the press. It is present with NO collection and no
   * request open, though — a curl line becomes a draft with no file behind
   * it (api-store.ts), so it is exactly the door an empty workbench needs.
   */
  onImportCurl?: () => void
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
        {/* The folder segment: only when there IS a folder. It sits between
            the collection and the request the way the path does, and it is
            what shows a move landed — the value changes with the file. It
            is placement only: the same muted text the collection gets. */}
        <Show when={props.name !== null && props.folder !== undefined && props.folder !== null}>
          <span class="api-crumbs__folder">{props.folder}</span>
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
      {/* The trailing group: what this request can be, and the one door that
          is about the NEXT one. It stands while there is either — a
          collection with nothing open in the form still offers New request,
          and that is the state a person is in when they need it most.
          SAVE USED TO OPEN IT and does not exist any more: the draft reaches
          its file when typing stops (api-store.ts), so the button was being
          pressed for insurance rather than for a decision — Send already
          wrote the file before sending, and the only thing Save bought was
          not losing an experiment on the way to it. */}
      <Show when={props.name !== null || props.onNew || props.onImportCurl}>
        <div class="api-crumbs__save">
          <Show when={props.onNew}>
            <IconButton
              id="api-new-request"
              size="sm"
              title="New request"
              ariaLabel="New request"
              onClick={() => props.onNew?.()}
            >
              <PlusIcon />
            </IconButton>
          </Show>
          {/* The import mark is the collections menu's own (ArrowDownIcon),
              because it is the same verb one level down: something written
              elsewhere arrives here. */}
          <Show when={props.onImportCurl}>
            <IconButton
              id="api-import-curl-open"
              size="sm"
              title="Import a curl command"
              ariaLabel="Import a curl command"
              onClick={() => props.onImportCurl?.()}
            >
              <ArrowDownIcon />
            </IconButton>
          </Show>
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
