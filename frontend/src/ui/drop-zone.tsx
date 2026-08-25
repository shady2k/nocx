// A surface that accepts a dropped file, as the KIT's — in both currencies a
// drop can arrive in.
//
// ## Two halves, and never both on one gesture
//
// **The native half.** Inside the Wails window the drop never becomes a DOM
// event we act on: the runtime hands Go the absolute paths, Go reads
// `data-file-drop-target` (this surface's NAME) and `data-session-id` (the
// tab) off the element the drop landed on, and the answer comes back as a
// `files.dropped` notification the caller subscribes to. So there is no path
// in a callback here and there cannot be one.
//
// **The browser half.** Everywhere else — `make dev-web`, the e2e harness,
// and any nocx reached over a forwarded port — a drop IS a DOM event, and it
// carries `File` objects with BYTES. That is `onFiles`, and it is the general
// case rather than the degraded one: bytes reach a backend wherever it runs,
// while a path names a file on the machine running Go and is right only when
// that machine is also the person's (spec §1a).
//
// `onFiles` is IGNORED where the window hands drops to the backend — the
// caller says so with `native` — and that is the whole of the
// double-handling rule: Go has already taken that gesture and is describing
// it, so acting on the DOM event too would answer one drop twice, once as a
// path and once as bytes. `terminal-drop.ts` states the same rule for the
// terminal pane.
//
// IT IS A PROP AND NOT A QUESTION THIS COMPONENT ASKS. `hasWailsWebview()`
// lives outside `ui/` and the kit may not import back into the app (the
// dependency direction rule); nor should it, because the answer already has
// an owner and a place where it is read: the composition root asks it once,
// when it decides whether this build gets a native drop capability at all
// (main.tsx). A second reading here would be a second answer to it.
//
// THE AFFORDANCE IS PERMANENT. It used to be drawn on dragover only, on the
// argument that a dashed box in a dialog already carrying two fields and a
// footer is a third thing competing for the same column. That argument was
// about the layout and forgot the person: the owner opened the finished ask
// in the Wails window and said the import had not changed at all (nocx-9hb5g),
// because the one new capability said nothing about itself until you were
// already doing it. A gesture nobody can discover is a gesture nobody
// performs, and 60px is a cheaper loss than the feature costing all of
// itself. The drag state stays — it is now a change of appearance rather
// than an appearance from nothing.
//
// The region offers the OTHER way to answer the same question — a picker —
// and which picker that is follows the same halves. `onPick` is the caller's
// NATIVE one (`dialog.openFile`, which answers a path) and wins where it
// exists; otherwise the browser half draws the kit's own FileInput, whose
// answer is a `File` and therefore goes to the very callback the drop goes
// to. One derivation of "here is the file", never two.
//
// WHERE NEITHER HALF CAN ANSWER, NOTHING IS DRAWN. `SourceTicketStore.Dropped`
// refuses a target that names no open session, so the native half needs one;
// the browser half needs a caller that can take bytes and a build outside the
// webview. With neither, the region would advertise a gesture that could not
// arrive. Absence is the capability, which is the rule the dialog's pickers
// already follow.

import { Show, createSignal } from 'solid-js'
import type { JSX } from 'solid-js'
import { Button } from './button'
import { FileInput } from './file-input'
// ArrowDownIcon reads as a file ARRIVING here: it is a direction rather than
// an object, which is what a drop is, and it is already this product's
// import mark — the collections menu's Import entry and the request form's
// import button both wear it (`api-pane.tsx`, `request-form.tsx`). The
// file-shaped glyphs are spoken for as file-TYPE marks in the tree
// (FileIcon, FileSymlinkIcon), and FolderOpenIcon means "choose with the
// system picker" on the very controls beside this one.
import { ArrowDownIcon } from './icons'

