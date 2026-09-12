const { Terminal } = require('@xterm/headless')
const { Unicode11Addon } = require('@xterm/addon-unicode11')

function split(s, k) {
  const buf = Buffer.from(s, 'utf8')
  return [buf.slice(0, k), buf.slice(k)]
}
function cellsOf(writes) {
  return new Promise((resolve) => {
    const t = new Terminal({ cols: 20, rows: 3, allowProposedApi: true })
    t.loadAddon(new Unicode11Addon())
    t.unicode.activeVersion = '11'
    let i = 0
    const step = () => {
      if (i >= writes.length) {
        const b = t.buffer.active
        const cells = []
        for (let x = 0; x < 20; x++) {
          const c = b.getLine(0).getCell(x)
          cells.push(c && c.getChars() ? JSON.stringify(c.getChars()) + '/' + c.getWidth() : '.')
        }
        return resolve(cells.join(' ').trim() + ' cursor_x=' + b.cursorX)
      }
      t.write(writes[i++], step)
    }
    step()
  })
}
;(async () => {
  const zwj = '\u{1F468}\u200D\u{1F469}\u200D\u{1F467}\u200D\u{1F466}'
  const buf = Buffer.from(zwj, 'utf8')
  const whole = await cellsOf([buf])
  console.log('WHOLE', whole)
  for (let k = 1; k < buf.length; k++) {
    const r = await cellsOf(split(zwj, k))
    console.log((r === whole ? 'ok   ' : 'DIFF ') + 'offset=' + k + ' ' + r)
  }
})()
