import { readFileSync } from 'node:fs'

export default function (pi) {
  const turn = JSON.parse(readFileSync(process.env.NOCX_PI_TURN_FILE, 'utf8'))
  if (turn.command?.length) {
    pi.registerMcpServer('nocx', {
      command: turn.command[0],
      args: turn.command.slice(1),
      env: turn.env,
      exposure: 'direct',
    })
  }
  pi.on('before_agent_start', (event) => ({
    systemPrompt: event.systemPrompt + '\n\n' + turn.systemPrompt,
  }))
}
