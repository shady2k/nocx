// @vitest-environment jsdom
/**
 * i18n tests for the notifications panel's own chrome (nocx-dej6.3.8's
 * review, defects 2 and 5).
 *
 * Two shapes, one file:
 *   - the filter axes were built once in the component body, so their labels
 *     and placeholders stayed in the language the panel was mounted in;
 *   - the dropped-notice count was a number concatenated to a fixed phrase,
 *     which in Russian reads "1 уведомлений". A plural key owns the sentence
 *     and the number together.
 */
import { describe, expect, it, beforeEach, afterEach } from 'vitest'
import { render } from '@solidjs/testing-library'
import { NotificationsPanel } from './notifications-panel'
import type { FeedStore } from './feed-store'
import type { NotifyFeedRead, Occurrence } from '../generated/notify.feed.read'
import { setActiveLocale } from '../i18n/locale-model'

function occurrence(over: Partial<Occurrence> = {}): Occurrence {
  return {
    id: 'occ-1',
    at: '2026-08-22T10:00:00Z',
    title: 'build finished',
    body: '',
    titleSegments: [],
    bodySegments: [],
    kind: 'block.finished',
    level: 'success',
    count: 1,
    read: false,
    backendId: 'local',
    sessionId: 'sess-1',
    host: 'localhost',
    run: [],
    runDropped: 0,
    ...over,
  }
}

function store(read: Partial<NotifyFeedRead>): FeedStore {
  const snapshot: NotifyFeedRead = {
    revision: 1,
    unreadCount: 0,
    occurrences: [occurrence()],
    dropped: { count: 0, oldest: '', newest: '' },
    ...read,
  }
  return {
    occurrences: () => snapshot.occurrences,
    visibleOccurrences: () => snapshot.occurrences,
    unreadCount: () => snapshot.unreadCount,
    readKnown: () => true,
    dropped: () => snapshot.dropped,
    markRead: () => {},
    destroy: () => {},
  }
}

function mount(read: Partial<NotifyFeedRead>) {
  const container = document.body.appendChild(document.createElement('div'))
  render(
    () => <NotificationsPanel store={store(read)} canActivate={() => true} onActivate={() => {}} />,
    { container },
  )
  return container
}

beforeEach(() => setActiveLocale('en'))
afterEach(() => {
  document.body.innerHTML = ''
  setActiveLocale('en')
})

describe('the notifications panel speaks the active language', () => {
  it('re-reads the filter axes when the language switches', () => {
    const container = mount({
      occurrences: [occurrence(), occurrence({ id: 'occ-2', host: 'other.example' })],
    })
    expect(container.textContent).toContain('All hosts')
    setActiveLocale('ru')
    expect(container.textContent).toContain('Все хосты')
  })

  it('agrees with the number of dropped notifications', () => {
    setActiveLocale('ru')
    const container = mount({
      dropped: { count: 1, oldest: '2026-08-22T09:00:00Z', newest: '2026-08-22T09:30:00Z' },
    })
    const dropped = container.querySelector('[data-testid="notifications-dropped"]')
    expect(dropped?.textContent).toContain('1 уведомление выброшено')
  })
})

describe('a delivery sentence is translated per segment, and falls back to the raw text', () => {
  it('composes a keyed title in the active language, keeping the event title verbatim', () => {
    setActiveLocale('ru')
    const container = mount({
      occurrences: [
        occurrence({
          title: 'Not delivered: build finished',
          titleSegments: [
            { key: 'notify.delivery.failed.title' },
            { key: 'notify.delivery.failed.titleSeparator' },
            { text: 'build finished' },
          ],
        }),
      ],
    })
    expect(container.textContent).toContain('Не доставлено: build finished')
    expect(container.textContent).not.toContain('Not delivered')
  })

  it('shows the raw title when a slot has no segments', () => {
    setActiveLocale('ru')
    const container = mount({
      occurrences: [occurrence({ title: 'build finished' })],
    })
    expect(container.textContent).toContain('build finished')
  })
})
