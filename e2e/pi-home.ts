/** Configure real pi inside VaultBackend's disposable home before it starts. */
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import type { ScriptedToolCall } from './fake-openai'

export function configurePiHome(root: string, baseUrl: string): NodeJS.ProcessEnv {
  const agentDir = join(root, 'home', '.pi', 'agent')
  mkdirSync(agentDir, { recursive: true })
  writeFileSync(
    join(agentDir, 'models.json'),
    JSON.stringify({
      providers: {
        'nocx-e2e': {
          baseUrl,
          api: 'openai-completions',
          apiKey: 'disposable-e2e-key',
          models: [
            {
              id: 'e2e-model',
              name: 'Disposable E2E model',
              reasoning: false,
              contextWindow: 128000,
              maxTokens: 4096,
              cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
            },
          ],
        },
      },
    }),
    { mode: 0o600 },
  )
  return { PI_CODING_AGENT_DIR: agentDir }
}

/** Script the name real pi advertised for nocx's session.read MCP tool. */
export function readScreenTool(body: string): ScriptedToolCall[] {
  const request = JSON.parse(body) as {
    tools?: { function: { name: string } }[]
  }
  const tool = request.tools?.find((entry) => entry.function.name.endsWith('__session_read'))
  if (!tool) throw new Error('real pi did not offer nocx session.read')
  return [{ name: tool.function.name, arguments: {} }]
}
