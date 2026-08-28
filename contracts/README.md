# `contracts/` — the wire format, declared once

Every JSON-RPC **result** shape the renderer depends on, and each covered method's
**params** shape, is declared here as a JSON Schema and nowhere else. A filename ending
in `.params.schema.json` describes request params; the existing `<method>.schema.json`
form describes that method's result.

```
                    contracts/vault.status.schema.json
                                   │
                 ┌─────────────────┴──────────────────┐
       generated │                                    │ validated
                 ▼                                    ▼
  frontend/src/generated/vault.status.ts    Go: the marshalled DTO, and the
  (committed; never hand-edited)            real result off the WebSocket
```

## The two directions are not symmetric, on purpose

- **Renderer — impossible to drift.** `frontend/src/generated/*.ts` is generated and
  committed; `vault-client.ts` re-exports it and declares nothing of its own. A type
  that wants a field the wire does not carry cannot be written.
- **Go — reliably detected.** The transport keeps its own hand-written structs, because
  generating Go wire DTOs would either infect the domain types or need a mapping layer
  neither of which this seam has earned. `additionalProperties: false` plus `required`
  makes the check exact in both directions: an extra field fails, a missing one fails.

## What runs, and where

| Check                                                   | Where                                           | Catches                                                     |
| ------------------------------------------------------- | ----------------------------------------------- | ----------------------------------------------------------- |
| Generated TS matches the schema                         | `npm run contracts:check`, pre-commit           | someone edited the schema and forgot to regenerate          |
| The Go DTO marshals to something the schema accepts     | `TestVaultStatus_DTOConformsToContract`         | field tags, `omitempty`, nil-slice-as-`null`, enum spelling |
| The **real result off the socket** satisfies the schema | `TestVaultStatus_OverTheWireConformsToContract` | the handler not sending what the DTO could have             |

The third row is the one that matters, and the reason for the whole directory. A test
that validates a payload the test itself constructed proves the struct is well-formed,
not that the server sends it. Only driving the real method through the real socket does
that.

## Adding a method

1. Write `contracts/<method>.schema.json` for a result or
   `contracts/<method>.params.schema.json` for request params. Required, always:
   `additionalProperties: false` and an explicit `required` list — without them the
   schema accepts anything and the gate is theatre. Use `enum` where the set is
   genuinely closed, `["string", "null"]` for a field that is nullable rather than
   optional.
2. `cd frontend && npm run contracts` — commit generated files with the schemas.
3. Import generated result types in the client; do not re-declare them. Params schemas
   are consumed by the Go registration agreement test rather than emitted as renderer
   result types.
4. Add the method's result contract to the Go table in
   `internal/transport/ws_contract_test.go`: one DTO case per interesting shape
   (populated, empty, error-adjacent), and one over-the-socket case.

## Why this exists

`vault.status` shipped without `defaultProvider`. The renderer's `VaultStatus` declared
it, the Vault page read it on every render to mark which store new secrets go to, and
`vault.SetDefaultProvider` wrote a value nobody could ever read back. Both suites were
green.

Neither side was under-tested; they were tested **separately**. The Go tests decode into
anonymous structs naming only the fields under test — and a field nobody names is a field
whose absence nobody notices. The renderer's tests mocked the client with fixtures
written _from the interface_, so they carried the field because the renderer wanted it,
not because anything sent it. Each side proved it agreed with itself.

The first version of this directory was a fully-populated sample response with a key-set
comparison. It was replaced within the hour: a key set cannot express types, nullability
or enums, so changing `autoSealMinutes` from `int` to `string` kept it green. On its
first run the schema immediately found a second defect the samples had missed —
`providers` marshalling as `null` rather than `[]` on a vault with no providers, which
is the same class of bug as `nocx-25k9.14` and would have thrown on the renderer's first
`.map`.

## Deliberately out of scope

- **The binary data plane** (AD-1). It has no JSON shape to pin.
- _*Params, except for the initial notes.* and snippets._ slice.** Params contracts now
  exist for those eleven registered methods, and `internal/transport` proves each
  schema's rejected shapes are rejected by that method's registered validator. The
  remaining registered methods are listed in `params-unswept.txt` for the later sweep.
- **OpenRPC.** It is the protocol-native way to describe a whole JSON-RPC surface and is
  worth adopting when method-level metadata starts being duplicated — not to fix one
  response shape.
- **Runtime validation in production.** Both ends of this socket are ours.
