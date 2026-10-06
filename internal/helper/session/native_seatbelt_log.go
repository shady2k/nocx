package session

import (
	"bufio"
	"strings"

	"github.com/shady2k/nocx/internal/sandbox"
)

const (
	seatbeltLogLineLimit    = 16 * 1024
	seatbeltLogBatchLimit   = 256 * 1024
	seatbeltLogBatchRecords = 64
	seatbeltLogReadChunk    = 4096
)

// Compact unified-log output puts the denial and its message tag on separate
// physical lines. Keep one bounded metadata record, never an unbounded message.
// The observer validates the fixed nonce before starting this parser.
type seatbeltLogParser struct {
	nonce      string
	pending    sandbox.DiagnosticObservation
	hasPending bool
}

func (p *seatbeltLogParser) consume(line string) (sandbox.DiagnosticObservation, bool) {
	line = strings.TrimSpace(line)
	if line == p.nonce {
		if !p.hasPending {
			return sandbox.DiagnosticObservation{}, false
		}
		observation := p.pending
		p.pending, p.hasPending = sandbox.DiagnosticObservation{}, false
		return observation, true
	}
	p.pending, p.hasPending = parseSeatbeltDenialLine(line)
	return sandbox.DiagnosticObservation{}, false
}

// readBoundedSeatbeltLine retains bufio's unread suffix at each newline.
// Oversize input is discarded through newline without losing following records.
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

func parseSeatbeltDenialLine(line string) (sandbox.DiagnosticObservation, bool) {
	var observation sandbox.DiagnosticObservation
	marker := strings.Index(line, "Sandbox:")
	if marker < 0 {
		return observation, false
	}
	body := strings.TrimSpace(line[marker+len("Sandbox:"):])
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
		Operation: strings.Clone(operation), Access: sandbox.DiagnosticUnknown,
		Source: sandbox.DiagnosticMacSeatbelt, Precision: sandbox.PrecisionReportedDenial,
	}
	if strings.HasPrefix(operation, "file-read") {
		observation.Access = sandbox.DiagnosticRead
	} else if strings.HasPrefix(operation, "file-write") {
		observation.Access = sandbox.DiagnosticWrite
	}
	if len(path) <= sandbox.MaxPathBytes && strings.HasPrefix(path, "/") && !strings.ContainsAny(path, "\"'() \t\r\n") {
		observation.Path, observation.PathKnown = strings.Clone(path), true
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
