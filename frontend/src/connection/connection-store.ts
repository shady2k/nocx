// The renderer's ONE owner of a pane's connection (ADR-ba6xet1b).
//
// A pane.connection notification is the backend's answer to "what is this
// pane's connection doing", and it is the only answer the renderer may draw.
// This module subscribes to it, refuses observations that cannot be newer than
// the one it holds, and folds the result into the two things a surface asks:
// the one condition (connection-condition.ts) and the one offer (the button
// that appears exactly when the backend offers it).
//
// IT DECIDES NOTHING. It does not retry, does not reconnect, does not infer a
// loss from an error, and does not mint a state the backend did not send. A
// missing observation is `reachable` — the same nothing the corner mark draws
// in the healthy state — and a pane with no connection at all is a LOCAL pane,
// which has no connection to have a state about.
//
// WHY PER PANE. The contract is keyed by paneId because an SSH pane's
// connection exists before its session does (design §5.5). A store instance
// belongs to one pane and filters the notifications it sees; every instance is
// a subscriber of the same dispatcher, and the pane is what routes between
// them. That is also what makes the pane's own tests able to drive one pane
// without inventing a second wire.

import type { PaneConnection } from '../generated/pane.connection'
import type { PaneOpened } from '../generated/pane.opened'
import type { Exit } from '../generated/exit'
import { connectionCondition, type ConnectionCondition } from '../connection-condition'

/** The narrow subscription seam this module needs. The dispatcher implements
 *  it; a test passes a recorder. Taking the shape rather than the dispatcher
 *  keeps this module out of the transport's implementation. */
export interface ConnectionSource {
  subscribe(method: string, handler: (params: unknown) => void): () => void
}

/** One pane's applied connection observation. A restart's explicit session
 * end is stored separately; it never becomes an invented observation. */
export interface PaneConnectionState {
  readonly observation: PaneConnection
}

/** What a Reconnect press must carry: the epoch of the observation that
 *  offered it, and how many shells pressing would end. */
export interface ConnectionOffer {
  readonly connectionEpoch: number
  readonly endsShells: number
}

const STAGES = ['resolve', 'tcp', 'ssh-version', 'key-exchange', 'sign-in', 'shell'] as const
const PERSONS = [
  'host-key',
  'helper-consent',
  'vault-unlock',
  'passphrase',
  'password',
  'second-factor',
  'key-signature',
] as const
const FAILED_CAUSES = [
  'host-key-changed',
  'auth-rejected',
  'no-auth-method',
  'plain-only',
  'profile-gone',
] as const
const ENDED_CAUSES = ['released', 'silence', 'tainted', 'lost-link'] as const
const SHOWN = ['none', 'integrated', 'plain'] as const
const CANDIDATE = ['trying', 'ready', 'failed', 'none'] as const

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function onlyKeys(value: Record<string, unknown>, allowed: readonly string[]): boolean {
  return Object.keys(value).every((key) => allowed.includes(key))
}

function isString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0
}

function isNonNegativeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 1
}

function isEnum<T extends string>(value: unknown, values: readonly T[]): value is T {
  return typeof value === 'string' && (values as readonly string[]).includes(value)
}

/**
 * Whether a payload is a pane.connection this contract admits.
 *
 * The schema's oneOf is restated branch by branch, including what each branch
 * FORBIDS: a connected connection carrying a stage is not a shape this
 * renderer may draw, and a malformed notification is dropped rather than
 * coerced into the branch written last. The fields with no branch of their own
 * (paneId, connectionId, epoch, attempt, observedAt, quality, reconnect) are
 * checked once.
 */
export function parsePaneConnection(value: unknown): PaneConnection | null {
  if (!isRecord(value)) return null
  if (
    !onlyKeys(value, [
      'paneId',
      'connectionId',
      'connectionEpoch',
      'generation',
      'instanceId',
      'sessionId',
      'transport',
      'quality',
      'attempt',
      'reconnect',
      'shellFailure',
      'observedAt',
    ])
  ) {
    return null
  }
  if (!isString(value.paneId) || !isString(value.connectionId)) return null
  if (!isPositiveInteger(value.connectionEpoch)) return null
  if (!isPositiveInteger(value.generation)) return null
  if (!isString(value.instanceId)) return null
  if (value.sessionId !== undefined && !isString(value.sessionId)) return null
  if (!isString(value.observedAt) || Number.isNaN(Date.parse(value.observedAt))) return null
  if (!parseAttempt(value.attempt)) return null
  if (!parseQuality(value.quality)) return null
  if (!parseReconnect(value.reconnect)) return null
  if (!parseShellFailure(value.shellFailure)) return null
  if (!parseTransport(value.transport)) return null
  if (!shellFailureRelations(value)) return null
  return value as unknown as PaneConnection
}

