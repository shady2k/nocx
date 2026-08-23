// The model's three answers: the order, the count, and the aggregate.
//
// Driven with plain objects rather than a store, because everything here is
// arithmetic over a list — which is the whole reason it is a model and not
// something the indicator does inline.
import { describe, expect, it, vi } from 'vitest'

import { createOperationsModel, type Operation } from './operations'
import type { OperationPhase } from '../ui/operation'

function op(over: Partial<Operation> & { id: string }): Operation {
  return {
    kind: 'upload',
    title: over.id,
    destination: '/srv',
    machine: 'deploy@srv-01',
    phase: 'running',
    done: null,
    total: 100,
    speedBytesPerSecond: null,
    error: null,
    startedAt: null,
    endedAt: null,
    cancel: null,
    ...over,
  }
}

function finished(id: string, endedAt: number, phase: OperationPhase = 'written'): Operation {
  return op({ id, phase, endedAt })
}

describe('the order the list is read in', () => {
  it('puts the live ones first, in the order they started', () => {
    const model = createOperationsModel([() => [op({ id: 'a' }), op({ id: 'b' })]])
    expect(model.operations().map((o) => o.id)).toEqual(['a', 'b'])
  })

  it('puts the finished ones under them, most recent first', () => {
    // Most recent at the TOP of the finished section, so that section's
    // head is stable as older entries fall off its end.
    const model = createOperationsModel([
      () => [finished('old', 10), op({ id: 'live' }), finished('new', 30)],
    ])
    expect(model.operations().map((o) => o.id)).toEqual(['live', 'new', 'old'])
  })

  it('orders the finished ones by when they ENDED, not by where they sit', () => {
    // A transfer that started first does not finish first, and the source's
    // array is in start order — so position cannot answer this.
    const model = createOperationsModel([() => [finished('first', 90), finished('second', 20)]])
    expect(model.operations().map((o) => o.id)).toEqual(['first', 'second'])
  })

  it('never reorders a source’s own array', () => {
    // The sort runs on a copy. A model that sorted in place would reorder
    // a store's list underneath it, and the next read would come back
    // scrambled.
    const list = [finished('a', 10), finished('b', 20)]
    const model = createOperationsModel([() => list])
    model.operations()
    expect(list.map((o) => o.id)).toEqual(['a', 'b'])
  })

  it('reads every source it was given', () => {
    // The shape download joins through: a second function, not a second
    // list type and not a change to this module.
    const model = createOperationsModel([() => [op({ id: 'u' })], () => [op({ id: 'd' })]])
    expect(model.operations().map((o) => o.id)).toEqual(['u', 'd'])
  })

  it('asks its sources every time, so a surface reading it follows them', () => {
    const source = vi.fn(() => [op({ id: 'a' })])
    const model = createOperationsModel([source])
    model.operations()
    model.operations()
    expect(source.mock.calls.length).toBeGreaterThan(1)
  })
})

describe('the count on the badge', () => {
  it('counts what is still live and nothing else', () => {
    const model = createOperationsModel([
      () => [op({ id: 'a' }), finished('b', 1), op({ id: 'c', phase: 'unsettled' })],
    ])
    // `unsettled` is live: the backend may be finishing it this moment and
    // cancelling still reaches it, so it is work in progress and not an
    // outcome.
    expect(model.activeCount()).toBe(2)
  })

  it('drops a finished operation from the count at once, and keeps it in the list', () => {
    // Success does not shout — and it does not vanish without trace
    // either, so somebody who goes to look can see it really landed.
    let phase: OperationPhase = 'running'
    const model = createOperationsModel([() => [op({ id: 'a', phase, endedAt: 5 })]])
    expect(model.activeCount()).toBe(1)
    phase = 'written'
    expect(model.activeCount()).toBe(0)
    expect(model.operations().map((o) => o.id)).toEqual(['a'])
  })

  it('counts every terminal phase as finished, cancellation and skip included', () => {
    const phases: OperationPhase[] = ['written', 'skipped', 'cancelled', 'failed']
    for (const phase of phases) {
      const model = createOperationsModel([() => [op({ id: 'a', phase, endedAt: 1 })]])
      expect(model.activeCount()).toBe(0)
    }
  })
})

describe('the aggregate under the icon', () => {
  it('is absent when nothing is running', () => {
    // Null and not zero: an empty bar is a claim that something is at the
    // start, and the bar's whole job is to be there only while work is.
    expect(createOperationsModel([() => []]).progress()).toBeNull()
    expect(createOperationsModel([() => [finished('a', 1)]]).progress()).toBeNull()
  })

  it('is the bytes done over the bytes to do, across every live operation', () => {
    const model = createOperationsModel([
      () => [
        op({ id: 'a', done: 50, total: 100 }),
        op({ id: 'b', done: 25, total: 100 }),
        // A finished one contributes nothing: the bar reports what is
        // still happening.
        finished('c', 1),
      ],
    ])
    expect(model.progress()?.fraction).toBeCloseTo(0.375)
  })

  it('treats an unobserved byte count as nothing done rather than as nothing to do', () => {
    const model = createOperationsModel([() => [op({ id: 'a', done: null, total: 100 })]])
    expect(model.progress()).toEqual({ fraction: 0 })
  })

  it('is zero, never NaN, when every live operation has no size', () => {
    // An empty file is a file. 0/0 painted through a bar leaves the LAST
    // width on screen — a bar frozen wherever it happened to be.
    const model = createOperationsModel([() => [op({ id: 'a', done: 0, total: 0 })]])
    expect(model.progress()).toEqual({ fraction: 0 })
  })

  it('clamps at one when a byte count overshoots its declared total', () => {
    const model = createOperationsModel([() => [op({ id: 'a', done: 140, total: 100 })]])
    expect(model.progress()).toEqual({ fraction: 1 })
  })
})

// ── Waiting work is outstanding work (nocx-hbdw4.6) ──────────────────────
describe('what the badge counts once a batch has a waiting half', () => {
  it('counts a queued operation, because a person dropped it and it has not happened', () => {
    // The number is the answer to "how much does nocx still owe me". Drop
    // five files and a count of one would be wrong the instant the batch
    // was dropped, which is the exact moment somebody looks at it.
    const model = createOperationsModel([
      () => [op({ id: 'running' }), op({ id: 'waiting', phase: 'queued' })],
    ])
    expect(model.activeCount()).toBe(2)
  })

  it('keeps a queued operation on the live side of the list, never among the finished', () => {
    const model = createOperationsModel([
      () => [finished('done', 10), op({ id: 'waiting', phase: 'queued' })],
    ])
    expect(model.operations().map((o) => o.id)).toEqual(['waiting', 'done'])
  })

  it('counts a queued operation into the aggregate, so the bar is about the batch', () => {
    // Its size is known — the one measurement a waiting file has — and the
    // bar a person watches is about everything they dropped, not about
    // whichever file happens to be moving.
    const model = createOperationsModel([
      () => [op({ id: 'running', done: 50, total: 100 }), op({ id: 'waiting', phase: 'queued' })],
    ])
    expect(model.progress()?.fraction).toBeCloseTo(0.25)
  })
})
