// What a tool call is CALLED — the one thing that tells two calls apart
// (ADR-0040).
//
// These assert the SENTENCE a person reads, not the shape of a data
// structure: the defect was that four announcements read as the same three
// words, and the only way to catch that is to compare the words.

import { describe, it, expect } from 'vitest'
import { toolCallTitle } from './tool-call-title'

const SESSION = '9bb9a7602c27e8ba0741972c7049b54b'
const inThisWindow = { sessionName: (id: string) => (id === SESSION ? 'home/dev' : null) }

describe('toolCallTitle', () => {
  it('tells two calls of one tool apart by what they asked for', () => {
    const read = (blockId: string) =>
      toolCallTitle(
        {
          tool: 'blocks.read',
          args: { sessionId: SESSION, blockId },
          resource: { kind: 'session', id: SESSION },
        },
        inThisWindow,
      )
    expect(read('3')).toBe('blocks.read sessionId=home/dev blockId=3')
    expect(read('4')).toBe('blocks.read sessionId=home/dev blockId=4')
    expect(read('3')).not.toBe(read('4'))
  })

  it('names a session and never numbers it', () => {
    const title = toolCallTitle(
      {
        tool: 'readScreen',
        args: { sessionId: SESSION },
        resource: { kind: 'session', id: SESSION },
      },
      inThisWindow,
    )
    expect(title).toBe('readScreen sessionId=home/dev')
    expect(title).not.toContain(SESSION)
  })

  it('leaves an unnameable session out rather than printing the id', () => {
    // The pane was closed, or it belongs to another window. Null is a real
    // answer, and the id is never the fallback.
    const title = toolCallTitle(
      {
        tool: 'blocks.read',
        args: { sessionId: SESSION, blockId: '7' },
        resource: { kind: 'session', id: SESSION },
      },
      { sessionName: () => null },
    )
    expect(title).toBe('blocks.read blockId=7')
    expect(title).not.toContain(SESSION)
  })

  it('a call whose only argument was an unnameable session is its tool alone', () => {
    expect(
      toolCallTitle(
        {
          tool: 'readScreen',
          args: { sessionId: SESSION },
          resource: { kind: 'session', id: SESSION },
        },
        {},
      ),
    ).toBe('readScreen')
  })

  it('a resource that is not a session is not touched, and a path shows verbatim', () => {
    expect(
      toolCallTitle(
        {
          tool: 'files.read',
          args: { path: '/repo/src/main.ts' },
          resource: { kind: 'path', id: '/repo/src/main.ts' },
        },
        inThisWindow,
      ),
    ).toBe('files.read path=/repo/src/main.ts')
  })

  it('a call with no arguments is its tool name', () => {
    expect(toolCallTitle({ tool: 'git.status' })).toBe('git.status')
    expect(toolCallTitle({ tool: 'git.status', args: {} })).toBe('git.status')
  })

  it('keeps the order the model spelled its arguments in', () => {
    expect(
      toolCallTitle({ tool: 'blocks.read', args: { start: 0, count: 20, blockId: 'b' } }),
    ).toBe('blocks.read start=0 count=20 blockId=b')
  })

  it('a value that is not a string is its compact JSON — what the model sent', () => {
    expect(
      toolCallTitle({ tool: 'run', args: { command: 'ls -la', timeoutMs: 5000, quiet: true } }),
    ).toBe('run command=ls -la timeoutMs=5000 quiet=true')
    expect(toolCallTitle({ tool: 'files.write', args: { lines: ['a', 'b'] } })).toBe(
      'files.write lines=["a","b"]',
    )
  })

  it('nothing is truncated: the title is what the ⋮ copies', () => {
    const long = '/repo/' + 'very-long-directory-name/'.repeat(20) + 'file.ts'
    expect(toolCallTitle({ tool: 'files.read', args: { path: long } })).toBe(
      `files.read path=${long}`,
    )
  })

  it('an argument with no readable value is left out rather than shown empty', () => {
    expect(toolCallTitle({ tool: 'files.read', args: { path: '/a', cursor: null } })).toBe(
      'files.read path=/a',
    )
  })
})
