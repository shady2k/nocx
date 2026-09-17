// Package obs emits one observation per JSON line in the same schema the
// qualification spike's drivers use, so the wasm build's answers and the
// committed native answers are directly comparable files.
package obs

import (
	"encoding/json"
	"os"
	"strings"
)

// Emit writes one observation: probe, case, key, value.
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
// itself, C escapes for controls, \xNN for everything else. The encoding is
// the qualification spike's, byte for byte (uppercase hex), because the
// comparison between the two drivers is a diff of their output files.
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
			sb.WriteString(`\x` + strings.ToUpper(hex2(c)))
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func hex2(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&0x0f]})
}