function parseAttempt(value: unknown): boolean {
  if (!isRecord(value) || !onlyKeys(value, ['shown', 'candidate', 'reason'])) return false
  return (
    isEnum(value.shown, SHOWN) &&
    isEnum(value.candidate, CANDIDATE) &&
    typeof value.reason === 'string'
  )
}

function parseQuality(value: unknown): boolean {
  if (value === undefined) return true
  if (!isRecord(value) || !onlyKeys(value, ['slow', 'roundTripMs'])) return false
  return typeof value.slow === 'boolean' && isNonNegativeInteger(value.roundTripMs)
}

function parseReconnect(value: unknown): boolean {
  if (value === undefined) return true
  if (!isRecord(value) || !onlyKeys(value, ['endsShells'])) return false
  return isNonNegativeInteger(value.endsShells)
}

function parseShellFailure(value: unknown): boolean {
  if (value === undefined) return true
  if (!isRecord(value)) return false
  if (!onlyKeys(value, ['code', 'message', 'data'])) return false
  return (
    typeof value.code === 'number' &&
    Number.isSafeInteger(value.code) &&
    typeof value.message === 'string'
  )
}

/** The schema's cross-field rules for a healthy transport whose shell failed:
 *  the failure and a shell-only retry travel together, the retry ends no
 *  shells, and a failed shell never carries the session id of one that does
 *  not exist. */
function shellFailureRelations(value: Record<string, unknown>): boolean {
  const transport = value.transport as Record<string, unknown>
  const connected = transport.state === 'connected'
  const failure = value.shellFailure !== undefined
  if (failure && value.sessionId !== undefined) return false
  if (connected && value.reconnect !== undefined) {
    if (!failure) return false
    const offer = value.reconnect as Record<string, unknown>
    if (offer.endsShells !== 0) return false
  }
  if (connected && failure && value.reconnect === undefined) return false
  return true
}

/** The transport branch, by its state's own required and forbidden fields. */
function parseTransport(value: unknown): boolean {
  if (!isRecord(value) || !isString(value.state)) return false
  const allowedFor = (fields: readonly string[]): boolean =>
    onlyKeys(value, ['state', 'stateMs', ...fields])
  if (!isNonNegativeInteger(value.stateMs)) return false
  switch (value.state) {
    case 'opening':
      return allowedFor(['stage']) && isEnum(value.stage, STAGES)
    case 'waiting_person':
      return allowedFor(['person']) && isEnum(value.person, PERSONS)
    case 'connected':
      return allowedFor(['plainShells']) && isNonNegativeInteger(value.plainShells)
    case 'stalled':
      return (
        allowedFor(['plainShells', 'lookAnsweredAt']) &&
        isNonNegativeInteger(value.plainShells) &&
        (value.lookAnsweredAt === undefined ||
          (typeof value.lookAnsweredAt === 'string' &&
            !Number.isNaN(Date.parse(value.lookAnsweredAt))))
      )
    case 'reconnecting':
      return allowedFor(['attempt']) && isPositiveInteger(value.attempt)
    case 'failed':
      return allowedFor(['cause']) && isEnum(value.cause, FAILED_CAUSES)
    case 'ended':
      return allowedFor(['cause']) && isEnum(value.cause, ENDED_CAUSES)
    case 'plain':
      return allowedFor([])
    default:
      return false
  }
}

/**
 * One pane's connection, as the backend last answered it.
 *
 * Two refusals, and both are the same rule at two boundaries: an observation
 * from another backend instance is about a connection this renderer is not
 * drawing, and an epoch that is not greater than the last applied describes a
 * moment that has already been overtaken. Neither may reach a surface.
 */
