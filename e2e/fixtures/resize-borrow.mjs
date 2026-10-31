// Minimal model of omp 18.10.4's resize borrow, not an agent CLI.
// A real SIGWINCH starts an alternate-screen repaint. After its 120 ms
// settle window, the program returns to primary and repaints there.
// The receipt file counts actual PTY notifications independently of nocx.
// The workload ends on its own receipt so the test waits on an event, not a
// sleep. The redraw workload runs at a realistic 20 Hz.
import fs from 'node:fs'
let alt = false
let redraw = 0
let timer
const trace = process.argv[2]
function event(kind) {
  fs.appendFileSync(
    trace,
    JSON.stringify({
      at: new Date().toISOString(),
      kind,
      cols: process.stdout.columns,
      rows: process.stdout.rows,
      redraw,
    }) + '\n',
  )
}
function draw() {
  process.stdout.write(
    '\x1b[?2026h\x1b[2;20r\x1b[H\x1b[2JOMP-BANNER\x1b[6;1H429 rate limited retry ' +
      redraw +
      '\x1b[20;1HGLM-5.3 /tmp 1M\x1b[r\x1b[?2026l',
  )
}
process.stdin.setRawMode(true)
process.stdin.resume()
process.stdout.on('resize', () => {
  event('SIGWINCH')
  redraw++
  if (!alt) {
    alt = true
    event('1049h')
    process.stdout.write('\x1b[?1049h')
  }
  draw()
  clearTimeout(timer)
  timer = setTimeout(() => {
    event('1049l')
    process.stdout.write('\x1b[?1049l')
    alt = false
    draw()
  }, 120)
})
process.on('SIGINT', () => event('SIGINT'))
process.on('SIGTERM', () => {
  event('SIGTERM')
  process.exit(0)
})
process.stdin.on('data', (data) => {
  if (data.toString() !== 'x') {
    event('INPUT:' + data.toString('hex'))
    return
  }
  clearTimeout(timer)
  if (alt) process.stdout.write('\x1b[?1049l')
  process.exit(0)
})
draw()
event('IDLE')

let ticks = 0
const workload = setInterval(() => {
  ticks++
  draw()
  if (ticks === 30) {
    clearInterval(workload)
    event('WORKLOAD-DONE')
  }
}, 50)
