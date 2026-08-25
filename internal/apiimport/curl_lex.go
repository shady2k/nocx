package apiimport

import (
	"errors"
	"fmt"
	"strings"
)

// maxCurlLineBytes bounds the input. A curl line is something a person
// pasted; anything past this is not one, and an unbounded input is the
// cheapest way to make a parser somebody else's problem.
const maxCurlLineBytes = 1 << 20 // 1 MiB

// errUnterminatedQuote is returned rather than the guess a shell would make.
var errUnterminatedQuote = errors.New("apiimport: unterminated quote")

// tokenize splits a curl command line into words.
//
// These rules are OURS. They are close enough to POSIX sh that a pasted
// line comes out the way the person who copied it expects, and they are
// deliberately not sh: there is no expansion of any kind, so `$(...)`,
// backticks, `$VAR`, `~`, `*`, `;`, `|` and `>` are ordinary bytes. The
// design (§10) asks for exactly this — "our own quoting and continuation
// handling; no sh -c, ever" — and the reason it is a lexer rather than a
// sanitiser is that a sanitiser has to enumerate what is dangerous, while a
// lexer that never expands has nothing to enumerate.
//
// What IS honoured:
//   - unquoted whitespace separates words, and runs of it collapse;
//   - '...' is literal to the next ', with no escapes at all;
//   - "..." honours \" \\ \$ \` and a trailing \<newline>, and passes every
//     other backslash through as the two bytes it is (which is what sh does
//     and what makes a Windows path in a pasted line survive);
//   - outside quotes, \ escapes the next byte, and \<newline> is a line
//     continuation that joins the two halves;
//   - quoting is per-run, not per-word, so 'a'"b"c is one word abc.
//
// A NUL byte is refused: nothing downstream can carry one, and a filename
// that contains one is an attempt at a truncation trick rather than a path.
func tokenize(line string) ([]string, error) {
	if len(line) > maxCurlLineBytes {
		return nil, fmt.Errorf("apiimport: command line is %d bytes, over the %d-byte limit", len(line), maxCurlLineBytes)
	}
	if strings.IndexByte(line, 0) >= 0 {
		return nil, errors.New("apiimport: command line contains a NUL byte")
	}

	var (
		out     []string
		cur     strings.Builder
		started bool
	)
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}

	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()

		case c == '\'':
			started = true
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, errUnterminatedQuote
			}
			cur.WriteString(line[i+1 : i+1+end])
			i += end + 1

		case c == '"':
			started = true
			j := i + 1
			closed := false
			for j < len(line) {
				d := line[j]
				if d == '"' {
					closed = true
					break
				}
				if d == '\\' && j+1 < len(line) {
					n := line[j+1]
					switch n {
					case '"', '\\', '$', '`':
						cur.WriteByte(n)
						j += 2
						continue
					case '\n':
						// Line continuation inside the quotes: both bytes go.
						j += 2
						continue
					}
				}
				cur.WriteByte(d)
				j++
			}
			if !closed {
				return nil, errUnterminatedQuote
			}
			i = j

		case c == '\\':
			if i+1 >= len(line) {
				return nil, errors.New("apiimport: command line ends in a backslash")
			}
			if line[i+1] == '\n' {
				// A continuation joins what is on either side of it, so a
				// word is not flushed here.
				i++
				continue
			}
			started = true
			cur.WriteByte(line[i+1])
			i++

		default:
			started = true
			cur.WriteByte(c)
		}
	}
	flush()
	return out, nil
}
