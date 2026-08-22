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
 * - `queued` DRAWS NO PROGRESS BAR AND NO PERCENTAGE, and keeps its badge.
 *   The bar is now the row's loudest element, and a panel of bars at zero
 *   would read "five things are stalled" where the truth is "four are
 *   waiting their turn" — zero is a measurement and nothing has been
 *   measured. What it draws instead is the size it is going to send.
 *
 *   The badge stays even though the operations list groups queued rows
 *   under a heading that already says it, which is the opposite of the
 *   `written` decision two rows down. The reason is that the two rows are
 *   not equally self-describing: THE HEADING SCROLLS AWAY AND THE ROW DOES
 *   NOT, and a finished row still has its summary line — size, when, how
 *   long — to say what it is, while a queued row stripped of the badge is
 *   a name and a size with nothing at all to distinguish it.
 *
 * ## What the caller owns
 *
 * Whether there is a cancel at all. `onCancel` absent means cancelling this
 * operation would mean nothing any more — which is the model's judgement
 * about that operation, not a rendering rule this component could derive.
 */
import { Match, Show, Switch, type Component } from 'solid-js'
import { Badge, type BadgeTone } from './badge'
import { IconButton } from './icon-button'
import { CollectionRow } from './collection-view'
import { formatFinished, formatProgress } from './format-bytes'
import { formatTimestamp } from './format-time'
import { ArrowDownIcon, ArrowUpIcon, CloseIcon } from './icons'
import { ProgressBar } from './progress-bar'
import {
  isTerminalPhase,
  isWaitingPhase,
  type OperationKind,
  type OperationPhase,
  type TerminalOperationPhase,
} from './operation'

/** The kind's decorative glyph, decided here and never supplied by a
 *  surface — the same rule TreeRow follows for its type glyphs. Download
 *  joined as one more entry (nocx-9le.8.3), which is the whole of what this
 *  component had to learn about a second direction. */
const KIND_ICON: Record<OperationKind, Component> = {
  upload: ArrowUpIcon,
  download: ArrowDownIcon,
}

/** How a finished operation reads. See the module doc for why `cancelled`
 *  and `skipped` are neutral rather than danger. */
const PHASE_TONE: Record<TerminalOperationPhase, BadgeTone> = {
  written: 'success',
  sent: 'success',
  skipped: 'neutral',
  cancelled: 'neutral',
  failed: 'danger',
}

/** What a finished operation is called. `written` reads "Done" rather than
 *  "Uploaded" because one row serves every kind: the glyph and the title
 *  already say what was done, and a per-kind success word would be a second
 *  table indexed by two things for one cell. `sent` is the download's word
 *  for the same success and reads the same "Done" — which is exactly why
 *  the wire's two spellings could be carried in without a mapping layer:
 *  they differ on the wire and they do not differ to a person. */
