/**
 * A fake OpenAI-compatible /chat/completions SSE server for the e2e suite
 * (nocx-x8s2.2's proof).
 *
 * WHY A SECOND FAKE, WHEN internal/assistant/probe_test.go HAS ONE (AD-8).
 * The Go fake is an in-package httptest server living and dying inside one
 * `go test` process; nothing outside that process can reach it. The e2e
 * needs a server the DEVHARNESS process dials — devharness is spawned by
 * Playwright and must reach the model endpoint over 127.0.0.1, so the server
 * has to be owned by the spec process itself. A Node http server in the
 * Playwright worker is that owner: same host network namespace as the
 * devharness child it spawns, scripted by the very test driving the browser.
 *
 * The wire shape is kept IDENTICAL to the Go fake's (probe_test.go:
 * streamOK/chunkJSON): each content delta is one
 * `data: {chat.completion.chunk JSON}\n\n` frame carrying
 * delta.role/content and finish_reason ("" on content chunks, "stop" on the
 * last), then a `data: [DONE]\n\n` terminator. The eino adapter is the
 * single consumer of both, so a drift here would be a second protocol
 * variant with a delay fuse — the whole reason the shapes must match.
 *
 * AND THE MODEL CAN PROPOSE A TOOL. `toolCalls` scripts one
 * `delta.tool_calls` frame finishing the response with `tool_calls` — the
 * shape matched against the two Go fakes that already drive this client
 * (internal/assistant/policy_test.go streamToolCalls,
 * internal/transport/ws_readscreen_test.go streamToolCallChunk); see
 * toolCallFrame below for what each member is for. Without it the fake
 * could only make the model SPEAK, so the assistant's approval prompt had
 * no end-to-end coverage at all (nocx-aospw).
 *
 * SCRIPTED, NOT RANDOM: the test decides the answer, per request, in order.
 * `holdAfter` lets a response write its first N content chunks and then WAIT
 * for an explicit release() — the spec can hold one answer open while a
 * second ask is made, and can observe PARTIAL text in the flow (streaming)
 * before the answer closes. Nothing here sleeps: the spec polls request
 * state and releases explicitly, so the hold is a fact about the server,
 * never a timing bet.
 */
