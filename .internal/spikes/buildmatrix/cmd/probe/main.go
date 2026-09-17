// Command probe is the minimal caller in the build-matrix decision: it does
// something with the emulator that cannot be done by an empty CGo stub, so the
// link either pulls libghostty-vt in or fails trying.
//
// It writes an OSC 0 title and a DSR cursor query, reads the title and the
// answer back, resizes, and reports one JSON line. A run on the build host's
// own architecture is a link test and a behaviour test at once; the same
// source built for another architecture can only be checked by its link and,
// where an emulator is available, by running it.
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"nocx.internal/spikes/buildmatrix/ghostty"
)

// wantedTitle is what the OSC 0 below sets, and what reading it back has to
// return. A string the library never parsed and one it stored are different
// observations, which is the point of asserting on the value.
const wantedTitle = "bm1-buildmatrix"

type observation struct {
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	Title      string `json:"title"`
	ReplyHex   string `json:"reply_hex"`
	ReplyRunes int    `json:"reply_bytes"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
	OK         bool   `json:"ok"`
	Note       string `json:"note,omitempty"`
}

func main() {
	obs, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: %v\n", err)
		os.Exit(1)
	}
	b, err := json.Marshal(obs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(b))
	if !obs.OK {
		os.Exit(1)
	}
}

func run() (observation, error) {
	obs := observation{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}

	term, err := ghostty.New(80, 24)
	if err != nil {
		return obs, err
	}
	defer term.Free()

	// OSC 0 sets the title; CSI 6n asks the terminal to report the cursor,
	// which only reaches the caller through the write_pty callback.
	term.Write([]byte("\x1b]0;" + wantedTitle + "\x07"))
	term.Write([]byte("\x1b[6n"))

	title, err := term.Title()
	if err != nil {
		return obs, err
	}
	obs.Title = title
	reply := term.Reply()
	obs.ReplyHex = hex.EncodeToString(reply)
	obs.ReplyRunes = len(reply)

	if err := term.Resize(120, 40); err != nil {
		return obs, err
	}
	cols, rows, err := term.Size()
	if err != nil {
		return obs, err
	}
	obs.Cols, obs.Rows = cols, rows

	switch {
	case title != wantedTitle:
		obs.Note = fmt.Sprintf("title %q, wanted %q", title, wantedTitle)
	case len(reply) == 0:
		obs.Note = "no cursor report came back through write_pty"
	case reply[0] != 0x1b || reply[len(reply)-1] != 'R':
		obs.Note = "cursor report is not a CSI ... R sequence"
	case cols != 120 || rows != 40:
		obs.Note = fmt.Sprintf("geometry %dx%d after resize to 120x40", cols, rows)
	default:
		obs.OK = true
	}
	return obs, nil
}
