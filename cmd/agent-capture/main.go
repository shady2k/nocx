// Command agent-capture records and replays real terminal byte streams for agent-driver work.
//
// Agent TUIs change under their drivers. The driver's useful evidence is therefore
// not a guessed screen or a final snapshot: it is the bytes a real program emitted
// on a real PTY, together with the moments at which those bytes arrived. This tool
// makes that evidence reproducible. Capture pins terminal size and locale, drives
// the child from a small timed-keystroke script, and stores bytes only. Replay feeds
// those bytes through charmbracelet/x/vt and prints the screen at requested moments.
//
// The two operations are subcommands of one binary because they share one capture
// format and one workflow: take evidence, then inspect the exact moments a driver
// must classify.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	xvt "github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

type captureHeader struct {
	Agent   string   `json:"agent"`
	Argv    []string `json:"argv"`
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
	Started string   `json:"started"`
	Script  []string `json:"script"`
}

type captureChunk struct {
	AtMs   int64  `json:"atMs"`
	Offset int    `json:"offset"`
	Data   string `json:"data"`
}

type scriptStep struct {
	delay time.Duration
	send  []byte
	label string
}

type usageError struct {
	message string
}

func (e *usageError) Error() string { return e.message }

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agent-capture:", err)
		if _, ok := err.(*usageError); ok {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		if err := printUsage(stderr); err != nil {
			return fmt.Errorf("write usage: %w", err)
		}
		return &usageError{message: "missing subcommand (choose capture or replay)"}
	}
	switch args[0] {
	case "capture":
		return runCaptureCommand(args[1:], stderr)
	case "replay":
		return runReplayCommand(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		if err := printUsage(stdout); err != nil {
			return fmt.Errorf("write usage: %w", err)
		}
		return nil
	default:
		if err := printUsage(stderr); err != nil {
			return fmt.Errorf("write usage: %w", err)
		}
		return &usageError{message: fmt.Sprintf("unknown subcommand %q (choose capture or replay)", args[0])}
	}
}

func printUsage(w io.Writer) error {
	for _, line := range []string{
		"usage: agent-capture <capture|replay> [flags] [arguments]",
		"",
		"capture runs a program on a pinned PTY and writes JSONL bytes.",
		"replay renders a capture at one or more millisecond marks.",
		"",
		"examples:",
		"  agent-capture capture -out capture.jsonl -script steps.script -- claude",
		"  agent-capture replay -at 49000 capture.jsonl",
	} {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func runCaptureCommand(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		if _, err := fmt.Fprintln(stderr, "usage: agent-capture capture -out FILE [-script FILE] [-cols N] [-rows N] [-timeout DURATION] -- PROGRAM [ARGS...]"); err != nil {
			return
		}
		fs.PrintDefaults()
	}
	outPath := fs.String("out", "", "JSONL capture file to write")
	scriptPath := fs.String("script", "", "timed-keystroke script (<delayMs> [input] per line)")
	cols := fs.Int("cols", 120, "PTY columns")
	rows := fs.Int("rows", 40, "PTY rows")
	timeout := fs.Duration("timeout", 90*time.Second, "hard stop after this duration")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &usageError{message: err.Error()}
	}
	if *outPath == "" {
		return &usageError{message: "capture: -out is required (choose a JSONL destination)"}
	}
	if *cols <= 0 || *cols > math.MaxUint16 {
		return &usageError{message: fmt.Sprintf("capture: -cols must be between 1 and %d, got %d", math.MaxUint16, *cols)}
	}
	if *rows <= 0 || *rows > math.MaxUint16 {
		return &usageError{message: fmt.Sprintf("capture: -rows must be between 1 and %d, got %d", math.MaxUint16, *rows)}
	}
	if *timeout <= 0 {
		return &usageError{message: fmt.Sprintf("capture: -timeout must be positive, got %s", timeout.String())}
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return &usageError{message: "capture: missing program after -- (for example: -- bash -i)"}
	}

	var steps []scriptStep
	if *scriptPath != "" {
		var err error
		steps, err = parseScript(*scriptPath)
		if err != nil {
			return fmt.Errorf("capture: script %q: %w", *scriptPath, err)
		}
	}
	return captureProgram(*outPath, argv, *cols, *rows, *timeout, steps, *scriptPath != "", stderr)
}

func runReplayCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		if _, err := fmt.Fprintln(stderr, "usage: agent-capture replay -at MS[,MS...] CAPTURE.jsonl"); err != nil {
			return
		}
		fs.PrintDefaults()
	}
	at := fs.String("at", "", "comma-separated non-negative millisecond marks")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &usageError{message: err.Error()}
	}
	if *at == "" {
		return &usageError{message: "replay: -at is required (for example: -at 49000,70000)"}
	}
	marks, err := parseMarks(*at)
	if err != nil {
		return &usageError{message: "replay: " + err.Error()}
	}
	if fs.NArg() != 1 {
		return &usageError{message: "replay: provide exactly one CAPTURE.jsonl path"}
	}
	if err := replayCapture(fs.Arg(0), marks, stdout); err != nil {
		return fmt.Errorf("replay: %w", err)
	}
	return nil
}

func parseScript(path string) ([]scriptStep, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the operator explicitly supplies the script path
	if err != nil {
		return nil, fmt.Errorf("cannot read script: %w", err)
	}
	var steps []scriptStep
	for lineNumber, line := range strings.Split(string(data), "\n") {
		lineNumber++
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		separator := strings.IndexFunc(line, unicode.IsSpace)
		if separator < 0 {
			separator = len(line)
		}
		markText := line[:separator]
		mark, err := parseDelay(markText)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		raw := ""
		if separator < len(line) {
			raw = line[separator+1:]
		}
		decoded, err := unescape(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		steps = append(steps, scriptStep{
			delay: time.Duration(mark) * time.Millisecond,
			send:  decoded,
			label: fmt.Sprintf("+%dms %q", mark, raw),
		})
	}
	return steps, nil
}

func parseDelay(text string) (int64, error) {
	if text == "" {
		return 0, errors.New("missing millisecond mark; use <delayMs> [input]")
	}
	mark, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a millisecond mark; use a non-negative integer", text)
	}
	if mark < 0 {
		return 0, fmt.Errorf("millisecond mark %q is negative; use zero or a positive integer", text)
	}
	if mark > math.MaxInt64/int64(time.Millisecond) {
		return 0, fmt.Errorf("millisecond mark %q is too large for a Go duration", text)
	}
	return mark, nil
}

func unescape(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}
		i++
		if i >= len(s) {
			return nil, errors.New("trailing backslash; use \\\\ for a literal backslash")
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
				return nil, errors.New("short \\x escape; use exactly two hexadecimal digits")
			}
			value, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return nil, fmt.Errorf("bad \\x escape %q; use exactly two hexadecimal digits", s[i+1:i+3])
			}
			out = append(out, byte(value))
			i += 2
		default:
			return nil, fmt.Errorf("unknown escape \\%c; use r, n, t, e, \\, or xNN", s[i])
		}
	}
	return out, nil
}

func parseMarks(text string) ([]int64, error) {
	parts := strings.Split(text, ",")
	marks := make([]int64, 0, len(parts))
	var previous int64
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("mark %d is empty; use comma-separated non-negative integers", i+1)
		}
		mark, err := parseDelay(part)
		if err != nil {
			return nil, fmt.Errorf("mark %d: %w", i+1, err)
		}
		if i > 0 && mark < previous {
			return nil, fmt.Errorf("marks must be non-decreasing; %d follows %d", mark, previous)
		}
		marks = append(marks, mark)
		previous = mark
	}
	return marks, nil
}

