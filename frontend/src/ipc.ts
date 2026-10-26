import { decodeFrame, encodeFrame, isSessionID, MSG_TYPE_METADATA } from './frame'
import type { DecodedFrame } from './frame'
import { Dispatcher, RpcError, type RpcIntent } from './dispatcher'
import type { AttachResult } from './generated/attach'
import type { Exit } from './generated/exit'
import type { Open } from './generated/open'
import type { SessionEntry, SessionsInventoryResult } from './generated/sessions.inventory'
import type { LiveSession, SessionsLiveResult } from './generated/sessions.live'
export interface SessionSize {
  cols: number
  rows: number
  xpixel: number
  ypixel: number
}
import type { Gap as RecoveryGap, SessionRecoveryStatus } from './generated/session.recoveryStatus'
import type { SessionOutputGap as SessionOutputGapNotification } from './generated/session.outputGap'
import type { SessionRecoveryChanged } from './generated/session.recoveryChanged'
// The stretch nothing kept is spelled once, in recovery-account.ts, where the
// account that renders it is derived. Imported here because this file is what
// mints the derived range at a reclaim (reclaimSession), and one spelling of
// the word is the whole point (nocx-fz4qa).
import { UNRECORDED } from './recovery-account'

import type { SessionDisplaced } from './generated/session.displaced'
import type { SessionLiveness } from './generated/session.liveness'
import type { SessionFrame } from './generated/session.frame'
import type { SessionEffect } from './generated/session.effect'
import { isValidCommandSnapshotNames } from './command-snapshot'
import type { SessionHistoryPage } from './generated/session.historyPage'
import type { SessionHistoryPageRows } from './generated/session.historyPageRows'
import type { BlockOutputStart } from './generated/block.outputStart'
import type { SessionIntentResult } from './generated/session.intent'
import type { SessionObservationChanged } from './generated/session.observationChanged'
import type { SessionIntegrationChanged } from './generated/session.integrationChanged'
import { log } from './log'
import { isDriverState, isPaneProgress, readPaneChildren } from './pane-observation'
import type { SessionSignal } from './generated/session.signal'
import type { SecretsPaneClosed } from './generated/secrets.paneClosed'
import type { WorkersTabCreated } from './generated/workers.tabCreated'
import type { WorkersTabClosed } from './generated/workers.tabClosed'

const maxRememberedOutputGaps = 128

/** The open ack's wire shape (contracts/open.schema.json): the server
 *  assigns the session id (AD-7), and the resolved destination mode rides the
 *  same ack so the tab's capability control starts from the backend's own
 *  resolution (nocx-mlm7).
 *
 *  shellIntegrationReason is deliberately absent (nocx-dvql). It answered
 *  "is this session integrated" once, at open, and the two failures that
 *  matter most arrive later — a handshake that expires ten seconds in, and a
 *  channel lost mid-session. The session.integrationChanged notification
 *  answers it as a state instead; two answers would be the defect AD-8
 *  names. */
type OpenResult = {
  sessionId?: string
  instanceId?: string
  sessionEpoch?: number
  cwd?: string
  desiredMode?: Open['desiredMode']
  /** The workspace the backend RESOLVED for this session (nocx-fraus). It is
   *  derived, never sent: the renderer names a pane and the backend walks
   *  pane -> tab -> workspace itself, so this ack is the only place the
   *  renderer can learn where the session landed — the default workspace
   *  never renders, so the renderer has no name of its own for it. */
  workspaceId?: string
  /** The opener the backend ADMITTED, or null for a root session
   *  (nocx-9hu9d). Read rather than dropped because it is the only place the
   *  renderer ever learns it: the edge is written once, at open, and the ack
   *  is the one message that carries it. PROVENANCE ONLY — see
   *  SessionHandle.parent. */
  parent?: Open['parent']
  /** Whether this open has just entered the session into the integration
   *  axis `starting` (nocx-ui8q6.6): the fact that lets a pane tell "no
   *  status yet" apart from "no status ever coming" in the gap before the
   *  first session.integrationChanged, which arrives strictly after this ack
   *  (AD-7). See SessionHandle.awaitsIntegration for what it does and does
   *  not answer. */
  awaitsIntegration?: Open['awaitsIntegration']
  /** The epoch this pane's intents present (nocx-zg3k3.3.1). It rides this
   *  ack rather than a notification because a notification would arrive after
   *  the person's first key, and a renderer cannot type without it — the
   *  session refuses an intent presented under any other epoch, and one
   *  presented under none. Absent when the backend could not read one, which
   *  the wire admits: the handle then presents none and the refusal names the
   *  epoch in force. See SessionHandle.accessEpoch. */
  accessEpoch?: Open['accessEpoch']
}

/**
 * What an open carries beyond its geometry and its destination.
 *
 * NAMED rather than positional, for the reason TerminalContentHooks is named
 * (see terminal-content.ts): `user` and `paneId` are both optional strings in
 * adjacent slots on openSSHSessionByHost, so two bare positionals would let
 * every misalignment type-check — which is exactly the defect that put
 * onSetupVault into the onAdoptabilityChange slot.
 */
/** The wire's encoding of one intent payload: base64 of its UTF-8 bytes, the
 *  shape contracts/session.intent.params.schema.json declares. It lives here,
 *  beside the handle that owns every other wire detail of an intent, so no
 *  surface has to know it. */
function intentPayload(text: string): string {
  const bytes = new TextEncoder().encode(text)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary)
}

/** The claim refusals that mean the BINDING is wrong rather than the session
 *  being gone (nocx-xn63t.6.33). One vocabulary, declared by the backend
 *  (refuseClaim, internal/transport/ws_reclaim.go) and read here by the
 *  `data.reason` discriminator it exists for — never by matching the message,
 *  which is prose and may be reworded. "unknown-session" is deliberately NOT
 *  here: that one says this backend holds no such session, and there is no
 *  other incarnation of it to claim. */
const FOREIGN_BINDING_REFUSALS = new Set(['foreign-instance', 'foreign-incarnation'])

/** Only a typed claim verdict establishes absence; transport and replay
 * failures say nothing about whether the helper's shell still exists. */
function claimRefusalReason(err: unknown): string | undefined {
  if (!(err instanceof RpcError) || err.code !== -32602) return undefined
  const data = err.data
  if (typeof data !== 'object' || data === null || !('reason' in data)) return undefined
  return typeof data.reason === 'string' ? data.reason : undefined
}

/** The kinds of input a person produces, as the wire spells them
 *  (contracts/session.intent.params.schema.json's `kind`). It is the whole of
 *  sessionruntime's intent vocabulary and not the printable subset of it: a
 *  click and a focus change are input too, and this renderer is the only
 *  source of them (nocx-zg3k3.3.1, design §6.1). What the payload looks like
 *  follows from the kind — a key name with modifiers, committed text, a paste
 *  body, a mouse event in cells, or "in"/"out". */
export type SessionIntentKind = 'key' | 'text' | 'paste' | 'mouse' | 'focus'

export interface OpenAnchor {
  /**
   * The pane this session is the pipe of: the renderer-minted UUIDv7 the
   * layout chain stores (design §7). The renderer is the only end that knows
   * it — the backend walks pane -> tab -> workspace from here, and told
   * nothing it can only answer "the default", which leaves every block this
   * session records anchored on nothing (nocx-rtg0.29).
   *
   * ABSENT AND EMPTY ARE DIFFERENT ANSWERS on the wire. validateOpenRaw
   * treats an absent paneId as legitimate — a session attached to no
   * recorded pane — and refuses a malformed one with -32602. An empty string
   * is the second, so the openers below drop the key rather than send it
   * blank.
   */
  paneId?: string
}

/** The paneId as the wire wants it: the key, or no key at all. One helper for
 *  all three openers, because "how an absent pane is expressed" is one fact
 *  and three copies of `...(x ? {paneId: x} : {})` would be three. */
function paneParam(anchor: OpenAnchor): { paneId?: string } {
  return anchor.paneId ? { paneId: anchor.paneId } : {}
}

// Ack throttle: at most one ack per session per ~100 ms. Per-frame acks on
// a fast-scrolling terminal would flood the control plane with thousands of
// tiny JSON-RPC notifications every second for no benefit: the ring is
// 256 KB and backpressure from a full ring kicks in at ~8 ms of 32 KB/ms
// output, so a 100 ms interval drains ~12 ring cycles per ack — well within
// the AD-9 trimming budget without needless chatter.
const ACK_INTERVAL_MS = 100

/** Metadata the pane can explain after reclaim. The screen itself comes from
 *  the backend's accepted frame/history sources, never replayed terminal bytes. */
export interface SessionRecovery {
  produced: number
  gaps: RecoveryGap[]
  statusUnavailable?: boolean
}

/** Whether a session.recoveryChanged payload is an answer this build can use:
 *  a non-negative safe-integer frontier, and every range a pair of safe offsets
 *  in stream order. SHAPE ONLY — the numbers are the ledger's, and computing
 *  them here would be a second owner of its arithmetic (nocx-zg3k3.14.24). */
function isRecoveryRevision(raw: Record<string, unknown>): boolean {
  const produced = raw.produced
  if (typeof produced !== 'number' || !Number.isSafeInteger(produced) || produced < 0) return false
  const gaps = raw.gaps
  if (!Array.isArray(gaps)) return false
  return gaps.every((gap: unknown) => {
    if (!gap || typeof gap !== 'object') return false
    const { start, end, reason } = gap as Record<string, unknown>
    return (
      typeof start === 'number' &&
      typeof end === 'number' &&
      typeof reason === 'string' &&
      Number.isSafeInteger(start) &&
      Number.isSafeInteger(end) &&
      start >= 0 &&
      end >= start
    )
  })
}

export type SessionAttachment =
  { kind: 'attached' } | { kind: 'attaching' } | { kind: 'unavailable'; message: string }

type AttachOutcome = 'resumed' | 'lost' | 'unavailable'

interface SessionState {
  // The session's incarnation identity (nocx-3oupk), as minted by the
  // backend at open (AD-7): instanceId + sessionEpoch. Every observation
  // of this session — exit, integrationChanged, lifecycle.changed — is
  // compared against the pair, so a message out of a previous backend
  // instance is refused instead of applied. Always present: the open
  // contract requires the pair, and _registerHandle refuses an ack
  // without it.
  instanceId: string
  sessionEpoch: number
  // Monotonic byte offset — the total count of payload bytes received for
  // this session. Counted as frame.payload.byteLength, NOT decoded string
  // length, because a multi-byte rune is several bytes and one character.
  offset: number

