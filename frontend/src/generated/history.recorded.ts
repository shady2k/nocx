/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/history.recorded.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the history.recorded notification: the backend's receipt after an authenticated lifecycle attempt closes its history entry. It replaces history.record's acknowledgement without moving masking, redaction or capture ownership into the renderer.
 */
export interface HistoryRecorded {
  sessionId: string
  attemptId: string
  maskedCount: number
  maskedKinds: string[]
  entryId: string
  source: 'user' | 'assistant'
  redactions: Redaction[]
  maskedCommand: string
  captures: Capture[]
}
export interface Redaction {
  kind:
    | 'private-key'
    | 'openai'
    | 'github-pat'
    | 'slack'
    | 'aws-access-key'
    | 'gitlab'
    | 'jwt'
    | 'url-userinfo'
    | 'db-connstring'
    | 'auth-header'
    | 'env-assignment'
    | 'high-entropy'
  start: number
  end: number
  prefix: string
  suffix: string
}
export interface Capture {
  id: string
  entryId: string
  redaction: Redaction
  suggestedName: string
}
