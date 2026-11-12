import { describe, expect, it } from 'vitest'
import { RpcError } from '../dispatcher'
import { RowReadQueue, rowReadRetryAfter } from './row-read-queue'

function refusal(ms = 25): RpcError {
  return new RpcError('The stored rows are busy; retry', -32014, { retryAfterMs: ms })
}

function deferred<T>(): {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason: unknown) => void
} {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('stored-row read queue', () => {
  it('recognises only the internal refusal and only its positive retry hint', () => {
    expect(rowReadRetryAfter(refusal(40))).toBe(40)
    expect(rowReadRetryAfter(new RpcError('no', -32005, { reason: 'not-started' }))).toBeNull()
    expect(rowReadRetryAfter(new Error('socket closed'))).toBeNull()
    expect(rowReadRetryAfter(new RpcError('busy', -32014, {}))).toBe(0)
  })

  it('waits out a refusal and sends the most urgent request instead of looping on the refused one', async () => {
    const waits: Array<{ ms: number; release: () => void }> = []
    const queue = new RowReadQueue<string>({
      limit: 1,
      wait: (ms) =>
        new Promise<void>((resolve) => {
          waits.push({ ms, release: resolve })
        }),
    })
    const order: string[] = []
    const visible = deferred<string>()

    let olderRuns = 0
    const olderRead = queue.submit(
      'older-block',
      () => 10,
      () => {
        olderRuns++
        order.push('older-block')
        return olderRuns === 1 ? Promise.reject(refusal(25)) : Promise.resolve('older')
      },
    )
    await Promise.resolve()
    await Promise.resolve()
    expect(waits).toHaveLength(1)
    expect(waits[0].ms).toBe(25)

    // The refused block waits without holding the slot; a more urgent block
    // submitted during that wait takes the free slot at once.
    const visibleRead = queue.submit(
      'visible-block',
      () => 1,
      () => {
        order.push('visible-block')
        return visible.promise
      },
    )
    expect(order).toEqual(['older-block', 'visible-block'])
    expect(olderRuns).toBe(1)

    // Releasing the wait must not retry the older block while the visible one
    // is still in flight — the queue takes what is most urgent only when a
    // slot is free.
    waits[0].release()
    await Promise.resolve()
    await Promise.resolve()
    expect(olderRuns).toBe(1)
    expect(order).toEqual(['older-block', 'visible-block'])

    visible.resolve('visible')
    await expect(visibleRead).resolves.toBe('visible')
    await expect(olderRead).resolves.toBe('older')
    expect(order).toEqual(['older-block', 'visible-block', 'older-block'])
  })

  it('measures each pending priority at most once per selection and refreshes ordering when a slot opens', async () => {
    const queue = new RowReadQueue<string>({ limit: 1 })
    const blocker = deferred<string>()
    const blocked = queue.submit(
      'blocker',
      () => 0,
      () => blocker.promise,
    )
    const order: string[] = []
    const jobs = [
      { key: 'oldest', priority: 40 },
      { key: 'middle', priority: 20 },
      { key: 'visible-first', priority: 0 },
      { key: 'older', priority: 30 },
      { key: 'visible-second', priority: 0 },
      { key: 'newest', priority: 10 },
    ].map((job) => ({ ...job, calls: 0, gate: deferred<string>() }))
    const reads = new Map(
      jobs.map((job) => [
        job.key,
        queue.submit(
          job.key,
          () => {
            job.calls++
            return job.priority
          },
          () => {
            order.push(job.key)
            return job.gate.promise
          },
        ),
      ]),
    )
    expect(jobs.map((job) => job.calls)).toEqual(jobs.map(() => 0))

    blocker.resolve('blocker')
    await blocked
    const expectedOrder = ['visible-first', 'oldest', 'visible-second', 'newest', 'middle', 'older']
    for (const [index, key] of expectedOrder.entries()) {
      expect(order).toEqual(expectedOrder.slice(0, index + 1))
      for (const job of jobs) {
        expect
          .soft(job.calls, `${job.key} priority calls for selection ${index + 1}`)
          .toBeLessThanOrEqual(1)
        job.calls = 0
      }
      // A formerly offscreen block becomes visible while the slot is held.
      // The next selection must measure it again, not reuse an earlier score.
      if (index === 0) jobs[0].priority = -1
      const selected = jobs.find((job) => job.key === key)!
      selected.gate.resolve(key)
      await expect(reads.get(key)).resolves.toBe(key)
    }
  })

  it('shares one read between callers of the same key', async () => {
    const queue = new RowReadQueue<number>({ limit: 2 })
    const gate = deferred<number>()
    let runs = 0
    const first = queue.submit(
      'block',
      () => 1,
      () => {
        runs++
        return gate.promise
      },
    )
    const second = queue.submit(
      'block',
      () => 1,
      () => {
        runs++
        return gate.promise
      },
    )
    gate.resolve(7)
    await expect(first).resolves.toBe(7)
    await expect(second).resolves.toBe(7)
    expect(runs).toBe(1)
  })
})
