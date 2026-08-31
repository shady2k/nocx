/**
 * The third state, said out loud (nocx-k6p18.5).
 *
 * A session can outlive the coordinator that opened it now, so the store no
 * longer decides at startup that every session it inherited is dead. What it
 * has instead is three answers — live, absent, and NOBODY COULD BE ASKED — and
 * the third is a state a person can sit in for hours: a laptop opened away from
 * the network, a host behind a jump box that is down, a credential waiting on a
 * vault somebody has to unlock.
 *
 * A block in that state is neither running nor finished. `restore-client.ts`
 * gives it the `unreconciled` status, which paints no outcome chip and claims
 * nothing; this module is the other half — WHY, in a sentence, once per pane
 * rather than once per block.
 *
 * WHY A CARD AND NOT A CHIP PER BLOCK. The cause is one fact about one host,
 * and five blocks from the same session would repeat it five times while each
 * copy had room for a word rather than a sentence. It is the shape
 * recovery-notice.tsx settled on for the same reason, and everything visible
 * here is a kit component this surface places (frontend/src/ui/README.md).
 *
 * WHAT IT MUST NOT SAY. It never claims the session ended and it never claims
 * it is still running — nobody knows, which is the whole point. It says what
 * was tried, what stopped it, and what the person can do about it when that is
 * something they can do.
 */

import { render } from 'solid-js/web'
import { IconButton } from './ui/icon-button'
import { StatusCard } from './ui/status-card'

/** The wire's closed vocabulary for why nobody could be asked
 *  (contracts/ledger.query.schema.json). Declared here because this is the one
 *  module that turns a cause into English; every other reader passes it
 *  through. */
export type UnreconciledCause =
  | 'notYetAsked'
  | 'noInventory'
  | 'connectionRefused'
  | 'timedOut'
  | 'hostUnreachable'
  | 'vaultSealed'

/** The sentence for each cause, keyed on the closed enum rather than on any
 *  prose the backend sent — a reworded Go error must never change what a user
 *  reads (the rule history-status.ts states for the same reason).
 *
 *  Each one says the same two things in its own words: nobody could check, and
 *  therefore nothing here is a claim about whether the command is still
 *  running. Two of them name something a person can act on. */
const SENTENCES: Record<UnreconciledCause, string> = {
  notYetAsked: 'nocx has not been able to check this host since it restarted.',
  noInventory: 'nothing on this host can be asked whether that session is still there.',
  connectionRefused: 'this host refused the connection, so its sessions could not be checked.',
  timedOut: 'this host did not answer in time, so its sessions could not be checked.',
  hostUnreachable: 'this host has not been reachable since nocx restarted.',
  vaultSealed:
    'the vault is locked, so the credential for this host could not be used to check it.',
}

/** What one pane's restored page says about its unreconciled blocks, or null
 *  when it has none.
 *
 *  NULL IS THE COMMON ANSWER and it is the important one: a pane whose blocks
 *  were all judged has nothing to report, and a notice that appeared for those
 *  would appear on every tab and therefore mean nothing.
 *
 *  Its own exported derivation because "is there anything to say" is a question
 *  about the page, not about the card: the card renders the answer, and the
 *  caller deciding whether to mount asks the same function rather than a second
 *  copy of the rule. */
export interface UnreconciledAccount {
  /** How many blocks in this pane are neither running nor finished. */
  blocks: number
  /** The causes present, de-duplicated, in the order they arrived. */
  causes: UnreconciledCause[]
}

/** One page row, narrowed to what this module reads. Structural rather than
 *  the whole RestorableBlock, so a change to the block's other fields cannot
 *  ripple in here. */
export interface UnreconciledRow {
  unreconciled: UnreconciledCause | null
}

export function unreconciledAccount(rows: readonly UnreconciledRow[]): UnreconciledAccount | null {
  const causes: UnreconciledCause[] = []
  let blocks = 0
  for (const row of rows) {
    if (row.unreconciled === null) continue
    blocks += 1
    if (!causes.includes(row.unreconciled)) causes.push(row.unreconciled)
  }
  return blocks > 0 ? { blocks, causes } : null
}

/** The headline: what is true of the blocks, in the words the state deserves.
 *  "Neither running nor finished" is the design's own phrase and it is the
 *  honest one — every shorter version picks a side. */
function unreconciledTitle(account: UnreconciledAccount): string {
  const what = account.blocks === 1 ? 'One command' : `${account.blocks} commands`
  return `${what} in this tab: neither running nor finished`
}

/** …and why. A cause this build has no sentence for is carried through in the
 *  wire's own word rather than translated into something this build cannot
 *  stand behind — the rule recovery-notice.tsx states for a gap reason. */
function unreconciledDescription(account: UnreconciledAccount): string {
  const reasons = account.causes.map((c) => SENTENCES[c] ?? `nobody could be asked ("${c}").`)
  return `Their session may still be running: ${reasons.join(' ')}`
}

export interface UnreconciledNoticeProps {
  /** The pane's restored page. */
  rows: readonly UnreconciledRow[]
  /** Take this card away. It records nothing: the state is the store's and the
   *  card is not, so a later restore that still finds unreconciled blocks says
   *  so again. */
  onDismiss: () => void
}

function UnreconciledNotice(props: { account: UnreconciledAccount; onDismiss: () => void }) {
  return (
    <StatusCard
      tone="neutral"
      title={unreconciledTitle(props.account)}
      description={unreconciledDescription(props.account)}
      action={
        <IconButton ariaLabel="Dismiss" size="sm" onClick={() => props.onDismiss()}>
          {'×'}
        </IconButton>
      }
    />
  )
}

/**
 * Mount the notice into a tab's pane, or answer null when this page has
 * nothing to report.
 *
 * NULL RATHER THAN AN EMPTY MOUNT, so the decision is taken exactly once, here,
 * from unreconciledAccount — the same shape mountRecoveryNotice uses, and it
 * goes in the same place for the same reason: the pane is a flex column, the
 * card takes its own height off the top, and a message about the session must
 * not cover the session.
 */
export function mountUnreconciledNotice(
  target: HTMLElement,
  props: UnreconciledNoticeProps,
): (() => void) | null {
  const account = unreconciledAccount(props.rows)
  if (account === null) return null
  const host = document.createElement('div')
  host.className = 'nocx-unreconciled-notice'
  target.insertBefore(host, target.firstChild)
  const dispose = render(
    () => <UnreconciledNotice account={account} onDismiss={props.onDismiss} />,
    host,
  )
  return () => {
    dispose()
    host.remove()
  }
}
