//go:build darwin

package session

import (
	"bufio"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/shady2k/nocx/internal/sandbox"
)

const (
	seatbeltLogLineLimit    = 16 * 1024
	seatbeltLogBatchLimit   = 256 * 1024
	seatbeltLogBatchRecords = 64
	seatbeltLogReadChunk    = 4096
)

type darwinNativeObserver struct {
	config    nativeObserverConfig
	command   *exec.Cmd
	stdout    io.ReadCloser
	done      chan struct{}
	closeOnce sync.Once
	closing   atomic.Bool
}

func startNativeObserver(config nativeObserverConfig) nativeObserver {
	observer := &darwinNativeObserver{config: config, done: make(chan struct{})}
	if !validSeatbeltNonce(config.Nonce) {
		observer.setStatus(sandbox.ObserverUnavailable)
		return observer
	}
	predicate := `(eventMessage ENDSWITH "` + config.Nonce + `")`
	observer.command = exec.Command("/usr/bin/log", "stream", "--predicate", predicate, "--style", "compact")
	observer.command.Stderr = io.Discard
	stdout, err := observer.command.StdoutPipe()
	if err != nil {
		observer.setStatus(sandbox.ObserverUnavailable)
		return observer
	}
	if err := observer.command.Start(); err != nil {
		_ = stdout.Close()
		observer.setStatus(sandbox.ObserverUnavailable)
		return observer
	}
	observer.stdout = stdout
	observer.setStatus(sandbox.ObserverActive)
	go observer.read()
	return observer
}

func (o *darwinNativeObserver) setStatus(status sandbox.ObserverStatus) {
	if o.config.Sink != nil {
		o.config.Sink.SetObserver(status)
	}
}

func (o *darwinNativeObserver) read() {
	defer close(o.done)
	reader := bufio.NewReaderSize(o.stdout, seatbeltLogReadChunk)
	var storage [seatbeltLogLineLimit]byte
	batchBytes, batchRecords := 0, 0
	for {
		line, size, err := readBoundedSeatbeltLine(reader, storage[:])
		batchBytes += size
		if len(line) != 0 && o.config.Sink != nil {
			if observation, ok := parseSeatbeltLogLine(string(line), o.config.Nonce); ok {
				// One complete record is delivered immediately. The helper inbox
				// owns bounded coalescing; sparse denies never await a full batch.
				o.config.Sink.Observe(observation)
				batchRecords++
			}
		} else if size > seatbeltLogLineLimit && o.config.Sink != nil {
			o.config.Sink.Drop(1)
		}
		if batchRecords >= seatbeltLogBatchRecords || batchBytes >= seatbeltLogBatchLimit {
			batchBytes, batchRecords = 0, 0
			runtime.Gosched()
		}
		if err != nil {
			_ = o.command.Wait()
			if o.closing.Load() {
				o.setStatus(sandbox.ObserverUnavailable)
			} else {
				o.setStatus(sandbox.ObserverFailed)
			}
			return
		}
	}
}

// readBoundedSeatbeltLine retains bufio's unread suffix at each newline.
// The reusable storage bounds a line; oversize input is discarded through its
// newline without throwing away any subsequent record in the same read chunk.
func readBoundedSeatbeltLine(reader *bufio.Reader, storage []byte) ([]byte, int, error) {
	line := storage[:0]
	discard, consumed := false, 0
	for {
		fragment, err := reader.ReadSlice('\n')
		consumed += len(fragment)
		payload := fragment
		if len(payload) > 0 && payload[len(payload)-1] == '\n' {
			payload = payload[:len(payload)-1]
		}
		if !discard {
			if len(line)+len(payload) > seatbeltLogLineLimit {
				discard, line = true, nil
			} else {
				line = append(line, payload...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if discard {
			return nil, consumed, err
		}
		return line, consumed, err
	}
}

func parseSeatbeltLogLine(line, nonce string) (sandbox.DiagnosticObservation, bool) {
	var observation sandbox.DiagnosticObservation
	line = strings.TrimSpace(line)
	if !validSeatbeltNonce(nonce) || !strings.HasSuffix(line, " "+nonce) {
		return observation, false
	}
	marker := strings.Index(line, "Sandbox:")
	if marker < 0 {
		return observation, false
	}
	body := strings.TrimSpace(strings.TrimSuffix(line[marker+len("Sandbox:"):], nonce))
	deny := strings.Index(body, " deny")
	if deny < 0 {
		return observation, false
	}
	tail := body[deny+len(" deny"):]
	if tail == "" || (tail[0] != '(' && tail[0] != ' ' && tail[0] != '\t') {
		return observation, false
	}
	tail = strings.TrimSpace(tail)
	if strings.HasPrefix(tail, "(") {
		end := strings.IndexByte(tail, ')')
		if end < 0 {
			return observation, false
		}
		tail = strings.TrimSpace(tail[end+1:])
	}
	operation, path := tail, ""
	if separator := strings.IndexAny(tail, " \t"); separator >= 0 {
		operation, path = tail[:separator], strings.TrimSpace(tail[separator+1:])
	}
	if operation == "" || len(operation) > 128 {
		return observation, false
	}
	observation = sandbox.DiagnosticObservation{
		Operation: strings.Clone(operation),
		Access:    sandbox.DiagnosticUnknown,
		Source:    sandbox.DiagnosticMacSeatbelt,
		Precision: sandbox.PrecisionReportedDenial,
	}
	if strings.HasPrefix(operation, "file-read") {
		observation.Access = sandbox.DiagnosticRead
	} else if strings.HasPrefix(operation, "file-write") {
		observation.Access = sandbox.DiagnosticWrite
	}
	if len(path) <= sandbox.MaxPathBytes && strings.HasPrefix(path, "/") && !strings.ContainsAny(path, "\"'() \t\r\n") {
		observation.Path = strings.Clone(path)
		observation.PathKnown = true
	}
	return observation, true
}

func validSeatbeltNonce(nonce string) bool {
	if len(nonce) != 32 {
		return false
	}
	for _, c := range nonce {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func (o *darwinNativeObserver) Close() {
	o.closeOnce.Do(func() {
		o.closing.Store(true)
		if o.command == nil || o.command.Process == nil {
			if o.config.Listener != nil {
				_ = o.config.Listener.Close()
			}
			return
		}
		_ = o.command.Process.Kill()
		if o.stdout != nil {
			_ = o.stdout.Close()
		}
		<-o.done
		if o.config.Listener != nil {
			_ = o.config.Listener.Close()
		}
	})
}
