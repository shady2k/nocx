// The namespace registry — one declaration of who may claim a {{ns:arg}}
// namespace, so a third feature cannot claim one twice.
//
// A colon is what commits a span to this registry. A `{{…}}` WITHOUT a colon
// is a parameter and belongs to no namespace at all — which is why `ask` is
// gone: a question is now written `{{port=8080}}`, and the two token shapes
// are separated by a character rather than by a lookup that could disagree
// (parameters-and-conditions design §3).
//
// Deliberately NOT a shared scan. `env` spans are resolved before the text
// reaches any destination, so no document secret-reference.ts scans ever
// contains one — a shared parser would buy nothing at runtime while placing
// the vault's resolution path in this feature's change budget. What is
// genuinely one concept is who OWNS a namespace, and that is what lives
// here. Snippets design §7.2.
export const REFERENCE_NAMESPACES = {
  secret: 'vault (secret-reference.ts / vault.resolveLine)',
  env: 'snippets (resolved at fire time)',
} as const

export type ReferenceNamespace = keyof typeof REFERENCE_NAMESPACES
