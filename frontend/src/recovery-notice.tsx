/**
 * The reclaimed pane's missing-output notice (nocx-fz4qa).
 *
 * A reclaim already knows exactly what it could not give back. `session.output`
 * answers with the byte ranges the retention bound dropped, and `reclaimSession`
 * adds the stretch neither the recording nor the replay ring holds (ipc.ts's
 * UNRECORDED, derived from `produced` against `replayFrom`). Both land on
 * SessionHandle.recovered.gaps — and until this module nothing read them. A
 * pane came back with a hole in the middle of its scrollback and said nothing,
 * which is the shape AGENTS.md forbids by name: a soft degrade must be visible
 * in the product, not only in a log. A scrollback that is silently short is
 * indistinguishable from a session that printed less.
 *
 * WHY IT IS NOT FOLDED INTO THE INTEGRATION NOTICE. That card reports a
 * SESSION STATE — the shell did not integrate, it may recover, and the card
 * comes and goes with the state (integration/notice.tsx). This reports a
 * FINISHED FACT about one reclaim: the bytes were already gone before this
 * client existed and nothing will make them appear. They have different
 * lifetimes and different remedies, and a card whose title had to cover both
 * would say something vague about each.
 *
 * WHAT IT MUST NOT SAY. It never claims the output can be got back, and it
 * never guesses at a reason. The two reasons that reach a client today are
 * the retention bound (`cap`, internal/content's TruncCap — the store's own
 * word for the middle it dropped) and `unrecorded`; anything else is carried
 * through verbatim rather than translated into a sentence this build cannot
 * stand behind.
 *
 * Everything visible is a kit component this surface places. The identity
 * class positions the card in the pane and repaints nothing, which is the
 * boundary frontend/src/ui/README.md draws.
 */

import { render } from 'solid-js/web'
import { IconButton } from './ui/icon-button'
import { StatusCard } from './ui/status-card'
import { formatBytes } from './ui/format-bytes'
import { UNRECORDED, type SessionRecovery } from './ipc'

/** The retention bound's word, minted by the store (internal/content's
 *  TruncCap) and repeated on the wire by session.output's gap reason. Named
 *  here rather than inlined so the one place that translates it into English
 *  is greppable from the Go side. */
const CAP = 'cap'

/** What the gaps of one reclaim add up to, split by who is answerable for
 *  each part. Its own exported derivation because "how much is missing" is
 *  a question about the data and not about the card: the card renders it,
 *  and a caller deciding whether there is anything to say asks it too. */
export interface RecoveryAccount {
  /** Every missing byte over the recovered span. */
  missing: number
  /** What the recording's retention bound dropped. */
  dropped: number
  /** What nothing kept at all — neither the recording nor the ring. */
  unrecorded: number
  /** Missing for a reason this build has no sentence for. */
  other: number
  /** Those reasons, in the wire's own words, de-duplicated and in the order
   *  they arrived. Carried so the card can print them instead of inventing
   *  an explanation. */
  reasons: string[]
}

/**
 * Add up one reclaim's holes, or null when there is nothing to report.
 *
 * NULL IS THE COMMON ANSWER and it is the important one: a pane that opened
 * its own session recovered nothing (`recovered` is null), and a reclaim that
 * got the whole recording back has no gaps. A notice that appeared for those
 * would appear on nearly every tab and would therefore mean nothing.
 *
 * A range that runs backwards or is not a finite number is SKIPPED, not
 * subtracted: it is a hole of unknown size, and letting it cancel a real one
 * would turn a reporting bug into a silence.
 */
export function recoveryAccount(recovery: SessionRecovery | null): RecoveryAccount | null {
  if (recovery === null) return null
  const account: RecoveryAccount = {
    missing: 0,
    dropped: 0,
    unrecorded: 0,
    other: 0,
    reasons: [],
  }
  for (const gap of recovery.gaps) {
    const bytes = gap.end - gap.start
    if (!Number.isFinite(bytes) || bytes <= 0) continue
    account.missing += bytes
    if (gap.reason === CAP) {
      account.dropped += bytes
      continue
    }
    if (gap.reason === UNRECORDED) {
      account.unrecorded += bytes
      continue
    }
    account.other += bytes
    if (!account.reasons.includes(gap.reason)) account.reasons.push(gap.reason)
  }
  return account.missing > 0 ? account : null
}

/** The clauses, one per owner of a part of the hole, in the order a person
 *  reads them: the bound first because it is the ordinary cause, then the
 *  stretch nothing kept, then whatever this build cannot name. Each carries
 *  its own byte count, so a reclaim that hit two causes is two answerable
 *  numbers rather than one that explains neither. */
function clauses(account: RecoveryAccount): string[] {
  const parts: string[] = []
  if (account.dropped > 0) {
    parts.push(`${formatBytes(account.dropped)} the recording's size limit dropped`)
  }
  if (account.unrecorded > 0) {
    parts.push(`${formatBytes(account.unrecorded)} that was never recorded`)
  }
  if (account.other > 0) {
    parts.push(`${formatBytes(account.other)} missing as "${account.reasons.join('", "')}"`)
  }
  return parts
}

/** The card's headline: the whole hole, as a number a person can weigh
 *  against the run they were watching. */
function title(account: RecoveryAccount): string {
  return `${formatBytes(account.missing)} of this session's output is missing`
}

/** …and what took it. The sentence ends where the honesty does — nothing
 *  here offers to get the bytes back, because nothing can. */
function description(account: RecoveryAccount): string {
  return `This tab was taken back after those bytes were gone: ${clauses(account).join(', and ')}.`
}

export interface RecoveryNoticeProps {
  /** What the reclaim recovered, and what it could not. */
  recovery: SessionRecovery
  /** Take this card away. It records nothing: the hole is permanent and the
   *  card is not, which is the whole of what dismissing it means. */
  onDismiss: () => void
}

function RecoveryNotice(props: { account: RecoveryAccount; onDismiss: () => void }) {
  return (
    <StatusCard
      tone="warning"
      title={title(props.account)}
      description={description(props.account)}
      action={
        <IconButton ariaLabel="Dismiss" size="sm" onClick={() => props.onDismiss()}>
          {'×'}
        </IconButton>
      }
    />
  )
}

/**
 * Mount the notice into a tab's pane, or answer null when this reclaim has
 * nothing to report.
 *
 * NULL RATHER THAN AN EMPTY MOUNT, so the decision "is there a hole" is taken
 * exactly once, here, from recoveryAccount. A caller that had to ask first and
 * then mount would be the second reader of the same derivation, and the two
 * would drift the first time either changed.
 *
 * It goes in the FLOW as the pane's first child, the same placement the
 * integration notice settled on (nocx-rzvq): the pane is a flex column, so
 * the card takes its own height off the top and the terminal is laid out in
 * what is left. Overlaying covers the first prompt line — a message about the
 * session must not hide the session. The caller re-measures the live region
 * after mounting and after disposing, exactly as it already does for the
 * integration card.
 */
export function mountRecoveryNotice(
  target: HTMLElement,
  props: RecoveryNoticeProps,
): (() => void) | null {
  const account = recoveryAccount(props.recovery)
  if (account === null) return null
  const host = document.createElement('div')
  host.className = 'nocx-recovery-notice'
  target.insertBefore(host, target.firstChild)
  const dispose = render(
    () => <RecoveryNotice account={account} onDismiss={props.onDismiss} />,
    host,
  )
  return () => {
    dispose()
    host.remove()
  }
}