const PHASE_LABEL: Record<TerminalOperationPhase, string> = {
  written: 'Done',
  sent: 'Done',
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
  //
  // THREE STATES AND NOT TWO, since nocx-hbdw4.6: an operation can be
  // waiting, moving, or over, and the numbers half of the row differs in
  // all three. `running` is the middle one alone — `unsettled` included,
  // because there the bytes genuinely were moving and the last count is
  // the truth about them.
  const waiting = () => isWaitingPhase(props.phase)
  const running = () => !isWaitingPhase(props.phase) && !isTerminalPhase(props.phase)
  const fraction = () => {
    const total = props.total
    return total !== null && total > 0 ? (props.done ?? 0) / total : 0
  }
  /** WHERE IT LANDED, as one fact. The machine first because it is the
   *  coarser half and the half that cannot be ellipsised away — the list is
   *  global, so `/var/www` with three connections open names nowhere. */
  /** The one number a person actually wants, and the one the row used to
   *  make them compute: "99.1 MB of 328.3 MB" is a third, and nothing said
   *  so (owner, 2026-08-22). Null when there is no total to be a fraction
   *  OF — an adopted operation — because a percentage of nothing is a lie
   *  rather than a zero. */
  const percent = () => {
    const total = props.total
    if (total === null || total <= 0) return null
    return Math.min(100, Math.max(0, Math.round(fraction() * 100)))
  }
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
              {/* THE PERCENTAGE IS THE ANCHOR of a running row. It sits on
                  the title line rather than under it because that line has
                  the room a badge used to take, and because a number the eye
                  lands on first is what the row was missing — everything in
                  it was the same size, so nothing led (nocx-hbdw4.5). */}
              <Show when={running() && percent() !== null}>
                <span class="ui-operation-row__percent">{percent()}%</span>
              </Show>
              {/* AN OUTCOME IS MARKED ONLY WHEN IT IS NEWS. `written` carries
                  no badge: the list groups finished work under its own
                  heading, so a "Done" pill repeated down every row said what
                  the heading had already said, in the space the file name
                  wanted. Everything else differs from the expected end and
                  keeps its mark. */}
              <Show when={isTerminalPhase(props.phase) && props.phase !== 'written'}>
                <Badge tone={PHASE_TONE[props.phase as TerminalOperationPhase]}>
                  {PHASE_LABEL[props.phase as TerminalOperationPhase]}
                </Badge>
              </Show>
              {/* IT SAYS IT IS WAITING. There is no percentage on this row
                  and no bar under it, so without this the row states
                  nothing about itself at all — see the module doc for why
                  this badge stays where `written`'s was removed. */}
              <Show when={waiting()}>
                <Badge tone="neutral">Queued</Badge>
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
            {/* THE NUMBERS UNDER THE NAME, one form per state. A Switch and
                not nested Shows: the three are exclusive and a reader has to
                be able to see that they are, which is what stopped a queued
                row falling through into the finished branch and reporting
                itself as over. */}
            <Switch>
              <Match when={waiting()}>
                {/* WHAT IT IS GOING TO SEND, and nothing that claims any of
                    it has moved. `done: null` is the absence of a
                    measurement and format-bytes.ts already renders that as
                    the size alone — the same line a running row shows
                    before its first progress frame, which is the same
                    fact. */}
                <span class="ui-operation-row__progress">
                  {formatProgress({
                    done: null,
                    total: props.total,
                    speedBytesPerSecond: null,
                  })}
                </span>
              </Match>
              <Match when={running()}>
                <ProgressBar value={fraction()} ariaLabel={`${props.title} progress`} size="md" />
                {/* Its OWN class, not `__detail`. The two shared one, and they
                    want opposite things: a failure reason is a sentence and
                    must wrap, while these numbers must never — they change
                    several times a second, and a wrap made the row's height
                    twitch as the digits changed width (owner, 2026-08-22). */}
                <span class="ui-operation-row__progress">
                  {formatProgress({
                    done: props.done,
                    total: props.total,
                    speedBytesPerSecond: props.speedBytesPerSecond ?? null,
                  })}
                </span>
              </Match>
              <Match when={isTerminalPhase(props.phase)}>
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
              </Match>
            </Switch>
            {/* Where it landed, under the numbers rather than beside the
                name: a path is longer than any panel and it is the part a
                person can do without. Machine and path read as ONE fact on
                ONE line — this list is global, and a row that named a path
                without a machine was meaningless the moment a second
                connection was open. It carries the whole value on hover,
                because what is on screen is an ellipsis of it. Empty draws
                nothing — an adopted transfer knows neither, and says so by
                carrying nothing. */}
            <Show when={(props.machine ?? '') !== '' || (props.destination ?? '') !== ''}>
              <span
                class="ui-operation-row__destination"
                title={[props.machine ?? '', props.destination ?? '']
                  .filter((p) => p !== '')
                  .join(' · ')}
              >
                <Show when={(props.machine ?? '') !== ''}>
                  <span class="ui-operation-row__machine">{props.machine}</span>
                </Show>
                <Show when={(props.machine ?? '') !== '' && (props.destination ?? '') !== ''}>
                  <span class="ui-operation-row__where-sep" aria-hidden="true">
                    {' · '}
                  </span>
                </Show>
                {/* THE PATH KEEPS ITS LEAF. Ellipsised from the end it read
                    `/home/...`, which hides the only part that identifies
                    the directory — the head of a path is the part you can
                    guess (owner, 2026-08-22). The span reverses its base
                    direction so the overflow falls off the FRONT, and
                    `unicode-bidi: plaintext` keeps the characters themselves
                    in logical order, which is what stops a leading slash
                    migrating to the other end. */}
                <Show when={(props.destination ?? '') !== ''}>
                  <span class="ui-operation-row__path">{props.destination}</span>
                </Show>
              </span>
            </Show>
          </span>
        </span>
      }
      actions={
        /* AN ICON, NOT A LABELLED BUTTON. "Cancel" took about a third of a
           rail-width row for something wanted rarely, and the content that is
           wanted always ellipsised to pay for it (owner, 2026-08-22). It stays
           a real <button> with an accessible name, so it is reachable by
           keyboard and announced by a screen reader; it is NOT hover-only,
           because an action that appears on hover is one a keyboard user and
           a toucher never find. */
        <Show when={props.onCancel}>
          <IconButton
            size="sm"
            ariaLabel={`Cancel ${props.title}`}
            onClick={() => props.onCancel?.()}
          >
            <CloseIcon />
          </IconButton>
        </Show>
      }
    />
  )
}
