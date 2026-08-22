/**
 * The notification centre's panel — what happened while you were not looking.
 *
 * It is a plain role="list" of kit RecordRows, the shape notes-panel.tsx
 * already uses, and deliberately NOT a CollectionView: that is the searchable
 * manager surface (it requires searchValue, onSearch, hasItems and an empty
 * slot), and search over the feed is epic 2's.
 *
 * Resolution of an occurrence to a tab is the RENDERER's and arrives as a
 * prop: the backend cannot do it at all — Attribution.Tab is a WebSocket
 * connection id — and the surface asking is what keeps this panel off the
 * banner-click path. The panel never marks anything read by being looked at:
 * "output arrived" and "you saw what we told you" are different facts, and
 * conflating them is the defect this centre exists to undo.
 */
import { For, Show } from 'solid-js'
import { EmptyState } from '../ui/empty-state'
import { RecordRow } from '../ui/record-row'
import type { BadgeTone } from '../ui/badge'
import type { FeedStore } from './feed-store'
import type { NotifyFeedRead } from '../generated/notify.feed.read'

type Occurrence = NotifyFeedRead['occurrences'][number]

/** Level maps onto the kit's tone vocabulary. info is neutral, not "info":
 *  an ordinary completion is not an advisory, and toning every row blue is
 *  how a feed stops distinguishing anything. */
function toneOf(level: Occurrence['level']): BadgeTone {
  switch (level) {
    case 'danger':
      return 'danger'
    case 'warning':
      return 'warning'
    case 'success':
      return 'success'
    default:
      return 'neutral'
  }
}

/** A row's instant in the person's locale — this surface's phrasing, which
 *  is why it is here and not in ui/. An unparseable stamp renders verbatim
 *  rather than as "Invalid Date": the wire's word is better than a lie. */
function formatWhen(at: string): string {
  const ms = Date.parse(at)
  if (Number.isNaN(ms)) return at
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

/** The row's meta line: where it came from, then when — joined only where
 *  there are two things to join. A LOCAL session has no host (the backend sets
 *  one only on the remote branch), so a fixed two-part template opened the line
 *  with " · " for the centre's very first source (nocx-lmmi5). The wording of
 *  the row's TITLE is the backend's and is not restated here. */
function metaLine(host: string, when: string): string {
  return host === '' ? when : `${host} · ${when}`
}

export interface NotificationsPanelProps {
  store: FeedStore
  /** Focus the tab this occurrence came from. */
  onActivate: (backendId: string, sessionId: string) => void
  /** Whether that tab still exists — a row whose session is gone is not
   *  activatable, and says so rather than doing nothing when clicked. */
  canActivate: (backendId: string, sessionId: string) => boolean
}

export function NotificationsPanel(props: NotificationsPanelProps) {
  const dropped = () => props.store.dropped()

  return (
    <div class="notifications-panel">
      <Show
        when={props.store.occurrences().length > 0}
        fallback={
          <EmptyState
            title="Nothing to catch up on"
            description="Notifications raised while you are elsewhere collect here."
          />
        }
      >
        <div class="notifications-panel__list" role="list" aria-label="Notifications">
          <For each={props.store.occurrences()}>
            {(o) => {
              const live = () => props.canActivate(o.backendId, o.sessionId)
              return (
                <RecordRow
                  density="dense"
                  title={o.count > 1 ? `${o.title} ×${o.count}` : o.title}
                  kind={{ label: o.kind, tone: toneOf(o.level) }}
                  meta={metaLine(o.host, live() ? formatWhen(o.at) : 'session closed')}
                  detail={o.body === '' ? undefined : o.body}
                  selected={!o.read}
                  onActivate={live() ? () => props.onActivate(o.backendId, o.sessionId) : undefined}
                  actions={<></>}
                />
              )
            }}
          </For>
        </div>
      </Show>

      <Show when={dropped().count > 0}>
        {/* Outside the list and never activatable: a soft degrade must be
            visible in the product, not only in a log. */}
        <div class="notifications-panel__dropped" data-testid="notifications-dropped">
          {dropped().count} notifications dropped between {formatWhen(dropped().oldest)} and{' '}
          {formatWhen(dropped().newest)}
        </div>
      </Show>
    </div>
  )
}
