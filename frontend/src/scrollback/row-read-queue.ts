// The renderer's own queue for stored-row reads (ADR-y45sp41c).
//
// The server caps how many stored-row responses may be outstanding on one
// connection and answers the rest at once with retryAfterMs. That answer is
// internal: this queue waits the stated time and then takes the MOST URGENT
// request it holds at that moment — visible first, then newest to oldest —
// rather than re-sending the same request in a loop. A person never meets the
// refusal, and a large transcript cannot grow an unbounded backlog of
// responses on the socket.

import { RpcError } from '../dispatcher'
import type { RowReadDeferred } from '../generated/row.readDeferred'

const ROW_READ_SHORT_RETRY_MS = 20

/** A page that came back shorter than the retained rows it asked for, while
 *  the artifact says more follow. The queue retries it; a short page is not
 *  "done" and the caller must not paint the hole as a loss (ADR-y45sp41c). */
export class RowReadShortError extends Error {
  readonly retryAfterMs = ROW_READ_SHORT_RETRY_MS

  constructor(message: string) {
    super(message)
    this.name = 'RowReadShortError'
  }
}

/** The server's retryAfterMs, when err is its internal refusal, or the short
 *  page's own retry hint. Null means the error is not one this queue owns and
 *  must reach the caller. */
export function rowReadRetryAfter(err: unknown): number | null {
  if (err instanceof RowReadShortError) return err.retryAfterMs
  if (!(err instanceof RpcError) || err.code !== -32014) return null
  const data = err.data as Partial<RowReadDeferred> | null | undefined
  const ms = data?.retryAfterMs
  return typeof ms === 'number' && Number.isFinite(ms) && ms > 0 ? ms : 0
}

interface Job<T> {
  readonly key: string
  readonly run: () => Promise<T>
  readonly promise: Promise<T>
  priority: () => number
  state: 'pending' | 'active' | 'waiting'
  resolve: (value: T) => void
  reject: (reason: unknown) => void
}

export interface RowReadQueueOptions {
  /** How many reads may be in flight at once. Two keeps a visible page
   *  landing without letting a scroll burst become a flood. */
  readonly limit?: number
  readonly wait?: (ms: number) => Promise<void>
}

const defaultWait = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms))

export class RowReadQueue<T = unknown> {
  private readonly jobs = new Map<string, Job<T>>()
  private readonly limit: number
  private readonly wait: (ms: number) => Promise<void>
  private active = 0

  constructor(options: RowReadQueueOptions = {}) {
    this.limit = Math.max(1, options.limit ?? 2)
    this.wait = options.wait ?? defaultWait
  }

  /** Submit one read. The same key joins the read already in flight or
   *  waiting, which is what makes a notification and a restore of one block
   *  cost one response. */
  submit(key: string, priority: () => number, run: () => Promise<T>): Promise<T> {
    const existing = this.jobs.get(key)
    if (existing) return existing.promise
    let resolve!: (value: T) => void
    let reject!: (reason: unknown) => void
    const promise = new Promise<T>((res, rej) => {
      resolve = res
      reject = rej
    })
    const job: Job<T> = { key, run, promise, priority, state: 'pending', resolve, reject }
    this.jobs.set(key, job)
    this.pump()
    return promise
  }

  /** The most urgent read still waiting, for tests and diagnostics. */
  pendingKeys(): string[] {
    return [...this.jobs.values()]
      .filter((job) => job.state === 'pending')
      .sort((a, b) => a.priority() - b.priority())
      .map((job) => job.key)
  }

  private pump(): void {
    while (this.active < this.limit) {
      const next = this.mostUrgent()
      if (!next) return
      this.start(next)
    }
  }

  private mostUrgent(): Job<T> | undefined {
    let next: Job<T> | undefined
    let bestPriority = Infinity
    for (const job of this.jobs.values()) {
      if (job.state !== 'pending') continue
      const priority = job.priority()
      if (!next || priority < bestPriority) {
        next = job
        bestPriority = priority
      }
    }
    return next
  }

  private start(job: Job<T>): void {
    job.state = 'active'
    this.active++
    void job.run().then(
      (value) => {
        this.finish(job)
        job.resolve(value)
      },
      (err: unknown) => {
        const retry = rowReadRetryAfter(err)
        if (retry === null) {
          this.finish(job)
          job.reject(err)
          return
        }
        // Wait without holding a slot, then let the queue choose again. A
        // more urgent request submitted during the wait goes first.
        job.state = 'waiting'
        this.active--
        void this.wait(retry).then(() => {
          job.state = 'pending'
          this.pump()
        })
        this.pump()
      },
    )
  }

  private finish(job: Job<T>): void {
    this.active--
    this.jobs.delete(job.key)
    this.pump()
  }
}