  // The geometry THIS client last reported for this session — what it told
  // the backend at open, and every resize since. It is kept because the
  // report has to survive the socket: the backend returns a session to its
  // named default the moment its last client detaches (nocx-eidfb.2), so a
  // reconnecting client that said nothing would leave a live window's
  // terminal running at 80x24. It rides the attach, which re-takes the
  // session and its size together.
  //
  // Null for a session this client has never reported a size for — a pane
  // reclaimed by a window that has not laid it out yet. That is not a report
  // of nothing: the attach then carries no geometry at all and the backend
  // leaves the session at the size it is running at.
  reported: SessionSize | null

  // screenFrameCallback receives one parsed session.frame document per
  // metadata frame (msg-type 0x02, the screen plane) for this session. It is
  // the byte path's sibling, not its part: a frame is a full snapshot, so it
  // never touches offset or acks (AD-9 counts PTY bytes only).
  // A frame arriving before registration is dropped: the next revision
  // repaints whole, without buffering superseded snapshots.
  screenFrameCallback: ((frame: SessionFrame) => void) | null
  effectCallback: ((effect: SessionEffect) => void) | null
  seenEffects: Set<string>
  pendingEffects: SessionEffect[]
  // historyPageCallback receives one parsed session.historyPageRows
  // document (nocx-zg3k3.10.3) — the rows of one live-history page, riding
  // the same metadata frame the screen plane shares, keyed by the pageId
  // the session.historyPage result names. The correlation is the caller's:
  // this layer delivers the document as parsed, exactly as a frame's.
  historyPageCallback: ((page: SessionHistoryPageRows) => void) | null
  // exitCallback receives the wire Exit (contracts/exit.schema.json): the
  // closed-set cause separating an authoritative shell exit (with its
  // status) from a loss. The failed-reattach path delivers a loss with the
  // same object, so a tab that lost its backend session is treated exactly
  // like a tab whose channel died — marked, never destroyed (nocx-ictcq).
  exitCallback: ((exit: Exit) => void) | null
  resetCallback: (() => void) | null
  outputGapCallback: ((gap: SessionOutputGapNotification) => void) | null
  pendingOutputGaps: SessionOutputGapNotification[]
  protocolErrorCallback: ((message: string) => void) | null
  pendingProtocolErrors: string[]
  protocolBlocked: boolean
  attachment: SessionAttachment
  attachmentCallback: ((attachment: SessionAttachment) => void) | null
  attachAttempt: Promise<AttachOutcome> | null
  seenOutputGaps: Set<string>

  // Fires when the backend reports that this session's write queue refused
  // a frame — the channel has stopped accepting bytes and the keystrokes
  // are being dropped. Once per stall, not once per key.
  inputStalledCallback: (() => void) | null

  // The integration axis is a state, not an event. Keep its latest fact so
  // a pane that mounts or remounts after the notification still sees it.
  integrationChangedCallback: ((fact: SessionIntegrationChanged) => void) | null
  integrationFact: SessionIntegrationChanged | null
  // The recording's own revised answer to "what is missing"
  // (session.recoveryChanged, nocx-zg3k3.14.24). Kept like the integration
  // fact and for the same reason: the revision may arrive between the reclaim
  // and the moment the pane subscribes to it, and a card that missed it would
  // state the number the reclaim happened to catch.
  recoveryChangedCallback: ((revision: SessionRecoveryChanged) => void) | null
  recoveryRevision: SessionRecoveryChanged | null
  // Fires when the backend revises what it believes about REACHING this
  // session: alive, or unknown when the host has stopped answering and
  // nothing has ended (contracts/session.liveness.schema.json). The
  // terminal half of that vocabulary never arrives here — a session that
  // ended is the exit notification's news.
  livenessCallback: ((liveness: SessionLiveness) => void) | null
  // What an ENROLLED agent pane's screen is currently inviting, as the
  // agent's driver classified it in the backend (nocx-szb40.3). Null for
  // every pane nobody enrolled, which is almost all of them.
  observationCallback: ((observation: SessionObservationChanged) => void) | null

  // The livenessEpoch of the last observation applied to this session. An
  // observation whose epoch is not GREATER describes an older moment,
  // whatever order it arrived in, and is dropped — the receiving half of
  // the rule the backend's record keeps (nocx-iarf9).
  livenessEpoch: number
}

// Per-session ack throttle state, tracked outside SessionState so the timer
// cancel/restart logic is self-contained.
interface AckThrottle {
  timer: ReturnType<typeof setTimeout> | null
  pendingOffset: number
}

export class SessionHandle {
  /** What a reclaim recovered from the recording, and what the recording has
   *  since revised it to (nocx-zg3k3.14.24). Seated by the constructor and
   *  replaced only by applyRecoveryRevision — DECLARED rather than a parameter
   *  property because it is the one fact on this handle that changes after the
   *  handle exists, and a `readonly` there would have made the revision a
   *  second field nobody assigns. */
  recovered: SessionRecovery | null

  constructor(
    private client: WSClient,
    readonly sessionId: string,
    /** Where the shell started, ~-abbreviated. Names the tab until a program
     *  sets a title; does not follow `cd` (that needs OSC 7, nocx-5mn.2). */
    readonly cwd: string,
    /** The resolved destination mode the backend stamped at open
     *  (nocx-mlm7): the connection-scope default the tab's capability
     *  control starts from. Never proof integration succeeded — the reason
     *  field and the arrival of markers confirm or downgrade it. */
    readonly desiredMode: Open['desiredMode'] = 'script',
    /** The session that opened this one, as the backend ADMITTED it, or null
     *  for a root session (nocx-9hu9d). The full identity, never a bare id:
     *  an id alone re-resolves to whatever holds it now.
     *
     *  PROVENANCE ONLY (nocx-wtv3p, ADR-0020 §5). It says "A created B" and
     *  confers nothing: no surface may read it to decide that one tab may
     *  observe, drive or close another, and the backend refuses such an
     *  attempt whatever the renderer believes
     *  (internal/transport/ws_lineage_prohibitions_test.go). The one thing it
     *  is read for is the ASK in PaneManager.closePane — naming what a close
     *  would leave running, which is the opposite of acting on them. */
    readonly parent: Open['parent'] = null,
    /** The workspace the backend resolved this session into (nocx-fraus).
     *
     *  IT CARRIES NO BEHAVIOUR, and the contract says so: nothing reads
     *  authority, addressability or reachability from it, and §5.5 forbids
     *  any surface before the fence epic from describing a workspace as
     *  safe, isolated or contained. It is read as PROVENANCE — what the
     *  backend resolved from the pane the renderer named — which is why it
     *  is decoded here rather than dropped: this ack is the only message
     *  that carries it. */
    readonly workspaceId: string = '',
    /** What a reclaim recovered from the backend's recording before it
     *  attached (nocx-22k1c.2), or null for a handle that was never
     *  reclaimed — an open starts an empty session and has nothing to
     *  recover.
     *
     *  It is READ BY THE SURFACE THAT DRAWS THE PANE, which is the only
     *  thing that can say what is missing where a person will see it. The
     *  bytes are already queued for the terminal by the time this handle
     *  exists; what this carries is the part the terminal cannot show —
     *  the ranges nothing kept. A recovered scrollback with a silent hole
     *  in it is the one outcome worse than a short one. */
    recovered: SessionRecovery | null = null,
    /** Whether the session's integration axis currently reads `starting`
     *  (nocx-ui8q6.6, contracts/open.schema.json and — for a reclaimed or
     *  reattached handle, nocx-ty0hc — contracts/attach.schema.json). It
     *  answers one question only — is a session.integrationChanged
     *  notification guaranteed to follow this ack — and only for the gap
     *  before the first one arrives: once a fact exists, the pane reads the
     *  fact (isAwaitingIntegration in integration/status.ts) and this is
     *  ignored.
     *
     *  An OPEN reads it off the open ack; a RECLAIM (ipc.ts reclaimSession)
     *  reads it off the attach ack instead, because that is the call that
     *  actually claims the session — the same axis, asked through the other
     *  door. The default here is `false` only for a caller that supplied
     *  neither, which no longer includes reclaimSession: it is the safe
     *  direction regardless, since a false default can only ever show a
     *  terminal a fact later says to hide, never the other way round. */
    readonly awaitsIntegration: boolean = false,
    /** The epoch this pane's intents present (nocx-zg3k3.3.1), or null when
     *  the backend could not read one.
     *
     *  THE HANDLE OWNS IT, and that is the point: the epoch belongs to the
     *  session the helper holds, not to the surface that draws the pane, and a
     *  renderer that had to carry it would be holding a second copy of a fact
     *  it cannot see. It is seated here from the ack that created or claimed
     *  the session, and re-seated by every answer that names one — which is
     *  how a revocation heals: the refusal says what to present next, and the
     *  intent it refused is presented again (intent() below).
     *
     *  Null is a state the wire admits rather than a default to invent: an
     *  open whose helper could not answer carries no epoch, the first intent
     *  presents none, and the refusal names the one in force. */
    public accessEpoch: number | null = null,
    /** The stretch this reclaim could not account for at all, derived HERE at
     *  the claim and kept for the life of the handle: `produced` had not
     *  reached the ring's replay base when this client attached, so no
     *  recording holds the bytes between them. It is a fact about the ATTACH,
     *  not about the ledger, which is why a later revision of the ledger's
     *  gaps is composed with it rather than replacing it
     *  (nocx-zg3k3.14.24). Empty for a handle that was opened, not reclaimed. */
    private readonly derivedRecoveryGaps: readonly RecoveryGap[] = [],
  ) {
    this.recovered = recovered
  }

  send(data: string): void {
    this.client.sendToSession(this.sessionId, data)
  }

  /**
   * Report this pane's geometry: the grid plus the WHOLE text area in
   * DEVICE pixels (TIOCSWINSZ's units; the helper decodes it into the
   * per-cell metric the frames carry, nocx-zg3k3.2.9, review round 1).
   *
   * False when the client deduped it — the same report the backend already
   * holds — so the caller marks the resize-echo window only around a
   * resize that actually went out.
   */
  sendResize(size: SessionSize): boolean {
    return this.client.sendResize(this.sessionId, size)
  }

