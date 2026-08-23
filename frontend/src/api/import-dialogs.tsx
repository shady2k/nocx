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
import { DropZone } from '../ui/drop-zone'
import { IconButton } from '../ui/icon-button'
import { FolderOpenIcon } from '../ui/icons'
import { TextField } from '../ui/text-field'

/** This ask's name in `data-file-drop-target`. Exported so the element that
 *  carries it and the subscriber that filters on it read one constant — two
 *  string literals is how they drift apart. */
export const API_IMPORT_DROP_TARGET = 'api-import'

export interface PostmanImportDialogProps {
  open: boolean
  /** The export to read, and the folder to make from it. */
  file: string
  dest: string
  onFile: (value: string) => void
  onDest: (value: string) => void
  /** The place collections go, as the backend gave it, or '' where this
   *  build has none. The ask needs it to recognise its own proposal: the
   *  field opens holding it, and the root by itself names a folder that is
   *  already there. */
  defaultRoot: string
  /** The local session a NATIVE drop on this ask belongs to, or null.
   *
   *  It addresses the Wails window's own drop and nothing else: Go reads the
   *  session off the dropped-on element, and a target naming none is refused.
   *  Null everywhere there is no Wails runtime — and the ask is not
   *  diminished by it, because the browser's drop needs no session at all
   *  (`onFiles` below). The ask decides neither half: which local tab is open
   *  is the pane manager's, and whether Go can take a drop is the build's. */
  dropSession: string | null
  /** True where this window hands drops to the BACKEND rather than to the
   *  DOM — the Wails webview. It is not a build question the ask asks: it is
   *  the capability it was handed, and the composition root read the runtime
   *  once when it decided whether to hand one over (main.tsx). The kit needs
   *  it to know whether a DOM drop would answer a gesture Go has already
   *  taken (drop-zone.tsx). */
  nativeWindow: boolean
  /** The files a BROWSER drop or the region's file input yielded — the
   *  general route, because bytes reach the backend wherever it runs while a
   *  path names a file on the backend's own machine (spec §1a). Every file is
   *  handed over; refusing several is this ask's own rule and is stated in
   *  ONE place, beside the native half's refusal. */
  onFiles?: (files: File[]) => void
  /** The backend's reason the last attempt was refused, or ''. */
  error: string
  busy: boolean
  /** Open the native directory picker for the DESTINATION. Absent wherever
   *  there is no Wails runtime — see CollectionDialog, same rule. */
  onBrowse?: () => void
  /** Open the native FILE picker for the EXPORT. Absent for the same reason
   *  and independently of `onBrowse`: they are two `dialog.*` methods and
   *  either can be missing on its own, so the ask draws two controls, one
   *  control or none rather than treating them as one capability. */
  onBrowseFile?: () => void
  onCancel: () => void
  onSubmit: () => void
}

export function PostmanImportDialog(props: PostmanImportDialogProps) {
  /** The root the ask proposed, with or without its trailing separator —
   *  the two values `askForImport` can leave in the field before anybody
   *  has said anything. */
  const isBareRoot = (value: string): boolean => {
    if (props.defaultRoot === '') return false
    const root = props.defaultRoot.replace(/[\\/]+$/, '')
    return value === root || value === `${root}/`
  }

  // A blank was already refused here because a call that could only be
  // refused is a round trip spent to learn what the form knew. The prefill
  // introduces a second value of exactly that kind: the collections root
  // certainly exists, so Import on it comes back "a folder is already
  // there" about a folder the person never chose.
  const ready = () => {
    const dest = props.dest.trim()
    return props.file.trim() !== '' && dest !== '' && !isBareRoot(dest) && !props.busy
  }
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
      {/* THE ASK IS A DROP TARGET, AND SAYS SO AT REST. It is the BODY that carries the
          attributes rather than the `<dialog>`: the kit's Dialog does not
          forward arbitrary `data-*`, and reaching past it to stamp them on
          the element itself would be the repaint rule in another form. The
          zone owns the affordance and none of the meaning — what a dropped
          file means to this ask is answered by the subscriber in
          api-pane.tsx, which is the same code path the export picker
          already goes through.

          BOTH HALVES ARE HANDED IN AND THE ASK CHOOSES NEITHER. `dropSession`
          addresses the native drop and `onFiles` takes the browser's bytes;
          which of them can answer is the kit's question, asked once inside
          DropZone, and asking it a second time here would be a second answer
          to it. The region is drawn wherever
          EITHER can — which is everywhere, and it used not to be: gated on a
          Wails runtime and a live terminal session, the ask drew nothing at
          all under `make dev-web` (nocx-1gfbw).

          Its picker control is handed `onBrowseFile` — the very handler the
          export field's trailing button below calls. Two controls, ONE
          derivation of "choose an export": a second one would agree with the
          first everywhere anybody looked and disagree about the proposed
          destination somewhere nobody did. Absent (no Wails file picker) the
          region offers the kit's own file input instead, whose answer is a
          FILE and therefore goes to `onFiles`, where the drop goes. */}
      <DropZone
        target={API_IMPORT_DROP_TARGET}
        sessionId={props.dropSession}
        hint="Drop a Postman export here to import it"
        pickLabel="Or select a file"
        // What the browser picker offers. The export is a JSON document
        // (design §10, Postman v2.1), and it bounds the PICKER only — a drop
        // can carry anything, which the backend refuses on its own terms.
        accept="application/json,.json"
        native={props.nativeWindow}
        onPick={props.onBrowseFile}
        onFiles={props.onFiles}
      >
        {/* THE EXPORT HAS A PICKER TOO, and it is the same control in the same
          slot as the destination's below. Without it this field named a
          file by PATH with no way to choose one: a person opened a
          terminal, found the export, copied its path and pasted it back —
          for a document they had just downloaded from Postman. The
          capability was there the whole time (`dialog.openFile`, used by
          Connections and Secrets for a private key); only the wiring was
          missing (nocx-6hg2w.15). */}
        <TextField
          id="api-import-postman-file"
          label="Postman v2.1 export"
          description="Read, never executed."
          placeholder="/work/acme.postman_collection.json"
          value={props.file}
          onInput={props.onFile}
          autoFocus
          required
          trailing={
            props.onBrowseFile ? (
              <IconButton
                size="sm"
                ariaLabel="Choose export…"
                title="Choose a Postman export with the system picker"
                onClick={() => props.onBrowseFile?.()}
              >
                <FolderOpenIcon />
              </IconButton>
            ) : undefined
          }
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
      </DropZone>
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
