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

import { Show } from 'solid-js'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
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
        description="Read, never executed. What the format cannot carry is named afterwards rather than dropped."
        placeholder="/work/acme.postman_collection.json"
        value={props.file}
        onInput={props.onFile}
        autoFocus
        required
      />
      {/* The field and its picker read as one control, so they sit on one
          row — placement, which is a surface's business; both are the kit's
          own and nothing here repaints them. */}
      <div class="api-path-row">
        <TextField
          id="api-import-postman-dest"
          label="New collection folder"
          description="A folder that does not exist yet. The import arrives whole or not at all."
          placeholder="/work/acme-api"
          value={props.dest}
          error={refusal()}
          onInput={props.onDest}
          required
        />
        <Show when={props.onBrowse}>
          <Button
            variant="default"
            onClick={() => props.onBrowse?.()}
            ariaLabel="Browse…"
            title="Choose a folder with the system picker"
          >
            Browse…
          </Button>
        </Show>
      </div>
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
