// The data plane's binary frame codec — the browser half of the wire contract
// documented in internal/transport/frame.go. Keep the two in lockstep; the
// golden vector in frame.test.ts and frame_test.go pins the layout on both
// sides.
//
//	byte 0      version    = 0x01
//	byte 1      msg-type   0x01 = data
//	bytes 2..17 session-id 16 raw bytes
//	bytes 18..  payload    raw PTY bytes
export const FRAME_VERSION = 0x01
export const MSG_TYPE_DATA = 0x01
export const FRAME_HEADER_SIZE = 18

const SESSION_ID_RE = /^[0-9a-f]{32}$/

export function isSessionID(hex: string): boolean {
  return SESSION_ID_RE.test(hex)
}

export function hexToBytes(hex: string): Uint8Array {
  if (!isSessionID(hex)) {
    throw new Error(`nocx: invalid session-id: ${hex}`)
  }
  const bytes = new Uint8Array(16)
  for (let i = 0; i < 32; i += 2) {
    bytes[i >> 1] = parseInt(hex.substring(i, i + 2), 16)
  }
  return bytes
}

export function encodeFrame(sessionIDHex: string, payload: Uint8Array): ArrayBuffer {
  const sidBytes = hexToBytes(sessionIDHex)
  const buf = new ArrayBuffer(FRAME_HEADER_SIZE + payload.byteLength)
  const view = new Uint8Array(buf)
  view[0] = FRAME_VERSION
  view[1] = MSG_TYPE_DATA
  view.set(sidBytes, 2)
  view.set(payload, FRAME_HEADER_SIZE)
  return buf
}

export interface DecodedFrame {
  sessionId: string
  payload: ArrayBuffer
}

// decodeFrame mirrors the server's drop-and-warn contract: a frame that is
// short, of an unknown version, or of an unexpected msg-type is dropped, never
// thrown on — a malformed frame must not tear down the connection.
export function decodeFrame(data: ArrayBuffer): DecodedFrame | null {
  if (data.byteLength < FRAME_HEADER_SIZE) {
    console.warn('nocx: frame too short:', data.byteLength)
    return null
  }
  const view = new Uint8Array(data)
  if (view[0] !== FRAME_VERSION) {
    console.warn('nocx: unknown frame version:', view[0])
    return null
  }
  if (view[1] !== MSG_TYPE_DATA) {
    console.warn('nocx: unexpected msg-type:', view[1])
    return null
  }
  const sessionId = Array.from(view.slice(2, FRAME_HEADER_SIZE))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
  return { sessionId, payload: data.slice(FRAME_HEADER_SIZE) }
}
