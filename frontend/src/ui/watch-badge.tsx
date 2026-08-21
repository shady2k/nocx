/**
 * WatchBadge — the one answer to "is this surface's view of a folder live,
 * and if not, why".
 *
 * The `files.watch` contract carries `mode` for exactly one reason, in its
 * own words: "so the UI can say why refresh may lag". Turning that field into
 * something a person can see is a JUDGEMENT, not a shape — `polling` on a
 * LOCAL binding WITH a reason is a real degrade, and every other combination
 * warns about nothing:
 *
 *   - a healthy local watch has nothing to explain;
 *   - a REMOTE binding is never degraded, because polling is its designed
 *     mode (SFTP has no change notification);
 *   - a local fallback the backend declined to explain is deliberate too —
 *     `ws_files.go` says a reason there "lights the badge for every user on
 *     every local binding forever", and a warning that is always on teaches
 *     the user to ignore the next one.
 *
 * Two surfaces deciding that separately is the shape AGENTS.md names: they
 * agree everywhere anybody looks and disagree the day one of them is edited.
 * The Files panel had it first (design §5.5) and the API workbench needed the
 * same answer, so the judgement moved here and both call it.
 *
 * The SLOT is not decoration. It carries the established mode as a data
 * attribute whether or not the badge renders, and it is the only observable
 * that says `files.watch` has RETURNED — the rows of a tree say `files.list`
 * returned, which is a different call. Something has to say it, or a check
 * that a change arrives has no way to know watching had begun and races the
 * baseline.
 */

import { Show } from 'solid-js'
import { Badge } from './badge'

export interface WatchBadgeProps {
  /**
   * Test id PREFIX, per surface: the slot is `${testId}-slot` and the badge
   * itself is `${testId}`. Two surfaces carrying one behaviour still have to
   * be addressable apart — a check that the workbench is watching must not
   * pass because the Files panel is.
   */
  testId: string
  /** The mode the binding's last `files.watch` reported, or null before the
   *  first answer — and for a surface that has nothing to watch yet. */
  mode: 'watching' | 'polling' | null
  /** Why refresh is degraded, or null. Present only when a LOCAL watch could
   *  not be established; it is the badge's hover detail. */
  reason: string | null
  /** Whether this binding's polling CAN be a degrade: true for a local
   *  binding, false for a remote one whose designed mode is polling. */
  local: boolean
}

export function WatchBadge(props: WatchBadgeProps) {
  return (
    <span
      class="ui-watch-badge"
      data-testid={`${props.testId}-slot`}
      data-watch-mode={props.mode ?? undefined}
    >
      <Show when={props.mode === 'polling' && props.reason !== null && props.local}>
        <Badge tone="warning" data-testid={props.testId} title={props.reason ?? undefined}>
          Polling
        </Badge>
      </Show>
    </span>
  )
}
