/**
 * RecordRow — the kit's composite for "describe a record in a row"
 * (nocx-pp3y.3).
 *
 * CollectionRow owns the row's FRAME (padding, gap, hover, selected,
 * density). What went inside it used to be a free-form `info` slot, so two
 * lists described one concept — a record — in two grammars:
 *
 *   connections   [kind Badge]  plain address text  [dot + status text]
 *   endpoints     [kind Badge]  plain models text   [status BADGE]
 *
 * Every part of both was legal kit usage, which is why no gate fired: each
 * surface then wrote CSS for the grammar it invented (.cm-item-name /
 * .cm-item-meta and .ep-item-name / .ep-item-meta). This component owns the
 * composite instead — a title, AT MOST ONE kind badge, meta text, and a
 * status as the kit's dot + text — so a surface cannot mix vocabularies,
 * because there is no slot to mix them in.
 *
 * The slots are named and typed:
 *
 *   title   the record's name, its own line.
 *   kind    the record's category badge — a typed {label, tone}, NOT a JSX
 *           element, so a second badge is structurally impossible. One kind,
 *           one badge; a second badge is not one of the slots.
 *   meta    the record's descriptive line (address, model count, path).
 *   status  the record's current state, rendered as the kit's StatusDot +
 *           text — never a badge. A state that has no colour to say uses the
 *           neutral tone.
 *
 * `actions` stays free-form (a row's controls are the surface's decision).
 * The genuinely free-form `info` slot on CollectionRow survives for rows
 * this composite does not describe — Secrets' glyph + two-line body rows,
 * and the Git panel's dense commit rows whose meta line carries several
 * ref badges — and only for those.
 */
import { Show, type JSX } from 'solid-js'
import { Badge, type BadgeTone } from './badge'
import { CollectionRow } from './collection-view'
import { StatusDot, type StatusDotTone } from './status-dot'

export interface RecordRowProps {
  /** The record's name — its own line, body text. */
  title: string
  /** The record's category badge. At most one: the composite renders the
   *  badge from this typed slot, so a surface cannot pass a second one. */
  kind?: { label: string; tone?: BadgeTone }
  /** The record's descriptive line, beside the kind badge. */
  meta?: string
  /** The record's current state: the kit's dot + text, never a badge. */
  status?: { tone: StatusDotTone; text: string }
  actions: JSX.Element
  /** Makes the row activatable — see CollectionRow. */
  onActivate?: (e: MouseEvent | KeyboardEvent) => void
  /** The caller's selection vocabulary — the row only renders it. */
  selected?: boolean
  /** The current keyboard target; reads stronger than selection. */
  focused?: boolean
  /** Row density — see CollectionRow. */
  density?: 'default' | 'dense'
}

/** The shared name/meta/status/actions record row inside a CollectionView. */
export function RecordRow(props: RecordRowProps) {
  return (
    <CollectionRow
      actions={props.actions}
      onActivate={props.onActivate}
      selected={props.selected}
      focused={props.focused}
      density={props.density}
      info={
        <>
          <div class="ui-record-row__title">{props.title}</div>
          <div class="ui-record-row__meta">
            <Show when={props.kind} keyed>
              {(kind) => <Badge tone={kind.tone ?? 'neutral'}>{kind.label}</Badge>}
            </Show>
            <Show when={props.meta}>
              <span class="ui-record-row__meta-text">{props.meta}</span>
            </Show>
            <Show when={props.status} keyed>
              {(status) => (
                <span class="ui-record-row__status" role="status" data-tone={status.tone}>
                  <StatusDot tone={status.tone} accessibleName={status.text}>
                    {status.text}
                  </StatusDot>
                </span>
              )}
            </Show>
          </div>
        </>
      }
    />
  )
}
