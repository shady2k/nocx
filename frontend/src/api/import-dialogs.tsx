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
// asks — one takes a Postman export, however it arrives, and mints a
// COLLECTION; the other pastes a command line and mints ONE REQUEST into the
// form. What they share is Dialog, the kit's own, and the rules
// CollectionDialog already established and this file keeps: the ask stays
// open when the backend refuses, the reason is the backend's own sentence
// rendered in the kit's validation slot, and a blank answer is not worth a
// round trip.
//
// AND THE CURL ASK SAYS WHAT IT IS ABOUT TO REPLACE. It fills THE form —
// the only one — so a person with an open request holding unsaved edits was
// having them taken by an import that asked nothing (nocx-86wvw). The
// question itself is the store's, because the store is what detaches the
// form from its file and so is the only place that knows there is anything
// to lose; what belongs here is the standing fact, said before the person
// presses anything rather than only in the modal that catches them.
//
// The Postman ask no longer NAMES two paths. It asks one question — paste
// the export or a URL, or drop the file — and offers the destination rather
// than demanding it (nocx-ysyy2). The field ids are unchanged
// (`api-import-postman-file`, `api-import-postman-dest`, `api-import-curl`)
// even though two of them stopped being visible: they are how the surface —
// and every test — addresses these fields, and moving a field is not
// renaming it.

import { Show } from 'solid-js'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { DropZone } from '../ui/drop-zone'
import { Field } from '../ui/field'
import { IconButton } from '../ui/icon-button'
import { CloseIcon, FolderOpenIcon, PencilIcon } from '../ui/icons'
import { Select, type SelectOption } from '../ui/select'
import { TextField } from '../ui/text-field'
import type { ApiConnection } from './api-client'
import type { ApiRoute } from './api-model'

/** This ask's name in `data-file-drop-target`. Exported so the element that
 *  carries it and the subscriber that filters on it read one constant — two
 *  string literals is how they drift apart. */
export const API_IMPORT_DROP_TARGET = 'api-import'

export interface PostmanImportDialogProps {
  open: boolean
  /**
   * What is in the PASTE BOX — the ask's one question, and the entrance two
   * of the four sources arrive through (the export's text, and a URL).
   *
   * It is the box's contents rather than the source it yielded: what the
   * text MEANS is decided once, in `classifyPastedSource`, by the level that
   * also holds the other two entrances (api-pane.tsx). A dialog that
   * classified its own box would be the second derivation of "is this a
   * URL", which is the `ssh`-without-a-space defect in another costume.
   */
  pasted: string
  onPaste: (value: string) => void
  /** Why the pasted text is not a source, or '' — a curl line, or anything
   *  that is neither a URL nor a JSON document. Said HERE and not by the
   *  backend, which would hand it to the curl parser instead. */
  pasteRefusal: string
  /**
   * The ONE source the ask is holding, as a person recognises it — a path, a
   * dropped file's name, a URL — or '' while it holds none.
   *
   * A person who dropped the wrong file must be able to see which file the
   * ask is holding and take it back, and a second source visibly replacing
   * the first is what makes "exactly one is held" a fact on screen rather
   * than a rule in a comment (spec §2).
   */
  sourceLabel: string
  onClearSource: () => void
  /**
   * Whether the held source is a URL — the one source the BACKEND goes and
   * gets, and therefore the only one that travels anywhere. A path is read
   * where Go runs; a pasted export and a dropped file are already in hand.
   * Nothing that does not travel can be asked which connection it travels
   * through, which is why the picker below hangs off this and not off
   * "is this ask holding something".
   *
   * A boolean rather than the held source's KIND, because that union has one
   * owner (api-pane.tsx, `HeldSource`) and a second spelling of its members
   * here would be a second answer to what the ask is holding.
   */
  sourceIsURL: boolean
  /**
   * The connections a fetch may travel through, in the order the store holds
   * them. Handed in, never fetched: the workbench already reads them for the
   * environment editor, and an ask that read them a second time would be a
   * second owner of the same list.
   *
   * Empty is the ordinary state of a build with no profile store —
   * `listConnections` is optional and its absence IS the capability — and
   * the picker then offers the one answer there is.
   */
  connections: readonly ApiConnection[]
  /** How the fetch travels, as the route the import will carry. */
  route: ApiRoute
  onRoute: (route: ApiRoute) => void
  /** Where the export lands. Still the field's value, still typed into by
   *  the same handler — what changed is that it is only SHOWN once somebody
   *  asks to change it. */
  dest: string
  /** The path a native drop or the system picker answered with. It is no
   *  longer a field a person fills in; it is where the answer to a gesture
   *  lands, which is why it keeps its id and its handler. */
  file: string
  onFile: (value: string) => void
  onDest: (value: string) => void
  /** Whether the destination is open for editing — the pencil's state. The
   *  ask also opens it on its own when it has nothing to propose or
   *  something to refuse; see `editing` below. */
  editingDest: boolean
  onEditDest: () => void
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