import { createServer, type Server, type IncomingMessage, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

export interface FakeRequest {
  readonly id: number
  /** The raw request body the backend sent (the OpenAI chat-completions
   *  request: model, messages, stream). */
  readonly body: string
  /** The Authorization header, verbatim ('' when the client sent none). */
  readonly authorization: string
  /** The request path, e.g. /v1/chat/completions. */
  readonly path: string
  /** Every request header the backend sent, verbatim — the endpoints
   *  surface's custom headers (nocx-lyyk) are asserted here. */
  readonly headers: Record<string, string | string[] | undefined>
  /** received — connection open, no content chunk written yet
   *  streaming — holdAfter content chunks flushed, response held or writing
   *  done — the whole stream, [DONE] included, was written out */
  state: 'received' | 'streaming' | 'done'
  /** Content chunks flushed so far. */
  chunksSent: number
}

/** One tool call the scripted model PROPOSES (design §6): the tool by the
 *  name the registry declares (internal/agenttools/registry.go — files.read,
 *  readScreen, run, git.status) and the arguments it is called with.
 *
 *  `arguments` is an object here and a JSON STRING on the wire, which is
 *  what OpenAI's shape carries and what the client parses back into the
 *  object the tool's params schema (contracts/tools/<name>.schema.json) is
 *  applied to. The spec writes the object; the serialisation is this
 *  module's business.
 *
 *  A resource argument must name the resource EXACTLY as the backend spells
 *  it — readScreen's `sessionId` is compared for identity against the run's
 *  grant scope (internal/assistant/policy.go inScope), so an invented id is
 *  REFUSED before it can ask. A spec that wants the ask must pass the
 *  session id the product itself used. */
export interface ScriptedToolCall {
  /** The tool name the model calls, e.g. 'readScreen'. */
  name: string
  /** The call's arguments, serialised to the wire's JSON string. */
  arguments: Record<string, unknown>
  /** The call id the approval binding names (default 'call_<n>'). */
  id?: string
}

export interface StreamScript {
  /** One string per content delta. The concatenation is the answer. */
  chunks: string[]
  /** Hold the response open after this many content chunks have been
   *  flushed (default: write all chunks without holding). A held response
   *  stays `streaming` until release(id) lets it finish. The hold is among
   *  CONTENT chunks only. */
  holdAfter?: number
  /** The model id echoed in the chunk frames (default 'e2e-model'). */
  model?: string
  /** Tool calls this response PROPOSES, written as one frame after the
   *  content chunks and finishing the response with `tool_calls` instead of
   *  `stop`. Absent or empty: a content-only response, exactly as before. */
  toolCalls?: ScriptedToolCall[]
}

const CHUNK_ID = 'chatcmpl-e2e'

/** One OpenAI chat.completion.chunk frame, byte-identical in shape to
 *  internal/assistant/probe_test.go's chunkJSON. */
function chunkFrame(model: string, content: string, finish: string): string {
  return JSON.stringify({
    id: CHUNK_ID,
    object: 'chat.completion.chunk',
    created: 0,
    model,
    choices: [{ index: 0, delta: { role: 'assistant', content }, finish_reason: finish }],
  })
}

/** One chat.completion.chunk frame carrying TOOL CALLS — the whole proposal
 *  in a single frame, finishing the response with `tool_calls`.
 *
 *  MATCHED AGAINST, not invented from the OpenAI docs: this is field for
 *  field `internal/assistant/policy_test.go`'s streamToolCalls and
 *  `internal/transport/ws_readscreen_test.go`'s streamToolCallChunk — the
 *  two Go fakes whose frames our own client already parses. Every member is
 *  there for a reason on the client side: eino's openai adapter decodes the
 *  delta into `[]openai.ToolCall` and maps it to `schema.ToolCall`
 *  (eino-ext/libs/acl/openai chat_model.go, toMessageToolCalls), so `id`
 *  becomes the call id the approval binding names, `type` must be
 *  "function", and `function.arguments` is a STRING the middleware
 *  validates against the tool's params schema. `index` is deliberately
 *  ABSENT: eino concatenates a stream's tool calls by index and a call
 *  delivered whole in one frame needs no merging (eino schema/message.go,
 *  concatToolCalls appends an index-less chunk as it stands) — which is
 *  exactly what both Go fakes do.
 *
 *  A REAL provider may split one call's arguments across several frames.
 *  This fake does not, because nothing in this suite tests eino's
 *  concatenation; if a spec ever needs that, it is a second frame shape and
 *  it gets its own matching exercise. */
function toolCallFrame(model: string, calls: readonly ScriptedToolCall[]): string {
  return JSON.stringify({
    id: CHUNK_ID,
    object: 'chat.completion.chunk',
    created: 0,
    model,
    choices: [
      {
        index: 0,
        delta: {
          role: 'assistant',
          tool_calls: calls.map((c, i) => ({
            id: c.id ?? `call_${i + 1}`,
            type: 'function',
            function: { name: c.name, arguments: JSON.stringify(c.arguments) },
          })),
        },
        finish_reason: 'tool_calls',
      },
    ],
  })
}

export class FakeOpenAI {
  private server: Server | null = null
  private baseUrl_ = ''
  private scripts: StreamScript[] = []
  private readonly requests_: FakeRequest[] = []
  private readonly releases = new Map<number, () => void>()
  private nextId = 1

  /** Bind 127.0.0.1:0 (OS-assigned port). Resolves with the base URL the
   *  endpoint form should be given: http://127.0.0.1:<port>/v1. */
  start(): Promise<string> {
    const { promise, resolve, reject } = Promise.withResolvers<string>()
    const server = createServer((req, res) => this.handle(req, res))
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      this.server = server
      const { port } = server.address() as AddressInfo
      this.baseUrl_ = `http://127.0.0.1:${port}/v1`
      resolve(this.baseUrl_)
    })
    return promise
  }

  /** The base URL the endpoint form should be given
   *  (http://127.0.0.1:<port>/v1). Valid after start(). */
  baseUrl(): string {
    if (!this.baseUrl_) throw new Error('fake-openai: not started')
    return this.baseUrl_
  }

  /** Close the listener. Idempotent. Held responses are released first so
   *  their sockets close and the listener can finish; a bounded fallback
   *  guarantees teardown never hangs the suite. */
  stop(): Promise<void> {
    const s = this.server
    this.server = null
    if (!s) return Promise.resolve()
    this.releaseAll()
    const { promise, resolve } = Promise.withResolvers<void>()
    const timer = setTimeout(() => {
      try {
        s.closeAllConnections()
      } catch {
        /* already closed */
      }
      resolve()
    }, 500)
    s.close(() => {
      clearTimeout(timer)
      resolve()
    })
    return promise
  }

  /** Script the NEXT request. Scripts are consumed in request order; a
   *  request with no script left answers with a single 'ok' chunk. */
  setScript(script: StreamScript): void {
    this.scripts.push(script)
  }

  /** Let a held request finish its remaining chunks and [DONE]. No-op for a
   *  request that is not held or already done. */
  release(id: number): void {
    const fn = this.releases.get(id)
    this.releases.delete(id)
    fn?.()
  }

  releaseAll(): void {
    for (const fn of this.releases.values()) fn()
    this.releases.clear()
  }

  /** A snapshot of every request received so far, oldest first. */
  requests(): readonly FakeRequest[] {
    return this.requests_.map((r) => ({ ...r }))
  }

  /** Poll until at least n requests have been received. Resolves with the
   *  requests; throws when the deadline passes. */
  async waitForRequests(n: number, timeoutMs = 15_000): Promise<readonly FakeRequest[]> {
    await this.poll(
      () => (this.requests_.length >= n ? this.requests() : undefined),
      `${n} request(s)`,
      timeoutMs,
    )
    return this.requests()
  }

  /** Poll until request id has reached the given state. */
  async waitForState(
    id: number,
    state: FakeRequest['state'],
    timeoutMs = 15_000,
  ): Promise<FakeRequest> {
    return this.poll(
      () => {
        const r = this.requests_.find((x) => x.id === id)
        return r && r.state === state ? { ...r } : undefined
      },
      `request ${id} → ${state}`,
      timeoutMs,
    )
  }

  private async poll<T>(probe: () => T | undefined, what: string, timeoutMs: number): Promise<T> {
    const deadline = Date.now() + timeoutMs
    for (;;) {
      const value = probe()
      if (value !== undefined) return value
      if (Date.now() > deadline) throw new Error(`fake-openai: timed out waiting for ${what}`)
      await new Promise((r) => setTimeout(r, 50))
    }
  }

  private handle(req: IncomingMessage, res: ServerResponse): void {
    const id = this.nextId++
    let body = ''
    req.on('data', (chunk: Buffer) => {
      body += chunk.toString('utf8')
    })
    req.on('end', () => {
      const record: FakeRequest = {
        id,
        body,
        authorization: req.headers.authorization ?? '',
        path: req.url ?? '',
        headers: { ...req.headers },
        state: 'received',
        chunksSent: 0,
      }
      this.requests_.push(record)
      // The connection check (a GET to /models, nocx-q27y) is a real
      // endpoint request too: answer it with a model list so the check has
      // something to find, and record its headers the same way.
      if (req.method === 'GET' && (req.url ?? '').endsWith('/models')) {
        record.state = 'done'
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ object: 'list', data: [{ id: 'e2e-model' }] }))
        return
      }
      const script = this.scripts.shift() ?? { chunks: ['ok'] }
      this.stream(record, res, script)
    })
    req.on('error', () => res.destroy())
  }

  private stream(record: FakeRequest, res: ServerResponse, script: StreamScript): void {
    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    })
    res.flushHeaders()

    const chunks = script.chunks
    const calls = script.toolCalls ?? []
    const model = script.model ?? 'e2e-model'
    const holdAfter = script.holdAfter ?? chunks.length
    let i = 0

    const writeNext = (): void => {
      while (i < chunks.length) {
        // A response that goes on to PROPOSE finishes with `tool_calls` on
        // the proposal frame, so no content chunk of it may carry `stop`:
        // the finish reason belongs to the response, and two of them in one
        // response is a shape no provider sends.
        const isLast = i === chunks.length - 1 && calls.length === 0
        res.write(`data: ${chunkFrame(model, chunks[i], isLast ? 'stop' : '')}\n\n`)
        record.chunksSent = ++i
        record.state = 'streaming'
        // `i < chunks.length` rather than `!isLast`: the same condition for
        // a content-only script (holdAfter defaults to chunks.length, so the
        // end of the loop never holds), and the honest one now that `isLast`
        // also depends on whether a proposal follows.
        if (i === holdAfter && i < chunks.length) {
          // Held: the stream is genuinely open with partial content. The
          // test observes `streaming` + chunksSent and calls release(id)
          // when it is ready for the rest.
          this.releases.set(record.id, writeNext)
          return
        }
      }
      if (calls.length > 0) {
        res.write(`data: ${toolCallFrame(model, calls)}\n\n`)
        record.state = 'streaming'
      }
      res.write('data: [DONE]\n\n')
      record.state = 'done'
      res.end()
      this.releases.delete(record.id)
    }

    writeNext()
  }
}
