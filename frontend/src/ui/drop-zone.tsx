// A surface that accepts a native window drop, as the KIT's.
//
// It owns the AFFORDANCE and the two attributes the backend reads, and
// nothing about what a drop MEANS: `data-file-drop-target` names the
// surface, `data-session-id` names the tab, Wails hands Go every attribute
// of the element the drop landed on, and the answer comes back as a
// `files.dropped` notification the caller subscribes to. So there is no
// `onDrop` here — the drop does not become a DOM event with a path in it,
// and a callback would be a promise this component cannot keep.
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
// and is handed it as `onPick`. It is the caller's own handler, never a
// second one: this component knows a drop surface, not what is being
// dropped.
//
// NO SESSION DRAWS NO TARGET, and now no region either. `SourceTicketStore.Dropped`
// refuses a target that names no open session, so advertising a drop surface
// without one advertises a gesture that will be refused. Absence is the
// capability, which is the rule the dialog's pickers already follow.

import { Show, createSignal } from 'solid-js'
import type { JSX } from 'solid-js'
import { Button } from './button'
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
  /** The session the drop belongs to, or null — null draws NO drop target. */
  sessionId: string | null
  /** The line naming the gesture. Drawn at rest, not only under a drag. */
  hint: string
  /** The words on the control that opens the caller's picker. */
  pickLabel?: string
  /** Open the caller's picker — the other way to answer the same question.
   *  Absent draws no control: a picker is a capability of its own and can be
   *  missing while the drop is not. */
  onPick?: () => void
  children: JSX.Element
}

export function DropZone(props: DropZoneProps) {
  const [active, setActive] = createSignal(false)
  const live = () => props.sessionId !== null
  let zone!: HTMLDivElement

  return (
    <div
      ref={zone}
      class="ui-drop-zone"
      data-file-drop-target={live() ? props.target : undefined}
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
      onDrop={() => setActive(false)}
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
        </div>
      </Show>
      {props.children}
    </div>
  )
}
