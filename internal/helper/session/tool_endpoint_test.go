package session_test

// The pane's TOOL ENDPOINT is the coordinator that opened the pane, carried on
// the spawn request (nocx-50w7p.18) — the LOCAL half of the same defect the ssh
// half states in ssh_tool_endpoint_test.go.
//
// # Why the daemon cannot decide this
//
// A helper endpoint socket is keyed by the GENERATION, and its directory is
// derived from the account's home and nothing else — so one account runs one
// daemon per generation and that daemon serves every coordinator of that
// account at once (D12). Until this bead a local pane's `NOCX_TOOL_SOCKET` came
// from the daemon's OWN start environment, which is the environment of
// whichever coordinator happened to start it; a pane opened by any other
// coordinator therefore told its shell to dial the first one's endpoint, and
// the agent inside it reached a backend that had never asked for that pane.
//
// # What is real here
//
// One shipped session service with the shipped local spawner, ONE daemon behind
// TWO coordinator connections (its own protocol engine and its own binding for
// each), a real bash under a real PTY started through internal/pty, the real
// in-memory shell-integration launch, and two real Unix sockets standing in for
// two coordinators' tool endpoints.
//
// # What is asserted, and where the assertion stops
//
// The PANE's own output is where its endpoint is read from: the shell was
// launched with the variable, the test asks the shell to print it, and the
// value is PARSED out of that output. The dial then uses the parsed value and
// not the one the request carried, so what is proven is the chain the criterion
// names — the pane names its own coordinator's endpoint, and that endpoint is
// the live socket that answers. What is NOT proven is an agent process making
// its own dial: this file's dial is the test making that act, because a local
// pane's agent is the agent binary in the shell and nothing here can be one.

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/shellintegration"
)

// toolEndpointStand is one coordinator's tool endpoint as these tests need it:
// a Unix socket at the path that coordinator names on its spawns, answering one
// line per line.
//
// It is deliberately a plain socket and not toolendpoint.Endpoint: the endpoint
// is the admission half (ssh_tool_socket_test.go's header says what of it is
// not proven here), and what is under test is WHICH socket a pane's bytes
// arrive at.
type toolEndpointStand struct {
	listener net.Listener
	// seen carries every line the endpoint was sent, in order, on a channel
	// rather than in a slice: the assertion is an EVENT the endpoint produced,
	// and a slice would have the test reading a field its own goroutine is
	// still writing.
	seen chan string
}

