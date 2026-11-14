import { createSignal, untrack } from 'solid-js'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'
import type { AgentApprovalRetired } from './generated/agent.approvalRetired'

function identity(ask: AgentApprovalRetired): string {
  return JSON.stringify([ask.runId, ask.attempt, ask.tool, ask.callId, ask.argHash, ask.reason])
}

/** One queue owns both phases, including questions arriving before an answer's RPC response. */
export function createApprovalQueue() {
  const pending = new Map<string, AgentApprovalRequested>()
  const [active, setActive] = createSignal<AgentApprovalRequested | null>(null)
  const [answering, setAnswering] = createSignal<string | null>(null)
  const next = () => setActive(pending.values().next().value ?? null)
  const retire = (ask: AgentApprovalRetired) => {
    const key = identity(ask)
    pending.delete(key)
    if (untrack(active) && identity(untrack(active)!) === key) next()
    if (untrack(answering) === key) setAnswering(null)
  }
  return {
    active,
    busy: () => answering() !== null,
    add(ask: AgentApprovalRequested) {
      const key = identity(ask)
      if (pending.has(key)) return
      pending.set(key, ask)
      if (!untrack(active)) next()
    },
    retire,
    async answer(record: (ask: AgentApprovalRequested) => Promise<boolean>) {
      const ask = untrack(active)
      if (!ask || untrack(answering)) return
      const key = identity(ask)
      setAnswering(key)
      try {
        if (await record(ask)) retire(ask)
      } finally {
        // Retirement can expose and start the next question while this RPC is still in flight.
        if (untrack(answering) === key) setAnswering(null)
      }
    },
    clear() {
      pending.clear()
      setActive(null)
      setAnswering(null)
    },
  }
}
