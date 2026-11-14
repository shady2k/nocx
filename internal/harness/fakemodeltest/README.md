# Scripted OpenAI model fixture

`fakemodeltest` (imported below as `fakemodel`) is an HTTP handler, not a fake pi RPC process. It can be mounted
in a Go test or an e2e stand without a separate binary:

```go
fake := fakemodel.New(
    fakemodel.Response{Text: []string{"Hello", " world"}},
    fakemodel.Response{ToolCalls: []fakemodel.ToolCall{{
        ID: "call-1", Name: "run_command", Arguments: `{"command":"pwd"}`,
    }}},
)
server := httptest.NewServer(fake)
defer server.Close()
// Set the OpenAI-compatible provider base URL to server.URL + "/v1".
```

Import `github.com/shady2k/nocx/internal/harness/fakemodeltest`. A non-test stand
can mount the same handler with `http.Server`. The endpoint is
`POST /v1/chat/completions` and requires `"stream": true`.

Each valid streaming request consumes one response in arrival order. Thinking
chunks (`Thinking`) use `reasoning_content`; text chunks (`Text`) use `content`.
Tool calls follow the text and include an index, ID, name, and complete JSON
arguments string. The stream ends with a finish reason and `[DONE]`.
Exhausting the script returns HTTP 500, so missing fixture steps cannot silently
succeed. Invalid routes, methods, JSON, or non-streaming requests do not consume
responses.

`Requests()` returns independent snapshots containing headers, original body,
model, stream flag, and raw JSON messages/tools. Use `WaitRequests(ctx, n)` to
wait for request capture without sleeps. Capture happens before a response's
optional `Block` channel is awaited. Close that channel to release the response;
request cancellation also unblocks the handler. A nonzero `StatusCode` sends an
OpenAI-shaped JSON error with the scripted `Error` message instead of SSE.

Run the fixture tests from the repository root:

```sh
GOTOOLCHAIN=go1.26.5 GOSUMDB=sum.golang.org go test -p 1 -tags gtk3 ./internal/harness/fakemodeltest
```
