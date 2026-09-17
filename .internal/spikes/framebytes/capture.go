// Package framebytes measures what a backend-owned screen costs to deliver as
// frames, against the raw PTY bytes nocx ships today.
//
// It is a measurement spike, not product code: nothing here is imported by the
// repository, and the only thing it produces is the byte counts in REPORT.md.
package framebytes

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Chunk is one recorded write to the PTY: the bytes, and how many milliseconds
// after the start of the recording they arrived.
type Chunk struct {
	AtMs int
	Data []byte
}

// Capture is one recorded program: the geometry its PTY was opened at, and the
// writes in arrival order.
type Capture struct {
	Name   string
	Cols   int
	Rows   int
	Chunks []Chunk
	// Note is a one-line provenance record for generated captures; empty for
	// the recorded corpus.
	Note string
}

// RawBytes is the total the program wrote. This is the baseline: today the
// renderer receives exactly these bytes.
func (c *Capture) RawBytes() int {
	n := 0
	for _, ch := range c.Chunks {
		n += len(ch.Data)
	}
	return n
}

// DurationMs is the wall-clock span of the recording, from the first write to
// the last.
func (c *Capture) DurationMs() int {
	if len(c.Chunks) == 0 {
		return 0
	}
	return c.Chunks[len(c.Chunks)-1].AtMs
}

// header is the first line of every recorded capture.
type header struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type recordedChunk struct {
	AtMs int    `json:"atMs"`
	Data string `json:"data"`
}

// LoadCapture reads one JSONL recording: a header line with the geometry,
// then one line per write.
func LoadCapture(path string) (*Capture, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	c := &Capture{Name: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	for sc.Scan() {
		b := sc.Bytes()
		line++
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		if line == 1 {
			var h header
			if err := json.Unmarshal(b, &h); err != nil {
				return nil, fmt.Errorf("%s:1: %w", path, err)
			}
			c.Cols, c.Rows = h.Cols, h.Rows
			continue
		}
		var rc recordedChunk
		if err := json.Unmarshal(b, &rc); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		c.Chunks = append(c.Chunks, Chunk{AtMs: rc.AtMs, Data: []byte(rc.Data)})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if c.Cols == 0 || c.Rows == 0 {
		return nil, fmt.Errorf("%s: no geometry header", path)
	}
	return c, nil
}

// LoadCorpus reads every *.jsonl capture in dir, sorted by name.
func LoadCorpus(dir string) ([]*Capture, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]*Capture, 0, len(paths))
	for _, p := range paths {
		c, err := LoadCapture(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no captures in %s", dir)
	}
	return out, nil
}
