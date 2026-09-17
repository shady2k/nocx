package completion

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/shady2k/nocx/internal/remoteprobe"
)

// ProbeConn is the minimal surface the SSH completer needs from a probe lease:
// ONE completion probe, named, with its typed arguments.
//
// There is no command in this interface and that is the whole change D3 forced
// (nocx-50w7p.9): the script is a fixed program that lives in
// internal/remoteprobe, the helper links the same package and runs it on the
// pooled connection, and what crosses the wire is a line, a caret and a nonce.
// A `Exec(ctx, cmd)` seam here would be a coordinator composing shell text and
// handing it to another process to put on somebody's machine.
type ProbeConn interface {
	Complete(ctx context.Context, probe CompletionProbe) (*ExecResult, error)
	Close() error
}

// CompletionProbe is one completion question: the typed arguments the fixed
// script takes.
//
// Cwd and Line are the user's, and they are the reason the script is fixed
// rather than composed per request: both arrive as quoted ARGUMENTS to it. Pos
// is the caret's byte offset in the line and Limit the caller's own bound on
// candidates.
type CompletionProbe struct {
	Cwd   string
	Line  string
	Pos   int
	Limit int
	Nonce string
}

// ExecResult mirrors what a probe lease returns, so this package does not
// import the wire — it is remoteprobe.Result, declared once for the reason the
// probe names are: the helper describes what it captured and this package reads
// it, and two structs with the same four fields would be the pair that drifts.
type ExecResult = remoteprobe.Result

// ProbeConnProvider creates a ProbeConn for the given host. The composition
// root wires a function that asks this machine's helper for a lease on it.
type ProbeConnProvider func(ctx context.Context, host string) (ProbeConn, error)

// SSHCompleter runs completion on a remote host through a second shell — the
// probe lane of ADR-0020. The user's line is never touched; no keystroke is
// ever forwarded (ADR-0004 §2).
type SSHCompleter struct {
	provider     ProbeConnProvider
	generateRand func() (string, error) // nonce generator; crypto/rand by default
}

func NewSSH(provider ProbeConnProvider) *SSHCompleter {
	return &SSHCompleter{
		provider:     provider,
		generateRand: defaultGenerateRand,
	}
}

// NewSSHWithRand is for tests: it pins the nonce generator so the response
// framing is deterministic.
func NewSSHWithRand(provider ProbeConnProvider, randFn func() (string, error)) *SSHCompleter {
	return &SSHCompleter{provider: provider, generateRand: randFn}
}

func defaultGenerateRand() (string, error) {
	var b [4]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Complete implements Completer for a remote bash host.
//
// It mints the nonce the script frames its answer with, asks for one probe
// through the lease, and parses what came back. The framing is what makes a
// banner-polluted answer rejectable WHOLE: a login banner, an MOTD or a chatty
// rc file lands on the same stream, and half-parsing one is how a banner line
// becomes a completion candidate.
func (c *SSHCompleter) Complete(ctx context.Context, req Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return emptyResponse("cancelled"), nil
	}

	conn, err := c.provider(ctx, req.Host)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	nonce, err := c.generateRand()
	if err != nil {
		return emptyResponse("completion unavailable"), nil
	}
	limit := req.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	result, err := conn.Complete(ctx, CompletionProbe{
		Cwd:   req.Cwd,
		Line:  req.Line,
		Pos:   req.Pos,
		Limit: limit,
		Nonce: nonce,
	})
	if err != nil {
		return classifyProbeError(err), nil
	}

	return parseCompletionOutput(result.Stdout, nonce, limit), nil
}

// parseCompletionOutput extracts candidates from the framed response.
// A response whose nonce markers are missing or mismatched is rejected
// whole — a banner-polluted answer must never be half-parsed.
func parseCompletionOutput(stdout []byte, nonce string, limit int) *Response {
	startMarker := remoteprobe.CompletionStart(nonce)
	endMarker := remoteprobe.CompletionEnd(nonce)

	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	inPayload := false
	seenEnd := false
	var candidates []Candidate
	truncated := false

	for scanner.Scan() {
		line := scanner.Text()
		if line == startMarker {
			inPayload = true
			continue
		}
		if line == endMarker {
			inPayload = false
			seenEnd = true
			break
		}
		if !inPayload {
			continue
		}
		// Parse TSV: source <TAB> name [<TAB> path <TAB> isDir]
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		source := parts[0]
		name := parts[1]
		c := Candidate{Name: name, Source: source}
		if source == "path" && len(parts) >= 4 {
			c.Path = parts[2]
			c.IsDir = parts[3] == "1"
		}
		candidates = append(candidates, c)
		if len(candidates) >= limit*2 {
			truncated = true
			break
		}
	}

	// No END marker after START: the output is incomplete or polluted.
	if !seenEnd && inPayload {
		truncated = true
	}
	// Never saw START at all: the response is polluted by a banner.
	// Reject whole rather than parsing what looks plausible.

	if len(candidates) == 0 && !truncated {
		return emptyResponse("")
	}
	return &Response{Candidates: candidates, Truncated: truncated}
}

// classifyProbeError maps a probe's failure onto a soft response. The dropdown
// must never show a spinner that never resolves; every failure path surfaces a
// stated reason.
//
// The kinds are TYPED now rather than matched against the error's text, which
// is what this used to do: the failure crosses the helper wire as a code and
// arrives as remoteprobe's own kind, so a sentence spelled differently in
// another process can no longer turn a refusal into "completion unavailable".
func classifyProbeError(err error) *Response {
	if errors.Is(err, context.Canceled) {
		return emptyResponse("cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return emptyResponse("timed out")
	}
	var probeErr *remoteprobe.Error
	if errors.As(err, &probeErr) {
		switch probeErr.Kind {
		case remoteprobe.KindSessionRefused:
			return emptyResponse("remote host limits sessions; completion unavailable")
		case remoteprobe.KindExecProhibited:
			return emptyResponse("remote host refused completion exec")
		case remoteprobe.KindCommandTooLong:
			return emptyResponse("completion probe too large for this host")
		case remoteprobe.KindConnectionLost:
			return emptyResponse("connection lost during completion")
		case remoteprobe.KindLeaseClosed:
			return emptyResponse("completion unavailable")
		}
	}
	return emptyResponse("completion unavailable")
}