  /** Wait for application, not admission or a locally deduped report. The
   *  renderer's input fence must not release input on a superseded resize. */
  resizeApplied(size: SessionSize): Promise<'applied' | 'superseded' | 'closed'> {
    return this.client.resizeApplied(this.sessionId, size)
  }

  /** Address a signal to the command running in this session (nocx-23rph).
   *  See WSClient.signalSession — this is the same call, spelled where a
   *  surface that already holds the handle can reach it. */
  signal(signal: SessionSignal['signal']): Promise<SessionSignal> {
    return this.client.signalSession(this.sessionId, signal)
  }

  close(): void {
    this.client.closeSession(this.sessionId)
  }

  detach(): void {
    this.client.detachSession(this.sessionId)
  }

  onAttachment(cb: (attachment: SessionAttachment) => void): void {
    this.client.onSessionAttachment(this.sessionId, cb)
  }

  /** Explicitly retry the existing session, never open a replacement shell. */
  retryAttach(): Promise<boolean> {
    return this.client.retrySessionAttach(this.sessionId)
  }

  onExit(cb: (exit: Exit) => void): void {
    this.client.onSessionExit(this.sessionId, cb)
  }

  // A reattach reset moves the transport cursor and resynchronizes
  // projections; screen contents still come from runtime frames.
  onReset(cb: () => void): void {
    this.client.onSessionReset(this.sessionId, cb)
  }

  /** Registers the visible live-stream notice for an output gap. */
  onOutputGap(cb: (gap: SessionOutputGapNotification) => void): void {
    this.client.onSessionOutputGap(this.sessionId, cb)
  }

  /** Registers for the recording's own revisions of what this session no
   *  longer holds (nocx-zg3k3.14.24). The callback carries nothing: this handle
   *  is where the fact lives, so a surface re-reads `recovered` — the same
   *  field it read when the pane mounted, now stating what the ledger states.
   *
   *  The latest revision is delivered at registration as well as on arrival
   *  (WSClient.onSessionRecoveryChanged), because a revision that landed
   *  between the reclaim and this subscription is the common case, not the
   *  rare one: the recorder's catch-up is precisely when they happen. */
  onRecoveryChanged(cb: () => void): () => void {
    return this.client.onSessionRecoveryChanged(this.sessionId, (revision) => {
      this.applyRecoveryRevision(revision)
      cb()
    })
  }

  /** Compose one revision with what only this handle could derive. The ledger
   *  owns its gaps; this handle owns the stretch nothing kept, and dropping it
   *  because the ledger has nothing to say about it would shrink the account
   *  the card states. Null `recovered` — a handle that opened rather than
   *  reclaimed — learns nothing from a revision: it has no card to correct, and
   *  giving it one would raise a notice about a session that never lost
   *  anything. */
  private applyRecoveryRevision(revision: SessionRecoveryChanged): void {
    if (this.recovered === null) return
    this.recovered = {
      produced: revision.produced,
      gaps: [...revision.gaps, ...this.derivedRecoveryGaps],
      statusUnavailable: false,
    }
  }

  /** Registers the session's current integration status. If the status
   *  arrived after the open ack but before the pane subscribed, that latest
   *  fact is delivered immediately. */
  onIntegrationChanged(cb: (fact: SessionIntegrationChanged) => void): () => void {
    return this.client.onSessionIntegrationChanged(this.sessionId, cb)
  }

  /** Registers the visible signal for a gap that violates the stream cursor. */
  onProtocolError(cb: (message: string) => void): void {
    this.client.onSessionProtocolError(this.sessionId, cb)
  }

  // onInputStalled registers a callback for the backend's report that this
  // session is dropping the input sent to it.
  onInputStalled(cb: () => void): void {
    this.client.onSessionInputStalled(this.sessionId, cb)
  }

  /** Registers a callback for the backend's revised belief about reaching
   *  this session (nocx-iarf9). Fires on a CHANGE — the backend publishes
   *  when the value changes, not once per probe — so a handler may treat
   *  every call as news. */
  onObservation(cb: (observation: SessionObservationChanged) => void): void {
    this.client.onSessionObservation(this.sessionId, cb)
  }

  onLiveness(cb: (liveness: SessionLiveness) => void): void {
    this.client.onSessionLiveness(this.sessionId, cb)
  }

  /** Registers a callback for the session's screen plane: one parsed
   *  session.frame document per metadata frame the backend publishes
   *  (nocx-zg3k3.2.8). Full snapshots, never bytes — the model on the other
   *  side refuses what it cannot install, and nothing here counts or acks. */
  onScreenFrame(cb: (frame: SessionFrame) => void): void {
    this.client.onSessionScreenFrame(this.sessionId, cb)
  }

  onEffect(cb: (effect: SessionEffect) => void): void {
    this.client.onSessionEffect(this.sessionId, cb)
  }

  /** Asks for one page of this session's live history, as the emulator
   *  holds it (nocx-zg3k3.10.3). before is the exclusive upper bound in the
   *  absolute row numbering every result carries — null asks for the head.
   *  The RESULT carries the page's facts; the rows arrive separately, on
   *  the screen plane, keyed by the result's pageId — see
   *  onHistoryPageRows. A dropped page (nobody attached, or the outbound
   *  queue refused it) rejects; the caller retries, it is not an error to
   *  survive silently. */
  historyPage(before: number | null, limit: number): Promise<SessionHistoryPage> {
    return this.client.historyPage(this.sessionId, before, limit)
  }

  /** Sends structured input to this pane's helper: what the person did, never
   *  the bytes it means. The helper owns terminal mode interpretation, and it
   *  refuses an intent presented under an obsolete access epoch before writing
   *  anything.
   *
   *  A REFUSAL IS RE-PRESENTED ONCE, and only one that wrote nothing
   *  (nocx-zg3k3.3.1). A revocation lands between two keystrokes: the epoch
   *  this handle holds becomes stale, the session refuses the intent with
   *  `access_revoked` and zero bytes, and the answer names the epoch in force.
   *  Re-seating the copy and sending the SAME intent again is what turns that
   *  from a keystroke lost forever into a round trip — and it is safe for
   *  exactly the reason the refusal is: nothing was written, so the second
   *  attempt is the first write. A partial write (bytesWritten > 0) is never
   *  re-presented, because that one did reach the program. */
  intent(kind: SessionIntentKind, payload: string): Promise<SessionIntentResult> {
    return this._intent(kind, payload, true)
  }

  /** The wire's own encoding of one intent payload, in the one place that
   *  decides it.
   *
   *  THE SCHEMA CARRIES BYTES AND THIS API CARRIES WHAT THE PERSON DID.
   *  contracts/session.intent.params.schema.json declares `payload` as base64
   *  (`contentEncoding`), because the intent's argument is bytes on the Go side
   *  — a key name, committed text, a paste body — and a renderer that sent the
   *  text raw would be sending a field the transport cannot decode. Measured:
   *  it cost the whole RAW-mode path, silently — the intent never reached the
   *  session, no refusal came back, and the shell's `read` waited forever,
   *  while every unit test passed because they assert the INTENT and not the
   *  frame it rides in (nocx-zg3k3.3.1).
   *
   *  UTF-8 and not `btoa(payload)`: an IME commit is what this carries most
   *  often, and btoa throws on anything outside Latin-1. */

  private async _intent(
    kind: SessionIntentKind,
    payload: string,
    mayRepresent: boolean,
  ): Promise<SessionIntentResult> {
    const presented = this.accessEpoch
    const result = await this.client.sessionIntent(
      this.sessionId,
      presented,
      kind,
      intentPayload(payload),
    )
    if (typeof result.accessEpoch === 'number' && result.accessEpoch > 0) {
      this.accessEpoch = result.accessEpoch
    }
    if (
      mayRepresent &&
      result.state === 'refused' &&
      result.refusal?.cause === 'access_revoked' &&
      result.bytesWritten === 0 &&
      this.accessEpoch !== presented
    ) {
      return this._intent(kind, payload, false)
    }
    return result
  }

  /** Registers a callback for one live-history page's rows: the parsed
   *  session.historyPageRows document whose pageId a historyPage result
   *  named. Same plane, same policy as onScreenFrame — the model validates
   *  on intake, nothing here counts or acks. */
  onHistoryPageRows(cb: (page: SessionHistoryPageRows) => void): void {
    this.client.onSessionHistoryPageRows(this.sessionId, cb)
  }
}

export class WSClient {
  private sessions = new Map<string, SessionState>()
  // Ack throttle: one per session.
  private acks = new Map<string, AckThrottle>()
  // Reattach-outcome subscribers (nocx-gbhwh): the notice consumes the
  // aggregate of one reconnect's session reattach pass.
  private reconnectResultHandlers = new Set<(r: { resumed: number; lost: number }) => void>()
  // Displacement subscribers (nocx-oevq4, D8): another client took a session
  // this one was holding. A client-level set rather than a per-session
  // callback because the surface that has to react — the pane holding the
  // session — is found BY the session id, and a pane that never registered a
  // callback is exactly the one that would otherwise go on advertising a
  // terminal it no longer has.
  private displacedHandlers = new Set<(displaced: SessionDisplaced) => void>()

  // workers.tabCreated subscribers (nocx-ui8q6.3): a participant's tab has
  // appeared in the shared layout chain. Same shape as displacedHandlers —
  // a client-level set, because the notification is a broadcast rather than
  // addressed to one session.
  private workerTabCreatedHandlers = new Set<(fact: WorkersTabCreated) => void>()

  // workers.tabClosed subscribers (nocx-xn63t.4.6): a participant's tab has
  // LEFT the shared layout chain, closed by workers.close. The created set's
  // own shape for the created fact's own reason — a broadcast, and the window
  // drawing the strip is not the caller that asked for the close.
  private workerTabClosedHandlers = new Set<(fact: WorkersTabClosed) => void>()
  private blockOutputStartHandlers = new Set<(fact: BlockOutputStart) => void>()

