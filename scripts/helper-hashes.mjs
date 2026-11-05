// The one owner of the helper hash files' platform key and of reading one.
//
// A line is `<goos>-<goarch> <sha256>`, and the key is the artifact name with
// its `nocx-helper-` prefix and `.gz` suffix removed. The release workflow
// writes those files in the helpers, signing and both build jobs, and the
// signing job reads the unsigned one back to prove it signs the bytes the
// helpers job built. Before this module that reader spelled the platform
// `darwin/<arch>`, which matches no line, so every scheduled nightly from
// 2026-11-02 stopped before it signed anything (nocx-vh2hr.1.18).
import { readFileSync } from 'node:fs'
import { basename } from 'node:path'
import { pathToFileURL } from 'node:url'

export function platformOfArtifact(file) {
  const name = basename(file)
  if (!name.startsWith('nocx-helper-') || !name.endsWith('.gz'))
    throw new Error(`${file} is not a nocx-helper artifact`)
  return name.slice('nocx-helper-'.length, -'.gz'.length)
}

export function lookupHash(text, platform) {
  for (const line of text.split('\n')) {
    const [key, hash] = line.trim().split(/\s+/)
    if (key === platform) return hash ?? ''
  }
  return ''
}

function main() {
  const [mode, ...args] = process.argv.slice(2)
  if (mode === 'platform' && args.length === 1) {
    console.log(platformOfArtifact(args[0]))
    return
  }
  if (mode === 'lookup' && args.length === 2) {
    process.stdout.write(lookupHash(readFileSync(args[0], 'utf8'), args[1]))
    return
  }
  console.error('usage: helper-hashes.mjs platform <artifact> | lookup <file> <goos-goarch>')
  process.exitCode = 2
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main()
