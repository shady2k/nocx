// Generates the OpenRPC surface manifest from the contract directory.
// The transport registration test is the authority for the method set; this
// script keeps the manifest's file references deterministic and never copies
// schema bodies into the manifest.

import { readdir, readFile, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = resolve(fileURLToPath(new URL('.', import.meta.url)))
const contractsDir = resolve(here, '../../contracts')
const manifestPath = resolve(contractsDir, 'openrpc.json')
const baseURL = 'https://nocx.local/contracts/'

const resultAliases = new Map([
  ['connections.test', 'connections.probe.schema.json'],
  ['uistate.get', 'uistate.schema.json'],
  ['uistate.set', 'uistate.schema.json'],
  ['agent.calibration.answer', 'agent.calibration.schema.json'],
])

function ref(file) {
  return { $ref: `${baseURL}${file}` }
}

function methodEntry(name, resultFiles) {
  const resultFile = resultFiles.has(`${name}.schema.json`)
    ? `${name}.schema.json`
    : resultAliases.get(name)
  const entry = {
    name,
    params: [{ name: 'params', required: false, schema: ref(`${name}.params.schema.json`) }],
    errors: [
      {
        code: -32601,
        message: 'Method not found',
        'x-nocx-errorSchema': ref('rpc.error.schema.json'),
      },
      {
        code: -32602,
        message: 'Invalid params',
        'x-nocx-errorSchema': ref('rpc.error.schema.json'),
      },
      {
        code: -32603,
        message: 'Internal error',
        'x-nocx-errorSchema': ref('rpc.error.schema.json'),
      },
      {
        code: -32004,
        message: 'Control plane busy',
        'x-nocx-errorSchema': ref('rpc.error.schema.json'),
      },
    ],
    'x-nocx-agent-disposition': 'operation-owned',
  }
  if (resultFile) {
    entry.result = { name: `${name}Result`, schema: ref(resultFile) }
  } else {
    // A method without a result schema is explicit in the manifest rather
    // than receiving an invented response shape. The transport registration
    // gate separately enforces that every registered method has this entry.
    entry['x-nocx-noResultSchema'] = true
  }
  return entry
}

async function build() {
  const names = (await readdir(contractsDir)).sort()
  const params = names.filter((name) => name.endsWith('.params.schema.json'))
  const results = names.filter(
    (name) =>
      name.endsWith('.schema.json') &&
      !name.endsWith('.params.schema.json') &&
      name !== 'rpc.error.schema.json',
  )
  const resultFiles = new Set(results)
  return {
    openrpc: '1.3.2',
    info: {
      title: 'nocx control plane',
      version: '0.1.0',
      description:
        'The JSON-RPC control-plane contract. Binary session data is outside this manifest.',
    },
    methods: params.map((file) =>
      methodEntry(file.replace(/\.params\.schema\.json$/, ''), resultFiles),
    ),
    'x-nocx-schemaRefs': results.map(ref),
  }
}

const generated = `${JSON.stringify(await build(), null, 2)}\n`
if (process.argv.includes('--check')) {
  const current = await readFile(manifestPath, 'utf8').catch(() => null)
  if (current !== generated) {
    console.error('stale: contracts/openrpc.json does not match contracts/*.schema.json')
    process.exit(1)
  }
} else {
  await writeFile(manifestPath, generated)
  console.log('contracts/openrpc.json')
}
