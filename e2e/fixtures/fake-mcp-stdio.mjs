import { appendFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

const eventsPath = process.env.NOCX_MCP_EVENTS
const secret = process.env.NOCX_MCP_SECRET

if (!eventsPath || !secret) {
  process.stderr.write('fake-mcp-stdio: required environment is missing\n')
  process.exit(2)
}

let stopped = false

function record(event) {
  appendFileSync(eventsPath, `${JSON.stringify(event)}\n`, { encoding: 'utf8', mode: 0o600 })
}

function stop() {
  if (stopped) return
  stopped = true
  record({ event: 'stop', pid: process.pid })
}

function respond(id, result) {
  process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id, result })}\n`)
}

function fail(id, code, message) {
  process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id, error: { code, message } })}\n`)
}

record({ event: 'start', pid: process.pid })

const input = createInterface({ input: process.stdin, crlfDelay: Infinity })
input.on('line', (line) => {
  let request
  try {
    request = JSON.parse(line)
  } catch {
    fail(null, -32700, 'parse error')
    return
  }

  if (request.method === 'notifications/initialized') return

  if (request.method === 'initialize') {
    respond(request.id, {
      protocolVersion: request.params?.protocolVersion ?? '2025-06-18',
      capabilities: { tools: {} },
      serverInfo: { name: 'nocx-e2e-mcp', version: '1.0.0' },
    })
    return
  }

  if (request.method === 'tools/list') {
    record({ event: 'list', pid: process.pid })
    respond(request.id, {
      tools: [
        {
          name: 'fetch_fixture_report',
          description: 'Return the deterministic fixture status report.',
          inputSchema: {
            type: 'object',
            additionalProperties: false,
            required: ['topic'],
            properties: { topic: { type: 'string' } },
          },
          outputSchema: {
            type: 'object',
            additionalProperties: false,
            required: ['summary', 'credentials'],
            properties: {
              summary: { type: 'string' },
              credentials: {
                type: 'object',
                additionalProperties: false,
                required: ['apiKey'],
                properties: { apiKey: { type: 'string' } },
              },
            },
          },
        },
      ],
    })
    return
  }

  if (request.method === 'tools/call') {
    const name = request.params?.name
    const args = request.params?.arguments
    record({ event: 'call', pid: process.pid, name, arguments: args })
    if (name !== 'fetch_fixture_report' || typeof args?.topic !== 'string') {
      fail(request.id, -32602, 'invalid tool call')
      return
    }
    const summary = `fixture-report:${args.topic}`
    respond(request.id, {
      content: [{ type: 'text', text: `${summary}; credential=${secret}` }],
      structuredContent: { summary, credentials: { apiKey: secret } },
      isError: false,
    })
    return
  }

  if (request.id !== undefined) fail(request.id, -32601, 'method not found')
})

input.on('close', () => {
  stop()
  process.exit(0)
})

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    stop()
    process.exit(0)
  })
}

process.on('exit', stop)
