import type { CommandMarker } from './renderers/types'

/**
 * Parses an OSC 133 payload into a CommandMarker. Returns null for invalid
 * or unrecognized payloads.
 *
 * Format: 'A' | 'B' | 'C' | 'D' | 'D;<exitcode>', optionally followed by
 * `;key=value` parameters — the parameter form OSC 133 already permits. A
 * `nocx_env=<id>` parameter tags the marker (spec §5.2); an untagged marker
 * keeps driving block boundaries exactly as before. A tag that is present
 * but malformed makes the whole marker invalid (never guessed at), while an
 * absent tag and unknown well-formed keys are tolerated.
 */
const OSC133_TAG_KEY = 'nocx_env'
const OSC133_TAG_VALUE_RE = /^[A-Za-z0-9._-]{1,64}$/

export function parseOsc133(payload: string): CommandMarker | null {
  if (payload.length === 0) return null
  const kind = payload[0] as CommandMarker['kind']
  if (kind !== 'A' && kind !== 'B' && kind !== 'C' && kind !== 'D') return null

  const marker: CommandMarker = { kind }
  if (payload.length === 1) return marker
  if (payload[1] !== ';') return marker // bare kind with trailing junk: unchanged

  // Everything after the kind is `;`-separated parameters. D's first
  // parameter is the positional exit code UNLESS it is itself a key=value
  // property (`D;nocx_env=id` has no exit code).
  const params = payload.slice(2).split(';')
  let i = 0
  if (kind === 'D' && params.length > 0 && params[0] !== '' && params[0].indexOf('=') === -1) {
    const codeStr = params[0]
    i = 1
    // Strict: reject negatives or out-of-range exit codes, keeping the
    // marker itself.
    if (/^\d+$/.test(codeStr)) {
      const code = parseInt(codeStr, 10)
      if (code >= 0 && code <= 255) marker.exitCode = code
    }
  }
  for (; i < params.length; i++) {
    const param = params[i]
    if (param === '') continue // empty parameter: tolerated (legacy `A;`)
    const eq = param.indexOf('=')
    if (eq === -1) return null // not key=value: malformed
    const key = param.slice(0, eq)
    const value = param.slice(eq + 1)
    if (key === OSC133_TAG_KEY) {
      if (!OSC133_TAG_VALUE_RE.test(value)) return null
      marker.nocxEnv = value
    }
    // Well-formed unknown keys are ignored: foreign parameter forms must
    // not break block boundaries.
  }
  return marker
}
