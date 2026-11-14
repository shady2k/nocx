/**
 * What the agent-record client puts ON THE WIRE for detection
 * (nocx-a62cl.12.2).
 *
 * The section's tests spy on this client's `detect`, so they see what the
 * surface asked for and not what was sent. This is the other half: the
 * backend caches a detection pass until a caller asks for a refresh, so a
 * client that dropped the flag would answer every refresh with the answer
 * already on screen — and the section's own test would go on passing, because
 * the section does pass `true`.
 */
import { describe, expect, it, vi } from 'vitest'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { AgentRecordsClient } from './agent-records-client'

function spied(): { client: AgentRecordsClient; calls: [string, unknown][] } {
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  const calls: [string, unknown][] = []
  vi.spyOn(dispatcher, 'call').mockImplementation((method: string, params?: unknown) => {
    calls.push([method, params])
    return Promise.resolve({ agents: [] })
  })
  return { client: new AgentRecordsClient(dispatcher), calls }
}

describe('the agent-record client’s detection call', () => {
  it('reads the cached answer with no refresh asked for', async () => {
    const { client, calls } = spied()
    await client.detect()
    await client.detect(false)
    expect(calls[0]).toEqual(['agentRecords.detect', {}])
    expect(calls[1]).toEqual(['agentRecords.detect', {}])
  })

  it('asks for a refresh as a parameter, never as a second method', async () => {
    const { client, calls } = spied()
    await client.detect(true)
    expect(calls[0]).toEqual(['agentRecords.detect', { refresh: true }])
  })
})
