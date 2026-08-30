// Dev-only Vite config for looking at the real UI in an ordinary browser —
// `make dev-web`, which starts the backend and passes the two values below in.
// Never used by `wails dev`, `wails build` or the e2e suite.
//
// It shims the Wails binding a plain browser cannot get: ResolveBackend.
// Everything else is inherited from vite.config.ts, so what you look at is
// the shipped build config.
import { defineConfig, mergeConfig, type Plugin } from 'vite'
import base from './vite.config'

// The fallbacks match scripts/dev-web.sh, which is what normally sets all three.
// They matter anyway: run this config by hand and the old 5173 fallback put the
// dev stand straight onto the port `npm run dev` and the headless e2e suite both
// serve from, which is the collision the script goes out of its way to avoid.
const port = Number(process.env.NOCX_WS_PORT ?? '9880')
const token = process.env.NOCX_WS_TOKEN ?? ''
const webPort = Number(process.env.NOCX_WEB_PORT ?? '5180')

const wailsShim: Plugin = {
  name: 'nocx-dev-wails-shim',
  transformIndexHtml() {
    return [
      {
        tag: 'script',
        injectTo: 'head-prepend',
        children: `
          window.go = {
            main: {
              WailsApp: {
                ResolveBackend: () =>
                  Promise.resolve({
                    ok: true,
                    host: '127.0.0.1',
                    port: ${port},
                    token: ${JSON.stringify(token)},
                    kind: '',
                    message: '',
                    remedy: '',
                  }),
                CheckForUpdate: () => Promise.resolve(null),
                ReportHealthy: () => Promise.resolve(),
                ApplyUpdate: () => Promise.resolve(),
              },
            },
          }
        `,
      },
    ]
  },
}

export default defineConfig(
  // strictPort on purpose: a silent bump to 5174 leaves the tunnel pointing at
  // a port nothing serves, which reads as a broken app rather than a busy port.
  mergeConfig(base, {
    plugins: [wailsShim],
    server: { port: webPort, strictPort: true },
  }),
)
