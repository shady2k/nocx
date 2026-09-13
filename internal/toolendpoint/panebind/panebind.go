// Package panebind is the PANE RECORD a forwarded connection carries: the one
// fixed-size frame a helper writes ahead of a far agent's own bytes, naming the
// session whose pane the connection arrived on (nocx-50w7p.16).
//
// # Why a record at all, and why it is not a claim
//
// The tool endpoint admits a connection from the kernel's peer credentials: a
// local pane's agent is a process in a backend-owned tree, so its pid answers
// "which pane". A remote pane's agent has no pid on this machine at all
// (internal/helper/session/spawn_ssh.go's sshProcess), and the only party that
// knows which pane a far connection belongs to is the helper, whose listener
// per pane IS that knowledge. So the helper says which pane, and the endpoint
// believes it *because of who is allowed to say it*: the record is read only on
// the lane the coordinator itself dialed its helper on (toolendpoint.Config's
// Lane), and a connection from anywhere else is admitted — or refused — by the
// kernel rule it always was. A record is therefore never a claim a caller can
// make; it is a fact reported by a process the coordinator launched.
//
// # The shape, and why it is fixed
//
//	magic "NXP1" | version (1 byte) | session (32 bytes, lowercase hex) | '\n'
//
// Fixed size, no JSON, no lengths and no optional members: this is read from a
// socket BEFORE anything else is, on a connection a far host partly controls,
// so a reader that had to parse a length would be a reader a hostile peer could
// make allocate. A session id is exactly 32 lowercase hex characters
// (proto.SessionHex), so "is this a pane record" is answered by reading 38
// bytes and looking at them.
//
// The trailing newline is not punctuation: it makes a truncated write visible
// in the reader's own error (a record that stops early cannot be mistaken for
// one that ended), and it keeps the record a line for anyone reading a capture
// of the socket by hand.
package panebind

import (
	"errors"
	"fmt"
	"io"
)

// Magic is the four bytes every pane record begins with.
const Magic = "NXP1"

// Version is the record's own version. It moves only if the frame's SHAPE
// moves: both ends of this record ship in one binary -- the helper and the
// coordinator are the same build -- so unlike the helper's frozen wire there is
// no generation to stay compatible with, and a version that never moves is a
// version nobody can use to refuse an old peer.
const Version byte = 1

// SessionHexLen is the length of a session id in the spelling every wire
// carries: 16 bytes of hex.
const SessionHexLen = 32

// RecordLen is the whole frame's length, terminator included.
const RecordLen = len(Magic) + 1 + SessionHexLen + 1

// ErrShortRecord means the connection ended before a whole record arrived: a
// helper that died mid-write, or a peer that sent fewer bytes and stopped.
var ErrShortRecord = errors.New("toolendpoint: the pane record ended before it was complete")

// ErrNotAPaneRecord means the bytes are not a record this build understands:
// a wrong magic, an unknown version, a session id that is not 32 lowercase hex
// characters, or a missing terminator. It is ONE error rather than four
// because the endpoint's answer is the same for all of them and the difference
// is only useful to whoever is reading the log.
var ErrNotAPaneRecord = errors.New("toolendpoint: these bytes are not a pane record")

// Encode renders one pane record for a session id.
//
// It refuses an id that is not the shape every session id has, rather than
// truncating or padding one: a record the reader will reject is worse than no
// record, because the far agent's own bytes follow it and would be read as the
// rest of a frame.
func Encode(session string) ([]byte, error) {
	if !isSessionHex(session) {
		return nil, fmt.Errorf("panebind: %q is not a session id", session)
	}
	out := make([]byte, 0, RecordLen)
	out = append(out, Magic...)
	out = append(out, Version)
	out = append(out, session...)
	return append(out, '\n'), nil
}

// Read reads exactly one record from r.
//
// It returns the session the record names, or an error that says whether the
// bytes were not a record at all (ErrNotAPaneRecord) or stopped early
// (ErrShortRecord). It never reads past the record: the caller's next read
// gets the agent's own bytes, untouched.
func Read(r io.Reader) (string, error) {
	buf := make([]byte, RecordLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "", ErrShortRecord
		}
		return "", err
	}
	if string(buf[:len(Magic)]) != Magic || buf[len(Magic)] != Version {
		return "", ErrNotAPaneRecord
	}
	if buf[RecordLen-1] != '\n' {
		return "", ErrNotAPaneRecord
	}
	session := string(buf[len(Magic)+1 : RecordLen-1])
	if !isSessionHex(session) {
		return "", ErrNotAPaneRecord
	}
	return session, nil
}

// isSessionHex reports whether s is a session id in its one wire spelling.
func isSessionHex(s string) bool {
	if len(s) != SessionHexLen {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
