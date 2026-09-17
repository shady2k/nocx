/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.signalUndelivered.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the session.signalUndelivered server-to-client notification: a Stop the person was told was ACCEPTED will not reach the command it was addressed to, and why (nocx-zas0d). It exists because session.signal answers `held` and then has no request left to answer with: an app attempt is open from editor submit, a round trip before its bytes reach the pty (ADR-0024 §5), so a Stop that arrives in that window is held for the attempt's authenticated start instead of being written into bash's parser — and the delivery can still fail, or find the command already gone. Silence here would mean a product that said a stop was coming and then never mentioned it again, which is the soft degrade AGENTS.md refuses. Sent at most once per held Stop, and only when the obligation ends without a write: a Stop that lands says nothing, because the block's own completion is what shows it. The renderer drops the stop mark it took on the acceptance and says what happened; the block's outcome is then the shell's own again.
 */
export interface SessionSignalUndelivered {
  /**
   * The session the held Stop belongs to. Server-authoritative (AD-7), the same id the open ack returned and session.signal answered about.
   */
  sessionId: string
  /**
   * The intent that was accepted and did not land. One value today — only `stop` is ever held, because an interrupt is about the present moment and a command that has not begun is not a present moment — and it is carried rather than assumed so the renderer can correlate the notice with a gesture it made.
   */
  signal: 'stop'
  /**
   * The attempt the Stop was accepted FOR, as the backend minted it. This, and not the session, is what the renderer marks: the block whose attemptId is this was shown the acceptance, so it is the one whose mark comes off. An attempt id the renderer no longer holds answers nothing, which is the right outcome for a notice about a command that has been replaced.
   */
  attempt: string
  /**
   * Why the byte never reached the command, from a closed set — the renderer branches on it, and a word this build does not know must be a type error there rather than a wrong sentence. attempt-closed: the attempt left `open` before the byte could be written, because the command ended (or, for an attempt that never started at all, because the line never ran); the check that decides this runs at the WRITE, not when the Stop was accepted, which is what keeps an interrupt out of the prompt and out of whatever command came next. write-refused: the session's input path refused the byte — the queue was full, or the session was inside its bootstrap quarantine, where a keystroke is refused rather than buffered, or it was already closed. write-failed: the terminal itself did not take it. lane-refused: the delivery never reached the terminal at all. unsupported: this session's channel cannot be signalled from here, which is the same word session.signal answers with on the request path.
   */
  reason: 'attempt-closed' | 'write-refused' | 'write-failed' | 'lane-refused' | 'unsupported'
}
