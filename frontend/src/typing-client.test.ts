/**
 * What the typing client puts ON THE WIRE (nocx-dkawo.1).
 *
 * The section's tests spy on this client's methods, so they see what the
 * surface asked for and not what was sent. This is the other half, and the
 * half where the bead's design would be given away: an `agent` or a `state`
 * reaching the backend from here would be a caller deciding what the backend
 * must read for itself. The backend refuses an unknown field outright, so a
 * client that grew one would fail every call — this asserts it before that
 * happens rather than after.
 */
import { describe, it, expect, vi } from 'vitest'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { TypingClient } from './typing-client'

function spied(): { client: TypingClient; calls: [string, unknown][] } {
  const dispatcher = new Dispatcher(fixedEndpoint(9876))
  const calls: [string, unknown][] = []
  vi.spyOn(dispatcher, 'call').mockImplementation((method: string, params?: unknown) => {
    calls.push([method, params])
    return Promise.resolve({
      sessionId: 'sess-1',
      agent: 'claude',
      outcome: 'typed',
      state: 'free_text',
    })
  })
  return { client: new TypingClient(dispatcher), calls }
}

describe('the typing client', () => {
  it('sends a pane and text, and nothing that names an agent or a state', async () => {
    const { client, calls } = spied()
    await client.type('sess-1', 'wake up')
    expect(calls[0]).toEqual(['agent.type', { sessionId: 'sess-1', text: 'wake up' }])
  })

  it('asks for the submit key explicitly, and leaves it off otherwise', async () => {
    const { client, calls } = spied()
    await client.type('sess-1', 'wake up')
    await client.submit('sess-1', 'wake up')
    // Absent rather than false: the backend defaults it to false, and the
    // default is the safe direction — a caller that forgot the field leaves
    // its text in the input region rather than starting a turn.
    expect(Object.keys(calls[0][1] as object).sort()).toEqual(['sessionId', 'text'])
    expect(calls[1][1]).toEqual({ sessionId: 'sess-1', text: 'wake up', submit: true })
  })
})
