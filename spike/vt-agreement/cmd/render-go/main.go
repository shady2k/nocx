// render-go renders a capture through each candidate Go VT library and emits
// the same frame shape the xterm.js ground truth emits, so the two can be
// diffed cell by cell.
//
// nocx-szb40.1 says explicitly not to choose on features or reputation. So
// every candidate is driven identically: same capture bytes, same geometry,
// same output shape, no per-library tuning to make one look better. Where a
// library cannot express something the frame needs, that is recorded as the
// answer rather than worked around.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	xvt "github.com/charmbracelet/x/vt"
	"github.com/hinshun/vt10x"
	"github.com/tonistiigi/vt100"
)

type captureHeader struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type captureChunk struct {
	Data string `json:"data"`
}

type cell struct {
	Ch      string `json:"ch"`
	W       int    `json:"w"`
	Fg      *int   `json:"fg"`
	Bg      *int   `json:"bg"`
	Bold    int    `json:"bold"`
	Inverse int    `json:"inverse"`
}

type row struct {
	Text  string  `json:"text"`
	Cells []*cell `json:"cells"`
}

type frame struct {
	Renderer string `json:"renderer"`
	Version  string `json:"version"`
	Source   string `json:"source"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Cursor   struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"cursor"`
	Grid []row `json:"grid"`
	// Notes records what a library could not answer, rather than letting a
	// zero value pass as a measurement.
	Notes []string `json:"notes,omitempty"`
}

func loadCapture(path string) (captureHeader, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return captureHeader{}, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	var hdr captureHeader
	var body strings.Builder
	first := true
	for sc.Scan() {
		if first {
			if err := json.Unmarshal(sc.Bytes(), &hdr); err != nil {
				return hdr, nil, err
			}
			first = false
			continue
		}
		var c captureChunk
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			return hdr, nil, err
		}
		body.WriteString(c.Data)
	}
	return hdr, []byte(body.String()), sc.Err()
}

func renderVT10x(hdr captureHeader, data []byte, src string) frame {
	term := vt10x.New(vt10x.WithSize(hdr.Cols, hdr.Rows))
	_, _ = term.Write(data)
	var fr frame
	fr.Renderer = "hinshun/vt10x"
	fr.Version = "v0.0.0-20220301184237"
	fr.Source = src
	fr.Cols, fr.Rows = hdr.Cols, hdr.Rows
	cur := term.Cursor()
	fr.Cursor.X, fr.Cursor.Y = cur.X, cur.Y
	for y := 0; y < hdr.Rows; y++ {
		r := row{Cells: make([]*cell, 0, hdr.Cols)}
		var sb strings.Builder
		for x := 0; x < hdr.Cols; x++ {
			g := term.Cell(x, y)
			ch := string(g.Char)
			if g.Char == 0 {
				ch = " "
			}
			sb.WriteString(ch)
			fg, bg := int(g.FG), int(g.BG)
			r.Cells = append(r.Cells, &cell{Ch: ch, W: 1, Fg: &fg, Bg: &bg})
		}
		r.Text = sb.String()
		fr.Grid = append(fr.Grid, r)
	}
	fr.Notes = append(fr.Notes,
		"Glyph carries no width: every cell is reported W=1, so a wide character's "+
			"second column cannot be distinguished from a real cell here.")
	return fr
}

func renderVT100(hdr captureHeader, data []byte, src string) frame {
	term := vt100.NewVT100(hdr.Rows, hdr.Cols)
	_, _ = term.Write(data)
	var fr frame
	fr.Renderer = "tonistiigi/vt100"
	fr.Version = "v0.0.0-20240514184818"
	fr.Source = src
	fr.Cols, fr.Rows = hdr.Cols, hdr.Rows
	fr.Cursor.X, fr.Cursor.Y = term.Cursor.X, term.Cursor.Y
	for y := 0; y < hdr.Rows; y++ {
		r := row{Cells: make([]*cell, 0, hdr.Cols)}
		var sb strings.Builder
		for x := 0; x < hdr.Cols; x++ {
			ch := " "
			if y < len(term.Content) && x < len(term.Content[y]) {
				if c := term.Content[y][x]; c != 0 {
					ch = string(c)
				}
			}
			sb.WriteString(ch)
			bold := 0
			if y < len(term.Format) && x < len(term.Format[y]) &&
				term.Format[y][x].Intensity == vt100.Bright {
				bold = 1
			}
			r.Cells = append(r.Cells, &cell{Ch: ch, W: 1, Bold: bold})
		}
		r.Text = sb.String()
		fr.Grid = append(fr.Grid, r)
	}
	fr.Notes = append(fr.Notes,
		"Colour is color.RGBA, not an index: fg/bg are omitted rather than "+
			"invented so no false agreement is recorded.",
		"The package's own doc says it handles no scrolling and misinterprets some "+
			"control codes; that claim is what the diff is here to check.")
	return fr
}

