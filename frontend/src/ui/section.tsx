/**
 * Section — titled grouping of controls or content in a view.
 *
 * Justified by callers:
 * - settings.ts: div.st-section > h2.st-section-heading + rows
 * - connections.ts: div.cm-form-section > h2 + fields
 * - export-section.ts: div.st-export wrapper with heading + description
 * - git-panel.tsx: the Staged / Unstaged / Commits groups (nocx-nak2)
 *
 * Children are spaced by the Stack primitive (one source of truth for vertical
 * rhythm). No `class` passthrough — see page-section.tsx for why the structural
 * containers stopped accepting one.
 *
 * Collapsible variant (nocx-nak2): the title renders inside a disclosure
 * button — the section keeps its accessible name — and the body folds away
 * while `open` is false. The state is CONTROLLED: the caller owns it,
 * because a collapse must outlive the mount (the Git panel keeps it in the
 * store, which survives the panel unmounting — design §5.5). The disclosure
 * is a native button with `aria-expanded`, the same vocabulary TreeRow uses.
 */
import { Show, children, createMemo, type JSX } from 'solid-js'
import { Stack } from './stack'
import { ChevronDownIcon } from './icons'

export type SectionProps =
  | {
      id?: string
      title: string
      /** When true, forwards to the inner Stack to draw separators between children. */
      divided?: boolean
      /** Forwards the Stack's dense rhythm — a scanned list rather than a read form. */
      dense?: boolean
      /** The discriminant: absent or false on the plain section, which
       *  renders exactly as it always has. */
      collapsible?: false
      /** Controls on the heading's row, at its trailing end — the actions
       *  that belong to the GROUP rather than to any row in it. */
      actions?: JSX.Element
      children: JSX.Element
    }
  | {
      id?: string
      title: string
      divided?: boolean
      dense?: boolean
      /** The collapsible variant: the heading becomes the disclosure and the
       *  body hides while `open` is false. Required with the pair — a
       *  disclosure without a state owner would lie about what it toggles. */
      collapsible: true
      open: boolean
      onToggle: () => void
      /** Controls on the heading's row, at its trailing end. They sit BESIDE
       *  the disclosure and never inside it: a button inside the button that
       *  folds the section would fold it on the way to being pressed. */
      actions?: JSX.Element
      children: JSX.Element
    }

type CollapsibleSectionProps = Extract<SectionProps, { collapsible: true }>

export function Section(props: SectionProps) {
  // Read ONCE, through the kit's own helper: a JSX element handed to a prop
  // is a getter, and every access rebuilds the subtree it describes — the
  // defect tabs.tsx paid for with a hung tab.
  const actions = children(() => props.actions)
  return (
    <Show
      when={props.collapsible === true}
      fallback={
        // The plain render, deliberately untouched: a section that is not
        // collapsible renders exactly as it always has (no button, no
        // data-*). The Show's `when` is the one read of the discriminant.
        <section id={props.id} class="ui-section" data-dense={props.dense ? 'true' : undefined}>
          <h2>
            {props.title}
            <Show when={actions()}>
              <span class="ui-section__actions">{actions()}</span>
            </Show>
          </h2>
          <Stack gap="default" divided={props.divided} dense={props.dense}>
            {props.children}
          </Stack>
        </section>
      }
    >
      {/* The `when` above guarantees this branch is the collapsible variant. */}
      <CollapsibleSection {...(props as CollapsibleSectionProps)} />
    </Show>
  )
}

/** The collapsible branch, typed at the variant: reads `open` reactively, so
 *  a caller re-rendering with a new open state folds or unfolds the body. */
function CollapsibleSection(props: CollapsibleSectionProps) {
  const actions = children(() => props.actions)
  // The body the disclosure controls; only present when the caller gave the
  // section an id, so aria-controls never dangles.
  const bodyId = createMemo(() => (props.id !== undefined ? `${props.id}-body` : undefined))
  return (
    <section
      id={props.id}
      class="ui-section"
      data-dense={props.dense ? 'true' : undefined}
      data-disclosure={props.open ? 'expanded' : 'collapsed'}
    >
      <h2>
        <button
          type="button"
          class="ui-section__disclosure"
          aria-expanded={props.open}
          aria-controls={bodyId()}
          onClick={() => props.onToggle()}
        >
          <span class="ui-section__disclosure-icon" aria-hidden="true">
            <ChevronDownIcon />
          </span>
          {props.title}
        </button>
        <Show when={actions()}>
          <span class="ui-section__actions">{actions()}</span>
        </Show>
      </h2>
      <Show when={props.open}>
        <Stack id={bodyId()} gap="default" divided={props.divided} dense={props.dense}>
          {props.children}
        </Stack>
      </Show>
    </section>
  )
}
