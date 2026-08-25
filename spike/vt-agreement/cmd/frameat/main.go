// frameat prints the screen a capture had reached at a given point in time, as
// plain text, so a state can be found by eye before it is asserted on.
//
// render-go answers "what did the whole stream leave on the screen"; a driver
// is built against MOMENTS — the input box before a turn, the spinner during
// one, the permission menu that interrupts it — and those are gone by the end
// of the capture. This replays a capture up to each requested millisecond mark
// and prints the grid there.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	xvt "github.com/charmbracelet/x/vt"
)

type header struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type chunk struct {
	AtMs   int64  `json:"atMs"`
	Offset int    `json:"offset"`
	Data   string `json:"data"`
}

func main() {
	at := flag.String("at", "", "comma-separated millisecond marks to print the screen at")
	flag.Parse()
	if flag.NArg() != 1 || *at == "" {
		fmt.Fprintln(os.Stderr, "usage: frameat -at MS[,MS...] CAPTURE.jsonl")
		os.Exit(2)
	}
	var marks []int64
	for _, s := range strings.Split(*at, ",") {
		v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "bad mark:", s)
			os.Exit(2)
		}
		marks = append(marks, v)
	}

	f, err := os.Open(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	if !sc.Scan() {
		fmt.Fprintln(os.Stderr, "empty capture")
		os.Exit(1)
	}
	var hdr header
	if err := json.Unmarshal(sc.Bytes(), &hdr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var chunks []chunk
	for sc.Scan() {
		var c chunk
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		chunks = append(chunks, c)
	}

	term := xvt.NewEmulator(hdr.Cols, hdr.Rows)
	// The emulator replies upstream through an unbuffered pipe, so Write
	// blocks the moment nothing reads them (nocx-szb40.1 deadlocked here).
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := term.Read(buf); err != nil {
				if !errorsIsEOF(err) {
					fmt.Fprintln(os.Stderr, "drain:", err)
				}
				return
			}
		}
	}()

	i := 0
	for _, m := range marks {
		for ; i < len(chunks) && chunks[i].AtMs <= m; i++ {
			// data is a plain JSON string of the bytes read, not base64 —
			// capture's own struct comment says base64 and is wrong.
			if _, err := term.Write([]byte(chunks[i].Data)); err != nil {
				fmt.Fprintln(os.Stderr, "write:", err)
				os.Exit(1)
			}
		}
		off := 0
		if i > 0 {
			off = chunks[i-1].Offset
		}
		pos := term.CursorPosition()
		fmt.Printf("=== at %dms (through chunk %d, offset %d) cursor %d,%d alt=%v ===\n",
			m, i, off, pos.X, pos.Y, term.IsAltScreen())
		for y := 0; y < hdr.Rows; y++ {
			var sb strings.Builder
			for x := 0; x < hdr.Cols; x++ {
				c := term.CellAt(x, y)
				if c == nil || c.Width == 0 {
					if c == nil {
						sb.WriteString(" ")
					}
					continue
				}
				if c.Content == "" {
					sb.WriteString(" ")
					continue
				}
				sb.WriteString(c.Content)
			}
			fmt.Printf("%3d|%s\n", y, strings.TrimRight(sb.String(), " "))
		}
	}
}

func errorsIsEOF(err error) bool { return err == io.EOF }