  constructor(private readonly dispatcherImpl: Dispatcher) {
    dispatcherImpl.subscribe('block.outputStart', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      if (typeof raw.entryId !== 'string' || raw.entryId.length === 0) return
      if (typeof raw.row !== 'number' || !Number.isSafeInteger(raw.row) || raw.row < 0) return
      if (
        typeof raw.screenRevision !== 'number' ||
        !Number.isSafeInteger(raw.screenRevision) ||
        raw.screenRevision < 0
      )
        return
      if (raw.bufferKind !== 'normal' && raw.bufferKind !== 'alternate') return
      if (typeof raw.bufferInstance !== 'string' || !/^[1-9][0-9]*$/.test(raw.bufferInstance))
        return
      if (typeof raw.cols !== 'number' || !Number.isSafeInteger(raw.cols) || raw.cols < 1) return
      if (typeof raw.rows !== 'number' || !Number.isSafeInteger(raw.rows) || raw.rows < 1) return
      const fact = raw as unknown as BlockOutputStart
      for (const handler of this.blockOutputStartHandlers) handler(fact)
    })
    // Wire binary frame handling and session reattach on every connect/reconnect.
    this.dispatcher.onConnect(() => {
      const ws = this.dispatcher.socket!
      ws.onmessage = (event: MessageEvent) => {
        if (event.data instanceof ArrayBuffer) {
          const frame = decodeFrame(event.data)
          if (frame) {
            // THE SCREEN PLANE routes before any byte accounting: a
            // metadata frame is not PTY bytes — counting it would corrupt
            // the replay offset (AD-9) and ack bytes nobody wrote.
            if (frame.msgType === MSG_TYPE_METADATA) {
              this._routeScreenFrame(frame)
              return
            }
            const state = this.sessions.get(frame.sessionId)
            if (state) {
              if (state.protocolBlocked) return
              // Raw output carries only the transport cursor here. The
              // runtime owns decoding; screen frames and effects carry all
              // presentation. Never retain or decode this payload.
              state.offset += frame.payload.byteLength
              this._scheduleAck(frame.sessionId, state.offset)
            }
          }
        }
      }

      // Reattach every session the client still knows about. Each attach
      // carries the last received byte offset so the server can replay
      // what the ring still holds. The outcomes are aggregated so a
      // listener can state what became of the sessions on this reconnect
      // (nocx-gbhwh): resumed, or gone — the backend no longer has them.
      const reattached = [...this.sessions.entries()].map(([sid, state]) =>
        this._reattachSession(sid, state),
      )
      // Report once every attach has settled — the socket is back before
      // this fires, never the other way round. Zero sessions resolves
      // immediately with { resumed: 0, lost: 0 }.
      void Promise.all(reattached).then((outcomes) => {
        const resumed = outcomes.filter((o) => o === 'resumed').length
        const lost = outcomes.filter((o) => o === 'lost').length
        for (const h of this.reconnectResultHandlers) {
          h({ resumed, lost })
        }
      })
    })
    this.dispatcher.onDisconnect(() => {
      for (const state of this.sessions.values()) {
        this._setAttachment(state, { kind: 'attaching' })
      }
    })