func captureProgram(outPath string, argv []string, cols, rows int, timeout time.Duration, steps []scriptStep, scriptProvided bool, stderr io.Writer) error {
	//nolint:gosec // the operator explicitly supplies the program and arguments
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = pinnedEnvironment(cols, rows)
	//nolint:gosec // cols and rows are validated against uint16 bounds before this call
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return fmt.Errorf("cannot start %q on a PTY: %w", argv[0], err)
	}

	started := time.Now().UTC()
	start := time.Now()
	stop := make(chan struct{})
	readDone := make(chan readResult, 1)
	scriptDone := make(chan struct{})
	var scriptDoneCase <-chan struct{}
	if scriptProvided {
		scriptDoneCase = scriptDone
	}
	scriptErr := make(chan error, 1)
	go readPTY(ptmx, start, readDone)
	go driveScript(ptmx, start, steps, stop, scriptDone, scriptErr)

	var result readResult
	var runErr error
	var endedByScript bool
	timer := time.NewTimer(timeout)
	select {
	case result = <-readDone:
	case err := <-scriptErr:
		runErr = err
		if killErr := killProcess(cmd); killErr != nil {
			runErr = fmt.Errorf("%w; also could not stop program: %v", runErr, killErr)
		}
		result = <-readDone
	case <-scriptDoneCase:
		endedByScript = true
		if killErr := killProcess(cmd); killErr != nil {
			runErr = fmt.Errorf("script ended; could not stop program: %w", killErr)
		}
		result = <-readDone
	case <-timer.C:
		runErr = fmt.Errorf("program did not exit within %s; it was killed", timeout)
		if killErr := killProcess(cmd); killErr != nil {
			runErr = fmt.Errorf("%w; also could not stop program: %v", runErr, killErr)
		}
		result = <-readDone
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	close(stop)
	if closeErr := ptmx.Close(); closeErr != nil && runErr == nil {
		runErr = fmt.Errorf("close PTY: %w", closeErr)
	}
	waitErr := cmd.Wait()
	if result.err != nil && !errors.Is(result.err, io.EOF) && !errors.Is(result.err, syscall.EIO) && runErr == nil {
		runErr = fmt.Errorf("read PTY: %w", result.err)
	}
	if waitErr != nil && runErr == nil && !endedByScript {
		runErr = fmt.Errorf("program %q exited with an error: %w", argv[0], waitErr)
	}
	if len(result.chunks) == 0 && runErr == nil && !endedByScript {
		runErr = fmt.Errorf("program %q exited without producing PTY output; check the command and arguments", argv[0])
	}

	header := captureHeader{
		Agent:   argv[0],
		Argv:    append([]string(nil), argv...),
		Cols:    cols,
		Rows:    rows,
		Started: started.Format(time.RFC3339Nano),
		Script:  scriptLabels(steps),
	}
	if writeErr := writeCapture(outPath, header, result.chunks); writeErr != nil {
		if runErr == nil {
			runErr = writeErr
		} else {
			runErr = fmt.Errorf("%w; also could not write capture: %v", runErr, writeErr)
		}
	}
	if runErr != nil {
		return runErr
	}
	if endedByScript {
		if _, err := fmt.Fprintln(stderr, "capture ended because script ended"); err != nil {
			return fmt.Errorf("report capture completion: %w", err)
		}
	}
	if _, err := fmt.Fprintf(stderr, "capture written to %s (%d chunks, %d bytes)\n", outPath, len(result.chunks), result.bytes); err != nil {
		return fmt.Errorf("report capture: %w", err)
	}
	return nil
}

type readResult struct {
	chunks []captureChunk
	bytes  int
	err    error
}

func readPTY(ptmx io.Reader, started time.Time, done chan<- readResult) {
	buf := make([]byte, 32*1024)
	result := readResult{}
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			data := string(buf[:n])
			result.chunks = append(result.chunks, captureChunk{
				AtMs:   time.Since(started).Milliseconds(),
				Offset: result.bytes,
				Data:   data,
			})
			result.bytes += n
		}
		if err != nil {
			result.err = err
			break
		}
	}
	done <- result
}

func driveScript(ptmx io.Writer, started time.Time, steps []scriptStep, stop <-chan struct{}, scriptDone chan<- struct{}, scriptErr chan<- error) {
	for _, step := range steps {
		timer := time.NewTimer(step.delay)
		select {
		case <-timer.C:
		case <-stop:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
		if _, err := ptmx.Write(step.send); err != nil {
			scriptErr <- fmt.Errorf("send %q at +%dms: %w", step.label, time.Since(started).Milliseconds(), err)
			return
		}
	}
	close(scriptDone)
}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func pinnedEnvironment(cols, rows int) []string {
	keys := map[string]struct{}{
		"TERM": {}, "LANG": {}, "LC_ALL": {}, "COLUMNS": {}, "LINES": {},
	}
	env := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, pinned := keys[key]; !pinned {
			env = append(env, entry)
		}
	}
	env = append(env,
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"COLUMNS="+strconv.Itoa(cols),
		"LINES="+strconv.Itoa(rows),
	)
	return env
}

func scriptLabels(steps []scriptStep) []string {
	labels := make([]string, 0, len(steps))
	for _, step := range steps {
		labels = append(labels, step.label)
	}
	return labels
}

