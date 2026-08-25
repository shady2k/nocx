# `contracts/files/` — the PERSISTED formats

The schemas one directory up describe **JSON-RPC results**: what crosses the socket
between our backend and our renderer, both ends ours, both ends this build.

These describe something else — **files on disk**, inside a folder the user owns, shares
through git and may bring to this build from a newer one. They are two boundaries and
they are not one schema doing both jobs:

|                               | `contracts/*.schema.json` | `contracts/files/*.schema.json`     |
| ----------------------------- | ------------------------- | ----------------------------------- |
| Who wrote the document        | our backend, this build   | anybody, any build, any release ago |
| Generated renderer types      | yes (`npm run contracts`) | **no** — nothing renders these      |
| Version protocol              | none, and none is needed  | `storage.Module`, `schemaVersion`   |
| A document from a newer build | cannot happen             | **refused before decoding**         |

The version protocol is the reason the split exists. A generated renderer DTO gives no
migrations, no refusal of a document from a newer build, and no answer at all for a file
that has been sitting on disk across three releases. `internal/apicoll`'s
`storage.Module` provides all three, and these schemas declare the shapes that protocol
governs.

**Not generated into TypeScript, and deliberately.** `frontend/scripts/gen-contracts.mjs`
reads `contracts/` and does not descend into this directory. Nothing in the renderer
reads a collection file; it reads `api.request.read`'s result, which is the RPC contract
one directory up.

**Strict, for the same reason and a stronger one.** `additionalProperties: false` plus an
explicit `required` on every object — a collection folder can arrive from a pull request,
so a field nobody declared is hostile input rather than a harmless extra.
