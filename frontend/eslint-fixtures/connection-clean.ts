// The positive control for the connection-owner fixture gate (ADR-ba6xet1b): a
// file with no connection code must be UNCHANGED, and the shapes a person
// writes every day must stay silent — a typed discriminator compared to its own
// closed set, an emptiness check, a switch on a declared union. A rule that
// reported those would be turned off, which is the same outcome as not having
// it.

type Sealed = 'vault-sealed' | 'vault-uninitialized'

export function fieldLabel(verdict: { reason: Sealed }): string {
  if (verdict.reason === 'vault-sealed') return 'Locked'
  if (verdict.reason === 'vault-uninitialized') return 'Not set up yet'
  return 'Unknown'
}

export function sentence(message: string): string {
  return message === '' ? 'nothing to say' : message
}

export function reconnectPolicy(choice: 'ask' | 'auto' | 'never'): string {
  switch (choice) {
    case 'auto':
      return 'Automatically'
    case 'never':
      return 'Never'
    default:
      return 'Ask'
  }
}
