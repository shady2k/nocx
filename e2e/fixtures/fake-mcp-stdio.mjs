import { randomUUID } from 'node:crypto'
import { appendFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

// No shell, network listener, or inherited test configuration: Settings supplies
// these two explicit environment bindings to the real managed child.
const eventsPath = process.env.NOCX_MCP_EVENTS
const secret = process.env.NOCX_MCP_SECRET
if (!eventsPath || !secret) {
  process.stderr.write('fake-mcp-stdio: required environment is missing\n')
  process.exit(2)
}

const reportTool = {
  name: 'fetch_fixture_report',
  description: 'Fetch the fixture approval proof report.',
  inputSchema: {
    type: 'object',
    additionalProperties: false,
    required: ['topic'],
    properties: { topic: { type: 'string' } },
  },
  outputSchema: {
    type: 'object',
    additionalProperties: false,
    required: ['summary', 'nonce', 'credentials'],
    properties: {
      summary: { type: 'string' },
      nonce: { type: 'string' },
      credentials: {
        type: 'object',
        additionalProperties: false,
        required: ['apiKey'],
        properties: { apiKey: { type: 'string' } },
      },
    },
  },
}
const otherTool = {
  name: 'unused_fixture_tool',
  description: 'A second page of discovery; this tool must remain disabled.',
  inputSchema: { type: 'object', additionalProperties: false, properties: {} },
}

function record(event) {
  appendFileSync(eventsPath, `${JSON.stringify({ ...event, pid: process.pid })}\n`, {
    encoding: 'utf8',
    mode: 0o600,
  })
}
function respond(id, result) {
  process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id, result })}\n`)
}
function fail(id, code, message) {
  process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id, error: { code, message } })}\n`)
}
let stopped = false
function stop() {
  if (stopped) return
  stopped = true
  record({ event: 'stop' })
}

record({ event: 'start' })
const input = createInterface({ input: process.stdin, crlfDelay: Infinity })
input.on('line', (line) => {
  let request
  try {
    request = JSON.parse(line)
  } catch {
    fail(null, -32700, 'parse error')
    return
  }
  if (request.id === undefined) return
  switch (request.method) {
    case 'initialize':
      respond(request.id, {
        protocolVersion: request.params?.protocolVersion ?? '2025-06-18',
        capabilities: { tools: {} },
        serverInfo: { name: 'nocx-e2e-mcp', version: '1.0.0' },
      })
      break
    case 'ping':
      respond(request.id, {})
      break
    case 'tools/list': {
      const cursor = request.params?.cursor ?? null
      record({ event: 'list', cursor })
      if (cursor === null) {
        respond(request.id, { tools: [reportTool], nextCursor: 'remaining-tools' })
      } else if (cursor === 'remaining-tools') {
        respond(request.id, { tools: [otherTool] })
      } else {
        fail(request.id, -32602, 'invalid cursor')
      }
      break
    }
    case 'tools/call': {
      const name = request.params?.name
      const args = request.params?.arguments
      // Mint ONLY when called. Neither the provider script nor the request can
      // know this nonce; the event is independent evidence of the result origin.
      const nonce = randomUUID()
      record({ event: 'call', name, arguments: args, nonce })
      if (
        name !== reportTool.name ||
        typeof args?.topic !== 'string' ||
        Object.keys(args).length !== 1
      ) {
        fail(request.id, -32602, 'invalid tool call')
        break
      }
      const summary = `fixture-report:${args.topic}:${nonce}`
      respond(request.id, {
        content: [{ type: 'text', text: `${summary}; credential=${secret}` }],
        structuredContent: { summary, nonce, credentials: { apiKey: secret } },
        isError: false,
      })
      break
    }
    default:
      fail(request.id, -32601, 'method not found')
  }
})
input.on('close', () => process.exit(0))
for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => process.exit(0))
}
process.on('exit', stop)