func renderXVT(hdr captureHeader, data []byte, src string) frame {
	e := xvt.NewEmulator(hdr.Cols, hdr.Rows)
	// Write, NOT InputPipe. InputPipe is the KEYBOARD side — bytes travelling
	// from a user into the application — while Write is the byte stream the
	// application produced, which is what every other renderer here is being
	// given. The first version of this file used InputPipe, and the emulator
	// returned a completely empty grid; reported as-is that would have been a
	// damning result for this candidate and a false one, produced entirely by
	// the harness.
	//
	// The drain stays. The emulator replies upstream — device attributes and
	// friends — and a real terminal always has a reader on the other side.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := e.Read(buf); err != nil {
				return
			}
		}
	}()
	_, _ = e.Write(data)
	var fr frame
	fr.Renderer = "charmbracelet/x/vt"
	fr.Version = "v0.0.0-20260823001701"
	fr.Source = src
	fr.Cols, fr.Rows = hdr.Cols, hdr.Rows
	pos := e.CursorPosition()
	fr.Cursor.X, fr.Cursor.Y = pos.X, pos.Y
	for y := 0; y < hdr.Rows; y++ {
		r := row{Cells: make([]*cell, 0, hdr.Cols)}
		var sb strings.Builder
		for x := 0; x < hdr.Cols; x++ {
			c := e.CellAt(x, y)
			if c == nil {
				sb.WriteString(" ")
				r.Cells = append(r.Cells, &cell{Ch: " ", W: 1})
				continue
			}
			ch := c.Content
			if ch == "" {
				ch = " "
			}
			sb.WriteString(ch)
			r.Cells = append(r.Cells, &cell{Ch: ch, W: c.Width})
		}
		r.Text = sb.String()
		fr.Grid = append(fr.Grid, r)
	}
	if e.IsAltScreen() {
		fr.Notes = append(fr.Notes, "ended in the alternate screen")
	}
	return fr
}

func main() {
	out := flag.String("outdir", "frames", "where to write frames")
	name := flag.String("name", "", "basename for the output files")
	budget := flag.Duration("budget", 20*time.Second, "per-candidate wall clock before it is recorded as not finishing")
	flag.Parse()
	if flag.NArg() != 1 || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: render-go -name NAME [-outdir DIR] CAPTURE.jsonl")
		os.Exit(2)
	}
	src := flag.Arg(0)
	hdr, data, err := loadCapture(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", src, err)
		os.Exit(1)
	}

	type candidate struct {
		slug string
		fn   func(captureHeader, []byte, string) frame
	}
	for _, c := range []candidate{
		{"vt10x", renderVT10x},
		{"vt100", renderVT100},
		{"xvt", renderXVT},
	} {
		// A candidate that panics or hangs on real input has answered the
		// question. Both are recorded as the result rather than being allowed
		// to take the run down with them.
		done := make(chan frame, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					done <- frame{
						Renderer: c.slug, Source: src, Cols: hdr.Cols, Rows: hdr.Rows,
						Notes: []string{fmt.Sprintf("PANICKED on this capture: %v", r)},
					}
				}
			}()
			done <- c.fn(hdr, data, src)
		}()
		var fr frame
		select {
		case fr = <-done:
		case <-time.After(*budget):
			fr = frame{
				Renderer: c.slug, Source: src, Cols: hdr.Cols, Rows: hdr.Rows,
				Notes: []string{fmt.Sprintf("DID NOT FINISH within %s on this capture", *budget)},
			}
		}
		path := fmt.Sprintf("%s/%s.%s.json", *out, *name, c.slug)
		b, _ := json.MarshalIndent(fr, "", " ")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "%-20s -> %s (cursor %d,%d)\n", c.slug, path, fr.Cursor.X, fr.Cursor.Y)
	}
}
