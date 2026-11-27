import { readFileSync, unlinkSync } from 'node:fs'

export default function (pi) {
  const configPath = process.env.NOCX_PI_SESSION_FILE
  let session
  try {
    session = JSON.parse(readFileSync(configPath, 'utf8'))
  } finally {
    unlinkSync(configPath)
    delete process.env.NOCX_PI_SESSION_FILE
  }
  const callKeys = new Map()
  let startSequence = 0
  if (session.command?.length) {
    pi.registerMcpServer('nocx', {
      command: session.command[0],
      args: session.command.slice(1),
      env: session.env,
      exposure: 'direct',
    })
    pi.on('tool_execution_start', (event) => {
      // Count every start to match the ordered RPC stream, even when Pi
      // rejects a call locally or the model reuses an ID in a later response.
      startSequence++
      callKeys.set(event.toolCallId, `${startSequence}:${event.toolCallId}`)
    })
    pi.on('tool_call', (event) => {
      if (event.toolName.startsWith('mcp__nocx__')) {
        // Pi passes the validated, per-call copy to MCP but drops the call ID.
        // The assistant bridge unwraps this field before forwarding arguments.
        event.input.__nocx_tool_call_key = callKeys.get(event.toolCallId)
      }
    })
  }
  pi.on('before_agent_start', (event) => {
    startSequence = 0
    callKeys.clear()
    return {
      systemPrompt: event.systemPrompt + '\n\n' + readFileSync(session.promptFile, 'utf8'),
    }
  })
}
