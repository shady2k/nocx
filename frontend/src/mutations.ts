// Mutation outcomes — the renderer's half of "did it happen?", and the one
// place that decides a mutation is never replayed automatically.
//
// A mutation may have changed something the caller cannot see. The backend
// mints nothing and remembers nothing by itself: the RENDERER mints an
// identity per mutating call and carries it on the request frame, so after the
// answer was lost — a cancellation, a dropped socket, a deadline the surface
// gave up on — the question can be asked and answered (ADR-8eej3e66 §4).
//
// Two rules live here, and they are the same rule twice:
//
//  1. A mutation is never re-sent automatically. The only re-send in the whole
//     renderer is ADR-0032's vault-unlock retry, and it is allowed exactly
//     when the backend says the first attempt did not start. A timeout is not
//     evidence of a rollback, so it is never evidence for a retry.
//  2. The person gets one of three answers — "did not start", "done", or
//     "checking what happened" — never a bare "aborted" (ADR-8eej3e66 §8).
//
// Which methods are mutations is the CATALOGUE's answer, not a list here: a
// call is a mutation when its declaration says a deadline leaves its outcome
// unknown. That is generated from contracts/operations.json, so a method
// cannot join the wire without saying what its absence means.

import type { Dispatcher } from './dispatcher'
import type { MutationReplayed } from './generated/mutation.replayed'
import type { OperationOutcome } from './generated/operation.outcome'
import { callOperationDeclaration } from './generated/operations'

/** The wire's closed set of answers. Not exported: the one consumer that
 *  named the type was the row that read an answer out, and a row is the wait
 *  (ADR-8eej3e66 §8) — the answer belongs to the call's own rejection, whose
 *  surface reads {@link MutationAssessment} instead. */
type MutationAnswer = OperationOutcome['outcome']

/** What one answer means to the person, in their terms. */
export interface MutationAssessment {
  answer: MutationAnswer
  /** The sentence a surface shows. */
  sentence: string
  /** Whether the backend may still change its answer: the work has not
   *  finished, and the record is still retained. */
  checking: boolean
}

/** Whether this call may have committed an effect, per the catalogue. */
export function isMutationMethod(method: string): boolean {
  return callOperationDeclaration(method)?.expired === 'outcome-unknown'
}

/** A fresh identity for one mutating call. It is opaque on the wire; the
 *  backend never parses it, and only equality matters. */
export function newMutationIdentity(): string {
  const c = (globalThis as { crypto?: Crypto }).crypto
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  // A renderer without randomUUID is still a renderer; the identity only has
  // to be unique among the calls of one session.
  return `m-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
}

/** Everything the surface needs from one operation.outcome result. */
export function assessmentOf(result: OperationOutcome): MutationAssessment {
  switch (result.outcome) {
    case 'not-started':
      return {
        answer: 'not-started',
        sentence: 'It did not start — nothing was changed.',
        checking: false,
      }
    case 'done':
      return { answer: 'done', sentence: 'It finished.', checking: false }
    case 'outcome-unknown':
      return { answer: 'outcome-unknown', sentence: 'Checking what happened…', checking: true }
    default:
      return {
        answer: 'unknown',
        sentence: unknownSentence(result.operation),
        checking: false,
      }
  }
}

/** Where checking ends. The record is gone — it expired, the backend
 *  restarted, or the identity was never seen — so the backend cannot say, and
 *  the sentence names what to look at instead of pretending either answer. */
function unknownSentence(operation: string): string {
  const waiting = operation === '' ? undefined : callOperationDeclaration(operation)?.waiting
  const subject = waiting === undefined ? 'the change' : `“${waiting}”`
  return `The outcome is unknown: nocx keeps no record of ${subject}. Check what it changed before trying again.`
}

/** The assessment a -32005 refusal carries, so a client that tried again
 *  without asking is told which of the three answers it is instead of a bare
 *  "already ran". */
export function replayedAssessment(data: MutationReplayed): MutationAssessment {
  return assessmentOf({ outcome: data.outcome, operation: data.operation })
}

/** The error a mutation whose replay is forbidden rejects with. It carries
 *  the assessment so the surface can say which of the three answers it is
 *  without a second request. */
export class MutationNotReplayedError extends Error {
  constructor(readonly assessment: MutationAssessment) {
    super(assessment.sentence)
    this.name = 'MutationNotReplayedError'
  }
}

/** Ask the backend what became of one mutation, and read the failure as "it
 *  cannot answer right now" — a mutation whose outcome could not be asked is
 *  in the same state as one the record has forgotten, and must never be
 *  replayed either.
 *
 *  A `report` intent: this is a passive question that never needs a secret and
 *  must not raise the unlock prompt. */
export async function resolveMutationOutcome(
  dispatcher: Dispatcher,
  mutation: string,
): Promise<MutationAssessment> {
  try {
    const result = await dispatcher.call<OperationOutcome>(
      'operation.outcome',
      { mutation },
      undefined,
      { intent: 'report' },
    )
    return assessmentOf(result)
  } catch {
    return assessmentOf({ outcome: 'unknown', operation: '' })
  }
}

/** A mutation the person withdrew. The name stays 'AbortError' — the call WAS
 *  withdrawn, and a surface that stays quiet for a person's own cancel keeps
 *  doing so — while the message is §8's sentence for it: one of "It did not
 *  start", "It finished" or "Checking what happened…". A row is the wait and
 *  goes when the call settles, so this is where a withdrawn mutation's answer
 *  reaches the surface that asked. */
export class MutationWithdrawnError extends Error {
  constructor(readonly assessment: MutationAssessment) {
    super(assessment.sentence)
    this.name = 'AbortError'
  }
}

/** The answer for a mutation withdrawn before it was ever sent: nothing ran,
 *  so the record is never asked. */
export function withdrawnBeforeStart(method: string): MutationAssessment {
  return assessmentOf({ outcome: 'not-started', operation: method })
}
