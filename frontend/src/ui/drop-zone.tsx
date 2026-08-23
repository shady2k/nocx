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
// NO SESSION DRAWS NO TARGET. `SourceTicketStore.Dropped` refuses a target
// that names no open session, so advertising a drop surface without one
// advertises a gesture that will be refused. Absence is the capability,
// which is the rule the dialog's pickers already follow.

import { Show, createSignal } from 'solid-js'
import type { JSX } from 'solid-js'

export interface DropZoneProps {
  /** This surface's name in `data-file-drop-target`. */
  target: string
  /** The session the drop belongs to, or null — null draws NO drop target. */
  sessionId: string | null
  /** What the zone says while a drag is over it. */
  hint: string
  children: JSX.Element
}

export function DropZone(props: DropZoneProps) {
  const [active, setActive] = createSignal(false)
  const live = () => props.sessionId !== null

  return (
    <div
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
      onDragLeave={() => setActive(false)}
      onDrop={() => setActive(false)}
    >
      {props.children}
      <Show when={live() && active()}>
        <div class="ui-drop-zone__hint" aria-hidden="true">
          {props.hint}
        </div>
      </Show>
    </div>
  )
}
