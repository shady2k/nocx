// History transport seams. The backend owns command recording at the
// authenticated lifecycle boundary; this module only exposes the recall query
// and the server-owned receipt notification consumed by terminal-content.

import type { HistoryQuery } from './generated/history.query'
import type { HistoryRecorded } from './generated/history.recorded'
import type { WSClient } from './ipc'
import type { RecallScope } from './recall'

export interface HistoryRecordedSubscription {
  bindSession: (sessionId: string) => void
  unsubscribe: () => void
}

/** Subscribe before session.open, then bind the server-authoritative session
 * id from its result. A receipt for another session is not ours to apply. */
export function subscribeHistoryRecorded(
  client: WSClient,
  handler: (receipt: HistoryRecorded) => void,
): HistoryRecordedSubscription {
  let sessionId: string | null = null
  let closed = false
  const unsubscribe = client.dispatcher.subscribe('history.recorded', (params: unknown) => {
    const receipt = params as HistoryRecorded
    if (!receipt || typeof receipt.sessionId !== 'string' || typeof receipt.attemptId !== 'string')
      return
    if (receipt.sessionId === sessionId) handler(receipt)
  })
  return {
    bindSession: (authoritativeSessionId: string): void => {
      if (closed) return
      if (sessionId !== null) throw new Error('history subscription is already bound')
      sessionId = authoritativeSessionId
    },
    unsubscribe: (): void => {
      if (closed) return
      closed = true
      unsubscribe()
    },
  }
}

export function queryHistory(
  client: WSClient,
  scope: RecallScope,
  cwd: string,
  host: string,
  text?: string,
  paneId?: string,
  signal?: AbortSignal,
): Promise<HistoryQuery> {
  const wireScope: RecallScope = text !== undefined && text !== '' ? 'everywhere' : scope
  const params: Record<string, unknown> = { scope: wireScope }
  if (wireScope === 'pane') {
    params.paneId = paneId
  } else if (wireScope === 'directory') {
    params.cwd = cwd
    params.host = host
  } else if (wireScope === 'host') {
    params.host = host
  }
  if (text !== undefined && text !== '') params.text = text
  return client.call<HistoryQuery>('history.query', params, signal)
}
