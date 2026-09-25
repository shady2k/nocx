import { describe, expect, it, vi } from 'vitest'
import {
  FRAME_HEADER_SIZE,
  FRAME_VERSION,
  MSG_TYPE_DATA,
  MSG_TYPE_METADATA,
  decodeFrame,
  encodeFrame,
  hexToBytes,
  isSessionID,
} from './frame'

const SID = '0123456789abcdef0011223344556677'

// The golden vector. internal/transport/frame_test.go asserts the Go encoder
// produces this exact byte string for the same session-id and payload, so a
// unilateral change to either codec fails a test instead of reaching a user as
// a silently mis-parsed stream.
const GOLDEN_HEX = '01010123456789abcdef001122334455667768690a'
const GOLDEN_PAYLOAD = 'hi\n'

function toHex(buf: ArrayBuffer): string {
  return Array.from(new Uint8Array(buf))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

function bytes(...values: number[]): ArrayBuffer {
  return new Uint8Array(values).buffer
}

describe('encodeFrame', () => {
  it('matches the Go encoder byte for byte', () => {
    const frame = encodeFrame(SID, new TextEncoder().encode(GOLDEN_PAYLOAD))
    expect(toHex(frame)).toBe(GOLDEN_HEX)
  })

  it('lays out version, msg-type, session-id and payload at the fixed offsets', () => {
    const payload = new TextEncoder().encode('hello world\n')
    const view = new Uint8Array(encodeFrame(SID, payload))

    expect(view[0]).toBe(FRAME_VERSION)
    expect(view[1]).toBe(MSG_TYPE_DATA)
    expect(toHex(view.slice(2, FRAME_HEADER_SIZE).buffer)).toBe(SID)
    expect(new TextDecoder().decode(view.slice(FRAME_HEADER_SIZE))).toBe('hello world\n')
    expect(view.byteLength).toBe(FRAME_HEADER_SIZE + payload.byteLength)
  })

  it('encodes an empty payload as a bare header', () => {
    expect(encodeFrame(SID, new Uint8Array(0)).byteLength).toBe(FRAME_HEADER_SIZE)
  })

  it('refuses a session-id that is not 32 lowercase hex chars', () => {
    expect(() => encodeFrame('nope', new Uint8Array(0))).toThrow(/invalid session-id/)
    expect(() => encodeFrame(SID.toUpperCase(), new Uint8Array(0))).toThrow(/invalid session-id/)
    expect(() => encodeFrame(SID + '00', new Uint8Array(0))).toThrow(/invalid session-id/)
  })
})

describe('decodeFrame', () => {
  it('round-trips through the encoder', () => {
    const payload = new TextEncoder().encode('round trip')
    const decoded = decodeFrame(encodeFrame(SID, payload))

    expect(decoded).not.toBeNull()
    expect(decoded?.sessionId).toBe(SID)
    expect(new TextDecoder().decode(decoded?.payload)).toBe('round trip')
  })

  it('decodes the golden vector produced by the Go encoder', () => {
    const raw = new Uint8Array(GOLDEN_HEX.length / 2)
    for (let i = 0; i < raw.length; i++) {
      raw[i] = parseInt(GOLDEN_HEX.substring(i * 2, i * 2 + 2), 16)
    }
    const decoded = decodeFrame(raw.buffer)

    expect(decoded?.sessionId).toBe(SID)
    expect(new TextDecoder().decode(decoded?.payload)).toBe(GOLDEN_PAYLOAD)
  })

  // Drop-and-warn, never throw: a malformed frame must not tear down the
  // connection (internal/transport/frame.go, "Forward-compat"). A msg-type
  // neither side has agreed on is forward-compat territory — dropped the
  // same way, so an older client ignores a newer plane instead of choking.
  it.each([
    ['a frame shorter than the header', bytes(FRAME_VERSION, MSG_TYPE_DATA, 0x00)],
    [
      'an unknown version',
      new Uint8Array([0x99, MSG_TYPE_DATA, ...new Array<number>(16).fill(0)]).buffer,
    ],
    [
      'an unknown msg-type',
      new Uint8Array([FRAME_VERSION, 0x99, ...new Array<number>(16).fill(0)]).buffer,
    ],
  ])('drops %s', (_label, input) => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    expect(decodeFrame(input)).toBeNull()
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })

  // The screen plane rides msg-type 0x02 (internal/transport/screen.go): a
  // whole session.frame document, never PTY bytes. decodeFrame's job ends at
  // the header — it carries the msg-type up and lets the demux decide where
  // the payload goes.
  it('decodes a metadata frame carrying its msg-type', () => {
    const payload = new TextEncoder().encode('{"revision":1}')
    const decoded = decodeFrame(encodeFrame(SID, payload, MSG_TYPE_METADATA))

    expect(decoded).not.toBeNull()
    expect(decoded?.msgType).toBe(MSG_TYPE_METADATA)
    expect(decoded?.sessionId).toBe(SID)
    expect(new TextDecoder().decode(decoded?.payload)).toBe('{"revision":1}')
  })

  it('carries the byte path msg-type so the demux can tell the planes apart', () => {
    const decoded = decodeFrame(encodeFrame(SID, new TextEncoder().encode('hi')))
    expect(decoded?.msgType).toBe(MSG_TYPE_DATA)
  })

  it('accepts a header-only frame as an empty payload', () => {
    const decoded = decodeFrame(encodeFrame(SID, new Uint8Array(0)))
    expect(decoded?.payload.byteLength).toBe(0)
  })
})

describe('hexToBytes', () => {
  it('converts each hex pair to one byte, in order', () => {
    expect(Array.from(hexToBytes(SID)).slice(0, 4)).toEqual([0x01, 0x23, 0x45, 0x67])
    expect(hexToBytes(SID).byteLength).toBe(16)
  })
})

describe('isSessionID', () => {
  it('accepts exactly 32 lowercase hex chars', () => {
    expect(isSessionID(SID)).toBe(true)
    expect(isSessionID('')).toBe(false)
    expect(isSessionID(SID.slice(0, 31))).toBe(false)
    expect(isSessionID(SID.toUpperCase())).toBe(false)
    expect(isSessionID(`${SID}\n`)).toBe(false)
  })
})
