import { describe, expect, it } from 'vitest'
import { EndpointClient, type EndpointWrite } from './endpoints'
import { onAiConfigChanged } from './ai-config'
import type { Dispatcher } from './dispatcher'

/** The client only relays, so the fake answers with the shape each method
 *  unwraps and nothing more. */
function clientOf(reply: (method: string) => Promise<unknown>): EndpointClient {
  return new EndpointClient({ call: reply } as unknown as Dispatcher)
}

const write: EndpointWrite = {
  name: 'Fake',
  baseUrl: 'http://127.0.0.1:1',
  noKey: false,
  key: 'k',
  credential: '',
  headers: [],
  models: [{ name: 'm-a', alias: null }],
}

// The model chip names the pair that will answer, and a write to that pair
// lands on another pane (nocx-1ebrs.66.1.28). Whoever shows the pair has to
// hear about it, and this is the one place every write goes through — so the
// announcement is made here, on the REPLY, rather than by each surface
// remembering to make it.
describe("a write to the assistant's configuration announces its landing (nocx-1ebrs.66.1.28)", () => {
  it('announces after the backend has taken the write, not before', async () => {
    let resolveReply: (value: unknown) => void = () => {}
    const client = clientOf(() => new Promise((resolve) => (resolveReply = resolve)))
    const heard: string[] = []
    const off = onAiConfigChanged(() => heard.push('changed'))
    try {
      const settling = client.setDefault({ endpointId: 'ep-1', model: 'm-a' })
      await Promise.resolve()
      expect(heard).toEqual([])
      resolveReply({ roles: [], default: { endpointId: 'ep-1', model: 'm-a' } })
      await settling
      expect(heard).toEqual(['changed'])
    } finally {
      off()
    }
  })

  it('announces every write of the configuration, and only the writes', async () => {
    const methods: string[] = []
    const client = clientOf((method) => {
      methods.push(method)
      if (method === 'endpoints.create' || method === 'endpoints.update') {
        return Promise.resolve({ endpoint: { id: 'ep-1', name: 'Fake', models: [] } })
      }
      return Promise.resolve({ roles: [], default: null })
    })
    const heard: string[] = []
    const off = onAiConfigChanged(() => heard.push('changed'))
    try {
      await client.createEndpoint(write)
      await client.updateEndpoint('ep-1', { ...write, key: '' })
      await client.setDefault({ endpointId: 'ep-1', model: 'm-a' })
      await client.assignRole({ role: 'answering', endpointId: 'ep-1', model: 'm-a' })
      await client.deleteEndpoint('ep-1')
      // A read is not a write, and neither is a probe: nothing behind the
      // chip changed, so nothing is announced.
      await client.listEndpoints()
      await client.listRoles()
      expect(methods).toHaveLength(7)
      expect(heard).toHaveLength(5)
    } finally {
      off()
    }
  })

  it('stays silent when the write is refused — nothing changed', async () => {
    const client = clientOf(() => Promise.reject(new Error('the store refused it')))
    const heard: string[] = []
    const off = onAiConfigChanged(() => heard.push('changed'))
    try {
      await expect(client.setDefault({ endpointId: 'ep-1', model: 'm-a' })).rejects.toThrow(
        'the store refused it',
      )
      await expect(client.createEndpoint(write)).rejects.toThrow('the store refused it')
      expect(heard).toEqual([])
    } finally {
      off()
    }
  })

  it('stops telling a listener that unsubscribed', async () => {
    const client = clientOf(() => Promise.resolve({ roles: [], default: null }))
    const heard: string[] = []
    const off = onAiConfigChanged(() => heard.push('changed'))
    off()
    await client.setDefault({ endpointId: 'ep-1', model: 'm-a' })
    expect(heard).toEqual([])
  })
})
