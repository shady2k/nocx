// "New collection" — the one form that asks for a name, and nothing else.
//
// A NAME, NOT A PATH, and the dialog could not offer one if it wanted to:
// `api.collections.create` takes a name and the backend derives the location
// (design §13.1, and the schema says so in as many words). A field for "where"
// beside a call that ignores it would be a control that governs nothing.
//
// IT STAYS OPEN WHEN THE BACKEND REFUSES. Empty, a path separator in it, `.`,
// `..`, a folder already there — each is a sentence about the name, and the
// name is in this field. Closing on refusal would put the reason somewhere the
// person is no longer looking and make them type it again to find out what was
// wrong; the reason renders under the field instead, which is the kit's own
// validation slot (`Field`'s `error`, `aria-invalid` included).
//
// The reason is the backend's own sentence, handed down by the surface from
// `store.error()` after a create — never composed here. This dialog decides
// exactly one thing on its own: that a blank name is not worth a round trip.

import { createEffect, createSignal } from 'solid-js'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { TextField } from '../ui/text-field'

export interface NewCollectionDialogProps {
  open: boolean
  /** The backend's reason the last attempt was refused, or '' when the last
   *  thing attempted worked. */
  error: string
  /** True while a create is in flight — the confirm must not fire twice. */
  busy: boolean
  onCancel: () => void
  onCreate: (name: string) => void
}

export function NewCollectionDialog(props: NewCollectionDialogProps) {
  const [name, setName] = createSignal('')
  const trimmed = () => name().trim()

  // A FRESH ASK STARTS EMPTY. The dialog is mounted for the life of the
  // surface, so without this the field still holds the name of the last
  // collection made — an offer nobody wrote, and one that Enter would submit
  // straight back to a backend that has just refused it as already there.
  // A refusal does not flip `open`, so what was typed survives it.
  createEffect(() => {
    if (props.open) setName('')
  })

  /**
   * The refusal as the kit wants it — a string when there is one and
   * `undefined` when there is not.
   *
   * '' is "nothing was refused", and it must not reach TextField as an empty
   * error: the kit marks a field `aria-invalid` whenever `error` is DEFINED,
   * so a blank string would announce every fresh name as wrong.
   */
  const refusal = (): string | undefined => (props.error !== '' ? props.error : undefined)

  const submit = (): void => {
    // The backend refuses a blank name and so does this: a call that could be
    // sent and refused is a round trip spent to learn what the form already
    // knew (name-colour-dialog.tsx keeps the same rule).
    if (trimmed() === '' || props.busy) return
    props.onCreate(trimmed())
  }

  return (
    <Dialog
      open={props.open}
      title="New collection"
      onClose={props.onCancel}
      onSubmit={submit}
      footer={
        <>
          <Button variant="default" onClick={props.onCancel}>
            Cancel
          </Button>
          <Button variant="primary" disabled={trimmed() === '' || props.busy} onClick={submit}>
            Create
          </Button>
        </>
      }
    >
      <TextField
        id="api-new-collection-name"
        label="Name"
        description="A name, not a path — the folder is made where nocx keeps collections. It is safe to commit: no secret value is ever written into it."
        placeholder="orders-api"
        value={name()}
        error={refusal()}
        onInput={setName}
        autoFocus
        required
      />
    </Dialog>
  )
}
