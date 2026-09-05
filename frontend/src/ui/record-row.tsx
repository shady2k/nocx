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
 *   kind    the record's category badge — a typed {label, tone, description?}, NOT a JSX
 *           element, so a second badge is structurally impossible. One kind,
 *           one badge; a second badge is not one of the slots. It is drawn
 *           BESIDE THE TITLE (nocx-6jc4f), not at the head of the meta line
 *           where it used to sit: what kind of record this is is what the
 *           name says, and a badge in front of the meta text reads as the
 *           first clause of the record's own sentence.
 *   meta    the record's descriptive line (address, model count, path).
 *   status  the record's current state, rendered as the kit's StatusDot +
 *           text — never a badge. A state that has no colour to say uses the
 *           neutral tone.
 *   detail  ONE line of verbatim evidence under the meta line — the last line
 *           a pane printed, the reason a check failed. It is the record's own
 *           words rather than the composite's, so it is typed as a string and
 *           rendered as text: a JSX slot here would be the free-form `info`
 *           coming back through the side door. Added for the workspace
 *           overview (nocx-edhcu), whose whole argument is that a card is text
 *           — the alternative was a second row grammar beside this one, which
 *           is exactly what this composite exists to prevent.
 *
 *   state   the control that carries the record's own state — the switch that
 *           enables a skill, and nothing that ACTS on the record. It is a slot
 *           rather than a typed value because a state control is a control and
 *           the kit does not know which one; what the kit owns is its PLACE,
 *           which is a cell of its own at the row's trailing edge (nocx-xa0cq).
 *           A row's state and a row's actions are two kinds of thing, and this
 *           is the geometry saying so.
 *
 * Why the state cannot simply live in `actions`, given that it is a control
 * like the others: `actions` is free-form on purpose, so its contents decide
 * each other's positions and the kit can reserve nothing inside it. The Skills
 * list put its enable switch first in the group and the switch then sat at
 * three different distances from the row's edge — no buttons, Delete, or
 * Re-approve and Delete — down a list a person reads by scanning. Reordering
 * to put the switch last is the cheap alternative and it is refused: a control
 * is anchored to the edge only for as long as it is last, so the next
 * conditional button hands the raggedness to whatever now precedes it, which
 * on that page is the destructive one. A ragged switch is a poor thing; a
 * Delete that moves between rows is a worse one.
 *
 * `actions` stays free-form (a row's controls are the surface's decision).
 * The genuinely free-form `info` slot on CollectionRow survives for rows
 * this composite does not describe — Secrets' glyph + two-line body rows,
 * and the Git panel's dense commit rows whose meta line carries several
 * ref badges — and only for those.
 *
 * Disclosure (nocx-ctl6q): a record can DISCLOSE what it stands for — the
 * notification feed's collapsed run opening to the occurrences inside it.
 * The state is CONTROLLED, as it is on Section: the caller owns `expanded`
 * and is told about `onToggle`, because a disclosure without a state owner
 * would lie about what it toggles. The vocabulary is the kit's existing one,
 * deliberately and to the letter — `data-disclosure` carrying
 * `expanded | collapsed | leaf`, a native button part with `aria-expanded`
 * in the leading slot, and the one chevron TreeRow and Section already turn.
 * A third word for one concept is how a kit stops being one.
 *
 * Three states, not two, and the third is the one worth writing down:
 *
 *   expandable absent   the row never heard of the disclosure. It renders
 *                       exactly as it did before this existed — no leading
 *                       slot at all — because a column reserved for a control
 *                       none of a list's rows can offer indents every one of
 *                       them for nothing. Connections, Endpoints, Footprint
 *                       and Notes are all this.
 *   expandable={false}  a leaf in a list that DOES disclose. It reserves the
 *                       disclosure's width, so titles still form one column —
 *                       TreeRow's leading slot, same reason.
 *   expandable          the button, and the caller's children while open.
 *
 * The Notifications panel is the pair: a row of one occurrence is the leaf,
 * a collapsed run is the button, and both sit in one list — which is the
 * case the middle state exists for.
 *
 * What is disclosed is the caller's, passed as the children slot: the kit
 * decides the geometry and never what goes inside. Both the disclosure and
 * the disclosed region keep their own click and their own Enter/Space —
 * expanding is not opening, and a click that did both would make expansion
 * unreachable with a mouse.
 */
