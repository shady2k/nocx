# `contracts/helper/` — the FROZEN helper ABI

The schemas one directory up describe the **JSON-RPC control plane** between our backend
and our renderer: both ends ours, both ends this build, and a mismatch lasts until the
next release. `contracts/files/` describes **files on disk**, which any build may have
written.

These describe a third boundary, and it is the one where being wrong is permanent: the
wire between a **coordinator** and a **helper generation** on some host.

|                               | `contracts/*.schema.json`  | `contracts/files/*.schema.json` | `contracts/helper/*.schema.json`         |
| ----------------------------- | -------------------------- | ------------------------------- | ---------------------------------------- |
| Who is at the other end       | our renderer, this build   | a document, any build           | a helper generation, possibly months old |
| Generated renderer types      | yes (`npm run contracts`)  | no                              | **no** — the renderer never speaks this  |
| Can a mismatch be fixed later | yes, next release          | yes, by the version protocol    | **no** — see below                       |
| Validated by                  | `internal/transport` tests | `internal/apicoll`              | `internal/helper/client` tests           |

## Why "no" is the whole point

A helper install is content-addressed and immutable, two generations are resident at
once, and a generation lingers for exactly as long as it holds a session — months, in
the case the whole level exists for. So there is no release at which every peer has been
upgraded, and a shape that was wrong when a generation shipped stays wrong for the life
of the sessions it holds.

Three consequences, and they are decisions rather than observations:

- **A generation whose ABI assumes one unnamed client can never later serve two
  observers correctly.** Hence a subscriber on attach, on data and on write, and an
  independent 64-bit cursor per subscriber — reserved and barely used today.
- **An opaque identifier can never later become authorization.** The trust boundary is
  the Unix account: any nocx under that account may connect, and no session capability is
  reserved. If independent same-UID servers must ever be isolated from one another, the
  capability is owed **before** the next generation ships.
- **A frame type nobody allocated is garbage.** The decoder resyncs past an unknown type
  byte one byte at a time — through a live PTY stream, in the case that matters — so the
  data-plane type byte is allocated now and both ends already recognise and drop it.

## Not generated into TypeScript, and deliberately

`frontend/scripts/gen-contracts.mjs` reads `contracts/` and does not descend here. Nothing
in the renderer speaks to a helper: the renderer's socket is the coordinator's, and what it
knows about a remote session it knows through the control plane one directory up. Putting
these shapes into `frontend/src/generated/` would hand the renderer a type for a wire it
must never reach.

A generated type exists to stop a hand-written one drifting from the wire. Here there is
no second hand-written type to drift: both ends of this socket are the same Go package,
`internal/helper/proto`. The drift these schemas catch is the other one — the shape
changing at all — which is what freezing means.

**Do not move these files up one directory.** `gen-openrpc.mjs` turns every top-level
`*.params.schema.json` into an OpenRPC **method**, and
`internal/transport/TestOpenRPCManifestMatchesRegisteredMethods` then demands a
JSON-RPC registration for it that does not and must not exist.

## `identities.schema.json`

It declares `$defs` and nothing else; every other schema here `$ref`s into it, so one
concept has one declaration (AD-8). The three identities are separate there for the same
reason they are separate Go types: before the correction of 2026-08-31 "the session"
meant the coordinator-owned PTY channel, so the channel's death was the session's death,
and conflating the two is exactly what makes a replacing coordinator delete live work.

## What is deliberately not here

- **The data plane.** AD-1 governs this wire as it governs the WebSocket: raw PTY bytes
  are never wrapped in JSON, JSON-RPC or base64, so the data frame has no JSON shape to
  pin. Its layout is frozen by literal-byte golden vectors in
  `internal/helper/proto/abi_test.go` instead.
- **The inventory and spawn shapes**, which land with the service that can answer them
  (`nocx-k6p18.3`). The reservation they owe is recorded in `internal/helper/proto/abi.go`
  so it is not lost between beads: an opaque `WorkspaceID`, never a display name.
- **Runtime validation in production.** Both ends of this socket are ours, exactly as one
  directory up.
