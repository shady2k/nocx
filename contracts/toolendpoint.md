# Tool endpoint contracts

The local `tool.sock` endpoint exposes five JSON-RPC methods. Their wire
contracts are the existing unified tool documents under [`tools/`](tools/):

| Method          | Contract document                                              |
| --------------- | -------------------------------------------------------------- |
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
