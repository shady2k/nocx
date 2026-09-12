const { Terminal } = require('@xterm/headless');
const { Unicode11Addon } = require('@xterm/addon-unicode11');

function run(seqs) {
  return new Promise((resolve) => {
    const t = new Terminal({ cols: 20, rows: 3, allowProposedApi: true });
    const u = new Unicode11Addon();
    t.loadAddon(u);
    t.unicode.activeVersion = '11';
    let i = 0;
    const next = () => {
      if (i >= seqs.length) return resolve(out);
      const [name, s] = seqs[i++];
      t.reset();
      t.write(s, () => {
        const b = t.buffer.active;
        const cells = [];
        for (let x = 0; x < 20; x++) {
          const c = b.getLine(0).getCell(x);
          if (!c) { cells.push('.'); continue; }
          const ch = c.getChars();
          if (!ch) { cells.push('.'); continue; }
          cells.push(JSON.stringify(ch) + '/' + c.getWidth());
        }
        out.push([name, cells.join(' ').trim(), 'cursor_x=' + b.cursorX]);
        next();
      });
    };
    const out = [];
    next();
  });
}

const zwj = '\u{1F468}\u200D\u{1F469}\u200D\u{1F467}\u200D\u{1F466}';
const skin = '\u{1F44D}\u{1F3FD}';
const flag = '\u{1F1F7}\u{1F1FA}';
const heart = '\u2764\uFE0F';
const comb = 'a\u0301';

(async () => {
  for (const [name, s] of [['zwj_family', zwj], ['skin_tone', skin], ['flag_ru', flag], ['heart_vs16', heart], ['combining', comb]]) {
    const r = await run([[name, s]]);
    console.log('WHOLE', r[0][0], '=>', r[0][1], r[0][2]);
  }
  // split invariance
  for (const [name, s] of [['zwj_family', zwj], ['skin_tone', skin], ['heart_vs16', heart], ['combining', comb]]) {
    const buf = Buffer.from(s, 'utf8');
    const whole = (await run([[name, s]]))[0][1];
    let ok = 0, bad = -1;
    for (let k = 1; k < buf.length; k++) {
      const parts = [buf.slice(0, k).toString('binary'), buf.slice(k).toString('binary')];
      const got = await new Promise((resolve) => {
        const t = new Terminal({ cols: 20, rows: 3, allowProposedApi: true });
        t.loadAddon(new Unicode11Addon());
        t.unicode.activeVersion = '11';
        t.write(Buffer.from(parts[0], 'binary'), () => {
          t.write(Buffer.from(parts[1], 'binary'), () => {
            const b = t.buffer.active; const cells = [];
            for (let x = 0; x < 20; x++) {
              const c = b.getLine(0).getCell(x);
              cells.push(c && c.getChars() ? JSON.stringify(c.getChars()) + '/' + c.getWidth() : '.');
            }
            resolve(cells.join(' ').trim());
          });
        });
      });
      if (got === whole) ok++; else if (bad < 0) bad = k;
    }
    console.log('SPLIT', name, 'identical=' + ok + '/' + (buf.length - 1), 'first_bad=' + bad);
  }
})();