export interface DropZoneProps {
  /** This surface's name in `data-file-drop-target`. */
  target: string
  /** The session a NATIVE drop belongs to, or null — null draws no drop
   *  target, and the region then rests on the browser half alone. */
  sessionId: string | null
  /** The line naming the gesture. Drawn at rest, not only under a drag. */
  hint: string
  /** The words on the control that opens the caller's NATIVE picker. */
  pickLabel?: string
  /** Open the caller's native picker — the other way to answer the same
   *  question. Absent, the browser half offers the kit's file input instead;
   *  with neither there is no picker control at all, because a picker is a
   *  capability of its own and can be missing while the drop is not. */
  onPick?: () => void
  /**
   * TRUE WHERE THE WINDOW HANDS DROPS TO THE BACKEND rather than to the DOM
   * — the Wails webview, and nowhere else.
   *
   * The caller knows it from the capability it was handed (a native drop port
   * exists only where there is a runtime to deliver one), so this is passed
   * rather than asked: see the note at the top of this file. Default false,
   * because a plain browser is the general case and a caller that says
   * nothing is in one.
   */
  native?: boolean
  /**
   * The files a BROWSER drop or the kit's file input yielded.
   *
   * Every file is handed over, including a drop of several: what a second
   * file MEANS belongs to the caller — the import refuses it with a sentence
   * of its own, in the one place its native half already refuses one — and a
   * refusal here would be a second owner of that rule.
   *
   * Never called in a native window, for the reason at the top of this file.
   */
  onFiles?: (files: File[]) => void
  /** What the browser picker offers, as an `<input type=file>` accept list —
   *  the CALLER's, because what this surface takes is the caller's question
   *  and not the kit's. Absent offers everything. It bounds the picker only;
   *  a drop can carry anything, which is why the caller checks what it got. */
  accept?: string
  children: JSX.Element
}

export function DropZone(props: DropZoneProps) {
  const [active, setActive] = createSignal(false)
  /** The native half: a session for Go to attribute the drop to. */
  const nativeHalf = () => props.sessionId !== null
  /** The browser half: a caller that can take bytes, in a window where the
   *  DOM event is ours to act on. */
  const browserHalf = () => props.onFiles !== undefined && props.native !== true
  const live = () => nativeHalf() || browserHalf()
  let zone!: HTMLDivElement

  return (
    <div
      ref={zone}
      class="ui-drop-zone"
      data-file-drop-target={nativeHalf() ? props.target : undefined}
      // The region is drawn: what the stylesheet keys its layout on, because
      // EITHER half draws it and only the native one names a target.
      data-drop-live={live() ? '' : undefined}
      data-session-id={props.sessionId ?? undefined}
      data-drop-active={active() ? '' : undefined}
      onDragOver={(e: DragEvent) => {
        if (!live()) return
        // Preventing the default is what makes this a drop target at all;
        // without it the engine treats the drag as a navigation.
        e.preventDefault()
        setActive(true)
      }}
      onDragLeave={(e: DragEvent) => {
        // A leave that lands on our own descendant is not a leave: the region
        // now HAS descendants at rest — an icon, a line and a button — and
        // `dragleave` bubbles, so dragging across them would otherwise strobe
        // the state off and on the whole way over. `relatedTarget` is what the
        // pointer entered, and null (a synthetic event, or the window edge) is
        // a real leave.
        const entered = e.relatedTarget
        if (entered instanceof Node && zone.contains(entered)) return
        setActive(false)
      }}
      onDrop={(e: DragEvent) => {
        setActive(false)
        if (!browserHalf()) return
        const files = Array.from(e.dataTransfer?.files ?? [])
        // A drag carrying no file is not this surface's — text dragged out of
        // a field lands here too, and preventing the default on it would take
        // a gesture that was never ours.
        if (files.length === 0) return
        e.preventDefault()
        props.onFiles?.(files)
      }}
    >
      <Show when={live()}>
        <div class="ui-drop-zone__region">
          <span class="ui-drop-zone__icon">
            <ArrowDownIcon />
          </span>
          <span class="ui-drop-zone__hint">{props.hint}</span>
          <Show when={props.onPick !== undefined}>
            <Button size="sm" onClick={() => props.onPick?.()}>
              {props.pickLabel}
            </Button>
          </Show>
          {/* The browser's picker, and only where there is no native one:
              two controls asking one question is what the native half's
              `onPick` already avoids by being the caller's own handler. Its
              answer is a `File`, so it goes where the drop goes. */}
          <Show when={props.onPick === undefined && browserHalf()}>
            <FileInput
              accept={props.accept}
              ariaLabel={props.pickLabel ?? props.hint}
              buttonLabel={props.pickLabel}
              onChange={(file) => {
                if (file !== null) props.onFiles?.([file])
              }}
            />
          </Show>
        </div>
      </Show>
      {props.children}
    </div>
  )
}
