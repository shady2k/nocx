// The renderer's ONE door to an SSH open and its ONE reconnect call
// (ADR-ba6xet1b, design §5.6, §5.8; nocx-oalo1.5.5).
//
// The client in ipc.ts carries the socket and the session records. What it may
// NOT carry is a connection entry: `openSSHSession`, `openSSHSessionByHost` and
// the reconnect were declared there and were therefore a second owner of "how
// a connection starts" — the very thing the ADR's ratchet refuses. They live
// here, beside the store that receives the backend's answer, and every caller
// takes them from here.
//
// RECONNECT IS NOT A SECOND OPEN. The renderer sends one call — `pane.reconnect`
// with the pane and the epoch of the observation that offered the button — and
// the backend REGISTERS the work and answers {paneId, registered}. The dial and
// the replacement shell follows on pane.opened, carrying the same result
// as an open acknowledgement. Nothing here retries, back offs or
// decides whether an attempt is allowed: the offer's presence already decided
// that, and the epoch refuses a press that is about a state the person is no
// longer looking at.

import type {
  OpenAnchor,
  OpenResult,
  OpenWatch,
  SessionHandle,
  SessionSize,
  WSClient,
} from '../ipc'
import type { PaneReconnect } from '../generated/pane.reconnect'
import type { ConnectionHelperRefreshResult } from '../generated/connection.helperRefresh'

/** The `paneId` an open names, when the pane's row exists. Absent is the
 *  ordinary state of a pane the chain has not registered yet. */
function paneParam(anchor: OpenAnchor): { paneId?: string } {
  return anchor.paneId ? { paneId: anchor.paneId } : {}
}

export class ConnectionClient {
  constructor(private readonly client: WSClient) {}

  /** Open an SSH session through a saved profile (the backend resolves the
   *  route, credential and jump host). */
  openProfile(
    size: SessionSize,
    profileId: string,
    anchor: OpenAnchor = {},
    watch?: OpenWatch,
  ): Promise<SessionHandle> {
    return this.client.dispatcher
      .call<OpenResult>(
        'open',
        { ...size, kind: 'ssh', profileId, ...paneParam(anchor) },
        undefined,
        { announced: watch },
      )
      .then((result) => this.client.adoptOpen(result, size))
  }

  /** Open an SSH session by host or alias, resolved through ~/.ssh/config.
   *
   *  No integration method travels with it: the mode is the HOST's, recorded by
   *  the connect-time ask under the fingerprint it named (ADR-727g63tr
   *  decision 1), so the open reads the record when the dial sees the key. */
  openByHost(
    size: SessionSize,
    host: string,
    user?: string,
    anchor: OpenAnchor = {},
    watch?: OpenWatch,
  ): Promise<SessionHandle> {
    return this.client.dispatcher
      .call<OpenResult>(
        'open',
        {
          ...size,
          kind: 'ssh',
          host,
          user,
          ...paneParam(anchor),
        },
        undefined,
        { announced: watch },
      )
      .then((result) => this.client.adoptOpen(result, size))
  }

  /**
   * The person's one action on a connection that is not usable: register the
   * reconnect with the backend.
   *
   * The answer is an ACKNOWLEDGEMENT, not a session: `{paneId, registered}`.
   * The dial is observed on pane.connection; pane.opened answers with the
   * replacement open result, which the pane adopts. Treating this result as an open
   * ack (the branch's first defect) adopted nothing and threw on every press.
   *
   * The epoch is the observation's own — the one whose button was pressed.
   * A press carrying anything else is about a state the person is no longer
   * looking at, and the backend refuses it with `stale connectionEpoch`.
   */
  /**
   * The person's one action on the far HELPER axis: redial a helper that was
   * serving and is gone (ADR-727g63tr decision 2).
   *
   * The press is the same shape as the reconnect's — the pane and the epoch of
   * the observation whose button was pressed — and the answer is an
   * acknowledgement, `{connectionId, refreshed}`. The redial and its hello are
   * observed on pane.connection; this call waits for neither and never
   * installs. It draws nothing yet: the surface that offers the button is a
   * later stage, and this is the one typed way it will press it.
   */
  async refreshHelper(paneId: string, connectionEpoch: number): Promise<string> {
    const result = await this.client.dispatcher.call<ConnectionHelperRefreshResult>(
      'connection.helperRefresh',
      { paneId, connectionEpoch },
    )
    if (
      typeof result !== 'object' ||
      result === null ||
      typeof result.connectionId !== 'string' ||
      result.connectionId === '' ||
      result.refreshed !== true
    ) {
      throw new Error('nocx: connection.helperRefresh did not accept the refresh')
    }
    return result.connectionId
  }

  async reconnectPane(paneId: string, connectionEpoch: number): Promise<void> {
    const result = await this.client.dispatcher.call<PaneReconnect>('pane.reconnect', {
      paneId,
      connectionEpoch,
    })
    if (
      typeof result !== 'object' ||
      result === null ||
      result.paneId !== paneId ||
      result.registered !== true
    ) {
      throw new Error('nocx: pane.reconnect did not register this pane')
    }
  }
}