// serveToolEndpoint binds one coordinator's tool socket and answers every
// connection with one line per line.
func serveToolEndpoint(t *testing.T, path string) *toolEndpointStand {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("the coordinator's tool endpoint could not listen at %s: %v", path, err)
	}
	e := &toolEndpointStand{listener: ln, seen: make(chan string, 8)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if line != "" {
						e.seen <- strings.TrimSuffix(line, "\n")
						if _, werr := conn.Write([]byte("tools-ok\n")); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return e
}

// waitLine waits for the endpoint to be SENT a line, which is the event that
// says the pane's bytes arrived — and that they arrived intact — rather than a
// duration that says they may have. It answers what it was sent.
func (e *toolEndpointStand) waitLine(t *testing.T) string {
	t.Helper()
	select {
	case line := <-e.seen:
		return line
	case <-time.After(toolEndpointWait):
		t.Fatalf("no line reached this coordinator's tool endpoint; its pane's bytes never arrived")
		return ""
	}
}

// nothingSeen asserts that NOTHING has reached this endpoint — the negative
// half of "a pane's tool connections belong to the coordinator that opened the
// pane", used after the arrivals the test expects have already been waited for,
// so there is no duration for it to be about.
//
// It is non-blocking rather than timed for exactly that reason: a positive
// arrival is an event with a bound, and a negative is a statement about what is
// already true.
func (e *toolEndpointStand) nothingSeen(t *testing.T, what string) {
	t.Helper()
	select {
	case line := <-e.seen:
		t.Fatalf("%s was sent %q", what, line)
	default:
	}
}

// toolEndpointWait bounds the waits in this file. It is a failure message's
// bound and never a schedule: every wait below is on an event the daemon or the
// pane produced (a line arriving at an endpoint, the pane printing its own
// environment), and this is only what turns "it never happened" into a sentence.
const toolEndpointWait = 15 * time.Second

// coordinatorHash is the content hash the daemon in these tests answers with.
// The handshake's whole job is to prove the peer IS the generation this client
// installed, and a test's daemon was never installed from a file, so the value
// is this test's own.
const coordinatorHash = "tool-endpoint-test-hash"

// addCoordinatorTo dials ONE coordinator connection to a helper daemon: its own
// protocol engine, its own binding to the daemon's process-scoped session
// service, and its own reverse-answer registry. Dialling it twice is what "two
// coordinators on one daemon" is — the shape this bead's defect needed, one
// endpoint socket per generation with several callers on it.
//
// register is how a caller adds the other services this daemon answers (the ssh
// service in the build that has one); the session service is registered here
// because every connection serves it.
func addCoordinatorTo(
	t *testing.T,
	svc *session.Service,
	contentHash string,
	log *slog.Logger,
	reverse *client.ReverseRegistry,
	register func(*host.Host),
) *client.Client {
	t.Helper()
	helperEnd, coordEnd := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := host.New(helperEnd, helperEnd, contentHash, "instance-coordinator", log)
	h.Register(svc)
	if register != nil {
		register(h)
	}
	release := svc.Bind(h)
	t.Cleanup(release)
	go func() { _ = h.Serve(ctx) }()

	c, err := client.Dial(ctx, client.Config{
		Exec:        client.NewSocketConn(coordEnd),
		ExpectHash:  contentHash,
		SentinelTTL: toolEndpointWait,
		Reverse:     reverse,
		Log:         log,
	})
	if err != nil {
		t.Fatalf("dial the coordinator: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestEachCoordinatorsLocalPaneIsToldItsOwnToolEndpoint is the bead's first
// acceptance criterion's local half: two coordinators ride ONE daemon, each
// opens a pane, and each pane's shell is told ITS OWN coordinator's tool
// endpoint as NOCX_TOOL_SOCKET.
//
// The value is read from the PANE, not from the test's own request: the shell
// was launched with the variable, so the test types a print and waits for the
// path it expects to appear in what the pane wrote. A pane that was told the
// other coordinator's endpoint — or the daemon's own environment value — fails
// here, on its own output.
func TestEachCoordinatorsLocalPaneIsToldItsOwnToolEndpoint(t *testing.T) {
	// The value a daemon-scoped implementation would have reached for. It is
	// set so that a regression to one fails LOUDLY (the pane prints this
	// instead of its own endpoint) rather than silently printing nothing.
	t.Setenv(shellintegration.ToolSocketEnvVar, "/run/nocx/daemon-start-tool.sock")

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is not installed and this test drives a real pane: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := session.New(session.Options{
		Generation: coordinatorHash,
		Spawner:    session.NewLocalSpawner(log, session.Shell{Path: bash}, ""),
		Inspector:  session.NewInspector(),
		Log:        log,
		Limits:     session.DefaultLimits(),
	})
	t.Cleanup(svc.Close)

	first := addCoordinatorTo(t, svc, coordinatorHash, log, nil, nil)
	second := addCoordinatorTo(t, svc, coordinatorHash, log, nil, nil)

	dir := t.TempDir()
	coordA, coordB := filepath.Join(dir, "coord-a.sock"), filepath.Join(dir, "coord-b.sock")
	endpointA := serveToolEndpoint(t, coordA)
	endpointB := serveToolEndpoint(t, coordB)

	printedA := mustPrintItsOwnToolEndpoint(t, first, coordA, "11111111111111111111111111111111")
	if printedA != coordA {
		t.Fatalf("the first coordinator's pane named %q as its tool endpoint, want its own %q", printedA, coordA)
	}
	printedB := mustPrintItsOwnToolEndpoint(t, second, coordB, "22222222222222222222222222222222")
	if printedB != coordB {
		t.Fatalf("the second coordinator's pane named %q as its tool endpoint, want its own %q", printedB, coordB)
	}

	// AND THE VALUE THE PANE NAMED IS THAT COORDINATOR'S LIVE ENDPOINT: two
	// panes, two endpoints, neither endpoint hearing the other's pane, and the
	// path dialed is the one READ OUT OF THE PANE rather than the one this test
	// sent.
	for _, tc := range []struct {
		name     string
		endpoint *toolEndpointStand
		path     string
		line     string
	}{
		{"the first coordinator's pane", endpointA, printedA, "a-tools"},
		{"the second coordinator's pane", endpointB, printedB, "b-tools"},
	} {
		conn, dialErr := net.Dial("unix", tc.path)
		if dialErr != nil {
			t.Fatalf("%s named %s, which is not a socket this machine answers: %v", tc.name, tc.path, dialErr)
		}
		if _, werr := conn.Write([]byte(tc.line + "\n")); werr != nil {
			t.Fatalf("write to %s: %v", tc.path, werr)
		}
		if got := tc.endpoint.waitLine(t); got != tc.line {
			t.Fatalf("%s named %s, and that endpoint was sent %q", tc.name, tc.path, got)
		}
		_ = conn.Close()
	}
	endpointA.nothingSeen(t, "the first coordinator's endpoint, after its own pane's single line")
	endpointB.nothingSeen(t, "the second coordinator's endpoint, after its own pane's single line")
}

// toolEndpointMarker is what a pane wraps its NOCX_TOOL_SOCKET in when this
// test asks it to print one. It is unbroken so that the echo of the typed
// command — whose format string reads `NOCX-TOOL-ENDPOINT[%s]` — is not
// mistakable for the answer: the value parsed is the one in the LINE the shell
// printed, which is always the later of the two.
const toolEndpointMarker = "NOCX-TOOL-ENDPOINT["

// mustPrintItsOwnToolEndpoint opens one pane on c whose tool endpoint is
// endpoint — the value THIS coordinator owns, exactly as a coordinator names it
// on the wire — asks the shell inside it to print NOCX_TOOL_SOCKET, and answers
// what the PANE wrote, parsed out of the pane's own output.
//
// The answer is what the caller compares against its own value, so a pane that
// was told nothing, or was told the daemon's start environment value instead,
// returns that and fails — which is the defect this test exists for.
func mustPrintItsOwnToolEndpoint(t *testing.T, c *client.Client, endpoint, subscriber string) string {
	t.Helper()
	entry, err := c.Spawn(context.Background(), proto.SpawnParams{
		Cwd: "/", Cols: 80, Rows: 24, AgentToolEndpoint: endpoint,
	})
	if err != nil {
		t.Fatalf("spawn a pane whose tool endpoint is %s: %v", endpoint, err)
	}
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: proto.SubscriberID(subscriber),
		Session: proto.HostSessionID{
			Generation: proto.GenerationID(entry.HostSessionID.Generation),
			Session:    entry.HostSessionID.Session,
		},
		Fresh: true, RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("attach to the pane whose tool endpoint is %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = attached.Close() })

	printed := `printf '` + toolEndpointMarker + `%s]\n' "$NOCX_TOOL_SOCKET"`
	if _, err := attached.Write([]byte(printed + "\n")); err != nil {
		t.Fatalf("ask the pane to print its tool endpoint: %v", err)
	}

	// The wait is on the pane's OWN bytes: the event is its printed line
	// arriving, and toolEndpointWait only turns a shell that never speaks into a
	// failure with a sentence.
	deadline := time.After(toolEndpointWait)
	read := make(chan string, 1)
	go func() {
		var seen []byte
		buf := make([]byte, 8*1024)
		for {
			n, readErr := attached.Read(buf)
			if n > 0 {
				seen = append(seen, buf[:n]...)
				if value, ok := printedToolEndpoint(seen); ok {
					read <- value
					return
				}
			}
			if readErr != nil {
				read <- ""
				return
			}
		}
	}()
	select {
	case value := <-read:
		return value
	case <-deadline:
		t.Fatalf("the pane never printed its tool endpoint")
		return ""
	}
}

// printedToolEndpoint reads the value out of a marker line in what a pane has
// written so far, and answers false while there is not one — which is what
// makes the caller's wait an event rather than a duration.
//
// The terminator after the closing bracket is what makes the answer
// unambiguous: the terminal ECHOES the typed command, whose format string also
// carries the marker, and what follows the bracket there is a literal `\n`
// inside quotes. Only the shell's own output ends the line.
func printedToolEndpoint(seen []byte) (string, bool) {
	for at := bytes.LastIndex(seen, []byte(toolEndpointMarker)); at >= 0; {
		rest := seen[at+len(toolEndpointMarker):]
		if end := bytes.IndexByte(rest, ']'); end >= 0 && end+1 < len(rest) {
			switch rest[end+1] {
			case '\n', '\r':
				return string(rest[:end]), true
			}
		}
		next := bytes.LastIndex(seen[:at], []byte(toolEndpointMarker))
		if next == at {
			break
		}
		at = next
	}
	return "", false
}
