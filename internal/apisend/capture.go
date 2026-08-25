package apisend

// The response is captured STREAMED and bounded, and the bound is inherited
// rather than chosen. `files.read` already answers "carry the content of an
// arbitrary object over the control plane" and its contract says how
// (contracts/files.read.schema.json):
//
//	bounded, streamed content of one file. The provider reads at most the
//	effective limit plus one byte and never the whole file, so the memory
//	guard holds for a 40 GB file … the parameter can only lower the 2 MiB
//	ceiling
//
// All four of its properties apply here unchanged (design §12.3): the reader
// stops at the limit plus one byte, so A CAP APPLIED AFTER READING IS NOT A
// CAP; 2 MiB is the default and a parameter may only lower it; truncated,
// binary and lossy are three distinct fields because they are three distinct
// sentences; and a binary body carries EMPTY TEXT, never base64 — base64
// would be exactly the bulk payload in JSON that AD-1 prohibits, arriving
// through a side door.

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// ceiling is `files.read`'s 2 MiB, inherited. A different number here would
// be a second answer to a question the product already answers.
const ceiling int64 = 2 << 20

// capture is what the reader made of the body.
type capture struct {
	Text      string
	Binary    bool
	Lossy     bool
	Truncated bool
	Size      int64
}

// captureBody reads at most limit+1 bytes and never the whole body. The
// extra byte is how truncation is known: if it was readable the result is a
// prefix, and it is dropped from the text.
//
// A read error that is not a clean end is returned rather than folded into a
// short body — a server that closes mid-response is a failure the user must
// see, not a shorter answer. That distinction is why this reads in a loop
// instead of calling io.ReadFull, whose own ErrUnexpectedEOF cannot be told
// apart from the body's.
func captureBody(body io.Reader, limit int64) (capture, error) {
	if limit <= 0 || limit > ceiling {
		limit = ceiling
	}
	buf := make([]byte, limit+1)
	var n int64
	for n < int64(len(buf)) {
		m, err := body.Read(buf[n:])
		n += int64(m)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return capture{}, err
		}
	}

	truncated := n == limit+1
	data := buf[:n]
	if truncated {
		data = data[:limit]
	}
	c := capture{
		Truncated: truncated,
		Size:      int64(len(data)),
		// The NUL heuristic of files.read, labelled as one: a binary whose
		// first bytes are NUL-free reads as text, accepted.
		Binary: bytes.IndexByte(data, 0) >= 0,
	}
	if !c.Binary {
		c.Text = string(data)
		if !utf8.Valid(data) {
			c.Lossy = true
			c.Text = strings.ToValidUTF8(c.Text, "�")
		}
	}
	return c, nil
}
