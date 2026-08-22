/**
 * OperationRow — one line of background work: what it is, how far along,
 * how it ended, and the control that stops it. Composed on the kit's
 * `CollectionRow` in its dense variant, the way `FileStatusRow` is — its
 * home is a sidebar-width panel.
 *
 * ## Why it is here and not in the Files panel
 *
 * It was there, as `.files-upload-row`, and the panel was the only place a
 * running transfer could be seen: switch sidebar view or collapse the panel
 * and a 2 GB upload became invisible and uncancellable while it went on
 * running. The list moved to the activity bar's operations indicator
 * (nocx-hbdw4), and the row moved into the kit rather than being copied
 * there — two surfaces drawing one concept is what two epics in this
 * repository were spent unwinding.
 *
 * ## What this component owns
 *
 * The OUTCOME VOCABULARY, exactly as FileStatusRow owns the git status
 * letters: which phases are worth a badge, what each is called, and what
 * tone it reads in. A surface passes the wire's phase and this module
 * decides what that means; it never passes a label or a tone.
 *
 * Two decisions inside that are worth naming:
 *
 * - `unsettled` is on the LIVE side of the split, not the finished side.
 *   The renderer lost sight of the operation, the backend may be finishing
 *   it right now, and cancelling still reaches it — so the cancel stays and
 *   the badge says we are waiting rather than that it failed.
 * - `cancelled` and `skipped` are NOT failures. A cancelled transfer's
 *   underlying error is a context cancellation and a skipped one is the
 *   person's own decision, so neither is danger.
 *
 * ## What the caller owns
 *
 * Whether there is a cancel at all. `onCancel` absent means cancelling this
 * operation would mean nothing any more — which is the model's judgement
 * about that operation, not a rendering rule this component could derive.
 */
import { Show, type Component } from 'solid-js'
import { Badge, type BadgeTone } from './badge'
import { Button } from './button'
import { CollectionRow } from './collection-view'
import { formatFinished, formatProgress } from './format-bytes'
import { formatTimestamp } from './format-time'
import { ArrowUpIcon } from './icons'
import { ProgressBar } from './progress-bar'
import {
  isTerminalPhase,
  type OperationKind,
  type OperationPhase,
  type TerminalOperationPhase,
} from './operation'

/** The kind's decorative glyph, decided here and never supplied by a
 *  surface — the same rule TreeRow follows for its type glyphs. Download
 *  joins as one more entry. */
const KIND_ICON: Record<OperationKind, Component> = {
  upload: ArrowUpIcon,
}

/** How a finished operation reads. See the module doc for why `cancelled`
 *  and `skipped` are neutral rather than danger. */
const PHASE_TONE: Record<TerminalOperationPhase, BadgeTone> = {
  written: 'success',
  skipped: 'neutral',
  cancelled: 'neutral',
  failed: 'danger',
}

/** What a finished operation is called. `written` reads "Done" rather than
 *  "Uploaded" because one row serves every kind: the glyph and the title
 *  already say what was done, and a per-kind success word would be a second
 *  table indexed by two things for one cell. */
const PHASE_LABEL: Record<TerminalOperationPhase, string> = {
  written: 'Done',
  skipped: 'Skipped',
  cancelled: 'Cancelled',
  failed: 'Failed',
}

export interface OperationRowProps {
  kind: OperationKind
  /** What the operation is about, as the person named it — the file. */
  title: string
  /** Where it is going. Rendered dimmed on its OWN line under the numbers,
   *  never beside the title — a path outruns any panel and would take the
   *  name's room with it. Empty draws nothing rather than an empty line. */
  destination?: string
  /** WHICH MACHINE the destination is on, as a person names it. It shares
   *  the destination's line and reads BEFORE the path, because the two
   *  answer one question together — where did this land — and a machine
   *  under its own path would read as a second, competing fact. Empty draws
   *  nothing; an adopted operation knows neither. */
  machine?: string
  phase: OperationPhase
  /** Bytes confirmed so far, or null while nothing has been observed. NOT
   *  zero — zero is a measurement and this is its absence, and the progress
   *  line says so by naming the size alone. */
  done: number | null
  /** The declared size. Zero is legitimate: an empty file is a file — and
   *  null is the absence of a declaration, which an adopted operation has. */
  total: number | null
  /** Derived by whoever counts the bytes; null until it is known. */
  speedBytesPerSecond?: number | null
  /** Why the row says what it says — the wire's reason on a failure, and
   *  what the renderer's own half hit on `unsettled`, which is a reason for
   *  not knowing and not a fault. */
  error?: string | null
  /** When it started and when it ended, on the source's clock. Together
   *  they are the duration a finished row reports; either may be null and
   *  the row then says less rather than guessing. */
  startedAt?: number | null
  endedAt?: number | null
  /** The clock the relative age is read against. A parameter and not
   *  `Date.now()` inside, for the reason format-time.ts gives: "2 min ago"
   *  ages, so somebody has to repaint it, and that somebody owns the tick.
   *  Defaults to now for a caller with nothing finished to show. */
  now?: number
  /** Stop it. Absent means stopping would mean nothing any more. */
  onCancel?: () => void
}

