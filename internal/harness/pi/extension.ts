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
  if (session.command?.length) {
    pi.registerMcpServer('nocx', {
      command: session.command[0],
      args: session.command.slice(1),
      env: session.env,
      exposure: 'direct',
    })
  }
  pi.on('before_agent_start', (event) => ({
    systemPrompt: event.systemPrompt + '\n\n' + session.systemPrompt,
  }))
}
