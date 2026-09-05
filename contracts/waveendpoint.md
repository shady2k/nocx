# Wave endpoint contracts

The local `wave.sock` endpoint exposes five JSON-RPC methods. Their wire
contracts are the existing unified tool documents under [`tools/`](tools/):

| Method          | Contract document                                              |
| --------------- | -------------------------------------------------------------- |
| `wave.spawn`    | [`wave.spawn.schema.json`](tools/wave.spawn.schema.json)       |
| `wave.say`      | [`wave.say.schema.json`](tools/wave.say.schema.json)           |
| `wave.wait`     | [`wave.wait.schema.json`](tools/wave.wait.schema.json)         |
| `wave.holdings` | [`wave.holdings.schema.json`](tools/wave.holdings.schema.json) |
| `wave.close`    | [`wave.close.schema.json`](tools/wave.close.schema.json)       |

Each document is the single declaration for that method's params and result:
the top-level schema describes params, and `$defs.result` describes the JSON
result. The endpoint's composition path embeds the same files through
`contracts/tools.Schemas`, assembles them with `agenttools.Assemble`, and sends
requests through `assistant.NewWaveDispatcher`. That dispatcher validates raw
params before execution and validates the executor's result against the same
`$defs.result` declaration.

`internal/waveendpoint/contract_test.go` keeps the second half of the contract
honest. It sends every method through a real Unix socket, reads the response's
actual `result` bytes, extracts `$defs.result` from the referenced document, and
validates those bytes. It also sends one malformed params payload per method to
prove the referenced params schema is enforced. No schema body is copied into
the endpoint or re-declared for this socket.