import { For, Show, children, type JSX } from 'solid-js'
import { Badge, type BadgeTone } from './badge'
import { CollectionRow } from './collection-view'
import { ChevronDownIcon } from './icons'
import { StatusDot, type StatusDotTone } from './status-dot'

export interface RecordRowProps {
  /** The record's name — its own line, body text. */
  title: string
  /** The record's category badge. At most one: the composite renders the
   *  badge from this typed slot, so a surface cannot pass a second one. */
  kind?: { label: string; tone?: BadgeTone; description?: string }
  /** The record's descriptive line, under the title and its kind badge. */
  meta?: string
  /** The record's current state: the kit's dot + text, never a badge. */
  status?: { tone: StatusDotTone; text: string }
  /** The record's own words, under the meta line: one line, or a few of them
   *  as an array — a pane's last output, a check's failing lines. Typed as
   *  strings, never a slot, so it stays the record's words and not a second
   *  free-form body — see the header. */
  detail?: string | readonly string[]
  actions: JSX.Element
  /** The control carrying the record's own state, in a cell of its own at the
   *  row's trailing edge — see the header for why it is not an action.
   *
   *  Three states, the disclosure's three: the prop ABSENT means this list has
   *  no row state at all and no cell is drawn, because a column reserved for a
   *  control none of a list's rows can offer holds width open on every one of
   *  them for nothing; anything PASSED draws the cell, including `null` for a
   *  row that has no state control beside rows that do — that row's buttons
   *  must stop where its neighbours' do, or the raggedness has only moved from
   *  the switch to the actions. */
  state?: JSX.Element
  /** Whether this row discloses anything. Absent means the row is not part of
   *  a disclosing list and reserves no width; `false` is a leaf beside rows
   *  that do expand, and holds the disclosure's width so titles align. */
  expandable?: boolean
  /** Expanded state — controlled by the caller; only read when expandable. */
  expanded?: boolean
  /** Called when the disclosure is activated (click, Enter, Space). */
  onToggle?: (e: MouseEvent) => void
  /** What the row discloses while expanded. The caller's, verbatim: the kit
   *  places it under the record's text and decides nothing about it. */
  children?: JSX.Element
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
  /** One string is one line; several are several. Blank lines are dropped —
   *  a row that spends its detail on emptiness reads as broken. */
  const detailLines = (): readonly string[] => {
    const detail = props.detail
    if (detail === undefined) return []
    const lines = typeof detail === 'string' ? [detail] : detail
    return lines.filter((line) => line.trim() !== '')
  }

  const expandable = () => props.expandable === true
  const expanded = () => expandable() && props.expanded === true
  const disclosure = () => (expandable() ? (expanded() ? 'expanded' : 'collapsed') : 'leaf')

  /** The row listens for Enter and Space (CollectionRow), and both the
   *  disclosure and whatever the caller disclosed sit inside it. Without
   *  this, pressing Enter on the chevron would expand the row AND open it,
   *  and the click a native button raises from that key would do it twice. */
  const keepOwnKeys = (e: KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') e.stopPropagation()
  }

  /** Resolved once and read twice — whether the cell exists, and what goes in
   *  it. A props slot is a getter, so reading it twice would build the
   *  caller's control twice and mount the second copy only. */
  const state = children(() => props.state)

