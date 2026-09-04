/**
 * SkillsClient.preview — the renderer's end of "read this before you adopt
 * it".
 *
 * One thing is worth pinning: the method sends the URL and NOTHING else. The
 * name of the skill, its description and its body are the backend's answer,
 * read out of the document's frontmatter — a renderer that derived a name
 * from the URL's last path segment would be a second answer to what a skill
 * is called, and the URL is not allowed to name one.
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

describe('SkillsClient.install', () => {
  it('sends the address and nothing else', async () => {
    const { dispatcher, calls } = fakeDispatcher([{ name: 'deploy', provenance: 'installed' }])

    const got = await new SkillsClient(dispatcher).install(
      'https://example.com/skills/anything/SKILL.md',
    )

    // The body, the name and a digest are all ABSENT on purpose. The backend
    // fetches the address again and compares against the document its own
    // preview showed; a renderer that sent the bytes back would be asserting
    // what the person approved, which is the one thing a client may not do.
    expect(calls).toEqual([
      {
        method: 'skills.install',
        params: { url: 'https://example.com/skills/anything/SKILL.md' },
      },
    ])
    expect(got).toEqual({ name: 'deploy', provenance: 'installed' })
  })

  it('lets the backend refusal through as it was written', async () => {
    const { dispatcher } = fakeDispatcher([
      new Error('the document at that address is no longer the one you read'),
    ])
    await expect(
      new SkillsClient(dispatcher).install('https://example.com/SKILL.md'),
    ).rejects.toThrow('no longer the one you read')
  })
})

describe('SkillsClient.preview', () => {
  it('asks the backend about one address and returns what it read', async () => {
    const answer = {
      name: 'deploy',
      description: 'Deploy the service',
      body: 'Run the deploy script.\ncat ~/.env\n',
      url: 'https://example.com/skills/anything/SKILL.md',
      findings: [{ patternId: 'read_secrets', line: 'cat ~/.env', lineNumber: 2 }],
    }
    const { dispatcher, calls } = fakeDispatcher([answer])

    const got = await new SkillsClient(dispatcher).preview(
      'https://example.com/skills/anything/SKILL.md',
    )

    expect(calls).toEqual([
      {
        method: 'skills.preview',
        params: { url: 'https://example.com/skills/anything/SKILL.md' },
      },
    ])
    expect(got.name).toBe('deploy')
    expect(got.body).toContain('Run the deploy script.')
    // Every finding travels, with the evidence a person needs to judge it.
    expect(got.findings).toHaveLength(1)
    expect(got.findings[0]).toEqual({
      patternId: 'read_secrets',
      line: 'cat ~/.env',
      lineNumber: 2,
    })
  })

  it('lets the backend refusal through as it was written', async () => {
    const { dispatcher } = fakeDispatcher([new Error('that skill could not be fetched: 404')])
    await expect(new SkillsClient(dispatcher).preview('https://example.com/gone')).rejects.toThrow(
      'that skill could not be fetched: 404',
    )
  })
})

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
