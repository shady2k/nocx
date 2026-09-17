// Package obs emits one observation per JSON line, so that the two drivers
// (x/vt and libghostty-vt) produce directly comparable records and the report
// quotes bytes rather than verdicts.
package obs

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

var mu sync.Mutex

// Emit writes one observation. probe names the probe, kase the case inside
// it, key the measured field and value the measurement.
func Emit(probe, kase, key string, value any) {
	b, err := json.Marshal(struct {
		Probe string `json:"probe"`
		Case  string `json:"case"`
		Key   string `json:"key"`
		Value any    `json:"value"`
	}{probe, kase, key, value})
	if err != nil {
		panic(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stdout.Write(append(b, '\n')); err != nil {
		panic(err)
	}
}

// Hex renders bytes as lowercase hex with no separator.
func Hex(b []byte) string {
	const digits = "0123456789abcdef"
	var sb strings.Builder
	for _, c := range b {
		sb.WriteByte(digits[c>>4])
		sb.WriteByte(digits[c&0x0f])
	}
	return sb.String()
}

// Esc renders bytes in a quoted, ASCII-visible form: printable ASCII as
// itself, C escapes for controls, \xNN for everything else.
func Esc(b []byte) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, c := range b {
		switch {
		case c == '\\':
			sb.WriteString(`\\`)
		case c == '"':
			sb.WriteString(`\"`)
		case c == 0x1b:
			sb.WriteString(`\e`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\a':
			sb.WriteString(`\a`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			sb.WriteString(`\x` + strings.ToUpper(fmt.Sprintf("%02x", c)))
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// Num formats a duration in nanoseconds and the byte counts of an
// allocation measurement as a single comparable string.
func Num(v int64) string { return strconv.FormatInt(v, 10) }
