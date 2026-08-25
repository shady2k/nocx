// The workbench's two asks — "name a new collection" and "which folder" —
// as ONE form.
//
// It began as `new-collection-dialog.tsx`, a form for the name alone, while
// the folder was a bare TextField and a Button stacked in the panel. The
// owner's reference is Bruno (nocx-84shs): making a collection is an ask,
// and so is opening one; a panel that wears a form is a panel asking you to
// fill something in before it will show you anything.
//
// ONE COMPONENT FOR BOTH, for exactly the reason `name-colour-dialog.tsx`
// gives about the workspace create and edit forms: a person who has met one
// of these has already learnt the other, and a second component for "ask for
// one string about a new thing" is the two-owners defect in miniature.
//
// IT STAYS OPEN WHEN THE BACKEND REFUSES. Empty, a path separator in a name,
// `.`, `..`, a folder already there, a path that is not a collection — each
// is a sentence about what was typed, and what was typed is in this field.
// Closing on refusal would put the reason somewhere the person is no longer
// looking and make them type it again to find out what was wrong; the reason
// renders under the field instead, which is the kit's own validation slot
// (`Field`'s `error`, `aria-invalid` included).
//
// The reason is the backend's own sentence, handed down by the surface from
// `store.error()` after the call — never composed here. This component
// decides exactly one thing on its own: that a blank answer is not worth a
// round trip.
//
// THE VALUE IS THE CALLER'S. It used to live in a signal here, reset by an
// effect on `open`. It cannot any more: Browse fills the field from outside,
// so the field has one owner and it is the surface. What used to be the
// reset effect is the surface clearing its own signal before it opens the
// ask — the same rule, in the place that can also honour it when a picker
// answers.

import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { IconButton } from '../ui/icon-button'
import { FolderOpenIcon } from '../ui/icons'
import { TextField } from '../ui/text-field'

export interface CollectionDialogProps {
  open: boolean
  /** The dialog's own title, and the word on its affirmative button. */
  title: string
  submitLabel: string
  /** The field: its id (the surface's handle on it), its label, the sentence
   *  under it and the example inside it. */
  fieldId: string
  fieldLabel: string
  fieldDescription: string
  placeholder: string
  value: string
  onInput: (value: string) => void
  /** The backend's reason the last attempt was refused, or '' when the last
   *  thing attempted worked. */
  error: string
  /** True while a call is in flight — the confirm must not fire twice. */
  busy: boolean
  /**
   * Open the native directory picker.
   *
   * ABSENT is the ordinary state, not the exceptional one: `dialog.*` is
   * unavailable wherever there is no Wails runtime, which is every
   * `make dev-web` run. So the control is not drawn at all rather than drawn
   * and refused when pressed — typing the path is how it is done there, and
   * it must not look like a fallback from something broken.
   */
  onBrowse?: () => void
  onCancel: () => void
  onSubmit: (value: string) => void
}

export function CollectionDialog(props: CollectionDialogProps) {
  const trimmed = () => props.value.trim()

  /**
   * The refusal as the kit wants it — a string when there is one and
   * `undefined` when there is not.
   *
   * '' is "nothing was refused", and it must not reach TextField as an empty
   * error: the kit marks a field `aria-invalid` whenever `error` is DEFINED,
   * so a blank string would announce every fresh answer as wrong.
   */
  const refusal = (): string | undefined => (props.error !== '' ? props.error : undefined)

  const submit = (): void => {
    // The backend refuses a blank and so does this: a call that could be
    // sent and refused is a round trip spent to learn what the form already
    // knew (name-colour-dialog.tsx keeps the same rule).
    if (trimmed() === '' || props.busy) return
    props.onSubmit(trimmed())
  }

  return (
    <Dialog
      open={props.open}
      title={props.title}
      onClose={props.onCancel}
      onSubmit={submit}
      footer={
        <>
          <Button variant="default" onClick={props.onCancel}>
            Cancel
          </Button>
          <Button variant="primary" disabled={trimmed() === '' || props.busy} onClick={submit}>
            {props.submitLabel}
          </Button>
        </>
      }
    >
      {/* The field and its picker are ONE control, so the picker sits in the
          kit's trailing slot rather than beside the field. Beside it, the
          button was aligned to the input by a hand-measured top margin that
          assumed a label and nothing under it; this field also carries a
          description, so the button floated up beside the sentence. The kit
          positions the slot, and the path keeps the panel's whole width. */}
      <TextField
        id={props.fieldId}
        label={props.fieldLabel}
        description={props.fieldDescription}
        placeholder={props.placeholder}
        value={props.value}
        error={refusal()}
        onInput={props.onInput}
        autoFocus
        required
        trailing={
          props.onBrowse ? (
            <IconButton
              size="sm"
              ariaLabel="Browse…"
              title="Choose a folder with the system picker"
              onClick={() => props.onBrowse?.()}
            >
              <FolderOpenIcon />
            </IconButton>
          ) : undefined
        }
      />
    </Dialog>
  )
}
