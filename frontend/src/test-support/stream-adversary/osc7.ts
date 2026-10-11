/** Parse the OSC 7 fixture payload used by the stream-adversary harness. */
export function parseOsc7Fixture(payload: string): { host: string; path: string } | null {
  if (!payload.startsWith('file://')) return null
  const uri = payload.slice('file://'.length)
  const slash = uri.indexOf('/')
  if (slash < 0) return null
  try {
    return {
      host: decodeURIComponent(uri.slice(0, slash)),
      path: decodeURIComponent(uri.slice(slash)),
    }
  } catch {
    return null
  }
}