    // Handle server-initiated exit notifications. The cause is the closed
    // set of contracts/exit.schema.json: "exited" (the shell exited, with
    // its status) or "interrupted" (a loss). A cause that is neither — or
    // absent, from a peer that predates the contract — is treated as a
    // loss, deliberately: a wrongly-marked tab is recoverable, a wrongly
    // destroyed tab is lost work, so the safe direction is to never close
    // on ambiguous data.
    this.dispatcher.subscribe('session.outputGap', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      if (typeof raw.sessionId !== 'string') return
      const sessionId = raw.sessionId
      const state = this.sessions.get(sessionId)
      if (!state || state.protocolBlocked) return

      const reportProtocolError = (message: string, details: Record<string, unknown>) => {
        state.protocolBlocked = true
        log.error('nocx: stopped session after an invalid output-gap notification', {
          sessionId,
          ...details,
        })
        if (state.protocolErrorCallback) state.protocolErrorCallback(message)
        else state.pendingProtocolErrors.push(message)
      }

      const start = raw.start
      const end = raw.end
      const reason = raw.reason
      const maxSafe = Number.MAX_SAFE_INTEGER
      if (
        typeof start !== 'number' ||
        typeof end !== 'number' ||
        typeof reason !== 'string' ||
        !Number.isSafeInteger(start) ||
        !Number.isSafeInteger(end) ||
        start < 0 ||
        end <= start ||
        end > maxSafe
      ) {
        const message = `Session output protocol error: malformed gap bounds [${String(start)}, ${String(end)}). Output is paused.`
        reportProtocolError(message, { start, end, reason })
        return
      }
      const gap: SessionOutputGapNotification = { sessionId, start, end, reason }
      const gapKey = JSON.stringify([start, end, reason])
      if (state.seenOutputGaps.has(gapKey)) return
      if (state.seenOutputGaps.size >= maxRememberedOutputGaps) {
        const message = `Session output protocol error: the ${maxRememberedOutputGaps}-range deduplication limit was reached. Output is paused.`
        reportProtocolError(message, { limit: maxRememberedOutputGaps, start, end })
        return
      }

      // A reset may have reached a new browser after its restored cursor has
      // already passed the hole. It is still a fact to show, but it is not a
      // stream transition: the cursor and ack have already moved on.
      if (end <= state.offset) {
        state.seenOutputGaps.add(gapKey)
        if (state.outputGapCallback) state.outputGapCallback(gap)
        else state.pendingOutputGaps.push(gap)
        return
      }

      // The only ordered transition is a hole at exactly the next expected
      // byte. Every other relation is ambiguous and stops this session's data
      // path rather than silently splicing or skipping output.
      if (start !== state.offset) {
        const message = `Session output protocol error: gap [${start}, ${end}) does not match current offset ${state.offset}. Output is paused.`
        reportProtocolError(message, { expected: state.offset, start, end })
        return
      }

      state.seenOutputGaps.add(gapKey)
      state.offset = end
      this._flushAck(sessionId)
      this._sendAck(sessionId, end)
      if (state.outputGapCallback) state.outputGapCallback(gap)
      else state.pendingOutputGaps.push(gap)
    })

    // The recording's own revision of what this session no longer holds
    // (nocx-zg3k3.14.24). session.recoveryStatus answers that question at the
    // instant it is asked, and a reclaim asks it ONCE — before it attaches,
    // while the recorder is still appending the bytes the helper replayed. So
    // the answer a card is built from is a fact about an instant that has
    // already passed, and it keeps passing while a person reads the card. This
    // is the same answer pushed when it changes, so the card states the
    // ledger's number rather than the number the reclaim caught.
    //
    // A malformed revision is DROPPED, not fatal: it cannot corrupt the byte
    // stream the way a malformed gap can (that one must stop the session), and
    // the next revision — or the next reclaim — carries the true answer.
    this.dispatcher.subscribe('session.recoveryChanged', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      if (typeof raw.sessionId !== 'string') return
      const state = this.sessions.get(raw.sessionId)
      if (!state) return
      if (!isRecoveryRevision(raw)) {
        log.debug('nocx: dropped a malformed recovery revision', { sessionId: raw.sessionId })
        return
      }
      const revision: SessionRecoveryChanged = {
        sessionId: raw.sessionId,
        produced: raw.produced as number,
        gaps: raw.gaps as SessionRecoveryChanged['gaps'],
      }
      state.recoveryRevision = revision
      state.recoveryChangedCallback?.(revision)
    })

    this.dispatcher.subscribe('exit', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      // The session's own identity (nocx-3oupk): the wire's pair is the
      // backend's word (AD-7), and the identity learned at open is the
      // fallback for a notification that predates the fields — the renderer
      // never mints one. With neither, the notification cannot be told from
      // a previous incarnation and is dropped rather than applied.
      const state = this.sessions.get(sid)
      const instanceId = typeof raw.instanceId === 'string' ? raw.instanceId : state?.instanceId
      const sessionEpoch =
        typeof raw.sessionEpoch === 'number' ? raw.sessionEpoch : state?.sessionEpoch
      if (typeof instanceId !== 'string' || typeof sessionEpoch !== 'number') {
        return
      }
      const isExited = raw.cause === 'exited' && Number.isInteger(raw.status)
      // A loss never carries a status; an exited event missing its status is
      // malformed and reads as a loss too (the schema's exited branch
      // requires it).
      const exit: Exit = isExited
        ? {
            sessionId: sid,
            cause: 'exited',
            status: raw.status as number,
            instanceId,
            sessionEpoch,
          }
        : { sessionId: sid, cause: 'interrupted', instanceId, sessionEpoch }
      this._flushAck(sid)
      state?.exitCallback?.(exit)
      this.sessions.delete(sid)
    })

    // The backend revised what it believes about reaching a session
    // (nocx-iarf9). Two refusals before anything is delivered, and both are
    // the point rather than defensiveness:
    //
    //   - the incarnation must be the one this tab opened. A report naming
    //     another backend instance, or another epoch of this session id, is
    //     about a different session that merely shares the id (nocx-3oupk).
    //   - the observation must be NEWER than the last one applied. A report
    //     delayed behind a faster path would otherwise revive a belief the
    //     backend has already moved on from, purely by arriving last.
    // What an enrolled agent pane's screen is inviting (nocx-szb40.3).
    //
    // Guarded at the boundary like every other unsolicited notification, and
    // bound to the INCARNATION here rather than downstream: this is the one
    // place that already holds the (instanceId, sessionEpoch) pair the open
    // ack minted, so an observation out of a previous incarnation is refused
    // once, for every consumer, instead of each of them remembering to.
    //
    // The state is checked against the closed set on the way in. A value
    // nobody wrote a branch for would otherwise reach the indicator and land
    // in whichever branch was written last — and every consumer of this
    // treats what it cannot read as busy, which only works if what it cannot
    // read never gets through.
    this.dispatcher.subscribe('session.observationChanged', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      const state = this.sessions.get(sid)
      if (!state) return
      if (raw.instanceId !== state.instanceId || raw.sessionEpoch !== state.sessionEpoch) return
      const agent = raw.agent
      if (typeof agent !== 'string' || agent === '') return
      const paneState = raw.state
      if (!isDriverState(paneState)) return
      // The progress facet is guarded the same way and with the same
      // consequence, because it is the same kind of claim about the same
      // pane: a scalar from a set nobody can add to. It is NOT carried
      // beside the children's tolerance — a child row is a separate claim
      // and a malformed one costs the row, while this is one of the two
      // things the observation is ABOUT.
      const progress = raw.progress
      if (!isPaneProgress(progress)) return
      // The child rows travel BESIDE the state and are guarded separately,
      // because they are a separate claim: a malformed row must cost the row
      // and not the classification, and an observation whose children could
      // not be read is still an observation about the pane.
      const children = readPaneChildren(raw.children)
      state.observationCallback?.({
        sessionId: sid,
        instanceId: state.instanceId,
        sessionEpoch: state.sessionEpoch,
        agent,
        state: paneState,
        progress,
        ...(children === null ? {} : { children }),
      })
    })

    this.dispatcher.subscribe('session.effect', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      const state = this.sessions.get(sid)
      if (!state) return
      const generation = raw.generation
      const effectId = raw.effectId
      const kind = raw.kind
      const title = raw.title
      const body = raw.body
      let commandNames: [string, ...string[]] | undefined
      if (typeof generation !== 'string' || !/^[1-9][0-9]*$/.test(generation)) return
      if (typeof effectId !== 'string' || !/^[1-9][0-9]*$/.test(effectId)) return
      if (
        kind !== 'bell' &&
        kind !== 'notification' &&
        kind !== 'clipboard' &&
        kind !== 'title' &&
        kind !== 'cwd' &&
        kind !== 'promptBoundary' &&
        kind !== 'recovery' &&
        kind !== 'commandSnapshot'
      ) {
        log.debug('nocx: session effect refused: unknown kind', { sessionId: sid, kind })
        return
      }
      if (typeof title !== 'string' || typeof body !== 'string') return
      if (kind === 'commandSnapshot') {
        if (title !== '' || body !== '' || !isValidCommandSnapshotNames(raw.commandNames)) return
        commandNames = raw.commandNames
      } else if (raw.commandNames !== undefined) return
      const episodeId = raw.episodeId
      const screenRevision = raw.screenRevision
      if (kind === 'promptBoundary') {
        if (
          typeof screenRevision !== 'number' ||
          !Number.isSafeInteger(screenRevision) ||
          screenRevision < 1
        )
          return
      } else if (screenRevision !== undefined) return
      if (raw.fence !== undefined || raw.nonce !== undefined || raw.recoveryNonce !== undefined)
        return
      if (kind === 'recovery') {
        if (
          typeof episodeId !== 'string' ||
          !/^rec-[0-9a-f]{32}$/.test(episodeId) ||
          title !== '' ||
          body !== ''
        )
          return
      } else if (episodeId !== undefined) return
      const identity = `${generation}:${effectId}`
      if (state.seenEffects.has(identity)) return
      state.seenEffects.add(identity)
      const effect: SessionEffect = {
        sessionId: sid,
        generation,
        effectId,
        kind,
        title,
        body,
        ...(typeof episodeId === 'string' ? { episodeId } : {}),
        ...(typeof screenRevision === 'number' ? { screenRevision } : {}),
        ...(commandNames ? { commandNames } : {}),
      }
      if (state.effectCallback) state.effectCallback(effect)
      else state.pendingEffects.push(effect)
    })

    this.dispatcher.subscribe('session.integrationChanged', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string' || typeof raw.shell !== 'string') return
      // Every refusal says so: a pane left on "Checking this shell…" is
      // otherwise indistinguishable from one whose fact never arrived.
      const state = this.sessions.get(sid)
      if (!state) {
        log.debug('nocx: integration fact dropped: unknown session', {
          sessionId: sid,
          status: raw.status,
        })
        return
      }
      if (raw.instanceId !== state.instanceId || raw.sessionEpoch !== state.sessionEpoch) {
        log.debug('nocx: integration fact dropped: another instance or epoch', {
          sessionId: sid,
          status: raw.status,
          instanceId: raw.instanceId,
          sessionEpoch: raw.sessionEpoch,
          heldInstanceId: state.instanceId,
          heldSessionEpoch: state.sessionEpoch,
        })
        return
      }
      if (
        raw.status !== 'starting' &&
        raw.status !== 'integrated' &&
        raw.status !== 'conventional' &&
        raw.status !== 'lost'
      ) {
        return
      }
      const fact = raw as unknown as SessionIntegrationChanged
      state.integrationFact = fact
      log.debug('nocx: integration fact', {
        sessionId: sid,
        status: fact.status,
        delivered: state.integrationChangedCallback !== null,
      })
      state.integrationChangedCallback?.(fact)
    })

    this.dispatcher.subscribe('session.liveness', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      const state = this.sessions.get(sid)
      if (!state) return
      if (raw.instanceId !== state.instanceId || raw.sessionEpoch !== state.sessionEpoch) return
      const epoch = raw.livenessEpoch
      if (typeof epoch !== 'number' || epoch <= state.livenessEpoch) return
      const value = raw.liveness
      if (value !== 'alive' && value !== 'unknown') return
      const observedAt = raw.observedAt
      if (typeof observedAt !== 'string') return
      // The MEASUREMENT travels with the belief, and both halves of it.
      //
      // This rebuilds the fact rather than passing the payload on — the
      // identity fields are taken from the session's own record, not from the
      // wire — and that is exactly how these two came to be dropped: they are
      // the only fields with no local counterpart to copy from, so a
      // reconstruction written field by field simply never named them. The
      // renderer then saw slow === undefined for every host and drew nothing,
      // however slow the link got (nocx-y3i0s).
      //
      // Absent stays absent. The contract says absent and zero are one
      // statement — "nothing was measured" — so a host that never answered
      // must not arrive looking like one that answered instantly, and a
      // default of 0 or false here would be exactly that lie.
      const roundTripMs = typeof raw.roundTripMs === 'number' ? raw.roundTripMs : undefined
      const slow = typeof raw.slow === 'boolean' ? raw.slow : undefined
      state.livenessEpoch = epoch
      state.livenessCallback?.({
        sessionId: sid,
        instanceId: state.instanceId,
        sessionEpoch: state.sessionEpoch,
        liveness: value,
        livenessEpoch: epoch,
        observedAt,
        ...(roundTripMs !== undefined ? { roundTripMs } : {}),
        ...(slow !== undefined ? { slow } : {}),
      })
    })

    // Another client took a session this one was holding (D8). The take has
    // already happened on the backend — no output is coming and no input will
    // be accepted — so this is not a request to give it up, it is the news
    // that it is gone.
    //
    // The session is dropped from the map BEFORE the handlers run, and that
    // order is the point: a handler that reacted while the entry was still
    // there could send one more keystroke into a stream this client no longer
    // owns, and sendToSession's only guard is the map.
    this.dispatcher.subscribe('session.displaced', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const sid = raw.sessionId
      if (typeof sid !== 'string') return
      const state = this.sessions.get(sid)
      // Unknown, or a previous incarnation of this id: not this client's
      // session, so nothing is dropped and nobody is told. The identity is
      // checked exactly as every other session-scoped notification checks it
      // (nocx-3oupk) — the renderer never mints one and never assumes one.
      if (!state) return
      if (raw.instanceId !== state.instanceId || raw.sessionEpoch !== state.sessionEpoch) return
      this._flushAck(sid)
      this.sessions.delete(sid)
      const displaced: SessionDisplaced = {
        sessionId: sid,
        instanceId: state.instanceId,
        sessionEpoch: state.sessionEpoch,
      }
      for (const h of this.displacedHandlers) h(displaced)
    })

    // A participant's tab has appeared (nocx-ui8q6.3). Unlike session.displaced
    // this names a session this client has never seen, so there is no prior
    // SessionState to compare against — the check instead uses
    // _currentInstanceId, which is ANY session already known on this
    // connection: one WebSocket speaks to exactly one backend instance for
    // its whole life (AD-7), so every session this client already holds
    // shares the fact this notification carries, and an instanceId that
    // disagrees with it can only be a fact queued before a reconnect this
    // client has since completed. With no session known yet (a pathological
    // first frame) there is nothing to judge it against, so it is accepted
    // rather than refused for a question that cannot yet be asked.
    this.dispatcher.subscribe('workers.tabCreated', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const known = this._currentInstanceId()
      if (known !== null && raw.instanceId !== known) return
      const fact = raw as unknown as WorkersTabCreated
      for (const h of this.workerTabCreatedHandlers) h(fact)
    })

    // A participant's tab has LEFT the window (nocx-xn63t.4.6), closed by
    // workers.close. The subscription, the audience and the staleness check
    // are the created fact's own, read the other way: a broadcast rather than
    // an answer to one session, and `_currentInstanceId` rather than a
    // SessionState because this client may well have no session for the tab
    // — a worker's session is one it never opened. An instanceId that
    // disagrees with any session already held names a fact queued before a
    // reconnect this client has since completed, and folding THAT in would
    // take a tab out of a strip the backend behind this socket still holds.
    this.dispatcher.subscribe('workers.tabClosed', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const raw = params as Record<string, unknown>
      const known = this._currentInstanceId()
      if (known !== null && raw.instanceId !== known) return
      const fact = raw as unknown as WorkersTabClosed
      for (const h of this.workerTabClosedHandlers) h(fact)
    })

    // The backend dropped input for a session: its write queue is full,
    // so the channel underneath has stopped accepting bytes.
    this.dispatcher.subscribe('inputStalled', (params: unknown) => {
      if (!params || typeof params !== 'object') return
      const sid = (params as Record<string, unknown>).sessionId
      if (typeof sid !== 'string') return
      this.sessions.get(sid)?.inputStalledCallback?.()
    })
  }

  /** The shared control-plane dispatcher (the sealed-access seam installed
   *  at the app root). Exposed for server-initiated notification
   *  subscriptions (lifecycle.changed) that ride the same socket; RPC stays
   *  behind the typed methods above. */
  get dispatcher(): Dispatcher {
    return this.dispatcherImpl
  }

  // Start discovery and connection. Sessions are not open yet — call
  // openSession() next to get a SessionHandle. The Dispatcher asks its
  // EndpointProvider for the endpoint on every attempt.
  start(): void {
    this.sessions.clear()
    this.acks.clear()
    this.dispatcher.start()
  }

  /** Call a control-plane method and resolve with its typed result. The
   *  control plane carries JSON-RPC (AD-1); session-bound calls (open,
   *  resize, close) go through the dedicated methods above so their
   *  correlation with the binary data plane stays owned here. */
  call<T = unknown>(
    method: string,
    params: unknown,
    signal?: AbortSignal,
    intent: RpcIntent = 'operation',
  ): Promise<T> {
    return this.dispatcher.call<T>(method, params, signal, intent)
  }

  /** Tell the backend a pane closed, so its PENDING CAPTURES die with it
   *  (nocx-tsajw). The paneId is the pane's one identity — the same UUIDv7
   *  the layout chain stores and lifecycle history carries — declared once
   *  in contracts/secrets.paneClosed.schema.json (SecretsPaneClosed is
   *  generated from it). Fire-and-forget: a lost notification is covered by
   *  transport disconnect, which is the same destruction the pane's death
   *  implies.
   *
   *  NOT the same act as panes.close, and the method was renamed to say so
   *  (nocx-isoph.4): this closes a capture scope and touches no store, while
   *  panes.close removes the pane from the durable chain and can mint a
   *  replacement tab. A pane the user closes sends both. */
  notifyPaneClosed(paneId: string): void {
    const params: SecretsPaneClosed = { paneId }
    this.dispatcher.notify('secrets.paneClosed', params)
  }

  // The screen plane's demux (msg-type 0x02), the one place a session.frame
  // document is parsed on this client. A frame for a session this client
  // does not hold, or a payload that is not a frame document, is dropped
  // with one debug line: neither is an error, and neither may throw out of
  // the socket handler. Shape validation is the model's intake job — the
  // document is handed over as parsed, refusals and all.
  private _routeScreenFrame(frame: DecodedFrame): void {
    const state = this.sessions.get(frame.sessionId)
    if (!state) {
      log.debug('nocx: screen frame dropped: unknown session', {
        sessionId: frame.sessionId,
      })
      return
    }
    let parsed: unknown
    try {
      // Boundary assertion: the payload is wire JSON the model validates on
      // intake; the parse here is the only shape this layer judges.
      parsed = JSON.parse(new TextDecoder().decode(new Uint8Array(frame.payload)))
    } catch {
      log.debug('nocx: screen plane dropped: payload is not a JSON document', {
        sessionId: frame.sessionId,
      })
      return
    }
    // One carrier, two cargos (nocx-zg3k3.10.3): a live-history page names
    // its pageId; a screen frame never does — the frame contract is
    // additionalProperties:false and has no such field, so a frame carrying
    // one would be malformed at the source. The discrimination is exact,
    // not a guess from shape.
    const candidate =
      parsed !== null && typeof parsed === 'object'
        ? (parsed as { pageId?: unknown; rows?: unknown })
        : undefined
    if (typeof candidate?.pageId === 'string' && Array.isArray(candidate.rows)) {
      state.historyPageCallback?.(parsed as SessionHistoryPageRows)
      return
    }
    state.screenFrameCallback?.(parsed as SessionFrame)
  }

  // --- ack plumbing -------------------------------------------------------

  // _scheduleAck posts a throttled ack for the session. If an ack is already
  // pending (timer running), the pending offset is updated but the timer is
  // not reset — this batches multiple frames into one ack per ACK_INTERVAL_MS.
  private _scheduleAck(sessionId: string, offset: number): void {
    let ack = this.acks.get(sessionId)
    if (!ack) {
      ack = { timer: null, pendingOffset: 0 }
      this.acks.set(sessionId, ack)
    }
    ack.pendingOffset = offset
    if (ack.timer !== null) return

    const throttled = ack
    throttled.timer = setTimeout(() => {
      throttled.timer = null
      this._sendAck(sessionId, throttled.pendingOffset)
    }, ACK_INTERVAL_MS)
  }

  private _flushAck(sessionId: string): void {
    const ack = this.acks.get(sessionId)
    if (!ack) return
    if (ack.timer !== null) {
      clearTimeout(ack.timer)
      ack.timer = null
    }
    this.acks.delete(sessionId)
  }

  private _sendAck(sessionId: string, offset: number): void {
    this.dispatcher.notify('ack', { sessionId, offset })
  }

  // --- session opening ----------------------------------------------------

  // openSession sends the JSON-RPC open request and resolves with a
  // SessionHandle carrying the server-assigned sessionId. Per AD-7, the
  // server assigns the authoritative id — nothing may be sent on the data
  // plane for this session before this resolves.
  //
  // Whether the session tries to become integrated is NOT ours to say
  // (nocx-tr2n): the backend asks for every session and falls back to a
  // plain terminal where it cannot. It used to be an `enhanced` argument
  // here, which both ssh openers below silently omitted — so an ssh tab
  // never established a lifecycle channel and could never show a block. The
  // renderer still wires the editor BEFORE this call, so no invisible
  // prompt gap can occur (nocx-4ff.10).
  openSession(size: SessionSize, anchor: OpenAnchor = {}): Promise<SessionHandle> {
    return this.dispatcher
      .call<OpenResult>('open', {
        ...size,
        ...paneParam(anchor),
      })
      .then((result) => this._registerHandle(result, size))
  }

  // openSSHSession opens an SSH session via a profile ID. The backend
  // resolves host, auth and jump host from the profile store.
  // Passwords are never sent over the wire.
  openSSHSession(
    size: SessionSize,
    profileId: string,
    anchor: OpenAnchor = {},
  ): Promise<SessionHandle> {
    return this.dispatcher
      .call<OpenResult>('open', {
        ...size,
        kind: 'ssh',
        profileId,
        ...paneParam(anchor),
      })
      .then((result) => this._registerHandle(result, size))
  }

  // openSSHSessionByHost opens a direct SSH session by hostname/alias,
  // resolved through ~/.ssh/config on the backend. No saved profile needed.
  //
  // desiredMode is the connect-time ask's one-shot answer for THIS session
  // (ADR-0069): a hand-typed connection has no saved profile to write the
  // choice onto, so a retry after the ask carries it here instead. Absent on
  // an ordinary open.
  openSSHSessionByHost(
    size: SessionSize,
    host: string,
    user?: string,
    anchor: OpenAnchor = {},
    desiredMode?: string,
  ): Promise<SessionHandle> {
    return this.dispatcher
      .call<OpenResult>('open', {
        ...size,
        kind: 'ssh',
        host,
        user,
        ...(desiredMode ? { desiredMode } : {}),
        ...paneParam(anchor),
      })
      .then((result) => this._registerHandle(result, size))
  }

  /** The open ack's wire shape (contracts/open.schema.json). Every open —
   *  local, profile SSH, direct-host SSH — carries the resolved launch
   *  policy and the refusal reason alongside the id and cwd. */
  private _registerHandle(result: OpenResult, reported: SessionSize): SessionHandle {
    const sid = result?.sessionId
    if (!sid || !isSessionID(sid)) {
      throw new Error(`nocx: invalid session-id from server: ${sid}`)
    }
    // The identity is required by the contract and never minted here
    // (AD-7): an ack without it cannot tell this session from a restored
    // record, so it is refused like an invalid id (nocx-3oupk).
    const { instanceId, sessionEpoch } = result
    if (typeof instanceId !== 'string' || typeof sessionEpoch !== 'number') {
      throw new Error(`nocx: invalid session identity from server: ${sid}`)
    }
    this._registerSession(sid, { instanceId, sessionEpoch }, 0, reported)
    return new SessionHandle(
      this,
      sid,
      result?.cwd ?? '',
      result?.desiredMode ?? 'script',
      result?.parent ?? null,
      result?.workspaceId ?? '',
      null,
      result?.awaitsIntegration ?? false,
      result?.accessEpoch ?? null,
    )
  }

  /** Mint the per-session state this client keeps. ONE PLACE, because an open
   *  and a reclaim are two ways of coming to hold the same thing, and a second
   *  literal would be the field the next callback is added to and forgotten
   *  in. The offset differs: an open starts at zero, a reclaim starts where
   *  the backend says the replay does. */
  private _registerSession(
    sessionId: string,
    identity: { instanceId: string; sessionEpoch: number },
    offset: number,
    reported: SessionSize | null = null,
  ): void {
    this.sessions.set(sessionId, {
      offset,
      reported,
      screenFrameCallback: null,
      effectCallback: null,
      seenEffects: new Set(),
      pendingEffects: [],
      historyPageCallback: null,
      exitCallback: null,
      resetCallback: null,
      outputGapCallback: null,
      pendingOutputGaps: [],
      protocolErrorCallback: null,
      pendingProtocolErrors: [],
      protocolBlocked: false,
      attachment: { kind: 'attached' },
      attachmentCallback: null,
      attachAttempt: null,
      seenOutputGaps: new Set(),
      inputStalledCallback: null,
      integrationChangedCallback: null,
      integrationFact: null,
      recoveryChangedCallback: null,
      recoveryRevision: null,
      livenessCallback: null,
      observationCallback: null,
      livenessEpoch: 0,
      instanceId: identity.instanceId,
      sessionEpoch: identity.sessionEpoch,
    })
  }

  // --- reattach -----------------------------------------------------------

  onSessionAttachment(sessionId: string, cb: (attachment: SessionAttachment) => void): void {
    const state = this.sessions.get(sessionId)
    if (!state) return
    state.attachmentCallback = cb
    cb(state.attachment)
  }

  async retrySessionAttach(sessionId: string): Promise<boolean> {
    const state = this.sessions.get(sessionId)
    if (!state) return false
    if (state.attachment.kind === 'attached') return true
    if (state.attachAttempt) return (await state.attachAttempt) === 'resumed'
    const outcome = await this._reattachSession(sessionId, state)
    // The same consumers refresh durable history/readiness after either an
    // explicit same-session retry or a socket-driven attach pass.
    for (const handler of this.reconnectResultHandlers) {
      handler({ resumed: outcome === 'resumed' ? 1 : 0, lost: outcome === 'lost' ? 1 : 0 })
    }
    return outcome === 'resumed'
  }

  private _setAttachment(state: SessionState, attachment: SessionAttachment): void {
    state.attachment = attachment
    state.attachmentCallback?.(attachment)
  }

  private _reattachSession(sessionId: string, state: SessionState): Promise<AttachOutcome> {
    if (state.attachAttempt) return state.attachAttempt
    this._setAttachment(state, { kind: 'attaching' })
    const attempt = this._sendAttach(sessionId, state.offset, state, state.reported)
      .then((result): AttachOutcome => {
        state.protocolBlocked = false
        if (result.reset) {
          state.offset = result.from ?? 0
          state.resetCallback?.()
        }
        return 'resumed'
      })
      .catch((err: unknown) => {
        const reason = claimRefusalReason(err)
        if (reason && FOREIGN_BINDING_REFUSALS.has(reason)) {
          return this._reclaimReAdopted(sessionId, state)
        }
        return this._attachFailed(sessionId, state, err)
      })
      .then((outcome) => {
        state.attachAttempt = null
        if (outcome === 'resumed') this._setAttachment(state, { kind: 'attached' })
        return outcome
      })
    state.attachAttempt = attempt
    return attempt
  }

  /** An authoritative absence keeps the pane's history, but ends this binding. */
  private _loseSession(sessionId: string, state: SessionState): 'lost' {
    state.exitCallback?.({
      sessionId,
      cause: 'interrupted',
      instanceId: state.instanceId,
      sessionEpoch: state.sessionEpoch,
    })
    this.sessions.delete(sessionId)
    return 'lost'
  }

  private _attachFailed(
    sessionId: string,
    state: SessionState,
    err: unknown,
  ): 'lost' | 'unavailable' {
    if (claimRefusalReason(err) === 'unknown-session') return this._loseSession(sessionId, state)
    return this._attachUnavailable(state, err)
  }

  private _attachUnavailable(state: SessionState, err: unknown): 'unavailable' {
    const detail = err instanceof Error ? err.message : String(err)
    const message = `Could not reattach this session: ${detail}. The shell has not been confirmed ended.`
    this._setAttachment(state, { kind: 'unavailable', message })
    return 'unavailable'
  }

  /** Reclaim only after a foreign binding verdict, using the authoritative
   * live identity and that incarnation's replay cursor (nocx-xn63t.6.33).
   * A failed list or attach is not evidence that the session ended. */
  private async _reclaimReAdopted(
    sessionId: string,
    state: SessionState,
  ): Promise<'resumed' | 'lost' | 'unavailable'> {
    let entry: LiveSession | undefined
    try {
      entry = (await this.listLiveSessions()).find((s) => s.sessionId === sessionId)
    } catch (err) {
      return this._attachUnavailable(state, err)
    }
    if (!entry) return this._loseSession(sessionId, state)
    const identity = { instanceId: entry.instanceId, sessionEpoch: entry.sessionEpoch }
    try {
      const result = await this._sendAttach(sessionId, entry.replayFrom, identity, state.reported)
      state.instanceId = identity.instanceId
      state.sessionEpoch = identity.sessionEpoch
      state.offset = result.from ?? entry.replayFrom
      state.protocolBlocked = false
      if (result.reset) state.resetCallback?.()
      return 'resumed'
    } catch (err) {
      return this._attachFailed(sessionId, state, err)
    }
  }

  /** The claim, in the one shape both callers use (contracts/attach.schema
   *  .json). The identity travels with it because a claim names a session
   *  COMPLETELY — instanceId and sessionEpoch, never a bare id — so a binding
   *  written against a backend that has since restarted is refused as what it
   *  is rather than answered "no such session". */
  private _sendAttach(
    sessionId: string,
    offset: number,
    identity: { instanceId: string; sessionEpoch: number },
    reported: SessionSize | null = null,
  ): Promise<AttachResult> {
    return this.dispatcher.call<AttachResult>('attach', {
      sessionId,
      offset,
      instanceId: identity.instanceId,
      sessionEpoch: identity.sessionEpoch,
      // The claiming client's own geometry — grid and pixels, one report
      // shape at all three doors (nocx-zg3k3.2.9). A claim takes the
      // session AND its size — the client that attached last is the one the
      // shared channel follows (nocx-eidfb.2) — so a reconnect that omitted
      // it would leave the terminal at the default the backend put it on
      // when this client went away. Omitted entirely, not sent as zeroes,
      // when this client has never laid the session out: the backend then
      // leaves the size alone rather than reading it as "no client".
      ...(reported ? reported : {}),
    })
  }

  // --- reclaim -------------------------------------------------------------

  /** What the BACKEND is holding right now: every live session with the pane
   *  it is the pipe of and the offset its replay starts at
   *  (contracts/sessions.live.schema.json).
   *
   *  This is the answer `this.sessions` cannot give. That Map is renderer
   *  process memory, so a window that has just started knows nothing, and the
   *  reconnect pass below reattaches only what is in it — which is why live
   *  PTYs were orphaned by closing the window. The list replaces the memory;
   *  it does not replace the reattach. */
  listLiveSessions(): Promise<LiveSession[]> {
    return this.dispatcher
      .call<SessionsLiveResult>('sessions.live', {})
      .then((result) => result.sessions)
  }

  /** The sessions currently held by authenticated helper generations known
   *  to the coordinator (contracts/sessions.inventory.schema.json). */
  listHelperSessions(): Promise<SessionEntry[]> {
    return this.dispatcher
      .call<SessionsInventoryResult>('sessions.inventory', {})
      .then((result) => result.sessions)
  }

  /** Read only offset and gap metadata for the reclaim notice. The visible
   *  terminal comes from runtime screen frames, never decoded recording bytes. */
  private readSessionRecoveryStatus(
    sessionId: string,
    identity: { instanceId: string; sessionEpoch: number },
  ): Promise<SessionRecoveryStatus> {
    return this.dispatcher.call<SessionRecoveryStatus>('session.recoveryStatus', {
      sessionId,
      ...identity,
    })
  }

  /** Take a live session back: register it as if this client had opened it,
   *  then attach at the offset the backend named.
   *
   *  The handle it returns knows the session and NOT the pane's own facts —
   *  no cwd, no mode, no workspace — because those belong to the pane, which
   *  is the renderer's durable identity and is read from the layout store.
   *  The backend owns the live binding and the renderer owns the pane; a
   *  reclaim is where the two are joined, not where either learns the other's
   *  half.
   *
   *  A reset here is not a loss to report: this client has drawn nothing yet,
   *  so it simply starts at the offset the backend resumed from. */
  reclaimSession(entry: LiveSession): Promise<SessionHandle> {
    const identity = { instanceId: entry.instanceId, sessionEpoch: entry.sessionEpoch }
    const recoveryStatus = this.readSessionRecoveryStatus(entry.sessionId, identity).catch(
      () => null,
    )
    return recoveryStatus.then((metadata) => {
      // The durable recorder owns this frontier; the transport ring owns the
      // replay frontier. Attach at the later offset, but recover the visible
      // screen only through the runtime's frame and history-page interfaces.
      const attachAt = Math.max(metadata?.produced ?? 0, entry.replayFrom)
      this._registerSession(entry.sessionId, identity, attachAt)
      const attach = this._sendAttach(entry.sessionId, attachAt, identity).then((result) => {
        const attached = this.sessions.get(entry.sessionId)
        if (attached) {
          attached.offset = result.from
        }
        return result
      })
      return attach
        .then((result) => {
          // The stretch nothing kept, derived HERE because only this client
          // holds both numbers: `produced` belongs to the recording and
          // `replayFrom` to the ring. Kept as its own list so a later
          // session.recoveryChanged, which carries the ledger's gaps only,
          // composes with it instead of erasing it (nocx-zg3k3.14.24).
          const derived: RecoveryGap[] =
            metadata && metadata.produced < entry.replayFrom
              ? [{ start: metadata.produced, end: entry.replayFrom, reason: UNRECORDED }]
              : []
          const gaps = metadata ? [...metadata.gaps, ...derived] : []
          return new SessionHandle(
            this,
            entry.sessionId,
            '',
            'script',
            null,
            '',
            {
              produced: metadata?.produced ?? entry.replayFrom,
              gaps,
              statusUnavailable: metadata === null,
            },
            result.awaitsIntegration,
            result.accessEpoch ?? null,
            derived,
          )
        })
        .catch((err) => {
          // A refused claim leaves NOTHING behind: the map must not hold a
          // session this client does not have.
          this.sessions.delete(entry.sessionId)
          throw err
        })
    })
  }

  /** Fires when another client takes a session this one was holding (D8,
   *  contracts/session.displaced.schema.json). The session is already gone
   *  from this client's map when a handler runs: the backend has stopped
   *  sending its output and refuses its input, so the only honest state here
   *  is not holding it. Returns an unsubscribe. */
  /** Subscribe to the session-scoped block.outputStart anchor notification. */
  onBlockOutputStart(cb: (fact: BlockOutputStart) => void): () => void {
    this.blockOutputStartHandlers.add(cb)
    return () => this.blockOutputStartHandlers.delete(cb)
  }

  onSessionDisplaced(cb: (displaced: SessionDisplaced) => void): () => void {
    this.displacedHandlers.add(cb)
    return () => this.displacedHandlers.delete(cb)
  }

  /** This connection's own backend instance id, read from any session this
   *  client already holds (AD-7: a connection speaks to exactly one
   *  instance for its whole life, so any one of them answers for all).
   *  Null before this client has ever registered a session — the only case
   *  in which a workers.tabCreated staleness check has nothing to compare
   *  against. */
  private _currentInstanceId(): string | null {
    for (const state of this.sessions.values()) return state.instanceId
    return null
  }

  /** Fires when workers.spawn mints a participant's tab (nocx-ui8q6.3),
   *  while this client is connected to watch it happen — before this
   *  window's next layout.read would have shown it. Broadcast, not
   *  addressed to a session: every connected window is the audience,
   *  because the fact is about the shared layout chain rather than about
   *  the session running inside the new pane. Returns an unsubscribe. */
  onWorkerTabCreated(cb: (fact: WorkersTabCreated) => void): () => void {
    this.workerTabCreatedHandlers.add(cb)
    return () => this.workerTabCreatedHandlers.delete(cb)
  }

  /** Registers a callback for a worker participant's tab having LEFT the
   *  layout chain (nocx-xn63t.4.6), closed on the backend by workers.close.
   *  Returns an unsubscribe, exactly as the created fact's own registration
   *  does — the two are one vocabulary about one tab, read at its two ends. */
  onWorkerTabClosed(cb: (fact: WorkersTabClosed) => void): () => void {
    this.workerTabClosedHandlers.add(cb)
    return () => this.workerTabClosedHandlers.delete(cb)
  }

  // --- data plane ---------------------------------------------------------

  // sendToSession frames raw PTY input for one session. Drops silently if
  // the session is not in the map (AD-7: the client MUST NOT send data
  // frames for a session before the open result arrives, or after exit).
  sendToSession(sessionId: string, data: string): void {
    const ws = this.dispatcher.socket
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    if (!this.sessions.has(sessionId)) return
    const error = this._sessionInputError(sessionId)
    if (error) throw error
    const payload = new TextEncoder().encode(data)
    const frame = encodeFrame(sessionId, payload)
    ws.send(frame)
  }

  /**
   * Report one session's geometry: the grid plus the WHOLE text area in
   * pixels — TIOCSWINSZ's own units (internal/session/size.go), which the
   * helper decodes into the per-cell metric the frames carry
   * (nocx-zg3k3.2.9).
   *
   * THE DEDUPE IS ON THE WHOLE REPORT, never on the grid alone: a zoom that
   * changes the cell metric at the same cols and rows must still reach the
   * backend, and a report identical to the last one must commit nothing
   * new. False means the report was deduped; true means a resize went out.
   *
   * Sent whatever the backend answers, and recorded BEFORE it is sent: the
   * report is what THIS client measured, and it is what the next attach
   * has to carry to take the session's size back (nocx-eidfb.2).
   */
  sendResize(sessionId: string, size: SessionSize): boolean {
    const ws = this.dispatcher.socket
    if (!ws || ws.readyState !== WebSocket.OPEN) return false
    const state = this.sessions.get(sessionId)
    if (!state) return false
    const last = state.reported
    if (
      last !== null &&
      last.cols === size.cols &&
      last.rows === size.rows &&
      last.xpixel === size.xpixel &&
      last.ypixel === size.ypixel
    ) {
      return false
    }
    state.reported = { ...size }
    // Fire-and-forget — response is silently dropped.
    void this.dispatcher.call('resize', { sessionId, ...size }).catch(() => {})
    return true
  }

  /** The receipt-bearing resize path for the renderer's input fence.
   *  Always submit, even at the last reported size: a report is not evidence
   *  of application. Keep the same reconnect report as sendResize, and let
   *  the dispatcher own response correlation and disconnect rejection. */
  async resizeApplied(
    sessionId: string,
    size: SessionSize,
  ): Promise<'applied' | 'superseded' | 'closed'> {
    const ws = this.dispatcher.socket
    const state = this.sessions.get(sessionId)
    if (!ws || ws.readyState !== WebSocket.OPEN || !state) return 'closed'
    const closed = (): boolean =>
      this.dispatcher.socket !== ws ||
      ws.readyState !== WebSocket.OPEN ||
      this.sessions.get(sessionId) !== state
    state.reported = { ...size }
    try {
      await this.dispatcher.call('resize', { sessionId, ...size })
      return closed() ? 'closed' : 'applied'
    } catch (err) {
      // Superseded and cancelled resizes share this wire code. Neither is
      // an application receipt, and their JSON-RPC error is not the fence's
      // vocabulary. Other failures remain errors unless this binding closed.
      if (err instanceof RpcError && err.code === -32013) return 'superseded'
      if (closed()) return 'closed'
      throw err
    }
  }

  /**
   * Address a signal to the command running in one session (nocx-23rph).
   *
   * NOT fire-and-forget, unlike resize: the answer is the whole point. A
   * signal sent to a pane sitting at a prompt is refused honestly in the
   * RESULT (`outcome`), because it is not a caller error and there is
   * nothing for the transport to report — and a control that silently does
   * nothing is indistinguishable from a broken one. The caller reads the
   * outcome and says so.
   *
   * A closed socket rejects rather than resolving a fiction: the renderer
   * cannot know whether the command is still running, and a made-up
   * "delivered" is the one answer that must never be given.
   */
  signalSession(sessionId: string, signal: SessionSignal['signal']): Promise<SessionSignal> {
    const error = this._sessionInputError(sessionId)
    if (error) return Promise.reject(error)
    return this.dispatcher.call<SessionSignal>('session.signal', { sessionId, signal })
  }
  private _sessionInputError(sessionId: string): Error | null {
    const state = this.sessions.get(sessionId)
    if (!state || state.attachment.kind === 'attached') return null
    return new Error('Session is not attached. Retry the existing session before sending input.')
  }

  /** Sends a control-plane session intent for the pane's renderer. Payload is
   *  the schema's base64 string; PTY bytes stay on the separate data plane. */
  sessionIntent(
    sessionId: string,
    accessEpoch: number | null,
    kind: SessionIntentKind,
    payload: string,
  ): Promise<SessionIntentResult> {
    const error = this._sessionInputError(sessionId)
    if (error) return Promise.reject(error)
    // The epoch is omitted rather than defaulted when the caller has none:
    // the session refuses an intent presented under a number nobody observed,
    // and it answers with the epoch in force, so the absence is a question
    // rather than a claim (contracts/session.intent.params.schema.json).
    return this.dispatcher.call<SessionIntentResult>('session.intent', {
      sessionId,
      kind,
      payload,
      ...(accessEpoch !== null && accessEpoch > 0 ? { accessEpoch } : {}),
    })
  }

  /** Asks for one page of a session's live history (nocx-zg3k3.10.3). The
   *  answer's facts ride the result; the rows ride the screen plane keyed
   *  by the result's pageId — one socket, one FIFO, so a caller reading in
   *  order finds the rows already queued when the promise resolves. */
  historyPage(
    sessionId: string,
    before: number | null,
    limit: number,
  ): Promise<SessionHistoryPage> {
    return this.dispatcher.call<SessionHistoryPage>('session.historyPage', {
      sessionId,
      before,
      limit,
    })
  }

  onSessionHistoryPageRows(sessionId: string, cb: (page: SessionHistoryPageRows) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.historyPageCallback = cb
    }
  }

  detachSession(sessionId: string): void {
    const ws = this.dispatcher.socket
    if (ws && ws.readyState === WebSocket.OPEN) {
      void this.dispatcher.call('detach', { sessionId }).catch(() => {})
    }
    this._flushAck(sessionId)
    this.sessions.delete(sessionId)
  }

  closeSession(sessionId: string): void {
    const ws = this.dispatcher.socket
    if (ws && ws.readyState === WebSocket.OPEN) {
      void this.dispatcher.call('close', { sessionId }).catch(() => {})
    }
    this._flushAck(sessionId)
    this.sessions.delete(sessionId)
  }

  // --- session callbacks --------------------------------------------------

  onSessionExit(sessionId: string, cb: (exit: Exit) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.exitCallback = cb
    }
  }

  onSessionReset(sessionId: string, cb: () => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.resetCallback = cb
    }
  }

  onSessionOutputGap(sessionId: string, cb: (gap: SessionOutputGapNotification) => void): void {
    const state = this.sessions.get(sessionId)
    if (!state) return
    state.outputGapCallback = cb
    const pending = state.pendingOutputGaps.splice(0)
    for (const gap of pending) cb(gap)
  }

  onSessionProtocolError(sessionId: string, cb: (message: string) => void): void {
    const state = this.sessions.get(sessionId)
    if (!state) return
    state.protocolErrorCallback = cb
    const pending = state.pendingProtocolErrors.splice(0)
    for (const message of pending) cb(message)
  }

  onSessionInputStalled(sessionId: string, cb: () => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.inputStalledCallback = cb
    }
  }

  onSessionScreenFrame(sessionId: string, cb: (frame: SessionFrame) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.screenFrameCallback = cb
    }
  }

  onSessionEffect(sessionId: string, cb: (effect: SessionEffect) => void): void {
    const state = this.sessions.get(sessionId)
    if (!state) return
    state.effectCallback = cb
    const pending = state.pendingEffects.splice(0)
    for (const effect of pending) cb(effect)
  }

  onSessionIntegrationChanged(
    sessionId: string,
    cb: (fact: SessionIntegrationChanged) => void,
  ): () => void {
    const state = this.sessions.get(sessionId)
    if (!state) return () => undefined
    state.integrationChangedCallback = cb
    if (state.integrationFact) cb(state.integrationFact)
    return () => {
      if (state.integrationChangedCallback === cb) state.integrationChangedCallback = null
    }
  }

  /** Subscribe to the recording's revisions for one session
   *  (session.recoveryChanged, nocx-zg3k3.14.24), delivering the latest one
   *  immediately when it is already in hand. */
  onSessionRecoveryChanged(
    sessionId: string,
    cb: (revision: SessionRecoveryChanged) => void,
  ): () => void {
    const state = this.sessions.get(sessionId)
    if (!state) return () => undefined
    state.recoveryChangedCallback = cb
    if (state.recoveryRevision) cb(state.recoveryRevision)
    return () => {
      if (state.recoveryChangedCallback === cb) state.recoveryChangedCallback = null
    }
  }

  onSessionObservation(
    sessionId: string,
    cb: (observation: SessionObservationChanged) => void,
  ): void {
    const state = this.sessions.get(sessionId)
    if (state) state.observationCallback = cb
  }

  onSessionLiveness(sessionId: string, cb: (liveness: SessionLiveness) => void): void {
    const state = this.sessions.get(sessionId)
    if (state) {
      state.livenessCallback = cb
    }
  }

  /** Report how many sessions resumed and how many are authoritatively absent.
   *  Unavailable sessions remain registered and count as neither. Fires after
   *  a socket's attach pass or an explicit same-session retry settles.
   *  Returns an unsubscribe. */
  onReconnectResult(cb: (r: { resumed: number; lost: number }) => void): () => void {
    this.reconnectResultHandlers.add(cb)
    return () => {
      this.reconnectResultHandlers.delete(cb)
    }
  }

  // --- accessors ----------------------------------------------------------

  get connected(): boolean {
    return this.dispatcher.connected
  }

  // For test introspection only: the current reconnect backoff value.
  get backoffMs(): number {
    return this.dispatcher.backoffMs
  }

  // For test introspection only: whether the reconnect timer is pending.
  get reconnectPending(): boolean {
    return this.dispatcher.reconnectPending
  }

  close(): void {
    this.dispatcher.close()
    this.sessions.clear()
    for (const ack of this.acks.values()) {
      if (ack.timer !== null) clearTimeout(ack.timer)
    }
    this.acks.clear()
  }
}
