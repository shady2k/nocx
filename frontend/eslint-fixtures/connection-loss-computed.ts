// Negative fixture for nocx/no-connection-loss (ADR-ba6xet1b,
// nocx-oalo1.3.5). A computed property key whose literal type is `cause` mints
// the same member as `cause:`, and `as const` preserves the value's literal
// type. Neither the backtick key nor the assertion is a way around the rule.

const CAUSE = `cause`

export function lost(): { cause: string } {
  return { [CAUSE]: 'interrupted' as const }
}
