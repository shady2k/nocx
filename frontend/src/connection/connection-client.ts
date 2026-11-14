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
// the backend closes the old connection, opens a new shell and answers with the
// open result. Nothing here retries, backs off or decides whether an attempt
// is allowed: the offer's presence already decided that, and the epoch refuses
// a press that is about a state the person is no longer looking at.

import type {
  OpenAnchor,
  OpenResult,
  OpenWatch,
  SessionHandle,
  SessionSize,
  WSClient,
} from '../ipc'

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
   *  desiredMode is the connect-time ask's one-shot answer for THIS session
   *  (ADR-0069): a hand-typed connection has no saved profile to write the
   *  choice onto, so a retry after the ask carries it here instead. */
  openByHost(
    size: SessionSize,
    host: string,
    user?: string,
    anchor: OpenAnchor = {},
    desiredMode?: string,
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
          ...(desiredMode ? { desiredMode } : {}),
          ...paneParam(anchor),
        },
        undefined,
        { announced: watch },
      )
      .then((result) => this.client.adoptOpen(result, size))
  }

  /**
   * The person's one action on a connection that is not usable: carry the
   * reconnect out in the backend and adopt the shell it answers with.
   *
   * The epoch is the observation's own — the one whose button was pressed.
   * A press carrying anything else is about a state the person is no longer
   * looking at, and the backend refuses it with `stale connectionEpoch`.
   */
  reconnectPane(
    size: SessionSize,
    paneId: string,
    connectionEpoch: number,
  ): Promise<SessionHandle> {
    return this.client.dispatcher
      .call<OpenResult>('pane.reconnect', { paneId, connectionEpoch })
      .then((result) => this.client.adoptOpen(result, size))
  }
}