export function OperationRow(props: OperationRowProps) {
  // Read inside the JSX, never hoisted into a const: every one of these is
  // a prop and a captured value would freeze the row at its first render.
  const running = () => !isTerminalPhase(props.phase)
  const fraction = () => {
    const total = props.total
    return total !== null && total > 0 ? (props.done ?? 0) / total : 0
  }
  /** WHERE IT LANDED, as one fact. The machine first because it is the
   *  coarser half and the half that cannot be ellipsised away — the list is
   *  global, so `/var/www` with three connections open names nowhere. */
  const where = () =>
    [props.machine ?? '', props.destination ?? ''].filter((p) => p !== '').join(' · ')
  /** What a finished row says about itself: size, when, how long. Composed
   *  by format-bytes.ts, never here — two callers wording a duration is how
   *  two vocabularies for one concept start. */
  const summary = () =>
    formatFinished({
      total: props.total,
      startedAt: props.startedAt ?? null,
      endedAt: props.endedAt ?? null,
      now: props.now ?? Date.now(),
    })

  return (
    <CollectionRow
      density="dense"
      info={
        <span class="ui-operation-row" data-phase={props.phase}>
          <span class="ui-operation-row__kind-icon" aria-hidden="true">
            <Show when={KIND_ICON[props.kind]} keyed>
              {(Icon) => <Icon />}
            </Show>
          </span>
          <span class="ui-operation-row__body">
            <span class="ui-operation-row__line">
              {/* THE NAME HAS THE LINE TO ITSELF (bar the badge). It used to
                  share it with the destination, and in a rail-width panel the
                  path won every pixel the name gave up — so which file this is,
                  which is what the row exists to say, was the first thing to
                  ellipsise. The destination reads on its own line below
                  (nocx-hbdw4.1). */}
              <span class="ui-operation-row__title" title={props.title}>
                {props.title}
              </span>
              <Show when={isTerminalPhase(props.phase)}>
                <Badge tone={PHASE_TONE[props.phase as TerminalOperationPhase]}>
                  {PHASE_LABEL[props.phase as TerminalOperationPhase]}
                </Badge>
              </Show>
              <Show when={props.phase === 'unsettled'}>
                {/* The reason is the badge's hover detail: the person was
                    already told it as a toast when it happened, and the row
                    is not where a sentence belongs. */}
                <Badge tone="warning" title={props.error ?? undefined}>
                  Waiting for the server
                </Badge>
              </Show>
            </span>
            <Show
              when={running()}
              fallback={
                <>
                  {/* The reason first: it is the news, and the numbers under
                      it are the same three facts every finished row carries
                      whether or not anything went wrong. */}
                  <Show when={props.error !== null && props.error !== undefined}>
                    <span class="ui-operation-row__detail">{props.error}</span>
                  </Show>
                  {/* SIZE, WHEN, AND HOW LONG. A finished row used to read
                      `appicon.png · Done · /home/dev` — a name, a word and a
                      path — so somebody coming back to the list learnt
                      nothing from it (owner, 2026-08-22). The exact moment
                      rides the title, because the label ages and a reader
                      who wants the clock time should not have to compute
                      it. */}
                  <Show when={summary() !== ''}>
                    <span
                      class="ui-operation-row__summary"
                      title={
                        props.endedAt !== null && props.endedAt !== undefined
                          ? formatTimestamp(props.endedAt)
                          : undefined
                      }
                    >
                      {summary()}
                    </span>
                  </Show>
                </>
              }
            >
              <ProgressBar value={fraction()} ariaLabel={`${props.title} progress`} />
              <span class="ui-operation-row__detail">
                {formatProgress({
                  done: props.done,
                  total: props.total,
                  speedBytesPerSecond: props.speedBytesPerSecond ?? null,
                })}
              </span>
            </Show>
            {/* Where it landed, under the numbers rather than beside the
                name: a path is longer than any panel and it is the part a
                person can do without. Machine and path read as ONE fact on
                ONE line — this list is global, and a row that named a path
                without a machine was meaningless the moment a second
                connection was open. It carries the whole value on hover,
                because what is on screen is an ellipsis of it. Empty draws
                nothing — an adopted transfer knows neither, and says so by
                carrying nothing. */}
            <Show when={where() !== ''}>
              <span class="ui-operation-row__destination" title={where()}>
                {where()}
              </span>
            </Show>
          </span>
        </span>
      }
      actions={
        <Show when={props.onCancel}>
          <Button size="sm" onClick={() => props.onCancel?.()}>
            Cancel
          </Button>
        </Show>
      }
    />
  )
}
