import { readFileSync, unlinkSync } from 'node:fs'

export default function (pi) {
  const configPath = process.env.NOCX_PI_TURN_FILE
  let turn
  try {
    turn = JSON.parse(readFileSync(configPath, 'utf8'))
  } finally {
    unlinkSync(configPath)
    delete process.env.NOCX_PI_TURN_FILE
  }
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