  return (
    <CollectionRow
      // The state cell sits INSIDE CollectionRow's actions region rather than
      // beside it, because that region is the one an activatable row excludes
      // from its whole-row click: a switch that opened the record it toggles
      // would be the disclosure's bug over again. It goes after the caller's
      // actions so the row's right edge, and not the width of the buttons, is
      // what decides where it sits.
      actions={
        <>
          {props.actions}
          <Show when={state() !== undefined}>
            <span class="ui-record-row__state">{state()}</span>
          </Show>
        </>
      }
      onActivate={props.onActivate}
      // The name below is the control (nocx-5xwub), so the row keeps the
      // click and hands the keyboard to it.
      activationInInfo={props.onActivate !== undefined}
      selected={props.selected}
      focused={props.focused}
      density={props.density}
      info={
        <div class="ui-record-row" data-disclosure={disclosure()}>
          <Show when={props.expandable !== undefined}>
            <span class="ui-record-row__leading">
              <Show when={expandable()}>
                <button
                  type="button"
                  class="ui-record-row__disclosure"
                  aria-expanded={expanded() ? 'true' : 'false'}
                  aria-label={expanded() ? `Collapse ${props.title}` : `Expand ${props.title}`}
                  onClick={(e: MouseEvent) => {
                    // The disclosure owns this click: expanding is not
                    // opening. Letting it reach the row would open the record
                    // every time somebody tried to look inside it.
                    e.stopPropagation()
                    props.onToggle?.(e)
                  }}
                  onKeyDown={keepOwnKeys}
                >
                  <span class="ui-record-row__disclosure-icon">
                    <ChevronDownIcon />
                  </span>
                </button>
              </Show>
            </span>
          </Show>
          <div class="ui-record-row__body">
            {/* THE NAME IS THE CONTROL when the record can be opened
                (nocx-5xwub). A row cannot announce the action itself: it is
                a `listitem` its list requires, and it holds real buttons of
                its own, so it can be neither a button nor the parent of one.
                The record's name can — and it needs no invented label,
                because `title` IS its accessible name.

                The click is stopped here so the whole-row shortcut on the
                row above does not fire the same activation a second time —
                the disclosure's reasoning, applied to the other control in
                the same row. */}
            {/* THE HEADING LINE: the record's name, and what kind of record
                it is (nocx-6jc4f). The kind badge used to open the META line,
                so a row read "name / [builtin] its own description" — the
                composite's word for the record wedged into the front of the
                record's own sentence, where a scanning eye reads it as the
                first clause of the description. It is not that: provenance,
                protocol, generation are all what the record IS, which is what
                the name says, so the badge belongs on the name's line. The
                meta line below is now the record's words alone. */}
            <div class="ui-record-row__heading">
              <Show
                when={props.onActivate !== undefined}
                fallback={<div class="ui-record-row__title">{props.title}</div>}
              >
                <button
                  type="button"
                  class="ui-record-row__title ui-record-row__open"
                  onClick={(e: MouseEvent) => {
                    e.stopPropagation()
                    props.onActivate?.(e)
                  }}
                >
                  {props.title}
                </button>
              </Show>
              <Show when={props.kind} keyed>
                {(kind) => (
                  <Badge tone={kind.tone ?? 'neutral'} title={kind.description}>
                    {kind.label}
                  </Badge>
                )}
              </Show>
            </div>
            <div class="ui-record-row__meta">
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
            <Show when={detailLines().length > 0}>
              <div class="ui-record-row__detail">
                <For each={detailLines()}>{(line) => <div>{line}</div>}</For>
              </div>
            </Show>
            <Show when={expanded()}>
              {/* What is inside the expansion is the caller's, including its
                  clicks and its keys — a row that opened itself when you
                  clicked one of its occurrences would take the pointer away
                  from the thing you aimed at. */}
              <div
                class="ui-record-row__disclosed"
                onClick={(e: MouseEvent) => e.stopPropagation()}
                onKeyDown={keepOwnKeys}
              >
                {props.children}
              </div>
            </Show>
          </div>
        </div>
      }
    />
  )
}
