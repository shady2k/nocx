import { test } from 'node:test'
import assert from 'node:assert/strict'
import { Agent, request } from 'node:http'

import { readFileSync } from 'node:fs'
import { stripTypeScriptTypes } from 'node:module'

// The Playwright helper is .ts in a CommonJS package; Node's native runner
// does not load its ESM exports directly. Strip only types, not behaviour,
// and load the same self-contained helper as an ES module.
const source = readFileSync(new URL('./fake-openai.ts', import.meta.url), 'utf8')
const { FakeOpenAI } = await import(
  `data:text/javascript,${encodeURIComponent(stripTypeScriptTypes(source))}`
)

function post(url: string, agent: Agent): Promise<string> {
  return new Promise((resolve, reject) => {
    const req = request(`${url}/chat/completions`, { method: 'POST', agent }, (res) => {
      let body = ''
      res.setEncoding('utf8')
      res.on('data', (chunk: string) => {
        body += chunk
      })
      res.once('end', () => resolve(body))
      res.once('error', reject)
    })
    req.once('error', reject)
    req.end(JSON.stringify({ messages: [], stream: false }))
  })
}

test('reuse closes only when armed, before consuming a model response', async () => {
  const fake = new FakeOpenAI()
  const agent = new Agent({ keepAlive: true, maxSockets: 1 })
  try {
    const url = await fake.start()
    await post(url, agent)
    await post(url, agent)
    const before = fake.requests()
    assert.equal(before[0].connectionId, before[1].connectionId)
    assert.deepEqual(fake.rejectedReuses(), [])

    assert.deepEqual(fake.closeOnNextReuse(), [before[0].connectionId])
    fake.setScript({ chunks: ['not consumed by a rejected request'] })
    await assert.rejects(post(url, agent), { code: 'ECONNRESET' })
    assert.equal(fake.requests().length, 2)
    assert.deepEqual(fake.rejectedReuses(), [
      {
        connectionId: before[0].connectionId,
        path: '/v1/chat/completions',
      },
    ])
    const answer = JSON.parse(await post(url, agent))
    assert.equal(answer.choices[0].message.content, 'not consumed by a rejected request')
    assert.notEqual(fake.requests()[2].connectionId, before[0].connectionId)
    // One-shot: the new connection can be reused normally again.
    await post(url, agent)
    assert.equal(fake.requests()[3].connectionId, fake.requests()[2].connectionId)
  } finally {
    agent.destroy()
    await fake.stop()
  }
})

test('a fresh connection delivers exactly one completion while the reuse close is armed', async () => {
  const fake = new FakeOpenAI()
  const agent = new Agent({ keepAlive: false })
  try {
    const url = await fake.start()
    await post(url, agent)
    const previous = fake.closeOnNextReuse()
    fake.setScript({ chunks: ['delivered once'] })
    const answer = JSON.parse(await post(url, agent))
    assert.equal(answer.choices[0].message.content, 'delivered once')
    assert.equal(fake.requests().length, 2)
    assert.ok(!previous.includes(fake.requests()[1].connectionId))
    assert.deepEqual(fake.rejectedReuses(), [])
  } finally {
    agent.destroy()
    await fake.stop()
  }
})
