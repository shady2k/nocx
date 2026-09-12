package framebytes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Hero geometry: the case §7 of the design document argues about. A 100×40
// terminal producing 600 distinct full-width lines per second.
const (
	HeroCols           = 100
	HeroRows           = 40
	HeroLinesPerSecond = 600
)

// heroAlphabet is the character set a hero line is drawn from.
const heroAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// Hero generates the hero capture. It is generated rather than recorded
// because no recording of it exists, and it is generated deterministically so
// that a reader can reproduce the same bytes:
//
//   - 100 columns × 40 rows, the geometry the argument names.
//   - One chunk per line, 600 lines a second: line i arrives at
//     floor(i × 1000/600) ms, the integer-millisecond positions of that rate,
//     so the 600 lines of a one-second run span 0…998 ms.
//   - Each line is 100 characters from a deterministic xorshift seeded by the
//     line index, then CRLF. Successive lines are therefore full-width
//     distinct, which is what "600 distinct full-width lines per second"
//     names: no two rows of the screen hold the same text, so a positional
//     encoder cannot be flattered by content that repeats.
//   - The raw bytes are 102 per line, i.e. 61,200 bytes a second, which is the
//     ≈60 KB/s the document quotes.
//
// seconds is the length of the generated run.
func Hero(seconds float64) *Capture {
	lines := int(seconds * HeroLinesPerSecond)
	c := &Capture{
		Name: "hero",
		Cols: HeroCols,
		Rows: HeroRows,
		Note: fmt.Sprintf("%d×%d, %d lines at %d lines/s, one write per line, deterministic xorshift content",
			HeroCols, HeroRows, lines, HeroLinesPerSecond),
	}
	for i := range lines {
		line := heroLine(i)
		data := make([]byte, 0, len(line)+2)
		data = append(data, line...)
		data = append(data, '\r', '\n')
		c.Chunks = append(c.Chunks, Chunk{
			AtMs: i * 1000 / HeroLinesPerSecond,
			Data: data,
		})
	}
	return c
}

// heroLine returns the i'th line: HeroCols characters of xorshift output.
func heroLine(i int) string {
	x := uint64(i)*0x9E3779B97F4A7C15 + 0x2545F4914F6CDD1D
	out := make([]byte, HeroCols)
	for j := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[j] = heroAlphabet[x%uint64(len(heroAlphabet))]
	}
	return string(out)
}

// Digest is the SHA-256 of the capture's whole byte stream, so a reader can
// check that their generation produced the same corpus.
func (c *Capture) Digest() string {
	h := sha256.New()
	for _, ch := range c.Chunks {
		h.Write(ch.Data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