  /** Whether the destination NAMES a folder yet — the same predicate `ready`
   *  submits on, read one more time by the summary, because a line that
   *  named the collections root would be naming the one destination Import
   *  is disabled for. */
  const nameable = (): boolean => {
    const dest = props.dest.trim()
    return dest !== '' && !isBareRoot(dest)
  }

  // A blank was already refused here because a call that could only be
  // refused is a round trip spent to learn what the form knew. The prefill
  // introduces a second value of exactly that kind: the collections root
  // certainly exists, so Import on it comes back "a folder is already
  // there" about a folder the person never chose.
  const ready = () => props.sourceLabel !== '' && nameable() && !props.busy
  const refusal = (): string | undefined => (props.error !== '' ? props.error : undefined)
  const pasteRefusal = (): string | undefined =>
    props.pasteRefusal !== '' ? props.pasteRefusal : undefined

  /**
   * WHEN THE DESTINATION IS A FIELD RATHER THAN A SENTENCE.
   *
   * The pencil is one of three reasons and the other two are not the
   * person's. A REFUSAL has to be readable: the backend's sentence is
   * rendered in this field's validation slot — that is where every refusal
   * in this ask has always been said — and a soft degrade nobody can see is
   * the failure AGENTS.md names, so a collapsed line may not swallow it. And
   * a source that proposes NOTHING (spec §3 — a share link with no usable
   * segment) leaves a summary with nothing to summarise, so the line opens
   * as the empty required field it would otherwise be hiding.
   */
  const editing = (): boolean =>
    props.editingDest || refusal() !== undefined || (props.sourceLabel !== '' && !nameable())

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
      {/* ONE QUESTION ACROSS THE TOP. The ask used to be a form of two
          ABSOLUTE PATHS — a file the person downloaded thirty seconds ago,
          and a folder that does not exist yet — neither of which can be
          answered without leaving the app. Both are gone from the surface:
          this box takes the export's TEXT or a URL, the region below takes
          the file itself, and the destination is an offer further down
          rather than a question (nocx-ysyy2).

          It does not decide what it is holding. `classifyPastedSource` does,
          once, for the ask and the call alike (api-paths.ts). */}
      <TextField
        id="api-import-paste"
        label="Paste a Postman export or a URL"
        description="Read, never executed."
        multiline
        value={props.pasted}
        error={pasteRefusal()}
        onInput={props.onPaste}
        autoFocus
      />
      {/* AND, FOR A URL, WHICH CONNECTION IT TRAVELS THROUGH. Only a URL is
          FETCHED, so only a URL has a route: the other three sources are
          already where the backend can read them. The picker appears with
          the URL and goes again with it, because a control that outlived the
          source it governs would be offering an answer to a question nobody
          is being asked.

          The grammar is environment-view.tsx's, deliberately — "Direct"
          plus one option per connection, the id as the value and the name as
          the label, in the store's order. Where there are no connections
          only Direct is drawn, exactly as the route control next door
          reduces to "This machine": one vocabulary for one concept, because
          a second spelling of "route through a connection" is two answers
          that agree until they do not. */}
      <Show when={props.sourceIsURL}>
        <Field for="api-import-route" label="Fetch through">
          <Select
            id="api-import-route"
            ariaLabel="The connection this fetch goes through"
            value={props.route.kind === 'connection' ? props.route.profileId : ''}
            onChange={(profileId) =>
              props.onRoute(
                profileId === ''
                  ? { kind: 'direct', profileId: '', insecureTls: false }
                  : { kind: 'connection', profileId, insecureTls: false },
              )
            }
            options={props.connections.map((c) => ({ value: c.id, label: c.name }))}
            placeholder="Direct"
            placeholderValue=""
          />
        </Field>
      </Show>
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

