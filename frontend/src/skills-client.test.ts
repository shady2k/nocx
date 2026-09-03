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
