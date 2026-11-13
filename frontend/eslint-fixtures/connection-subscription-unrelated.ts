// Positive control for nocx/no-connection-subscription (ADR-ba6xet1b,
// nocx-oalo1.3.5): a store's own `subscribe(handler)` is not the dispatcher's
// `subscribe(method, handler)`. The call is identified by the declaration its
// signature resolves to, so an unrelated store — with a dynamic name, even —
// is not a connection subscription.

interface Registry {
  subscribe(name: string, handler: () => void): void
}

export function register(registry: Registry, name: string): void {
  registry.subscribe(name, () => {})
}
