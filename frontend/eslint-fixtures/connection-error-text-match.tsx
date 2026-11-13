// Negative fixture for nocx/no-error-text-match (ADR-ba6xet1b, nocx-oalo1.3.5).
// It is linted by src/connection-owner-gate.test.ts through the production rule
// block at a virtual path inside src/, and never by the normal `eslint .` run,
// which ignores eslint-fixtures/.
//
// Deliberately broken: every decision below is made from an error's or a
// refusal's words. A panel that learns "the connection was lost" by finding
// `disconnected` in a sentence is the defect the rule exists to refuse.

export function refusalSentence(err: unknown): string {
  const message = err instanceof Error ? err.message : String(err)
  if (message.includes('disconnected')) return 'the connection was lost'
  if (/not connected|ws closed/.test(message)) return 'the connection was lost'
  return 'the change could not be made'
}

export function PortsFailure(props: { reason: string }) {
  return <p>{portsFailure(props.reason)}</p>
}

function portsFailure(reason: string): string {
  const r = reason.toLowerCase()
  if (r.startsWith('not connected')) return 'the connection was lost'
  return reason
}

export function verdict(err: { data?: { reason?: string } }): string {
  return err.data?.reason === 'unknown-session' ? 'lost' : 'unknown'
}
