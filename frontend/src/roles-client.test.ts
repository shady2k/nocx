import { expect, it } from 'vitest'
import { RolesClient } from './roles-client'
import { onAiConfigChanged } from './ai-config'
import type { Dispatcher } from './dispatcher'

it('announces a saved role choice only after the backend accepts it', async () => {
  let resolve!: (value: { agentId: string; roles: [] }) => void
  const reply = new Promise<{ agentId: string; roles: [] }>((done) => {
    resolve = done
  })
  const client = new RolesClient({ call: () => reply } as unknown as Dispatcher)
  let changes = 0
  const unsubscribe = onAiConfigChanged(() => changes++)
  try {
    const saving = client.assignRole({ agentId: 'pi', role: 'answering', model: 'model' })
    expect(changes).toBe(0)
    resolve({ agentId: 'pi', roles: [] })
    await saving
    expect(changes).toBe(1)
  } finally {
    unsubscribe()
  }
})

it('does not announce a refused role choice', async () => {
  const client = new RolesClient({
    call: () => Promise.reject(new Error('refused')),
  } as unknown as Dispatcher)
  let changes = 0
  const unsubscribe = onAiConfigChanged(() => changes++)
  try {
    await expect(
      client.assignRole({ agentId: 'pi', role: 'answering', model: 'model' }),
    ).rejects.toThrow('refused')
    expect(changes).toBe(0)
  } finally {
    unsubscribe()
  }
})
