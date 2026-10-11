import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const frontendRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const sourceRoot = join(frontendRoot, 'src')

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return sourceFiles(path)
    return /\.tsx?$/.test(entry.name) ? [path] : []
  })
}

describe('the xterm cutover', () => {
  it('keeps xterm packages out of the manifest and lockfile', () => {
    const manifest = JSON.parse(readFileSync(join(frontendRoot, 'package.json'), 'utf8')) as {
      dependencies?: Record<string, unknown>
      devDependencies?: Record<string, unknown>
    }
    const lockfile = JSON.parse(readFileSync(join(frontendRoot, 'package-lock.json'), 'utf8')) as {
      packages?: Record<string, unknown>
    }
    const declared = [
      ...Object.keys(manifest.dependencies ?? {}),
      ...Object.keys(manifest.devDependencies ?? {}),
    ]
    const locked = Object.keys(lockfile.packages ?? {}).map((name) =>
      name.replace(/^node_modules\//, ''),
    )

    expect([...declared, ...locked].filter((name) => name.startsWith('@xterm/'))).toEqual([])
  })

  it('keeps xterm imports out of every frontend source file', () => {
    const importPattern = /(?:from\s*|import\s*(?:\(\s*)?)['"]@xterm\/[^'"]+['"]/g
    const imports = sourceFiles(sourceRoot).flatMap((path) => {
      const text = readFileSync(path, 'utf8')
      return [...text.matchAll(importPattern)].map((match) => ({
        file: relative(frontendRoot, path),
        import: match[0],
      }))
    })

    expect(imports).toEqual([])
  })
})