          Its picker control is handed `onBrowseFile`, and it is now the ONLY
          control that opens the system picker. It used to be one of two, the
          other being a trailing button on the export field below; the field
          stopped being visible with the reshape, so keeping its button would
          have kept a control nobody can reach for a capability the region
          already offers. Two controls, ONE derivation of "choose an export",
          is still the rule — there is simply one control left to state it.
          Absent (no Wails file picker) the region offers the kit's own file
          input instead, whose answer is a FILE and therefore goes to
          `onFiles`, where the drop goes. */}
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
        {/* WHERE A PATH LANDS, AND NOT A FIELD ANY MORE. Hidden, present and
            still addressed by its own id: a native drop and the system
            picker both answer with a PATH, that answer has to live
            somewhere, and every drop test names this id to read what the
            gesture left. Moving a field is not renaming it. What a person
            sees instead is the source line below, which says the same thing
            in the currency they recognise and can take back. */}
        <div hidden>
          <TextField
            id="api-import-postman-file"
            label="Postman v2.1 export"
            description="Read, never executed."
            value={props.file}
            onInput={props.onFile}
          />
        </div>
      </DropZone>
      {/* WHAT THE ASK IS HOLDING, AND HOW TO TAKE IT BACK. Exactly one source
          at a time (spec §2): a person who dropped the wrong file has to be
          able to see which file that was, and an ask that could hold two
          would go on displaying the loser. */}
      <Show when={props.sourceLabel !== ''}>
        <p class="api-import-source">
          {props.sourceLabel}
          <IconButton
            size="sm"
            ariaLabel="Forget this source"
            title="Forget this source"
            onClick={props.onClearSource}
          >
            <CloseIcon />
          </IconButton>
        </p>
      </Show>
      {/* THE DESTINATION IS AN OFFER, SO IT IS A SENTENCE UNTIL SOMEBODY
          DISAGREES. It was a required absolute path for a folder that does
          not exist yet, asked before the person had said what they were
          importing. The field is still the truth and still carries every
          refusal; the pencil is what puts it back on screen. */}
      <Show when={!editing()}>
        <p class="api-import-dest">
          {props.sourceLabel !== '' && nameable()
            ? `Imports into: ${props.dest.trim()}`
            : 'Choose where this goes'}
          <IconButton
            size="sm"
            ariaLabel="Change where this goes"
            title="Change where this goes"
            onClick={props.onEditDest}
          >
            <PencilIcon />
          </IconButton>
        </p>
      </Show>
      {/* The picker sits INSIDE the field, in the kit's trailing slot, because
          the field and its picker are one control. Beside it, they were two:
          the button was aligned to the input by a hand-measured margin that
          assumed a label and nothing else, so a field that also carries a
          description — this one — floated its button up beside the sentence
          instead. A slot the kit positions cannot come apart that way, and on
          a 480px panel it gives the path the whole width. */}
      <div hidden={!editing()}>
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
  /**
   * WHERE THE REQUEST WILL LAND — a folder inside the active collection, ''
   * being its root.
   *
   * The ask is the one moment this request's destination is on screen, and
   * before it existed every imported curl line went to the collection's root
   * and had to be moved by hand (nocx-8aczn.10). It is OFFERED rather than
   * demanded, the way the Postman ask offers its destination: it arrives
   * holding the folder the person is standing in, so an answer is only
   * needed by somebody who disagrees.
   *
   * Nothing is written when this is answered. The conversion is a value and
   * not a file (design §10); this rides with the draft and is spent at Save.
   */
  dest: string
  onDest: (value: string) => void
  /** Every folder of the active collection, as paths within it — the same
   *  list the move chooser offers, and the collection's own `folders`. */
  folders: readonly string[]
  /** The active collection's name, for the root option's label. '' when
   *  there is no collection open, and then there is no destination control
   *  at all: nothing to choose, and Save would refuse anyway. */
  collectionName: string
}

export function CurlImportDialog(props: CurlImportDialogProps) {
  const ready = () => props.value.trim() !== '' && !props.busy
  const refusal = (): string | undefined => (props.error !== '' ? props.error : undefined)

  /** The collection's root, then its folders in the order the listing gave.
   *  The root is named after the COLLECTION rather than called "root": a
   *  person choosing where a request goes is choosing between places they
   *  can see in the tree, and "Playground" is what that place is called
   *  there. */
  const destinations = (): SelectOption[] => [
    { value: '', label: props.collectionName },
    ...props.folders.map((path) => ({ value: path, label: path })),
  ]

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
        description="Parsed, never executed — there is no shell behind this field. It fills the form, replacing what is open there; nothing is written until the request is saved. A request with unsaved changes is not replaced without asking."
        multiline
        value={props.value}
        error={refusal()}
        onInput={props.onInput}
        autoFocus
        required
      />
      {/* WHERE IT GOES. Absent with no collection open, because there is
          then nothing to choose between and a picker offering one dead
          option is a control that governs nothing. The kit's Select owns
          "one of N"; this places it in a Field, which is what gives it its
          visible label. */}
      <Show when={props.collectionName !== ''}>
        <Field for="api-import-curl-dest" label="Save it in">
          <Select
            id="api-import-curl-dest"
            value={props.dest}
            onChange={props.onDest}
            options={destinations()}
          />
        </Field>
      </Show>
    </Dialog>
  )
}