func writeCapture(path string, header captureHeader, chunks []captureChunk) error {
	file, err := os.Create(path) //nolint:gosec // the operator explicitly supplies the capture path
	if err != nil {
		return fmt.Errorf("cannot create capture %q: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(header); err != nil {
		_ = file.Close()
		return fmt.Errorf("write capture header: %w", err)
	}
	for _, chunk := range chunks {
		if err := encoder.Encode(chunk); err != nil {
			_ = file.Close()
			return fmt.Errorf("write capture chunk: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close capture %q: %w", path, err)
	}
	return nil
}

func replayCapture(path string, marks []int64, stdout io.Writer) (err error) {
	header, chunks, err := readCapture(path)
	if err != nil {
		return err
	}
	term := xvt.NewEmulator(header.Cols, header.Rows)
	drainDone := make(chan struct{})
	go drainEmulator(term, drainDone)
	defer func() {
		if closeErr := term.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close emulator: %w", closeErr)
		}
		<-drainDone
	}()

	consumed := 0
	for _, mark := range marks {
		previous := consumed
		consumed = chunksThroughMark(chunks, mark, consumed)
		if err := feedChunks(term, chunks[previous:consumed]); err != nil {
			return fmt.Errorf("feed capture at %dms: %w", mark, err)
		}
		if err := printFrame(stdout, term, header, mark, consumed, previousOffset(chunks, consumed)); err != nil {
			return err
		}
	}
	return nil
}

func readCapture(path string) (captureHeader, []captureChunk, error) {
	file, err := os.Open(path) //nolint:gosec // the operator explicitly supplies the capture path
	if err != nil {
		return captureHeader{}, nil, fmt.Errorf("cannot open capture %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return captureHeader{}, nil, fmt.Errorf("read capture header: %w", err)
		}
		return captureHeader{}, nil, errors.New("capture is empty; expected a JSONL header")
	}
	var header captureHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return captureHeader{}, nil, fmt.Errorf("decode capture header: %w", err)
	}
	if header.Agent == "" || len(header.Argv) == 0 {
		return captureHeader{}, nil, errors.New("capture header has no agent or argv")
	}
	if header.Cols <= 0 || header.Rows <= 0 {
		return captureHeader{}, nil, fmt.Errorf("capture header has invalid geometry %dx%d", header.Cols, header.Rows)
	}

	chunks := make([]captureChunk, 0)
	expectedOffset := 0
	var previousAt int64
	for lineNumber := 2; scanner.Scan(); lineNumber++ {
		var chunk captureChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			return captureHeader{}, nil, fmt.Errorf("decode capture chunk on line %d: %w", lineNumber, err)
		}
		if chunk.AtMs < 0 || chunk.AtMs < previousAt {
			return captureHeader{}, nil, fmt.Errorf("capture chunk on line %d has out-of-order atMs %d", lineNumber, chunk.AtMs)
		}
		if chunk.Offset != expectedOffset {
			return captureHeader{}, nil, fmt.Errorf("capture chunk on line %d starts at offset %d, expected %d", lineNumber, chunk.Offset, expectedOffset)
		}
		chunks = append(chunks, chunk)
		expectedOffset += len([]byte(chunk.Data))
		previousAt = chunk.AtMs
	}
	if err := scanner.Err(); err != nil {
		return captureHeader{}, nil, fmt.Errorf("read capture: %w", err)
	}
	return header, chunks, nil
}

func chunksThroughMark(chunks []captureChunk, mark int64, already int) int {
	for already < len(chunks) && chunks[already].AtMs <= mark {
		already++
	}
	return already
}

func previousOffset(chunks []captureChunk, consumed int) int {
	if consumed == 0 {
		return 0
	}
	return chunks[consumed-1].Offset + len([]byte(chunks[consumed-1].Data))
}

func feedChunks(term *xvt.Emulator, chunks []captureChunk) error {
	for _, chunk := range chunks {
		if _, err := term.Write([]byte(chunk.Data)); err != nil {
			return err
		}
	}
	return nil
}

func drainEmulator(term *xvt.Emulator, done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 4096)
	for {
		if _, err := term.Read(buf); err != nil {
			return
		}
	}
}

func printFrame(w io.Writer, term *xvt.Emulator, header captureHeader, mark int64, consumed, offset int) error {
	position := term.CursorPosition()
	if _, err := fmt.Fprintf(w, "=== at %dms (through chunk %d, offset %d) cursor %d,%d alt=%v ===\n", mark, consumed, offset, position.X, position.Y, term.IsAltScreen()); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	for y := 0; y < header.Rows; y++ {
		var line strings.Builder
		for x := 0; x < header.Cols; x++ {
			cell := term.CellAt(x, y)
			if cell == nil || cell.Width == 0 {
				if cell == nil {
					line.WriteByte(' ')
				}
				continue
			}
			if cell.Content == "" {
				line.WriteByte(' ')
				continue
			}
			line.WriteString(cell.Content)
		}
		if _, err := fmt.Fprintf(w, "%3d|%s\n", y, strings.TrimRight(line.String(), " ")); err != nil {
			return fmt.Errorf("write frame row %d: %w", y, err)
		}
	}
	return nil
}
