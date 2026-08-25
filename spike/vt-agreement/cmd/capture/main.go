// capture records the raw byte stream a real agent TUI writes to a PTY.
//
// nocx-szb40.1 says the bar is agreement with xterm.js on REAL agent output
// and "do not synthesise them", so this exists to produce the input the
// comparison runs on. It drives the agent with a scripted sequence of
// keystrokes rather than a human, because a capture nobody can reproduce is
// evidence of nothing — the same script against the same agent gives another
// run to check a disagreement against.
//
// What it records is the bytes and nothing else. No interpretation happens
// here; that is the whole point of the exercise downstream.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/creack/pty"
)

// chunk is one read from the PTY. Offset is the byte position of the first
// byte in the whole stream, so a disagreement found later can be quoted by
// offset rather than by eye.
type chunk struct {
	AtMs   int64  `json:"atMs"`
	Offset int    `json:"offset"`
	Data   string `json:"data"` // base64 via json.Marshal of []byte
}

type header struct {
	Agent   string   `json:"agent"`
	Argv    []string `json:"argv"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
	Started string   `json:"started"`
	Script  []string `json:"script"`
}

// step is one scripted input: wait, then send.
type step struct {
	delay time.Duration
	send  []byte
	label string
}

// parseScript reads lines of "<delayMs> <input>". The input is unescaped for
// \r \n \t \e \\ and \xNN, which is enough to drive a TUI: Enter, Escape, the
// arrow keys as CSI sequences, and a digit for a numbered menu row.
func parseScript(path string) ([]step, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var steps []step
	sc := bufio.NewScanner(f)
	for ln := 1; sc.Scan(); ln++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		sp := strings.SplitN(line, " ", 2)
		ms, err := strconv.Atoi(sp[0])
		if err != nil {
			return nil, fmt.Errorf("line %d: %q is not a delay in ms", ln, sp[0])
		}
		raw := ""
		if len(sp) > 1 {
			raw = sp[1]
		}
		dec, err := unescape(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", ln, err)
		}
		steps = append(steps, step{delay: time.Duration(ms) * time.Millisecond, send: dec, label: raw})
	}
	return steps, sc.Err()
}

func unescape(s string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}
		i++
		if i >= len(s) {
			return nil, fmt.Errorf("trailing backslash")
		}
		switch s[i] {
		case 'r':
			out = append(out, '\r')
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case 'e':
			out = append(out, 0x1b)
		case '\\':
			out = append(out, '\\')
		case 'x':
			if i+2 >= len(s) {
				return nil, fmt.Errorf("short \\x escape")
			}
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("bad \\x escape: %w", err)
			}
			out = append(out, byte(v))
			i += 2
		default:
			return nil, fmt.Errorf("unknown escape \\%c", s[i])
		}
	}
	return out, nil
}

func main() {
	var (
		out     = flag.String("out", "", "capture file to write (JSONL: header, then chunks)")
		script  = flag.String("script", "", "input script: lines of '<delayMs> <input>'")
		cols    = flag.Int("cols", 120, "PTY columns")
		rows    = flag.Int("rows", 40, "PTY rows")
		timeout = flag.Duration("timeout", 90*time.Second, "hard stop")
	)
	flag.Parse()
	argv := flag.Args()
	if *out == "" || len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: capture -out FILE [-script FILE] [-cols N] [-rows N] -- AGENT [ARGS...]")
		os.Exit(2)
	}

	var steps []step
	if *script != "" {
		var err error
		if steps, err = parseScript(*script); err != nil {
			fmt.Fprintf(os.Stderr, "script: %v\n", err)
			os.Exit(1)
		}
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	// A TUI decides what to draw from these. Pin them so a capture is
	// reproducible and does not inherit whatever ran it.
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"COLUMNS="+strconv.Itoa(*cols),
		"LINES="+strconv.Itoa(*rows),
	)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(*cols), Rows: uint16(*rows)})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pty: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = ptmx.Close() }()

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()
	w := bufio.NewWriter(f)
	defer func() { _ = w.Flush() }()

	labels := make([]string, 0, len(steps))
	for _, s := range steps {
		labels = append(labels, fmt.Sprintf("+%dms %q", s.delay.Milliseconds(), s.label))
	}
	hdr, _ := json.Marshal(header{
		Agent: argv[0], Argv: argv, Cols: *cols, Rows: *rows,
		Started: time.Now().UTC().Format(time.RFC3339), Script: labels,
	})
	fmt.Fprintf(w, "%s\n", hdr)

	start := time.Now()
	done := make(chan struct{})

	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		offset := 0
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				b, _ := json.Marshal(chunk{
					AtMs:   time.Since(start).Milliseconds(),
					Offset: offset,
					Data:   string(buf[:n]),
				})
				fmt.Fprintf(w, "%s\n", b)
				_ = w.Flush()
				offset += n
			}
			if err != nil {
				if err != io.EOF {
					fmt.Fprintf(os.Stderr, "read ended: %v\n", err)
				}
				return
			}
		}
	}()

	go func() {
		for _, s := range steps {
			time.Sleep(s.delay)
			if _, err := ptmx.Write(s.send); err != nil {
				fmt.Fprintf(os.Stderr, "write %q: %v\n", s.label, err)
				return
			}
			fmt.Fprintf(os.Stderr, "sent +%dms %q\n", time.Since(start).Milliseconds(), s.label)
		}
	}()

	select {
	case <-done:
	case <-time.After(*timeout):
		fmt.Fprintln(os.Stderr, "timeout reached; killing child")
		_ = cmd.Process.Kill()
		<-done
	}
	_ = cmd.Wait()
	fmt.Fprintf(os.Stderr, "capture written to %s\n", *out)
}
