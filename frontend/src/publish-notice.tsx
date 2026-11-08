/**
 * The pane's "your shell integration was not copied here" notice
 * (nocx-1kejz.4).
 *
 * WHAT IT REPLACES. An ssh pane copies the integration bundle onto the host
 * before it asks for the shell, and a copy that fails is deliberately not a
 * refusal: the far side keeps any generation it already had, byte-identical, so
 * the pane opens as an ordinary terminal (design §6.1 step 5, ADR-0004). What
 * the failure had was one slog line, which is the soft degrade AGENTS.md
 * refuses by name — a person got a tab that behaved as though delivery had been
 * configured while nocx knew, in that same second, that it had not.
 *
 * WHY IT IS ITS OWN CARD, and not the integration card beside it. That card
 * reports a SESSION STATE — the shell did not integrate, and here is why — and
 * it is still unanswerable when this fact is emitted: a host carrying an older
 * generation integrates from it happily. So this says what nocx could not
 * deliver, the integration card says what the shell then did, and neither
 * borrows the other's claim. It is the same reasoning that keeps the worker
 * tool-surface notice separate (tool-surface-notice.tsx).
 *
 * WHAT IT MUST NOT SAY. Never the backend's error text: a publish failure is an
 * SFTP status code and a path, which is meaningful in a log and meaningless in
 * a pane (nocx-viil). The wire carries a reason from a closed set, and the
 * sentences below are the whole of what each member means. Nothing here claims
 * the tab is unintegrated — see above — and nothing offers to fix it: the copy
 * is retried by the next pane that opens onto this host, which is a thing the
 * person can do rather than a button this card could press.
 *
 * Everything visible is a kit component this surface places. The identity class
 * positions the card in the pane and repaints nothing, which is the boundary
 * frontend/src/ui/README.md draws.
 */

import { createSignal } from 'solid-js'
import { render } from 'solid-js/web'

import type { Dispatcher } from './dispatcher'
import type { SessionPublishFailed } from './generated/session.publishFailed'
import { IconButton } from './ui/icon-button'
import { CloseIcon } from './ui/icons'
import { StatusCard } from './ui/status-card'
import { t } from './i18n/locale-model'

/** The closed wire vocabulary, as the generated type declares it: a member
 *  added to contracts/session.publishFailed.schema.json reaches this union
 *  through `npm run contracts`, and CAUSES below is a `Record` over it — so a
 *  member with no sentence is a type error rather than a card with a blank
 *  line in it. */
export type PublishFailureReason = SessionPublishFailed['reason']

/** What happened to the copy, one sentence per reason, in the person's terms.
 *
 *  The keys ARE the wire's vocabulary (PUBLISH_FAILURE_REASONS below reads them
 *  back), so this is the one owner of the words and the test that walks them is
 *  walking the contract (integration/status.ts took the same shape for the
 *  refusal reasons, for the same reason).
 *
 *  Each names what the far side or the connection did, and none guesses at a
 *  cause it did not observe: `connection-failed` says the connection did not
 *  stay up, not that the host refused it, because nothing in that failure says
 *  which. */
const CAUSES: Record<PublishFailureReason, string> = {
  'connection-failed':
    'The connection the copy needed did not stay up, so the files never finished going across.',
  'home-unavailable': 'The host did not report a home directory to copy them into.',
  'file-transfer-unavailable': 'The host would not open the file-transfer channel the copy needs.',
  'write-refused': 'The host refused the files nocx tried to write.',
  unknown: 'The copy failed, and nocx cannot say why.',
}

/** The wire's vocabulary, read back off the sentences rather than restated. */
export const PUBLISH_FAILURE_REASONS = Object.keys(CAUSES) as PublishFailureReason[]

/** WHAT THE TAB IS DOING INSTEAD, which is the half a person cannot see for
 *  themselves: the pane looks the same either way until a command does not get
 *  its block. It is deliberately conditional — a host that already carried a copy
 *  still activates it, and an older copy is a different problem from none — and
 *  it names no program, no path and no version. */
const INSTEAD =
  'This tab runs whatever copy of the integration the host already had — and, where it had none, no command blocks and no command editor.'

/** The card's title. One headline for every cause, because the question it
 *  answers — did the copy land — has one answer; the cause is the description's
 *  job. */
export const publishNoticeTitle = (): string =>
  t('shellintegration.publish.title', 'Shell integration was not copied')

/** The whole of what the card says about one cause. */
export function publishFailureSentence(reason: PublishFailureReason): string {
  return `${CAUSES[reason]} ${INSTEAD}`
}

export type PublishFailureFactHandler = (fact: SessionPublishFailed) => void

/** Subscribe to the backend's delivery fact for this session.
 *
 *  The payload gets a narrow boundary check for the same reason the integration
 *  and tool-surface seams do: it is server-initiated and unsolicited, so nothing
 *  at the call site has looked at its shape. The reason is checked against the
 *  closed set rather than merely being a string, so a member this build has no
 *  words for is refused rather than rendered as an undefined sentence — the
 *  contract refuses it at the schema too, and this is the half that keeps a
 *  stale generated type from drawing a card with the word "undefined" in it. */
export function subscribePublishFailed(
  dispatcher: Dispatcher,
  handler: PublishFailureFactHandler,
): () => void {
  return dispatcher.subscribe('session.publishFailed', (params: unknown) => {
    const p = params as SessionPublishFailed
    if (
      !p ||
      typeof p.sessionId !== 'string' ||
      typeof p.instanceId !== 'string' ||
      typeof p.sessionEpoch !== 'number' ||
      !PUBLISH_FAILURE_REASONS.includes(p.reason)
    ) {
      return
    }
    handler(p)
  })
}

export interface PublishNoticeHandle {
  /** Reword the card for a later attempt that failed for a different reason.
   *  ONE card per pane is the bound (nocx-z5lnt's rule for live output loss,
   *  applied here for the same reason): a publish that fails on every reconnect
   *  must not stack a wall of them. */
  update(reason: PublishFailureReason): void
  dispose(): void
}

/**
 * Place the card above the pane's terminal, or reword the one already there.
 *
 * It goes in the FLOW as the pane's first child, the placement every other pane
 * card settled on (nocx-rzvq): the pane is a flex column, so the card takes its
 * own height off the top and the terminal is laid out in what is left. A card
 * over the grid would cover the first prompt line, which is the line a person
 * is reading when it appears.
 */
export function mountPublishNotice(
  target: HTMLElement,
  reason: PublishFailureReason,
  onDismiss: () => void,
): PublishNoticeHandle {
  const host = document.createElement('div')
  host.className = 'nocx-publish-notice'
  target.insertBefore(host, target.firstChild)
  const [current, setCurrent] = createSignal(reason)
  const dispose = render(
    () => (
      <StatusCard
        tone="warning"
        title={publishNoticeTitle()}
        description={publishFailureSentence(current())}
        action={
          <IconButton ariaLabel="Dismiss" size="sm" onClick={onDismiss}>
            <CloseIcon />
          </IconButton>
        }
      />
    ),
    host,
  )
  return {
    update: setCurrent,
    dispose: () => {
      dispose()
      host.remove()
    },
  }
}