export class ConnectionStore {
  private state: PaneConnectionState | null = null
  private endedAfterRestart = false
  private lastEpoch = 0
  private generation = 0
  private openedGeneration = 0
  private readonly openedListeners = new Set<(fact: PaneOpened) => void>()
  /** The backend this store's epochs are ordered against. Only the CLIENT's
   *  current identity may move it; a new value resets the epoch counter,
   *  because a replacement backend starts its per-pane epochs over. */
  private observedBackend: string | null = null
  private readonly listeners = new Set<() => void>()
  private readonly unsubscribe: (() => void)[]

  constructor(
    source: ConnectionSource,
    readonly paneId: string,
    /** The client's CURRENT backend identity, or null before it knows one.
     *  Never learned from this store's own observations: a first observation
     *  can be a stale one, and pinning it would refuse the replacement
     *  backend for the life of the window. */
    private readonly backendIdentity: () => string | null = () => null,
  ) {
    this.unsubscribe = [
      source.subscribe('pane.connection', (params) => this.receive(params)),
      source.subscribe('pane.opened', (params) => this.receiveOpened(params)),
    ]
  }

  /** The latest observation, or null for a pane that has never had one — a
   *  local pane, or an SSH pane whose first observation has not arrived. */
  current(): PaneConnectionState | null {
    return this.state
  }

  /** A session end is distinct from a connection observation. Only the
   * backend's explicit restart verdict permits ordinary open again. */
  sessionEnded(exit: Pick<Exit, 'cause'>): void {
    if (exit.cause !== 'backend-restarted') return
    this.endedAfterRestart = true
    this.state = null
    this.emit()
  }

  restartEnded(): boolean {
    return this.endedAfterRestart
  }

  beginOpen(): void {
    this.endedAfterRestart = false
    this.state = null
    this.lastEpoch = 0
    this.generation = 0
    this.openedGeneration = 0
    this.observedBackend = null
    this.emit()
  }

  /** The one condition a mark or a tab draws, from the one fold. */
  condition(): ConnectionCondition {
    return this.endedAfterRestart ? 'lost' : connectionCondition(this.state?.observation ?? null)
  }

  /** The button's offer, or null when the backend offers nothing. Its
   *  PRESENCE is the whole rule: a pane that has an offer shows Reconnect,
   *  and no renderer-side policy can add or withhold one. */
  offer(): ConnectionOffer | null {
    const observation = this.state?.observation
    if (observation === undefined || observation.reconnect === undefined) return null
    return {
      connectionEpoch: observation.connectionEpoch,
      endsShells: observation.reconnect.endsShells,
    }
  }

  onChange(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  onOpened(listener: (fact: PaneOpened) => void): () => void {
    this.openedListeners.add(listener)
    return () => this.openedListeners.delete(listener)
  }

  dispose(): void {
    for (const stop of this.unsubscribe) stop()
    this.openedListeners.clear()
    this.listeners.clear()
    this.state = null
  }

  private acceptInstance(instanceId: string): boolean {
    const backend = this.backendIdentity()
    if (backend === null) return true
    if (this.observedBackend !== backend) {
      this.observedBackend = backend
      this.lastEpoch = 0
      this.generation = 0
      this.openedGeneration = 0
      this.state = null
    }
    return instanceId === backend
  }

  private receiveOpened(params: unknown): void {
    if (!isRecord(params) || params.paneId !== this.paneId) return
    if (!isPositiveInteger(params.generation) || !isRecord(params.open)) return
    if (!isString(params.open.instanceId) || !this.acceptInstance(params.open.instanceId)) return
    if (params.generation < this.generation || params.generation <= this.openedGeneration) return
    this.generation = params.generation
    this.openedGeneration = params.generation
    for (const listener of [...this.openedListeners]) listener(params as unknown as PaneOpened)
  }

  private receive(params: unknown): void {
    const observation = parsePaneConnection(params)
    if (observation === null || observation.paneId !== this.paneId) return
    if (!this.acceptInstance(observation.instanceId)) return
    if (observation.generation < this.generation) return
    if (observation.connectionEpoch <= this.lastEpoch) return
    this.lastEpoch = observation.connectionEpoch
    this.generation = observation.generation
    this.state = { observation }
    this.emit()
  }

  private emit(): void {
    for (const listener of [...this.listeners]) listener()
  }
}
