# Settings RPC contract (coordinator-owned, frozen for wave 3)

Three workers build against this contract in parallel without touching each other's files.
It is frozen: if you believe it is wrong, **escalate — do not change it unilaterally**, because
two other workers have already built against it.

Control plane only — JSON-RPC 2.0 over the single WebSocket (AD-1). No secret value ever
appears in a response.

## Data classes (ADR-0011 §3)

`publicConfig` · `privateMetadata` · `privateContent` · `secretAuthenticator`

Classification drives the generated UI, export eligibility and log handling. It does **not**
route storage.

## Control kinds

`toggle` · `text` · `number` · `select` · `secret`

## Methods

### `settings.describe` → `{ declarations: Declaration[] }`

Enumerates every declared setting. This is what the generated screen renders from; the screen
must contain no hand-maintained list of settings.

```ts
interface Declaration {
  key: string // stable dotted id, e.g. "terminal.fontSize"
  section: string // group heading in the UI
  label: string
  description: string
  control: 'toggle' | 'text' | 'number' | 'select' | 'secret'
  dataClass: 'publicConfig' | 'privateMetadata' | 'privateContent' | 'secretAuthenticator'
  default: unknown // absent for control:'secret'
  options?: { value: string; label: string }[] // control:'select' only
  min?: number // control:'number' only
  max?: number // control:'number' only
}
```

### `settings.getAll` → `{ values: Record<string, unknown> }`

Current values for every **non-secret** setting. A `control:'secret'` key is **never** present
in this response, even as null.

### `settings.set` `{ key: string, value: unknown }` → `{ ok: true }`

Validates against the declaration and persists. A validation failure is a JSON-RPC error, not
`ok: false`. Rejects a `control:'secret'` key — secrets go through `settings.secretSet`.

### `settings.reset` `{ key: string }` → `{ ok: true }`

Restores the declared default.

### Secret-class methods — set, delete, exists; never get (ADR-0011 §2)

- `settings.secretSet` `{ key: string, value: string }` → `{ ok: true }`
- `settings.secretDelete` `{ key: string }` → `{ ok: true }`
- `settings.secretExists` `{ key: string }` → `{ exists: boolean }`

There is deliberately no `settings.secretGet`. The renderer can learn that a secret **is set**
and can replace or clear it. It can never read it back. A secret editor in the UI therefore
renders as "configured / not configured" plus Replace and Clear actions — never as a populated
input.
