/**
 * SkillsClient — the renderer's end of the Skills page, which MANAGES skills
 * and does not acquire them (nocx-ojfuc.4).
 *
 * `preview` and `install` used to live here and are gone with the paste box
 * that was their only caller: acquisition is the assistant's `skills.install`
 * tool, which runs in Go and never reaches this socket, so leaving the two
 * methods would have left a client able to call a wire nothing serves. What
 * this client sends is what a person can do to a skill they already have —
 * list it, switch it, delete it, re-approve it, read its files and ask for a
 * reading of it.
 *
 * The check every case here makes is the same one: the method sends the
 * request and NOTHING else. What a skill is called, what it is made of and
 * what its bytes say are the backend's answers; a renderer that derived any
 * of them would be a second answer to a question that has an owner.
 */
import { describe, expect, it, vi } from 'vitest'
import { SkillsClient } from './skills-client'
import type { Dispatcher } from './dispatcher'

function fakeDispatcher(answers: unknown[]): {
  dispatcher: Dispatcher
  calls: { method: string; params: unknown }[]
} {
  const calls: { method: string; params: unknown }[] = []
  const call = vi.fn((method: string, params: unknown) => {
    calls.push({ method, params })
    const next = answers.shift()
    if (next instanceof Error) return Promise.reject(next)
    return Promise.resolve(next)
  })
  return { dispatcher: { call } as unknown as Dispatcher, calls }
}

describe('SkillsClient.file', () => {
  it('sends the skill and the path the person asked for, unresolved', async () => {
    const answer = {
      name: 'deploy',
      path: 'references/hosts.md',
      provenance: 'authored',
      text: 'prod is eu-1\n',
      refusal: '',
      maxBytes: 65536,
    }
    const { dispatcher, calls } = fakeDispatcher([answer])

    const got = await new SkillsClient(dispatcher).file('deploy', 'references/hosts.md')

    // The path travels VERBATIM. Cleaning or joining it here would be a
    // second answer to "is this inside the skill", which the backend settles
    // through the same containment the assistant's read tool goes through.
    expect(calls).toEqual([
      { method: 'skills.file', params: { name: 'deploy', path: 'references/hosts.md' } },
    ])
    expect(got.text).toBe('prod is eu-1\n')
    expect(got.refusal).toBe('')
  })

  it('resolves — not rejects — when the file exists but its bytes are not shown', async () => {
    // The two facts a viewer has to state in the person's words. Both are
    // true sentences about a file that IS there, so both arrive as results
    // carrying the path, the provenance and the budget that refused them; a
    // rejection would leave the caller with nothing but a message.
    const { dispatcher } = fakeDispatcher([
      {
        name: 'deploy',
        path: 'diagram.png',
        provenance: 'builtin',
        text: '',
        refusal: 'not-text',
        maxBytes: 65536,
      },
      {
        name: 'deploy',
        path: 'dump.log',
        provenance: 'installed',
        text: '',
        refusal: 'too-large',
        maxBytes: 65536,
      },
    ])
    const client = new SkillsClient(dispatcher)

    const notText = await client.file('deploy', 'diagram.png')
    expect(notText.refusal).toBe('not-text')
    expect(notText.text).toBe('')
    expect(notText.path).toBe('diagram.png')

    const tooLarge = await client.file('deploy', 'dump.log')
    expect(tooLarge.refusal).toBe('too-large')
    expect(tooLarge.maxBytes).toBe(65536)
  })

  it('lets a refusal of the request through as the backend wrote it', async () => {
    // A file that is gone has no subject to describe, so it rejects, and the
    // sentence is the backend's own rather than one invented here.
    const { dispatcher } = fakeDispatcher([
      new Error('skill "deploy" path "references/gone.md": no such file or directory'),
    ])
    await expect(new SkillsClient(dispatcher).file('deploy', 'references/gone.md')).rejects.toThrow(
      'no such file or directory',
    )
  })
})

describe('SkillsClient.files', () => {
  it('sends the skill and nothing else, and answers with the manifest as it is on disk', async () => {
    const answer = {
      name: 'weather',
      provenance: 'installed',
      files: ['SKILL.md', 'references/stations.md', 'scripts/fetch.sh'],
      truncated: false,
      maxFiles: 256,
    }
    const { dispatcher, calls } = fakeDispatcher([answer])

    const got = await new SkillsClient(dispatcher).files('weather')

    // One name and no path: this asks what the skill IS MADE OF, and the
    // paths are the answer rather than part of the question.
    expect(calls).toEqual([{ method: 'skills.files', params: { name: 'weather' } }])
    // Every file, script included — which is what design §8 has the person
    // read before they turn the skill on.
    expect(got.files).toContain('scripts/fetch.sh')
    expect(got.truncated).toBe(false)
  })

  it('lets a refusal through as it was written', async () => {
    const { dispatcher } = fakeDispatcher([new Error('skill "absent" was not found')])
    await expect(new SkillsClient(dispatcher).files('absent')).rejects.toThrow('was not found')
  })
})

describe('SkillsClient.audit', () => {
  it('sends the skill and nothing about the model, and answers with a reading', async () => {
    const answer = {
      name: 'weather',
      provenance: 'installed',
      role: 'auditing',
      endpoint: 'Local',
      model: 'qwen3',
      report: 'It asks a station and curls example.test.',
      read: ['SKILL.md', 'scripts/fetch.sh'],
      omitted: [],
      maxBytes: 131072,
      findings: [],
    }
    const { dispatcher, calls } = fakeDispatcher([answer])

    const got = await new SkillsClient(dispatcher).audit('weather')

    // NOTHING ABOUT THE MODEL is a parameter. Which model reads a skill is
    // the auditing role's assignment, resolved on the backend in the one
    // place a role becomes an (endpoint, model) pair; a renderer that named
    // one would be a second answer to that.
    expect(calls).toEqual([{ method: 'skills.audit', params: { name: 'weather' } }])
    expect(got.report).toBe('It asks a station and curls example.test.')
    expect(got.role).toBe('auditing')
  })

  it('lets a refusal through as it was written', async () => {
    // A reading that did not happen is a refusal the person reads, never an
    // empty report — an empty report is indistinguishable from a clean one.
    const { dispatcher } = fakeDispatcher([
      new Error('no model is assigned to the auditing role, and none to the answering role either'),
    ])
    await expect(new SkillsClient(dispatcher).audit('weather')).rejects.toThrow(
      'no model is assigned',
    )
  })
})
