// Negative fixture for nocx/no-error-text-match (ADR-ba6xet1b,
// nocx-oalo1.3.5). A computed key resolves by its TYPE: `const FIELD =
// 'message'` is `err.message`, so the `.includes` is a decision on error text.
// A key that is not a literal at all on an Error/refusal value is reported
// directly — it may be `message` or `reason`, and the check cannot follow it.

const FIELD = 'message'

export function classify(err: unknown, dynamicField: string): string {
  const e = err as { message: string }
  if (e[FIELD].includes('disconnected')) return 'the connection was lost'
  if (e[dynamicField].includes('disconnected')) return 'the connection was lost'
  return 'the change could not be made'
}
