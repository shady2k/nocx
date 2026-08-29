// The grammar's tests, written from the spec rather than from the scanner:
// what a terminal user expects to be clickable, and — the half that costs
// more — what must NOT be, because a false positive turns ordinary output
// into a minefield of dead underlines.
import { describe, expect, it } from 'vitest'
import { detectLinks, type LinkSpan } from './detect'

/** Terse assertion helper: the substring each span covers, in order. */
function texts(text: string): string[] {
  return detectLinks(text).map((s) => text.slice(s.from, s.to))
}

function only(text: string): LinkSpan {
  const spans = detectLinks(text)
  expect(spans).toHaveLength(1)
  return spans[0]
}

describe('detectLinks — paths', () => {
  it('finds a relative path with a line suffix', () => {
    const span = only('see docs/architecture.md:101 for the rest')
    expect(text_of(span, 'see docs/architecture.md:101 for the rest')).toBe(
      'docs/architecture.md:101',
    )
    expect(span.target).toEqual({ kind: 'path', path: 'docs/architecture.md', line: 101 })
  })

  it('finds a bare filename when a line suffix names one', () => {
    // AGENTS.md:84 has no slash: the line suffix is what makes it a path
    // reference rather than a word with a dot in it.
    expect(only('AGENTS.md:84').target).toEqual({ kind: 'path', path: 'AGENTS.md', line: 84 })
  })

  it('takes line and column', () => {
    expect(only('src/main.ts:12:5').target).toEqual({
      kind: 'path',
      path: 'src/main.ts',
      line: 12,
      col: 5,
    })
  })

  it('finds an absolute path with no line', () => {
    expect(only('/usr/local/bin/nocx').target).toEqual({
      kind: 'path',
      path: '/usr/local/bin/nocx',
    })
  })

  it('finds ~, ./ and ../ prefixed paths', () => {
    expect(texts('~/.zshrc ./Makefile ../sibling/file.go')).toEqual([
      '~/.zshrc',
      './Makefile',
      '../sibling/file.go',
    ])
  })

  it('finds several on one line', () => {
    expect(texts('a/b.ts:1 and c/d.go:2')).toEqual(['a/b.ts:1', 'c/d.go:2'])
  })

  it('drops trailing sentence punctuation but keeps a path that ends in one', () => {
    expect(texts('open docs/vision.md, then docs/architecture.md.')).toEqual([
      'docs/vision.md',
      'docs/architecture.md',
    ])
  })

  it('does not swallow the closing bracket of a citation', () => {
    expect(texts('(see src/app.ts:9)')).toEqual(['src/app.ts:9'])
  })
})

describe('detectLinks — what must not match', () => {
  it('ignores a bare word with a dot and no line suffix', () => {
    // "e.g." and "v0.3.0" are the two that appear in this repo's own output.
    expect(texts('e.g. v0.3.0 and node_modules')).toEqual([])
  })

  it('ignores a version-like token even with a numeric suffix', () => {
    expect(texts('bumped to v0.3.0:1')).toEqual([])
  })

  it('ignores a bare number and a time', () => {
    expect(texts('took 12:30 and 1024')).toEqual([])
  })

  it('ignores an scp-style user@host:path', () => {
    // A colon that survives the line-suffix strip means this is not a path
    // we can open — an ssh destination is a different concept entirely.
    expect(texts('scp user@host:~/notes.md .')).toEqual([])
  })

  it('ignores a command flag', () => {
    expect(texts('run with --no-verify')).toEqual([])
  })

  it('ignores a ratio or fraction written with a slash', () => {
    expect(texts('passed 12/20 checks')).toEqual([])
  })
})

describe('detectLinks — urls', () => {
  it('finds an http(s) url', () => {
    expect(only('see https://example.com/a/b?q=1#f').target).toEqual({
      kind: 'url',
      url: 'https://example.com/a/b?q=1#f',
    })
  })

  it('keeps a port and does not read it as a line number', () => {
    expect(only('http://localhost:5173/index.html').target).toEqual({
      kind: 'url',
      url: 'http://localhost:5173/index.html',
    })
  })

  it('drops trailing punctuation after a url', () => {
    expect(texts('open https://example.com/a.')).toEqual(['https://example.com/a'])
  })

  it('drops a closing paren that belongs to the prose', () => {
    expect(texts('(https://example.com/a)')).toEqual(['https://example.com/a'])
  })

  it('does not also report the url as a path', () => {
    expect(detectLinks('https://example.com/a/b.ts:12')).toHaveLength(1)
  })

  it('ignores a scheme it cannot open', () => {
    expect(texts('ftp://example.com/x javascript:alert(1)')).toEqual([])
  })
})

describe('detectLinks — spans', () => {
  it('reports offsets into the original text, in order', () => {
    const text = 'a/b.ts:1 https://x.test/y c/d.go'
    const spans = detectLinks(text)
    expect(spans.map((s) => [s.from, s.to])).toEqual([
      [0, 8],
      [9, 25],
      [26, 32],
    ])
    expect(spans.map((s) => text.slice(s.from, s.to))).toEqual([
      'a/b.ts:1',
      'https://x.test/y',
      'c/d.go',
    ])
  })

  it('returns nothing for empty text', () => {
    expect(detectLinks('')).toEqual([])
  })
})

function text_of(span: LinkSpan, text: string): string {
  return text.slice(span.from, span.to)
}
