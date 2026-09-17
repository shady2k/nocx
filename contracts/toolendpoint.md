# Tool endpoint contracts

The local `tool.sock` endpoint exposes five JSON-RPC methods. Their wire
contracts are the existing unified tool documents under [`tools/`](tools/):

| Method             | Contract document                                                    |
| ------------------ | -------------------------------------------------------------------- |
| `workers.spawn`    | [`workers.spawn.schema.json`](tools/workers.spawn.schema.json)       |
| `workers.say`      | [`workers.say.schema.json`](tools/workers.say.schema.json)           |
| `workers.wait`     | [`workers.wait.schema.json`](tools/workers.wait.schema.json)         |
| `workers.holdings` | [`workers.holdings.schema.json`](tools/workers.holdings.schema.json) |
| `workers.close`    | [`workers.close.schema.json`](tools/workers.close.schema.json)       |

Each document is the single declaration for that method's params and result:
the top-level schema describes params, and `$defs.result` describes the JSON
result. The endpoint's composition path embeds the same files through
`contracts/tools.Schemas`, assembles them with `agenttools.Assemble`, and sends
requests through `assistant.NewToolDispatcher`. That dispatcher validates raw
params before execution and validates the executor's result against the same
`$defs.result` declaration.

`internal/toolendpoint/contract_test.go` keeps the second half of the contract
honest. It sends every method through a real Unix socket, reads the response's
actual `result` bytes, extracts `$defs.result` from the referenced document, and
validates those bytes. It also sends one malformed params payload per method to
prove the referenced params schema is enforced. No schema body is copied into
the endpoint or re-declared for this socket.

## `tools.catalogue` — what this caller may call

The five method documents above say what each call takes and returns. They do
not say **which of them this caller is allowed to make**, and that is not a
constant: `Registry.ForGrant` narrows the offered set to the admitted grant, and
the authorizer mints deliberately disjoint grants for a coordinator and for a
worker (`internal/app/waveauth.go`). A caller that assumed the five would be
telling its model about calls that do not exist for it.

`tools.catalogue` is that answer, and it is the endpoint's because the endpoint
is where the grant is. **No caller may derive its own eligibility.**

Params: none. The grant is the admitted invocation's, never a parameter.

Result:

```json
{
  "tools": [
    {
      "name": "workers.spawn",
      "summary": "start one worker in a terminal pane of its own and hand it a task",
      "params": { "type": "object" },
      "result": { "type": "object" }
    }
  ]
}
```

`params` is `Tool.ParamsSchema` and `result` is `Tool.ResultSchema` — the two
halves the contract document already declares, lifted apart by
`agenttools.Assemble` so that the return shape never rides inside the parameters
(`nocx-ydu92`). `summary` is the declaration's description.

**The names here are ours, not any client's.** A caller that speaks a protocol
with its own vocabulary — MCP calls these `description`, `inputSchema` and
`outputSchema` — translates at its own boundary. That translation is what an
adapter is for, and letting a client's field names into this document is how a
domain acquires a vendor.

An empty `tools` array is a valid answer and means the grant reaches nothing. It
is not an error, and a caller must render it as "no tools", never as a failure to
list them.
