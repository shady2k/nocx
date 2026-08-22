// The two ways a request arrives from somewhere else, each as an ASK.
//
// Both used to be a form the panel WORE: a collapsible "Import" section in
// the sidebar holding a curl box, two path fields and two buttons — four
// controls and a disclosure, permanently occupying the column whose job is
// the tree. It is the same defect the folder field had before nocx-84shs
// (collection-dialog.tsx says it): a panel that wears a form is a panel
// asking you to fill something in before it will show you anything.
//
// They are two components rather than one because they are two different
// asks — one names two paths and mints a COLLECTION, the other pastes a
// command line and mints ONE REQUEST into the form. What they share is
// Dialog, the kit's own, and the rules CollectionDialog already established
// and this file keeps: the ask stays open when the backend refuses, the
// reason is the backend's own sentence rendered in the kit's validation
// slot, and a blank answer is not worth a round trip.
//
// The field ids are unchanged (`api-import-postman-file`,
// `api-import-postman-dest`, `api-import-curl`): they are how the surface —
// and every test — addresses these fields, and moving a field is not
// renaming it.

import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { IconButton } from '../ui/icon-button'
import { FolderOpenIcon } from '../ui/icons'
import { TextField } from '../ui/text-field'

export interface PostmanImportDialogProps {
  open: boolean
  /** The export to read, and the folder to make from it. */
  file: string
  dest: string
  onFile: (value: string) => void
  onDest: (value: string) => void
  /** The backend's reason the last attempt was refused, or ''. */
  error: string
  busy: boolean
  /** Open the native directory picker for the DESTINATION. Absent wherever
   *  there is no Wails runtime — see CollectionDialog, same rule. */
  onBrowse?: () => void
  onCancel: () => void
  onSubmit: () => void
}

export function PostmanImportDialog(props: PostmanImportDialogProps) {
  const ready = () => props.file.trim() !== '' && props.dest.trim() !== '' && !props.busy
  const refusal = (): string | undefined => (props.error !== '' ? props.error : undefined)

  const submit = (): void => {
    if (!ready()) return
    props.onSubmit()
  }

  return (
    <Dialog
      open={props.open}
      title="Import collection"
      onClose={props.onCancel}
      onSubmit={submit}
      footer={
        <>
          <Button variant="default" onClick={props.onCancel}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!ready()} onClick={submit}>
            Import
          </Button>
        </>
      }
    >
      <TextField
        id="api-import-postman-file"
        label="Postman v2.1 export"
        description="Read, never executed."
        placeholder="/work/acme.postman_collection.json"
        value={props.file}
        onInput={props.onFile}
        autoFocus
        required
      />
      {/* The picker sits INSIDE the field, in the kit's trailing slot, because
          the field and its picker are one control. Beside it, they were two:
          the button was aligned to the input by a hand-measured margin that
          assumed a label and nothing else, so a field that also carries a
          description — this one — floated its button up beside the sentence
          instead. A slot the kit positions cannot come apart that way, and on
          a 480px panel it gives the path the whole width. */}
      <TextField
        id="api-import-postman-dest"
        label="New collection folder"
        description="Must not exist yet — the import arrives whole or not at all."
        placeholder="/work/acme-api"
        value={props.dest}
        error={refusal()}
        onInput={props.onDest}
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

export interface CurlImportDialogProps {
  open: boolean
  value: string
  onInput: (value: string) => void
  error: string
  busy: boolean
  onCancel: () => void
  onSubmit: () => void
}

export function CurlImportDialog(props: CurlImportDialogProps) {
  const ready = () => props.value.trim() !== '' && !props.busy
  const refusal = (): string | undefined => (props.error !== '' ? props.error : undefined)

  const submit = (): void => {
    if (!ready()) return
    props.onSubmit()
  }

  return (
    <Dialog
      open={props.open}
      title="Import a curl command"
      onClose={props.onCancel}
      onSubmit={submit}
      footer={
        <>
          <Button variant="default" onClick={props.onCancel}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!ready()} onClick={submit}>
            Convert to a request
          </Button>
        </>
      }
    >
      <TextField
        id="api-import-curl"
        label="curl command line"
        description="Parsed, never executed — there is no shell behind this field. It fills the form; nothing is written until the request is saved."
        multiline
        value={props.value}
        error={refusal()}
        onInput={props.onInput}
        autoFocus
        required
      />
    </Dialog>
  )
}
