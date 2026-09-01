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

/** Where the shimmed `ResolveBackend` asks for the current endpoint. */
const ENDPOINT_ROUTE = '/__nocx/endpoint'

const wailsShim: Plugin = {
  name: 'nocx-dev-wails-shim',
  /**
   * The endpoint is served per request, not baked into the page.
   *
   * The token is minted per backend launch. Injecting it into the HTML froze
   * it for the life of the vite process, and the shipped app does not work
   * that way: the dispatcher re-resolves on EVERY attempt, so a desktop client
   * picks a restarted backend's new token up by itself. On this stand it could
   * not — the page kept offering the dead launch's token, the backend answered
   * 401 before the upgrade, and since the browser WebSocket API does not
   * expose the HTTP status, the client could not tell that from a closed port
   * and retried against it for as long as the tab stayed open. Reported as "I
   * brought the backend back up and it does not reconnect", and it was this
   * rather than the client.
   *
   * Reading `process.env` inside the handler rather than at config load is the
   * whole fix: `scripts/dev-web.sh` restarts vite with the backend, so the
   * next attempt from an already-open page gets the live token.
   */
  configureServer(server) {
    server.middlewares.use(ENDPOINT_ROUTE, (_req, res) => {
      res.setHeader('Content-Type', 'application/json')
      res.setHeader('Cache-Control', 'no-store')
      res.end(
        JSON.stringify({
          ok: true,
          host: '127.0.0.1',
          port: Number(process.env.NOCX_WS_PORT ?? String(port)),
          token: process.env.NOCX_WS_TOKEN ?? token,
          kind: '',
          message: '',
          remedy: '',
        }),
      )
    })
  },
  transformIndexHtml() {
    return [
      {
        tag: 'script',
        injectTo: 'head-prepend',
        children: `
          window.go = {
            main: {
              WailsApp: {
                ResolveBackend: async () => {
                  // A stand whose vite has gone away has no endpoint to
                  // report, which is the honest answer rather than the last
                  // one that happened to work.
                  const res = await fetch(${JSON.stringify(ENDPOINT_ROUTE)}, { cache: 'no-store' })
                  if (!res.ok) throw new Error('dev stand: endpoint unavailable')
                  return await res.json()
                },
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
