package filesystem

import (
	"bytes"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxReadBytes bounds a file preview (spec §5.1), not a streamed download.
const MaxReadBytes = 2 << 20

// ReadLimit applies the shared file preview ceiling.
func ReadLimit(requested int64) int64 {
	if requested <= 0 || requested > MaxReadBytes {
		return MaxReadBytes
	}
	return requested
}

// PreviewContent applies the file viewer's binary and lossy-text rules.
func PreviewContent(path, canonical string, data []byte, mtime time.Time, truncated, changed bool) Content {
	c := Content{Path: path, Canonical: canonical, Size: int64(len(data)), ModTime: mtime, Truncated: truncated, Changed: changed, Binary: bytes.IndexByte(data, 0) >= 0}
	if !c.Binary {
		c.Text = string(data)
		if !utf8.Valid(data) {
			c.Lossy = true
			c.Text = strings.ToValidUTF8(c.Text, "\uFFFD")
		}
	}
	return c
}
