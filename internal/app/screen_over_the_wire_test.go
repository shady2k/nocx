package app

// THE criterion that was unreachable since round one, now over the real
// path: the shipped helper daemon hosts a real shell on a real PTY, the
// runtime beside it publishes the screen, the drain forwards it through the
// carrier, the app publishes it on the data plane — and this test reads the
// frame off a real websocket and validates it against the contract. A
// payload built by the test proves only that the test agrees with itself;
// this one validates what the server sent.
//
// The fixture is restart_screen_test.go's: the shipped daemon built from
// cmd/nocx-helper, installed into an isolated home, reached over its own
// endpoint socket. Every wait is on an observable — a frame on the socket,
// a marker through the product's own read.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

func loadScreenContractSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	f, openErr := os.Open(filepath.Join("..", "..", "contracts", "session.frame.schema.json"))
	if openErr != nil {
		t.Fatalf("open session.frame.schema.json: %v", openErr)
	}
	defer func() { _ = f.Close() }()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	const id = "https://nocx.local/contracts/session.frame.schema.json"
	if addErr := c.AddResource(id, doc); addErr != nil {
		t.Fatalf("add schema resource: %v", addErr)
	}
	s, err := c.Compile(id)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return s
}

// dialRenderer connects one renderer to the shipped transport: the same
// per-launch token the real handshake presents, on the session path.
func dialRenderer(t *testing.T, a *App) *websocket.Conn {
	t.Helper()
	u := "ws://" + a.Transport.Addr() + "/session"
	// The token rides as a subprotocol under the wire's own namespace prefix
	// (ws_auth.go): a bare token is a handshake the auth gate refuses.
	d := websocket.Dialer{Subprotocols: []string{"nocx.token." + a.Transport.Token()}}
	conn, _, err := d.Dial(u, nil)
	if err != nil {
		t.Fatalf("dial the transport: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// attachRenderer claims the session the way a renderer does — the attach RPC
// — and answers only when the control-plane response has arrived, so the
// subscriber slot is filled before the test draws.
func attachRenderer(t *testing.T, conn *websocket.Conn, sid string) {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "attach",
		"params": map[string]any{"sessionId": sid, "offset": 0},
	})
	if err != nil {
		t.Fatalf("marshal attach: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("send attach: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read while attaching: %v", err)
		}
		if mt != websocket.TextMessage {
			continue // binary frames replay the byte stream; the answer is text
		}
		var env struct {
			ID uint64 `json:"id"`
		}
		if json.Unmarshal(data, &env) == nil && env.ID == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the attach response never arrived")
		}
	}
}

// nextScreenFrame reads binary messages until a metadata-seat frame arrives —
// the screen plane — and returns it. Byte-stream frames (MsgTypeData) are the
// PTY replay and are skipped: they are the other plane's.
func nextScreenFrame(t *testing.T, conn *websocket.Conn) transport.Frame {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		f, err := transport.DecodeFrame(data)
		if err != nil {
			t.Fatalf("decode data frame: %v", err)
		}
		if f.MsgType == transport.MsgTypeMetadata {
			return f
		}
	}
}

// draw runs one printf through the pane's shell — real output from a real
// program on a real PTY.
// screenText flattens one published frame's rows into the text they show.
// A row's own `text` (nocx-zg3k3.2.12) already is that flattened text — one
// grapheme per surviving position, spacers and blanks contributing nothing —
// so the marker the program drew IS a contiguous substring of it, unlike
// the [grapheme, width, hasText]-per-column shape this replaced.
func screenText(t *testing.T, payload []byte) string {
	t.Helper()
	var doc struct {
		Rows []struct {
			Text string `json:"text"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("payload is not a frame document: %v", err)
	}
	var b strings.Builder
	for _, row := range doc.Rows {
		b.WriteString(row.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// waitForMarkerOnAnyRow waits, through the product's own frame read, for the
// marker a plain printf drew. Not restart_screen_test.go's waitForMarker:
// that one reads row 2 because its program clears the screen and places the
// cursor on row 3, while a plain printf lands wherever the shell's prompt and
// startup left the cursor — row 2 for bash on the Linux runner, another row on
// macOS (nocx-2v80t.3.57).
func waitForMarkerOnAnyRow(t *testing.T, a *App, sid string, marker string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	var lastScreen []string
	for {
		f, err := a.paneViews.Frame(sid)
		if err == nil {
			lastScreen = lastScreen[:0]
			for row := range f.Lines {
				line := f.Text(row)
				if strings.Contains(line, marker) {
					return
				}
				lastScreen = append(lastScreen, line)
			}
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the marker %q never reached the screen read (last error: %v); the last screen read:\n%s",
				marker, lastErr, strings.Join(lastScreen, "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func draw(t *testing.T, opened transport.OpenedSession, text string) {
	t.Helper()
	if _, err := opened.Session.Write([]byte("printf '" + text + "\\n'\n")); err != nil {
		t.Fatalf("write to the pane: %v", err)
	}
}

// TestAScreenFrameOffTheWireConformsToContract is the acceptance: open a
// real pane, run a real program, attach a real renderer, and validate the
// frame the server PUBLISHED — read off the socket — against the contract.
func TestAScreenFrameOffTheWireConformsToContract(t *testing.T) {
	schema := loadScreenContractSchema(t)
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	t.Cleanup(func() { endTheDaemon(t, filepath.Join(helperRoot(home, src.hash()), "nocx-helper")) })

	a := bootLocalAppOn(t, src)
	opened, err := a.Transport.OpenSession(context.Background(), transport.OpenSpec{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	sid := opened.Session.ID()

	const marker = "SCREEN-WIRE"
	draw(t, opened, marker)
	// The product's own read proves the marker reached the runtime's screen
	// before the wire is asked for it: what is validated below is what the
	// server published, not whether the shell was fast.
	if err := a.paneViews.Enrol(string(sid)); err != nil {
		t.Fatalf("watching the pane: %v", err)
	}
	waitForMarkerOnAnyRow(t, a, string(sid), marker)

	conn := dialRenderer(t, a)
	attachRenderer(t, conn, string(sid))
	draw(t, opened, "SECOND-LINE")

	frame := nextScreenFrame(t, conn)
	var doc any
	if err := json.Unmarshal(frame.Payload, &doc); err != nil {
		t.Fatalf("published payload is not JSON: %v\npayload: %s", err, frame.Payload)
	}
	if err := schema.Validate(doc); err != nil {
		t.Fatalf("the published frame does not satisfy the contract:\n%v", err)
	}
	if got := screenText(t, frame.Payload); !strings.Contains(got, marker) {
		t.Fatalf("the published frame shows %q, want the marker the program drew", got)
	}
}

// TestAssistantScreenCaptureReadsTheOwningRuntimeWithoutARenderer proves the
// assistant's screen seam goes to the helper-owned runtime, with absolute rows
// and buffer identity, even when no renderer is connected to answer a request.
func TestAssistantScreenCaptureReadsTheOwningRuntimeWithoutARenderer(t *testing.T) {
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	t.Cleanup(func() { endTheDaemon(t, filepath.Join(helperRoot(home, src.hash()), "nocx-helper")) })
	a := bootLocalAppOn(t, src)
	opened, openErr := a.Transport.OpenSession(context.Background(), transport.OpenSpec{Cols: 100, Rows: 30})
	if openErr != nil {
		t.Fatalf("open local session: %v", openErr)
	}
	if err := a.paneViews.Enrol(string(opened.Session.ID())); err != nil {
		t.Fatalf("enrol the pane for its runtime screen: %v", err)
	}
	var command strings.Builder
	command.WriteString("printf '")
	for row := range 35 {
		command.WriteString("CAPTURE-ROW-")
		command.WriteString(strconv.Itoa(row))
		command.WriteString("\\n")
	}
	command.WriteString("'\n")
	if _, err := opened.Session.Write([]byte(command.String())); err != nil {
		t.Fatalf("write the screen-producing command: %v", err)
	}
	waitForMarkerOnAnyRow(t, a, string(opened.Session.ID()), "CAPTURE-ROW-34")

	body, err := a.Transport.RequestScreen(context.Background(), string(opened.Session.ID()), nil)
	if err != nil {
		t.Fatalf("read the backend-owned screen without a renderer: %v", err)
	}
	var capture struct {
		Floor int `json:"floor"`
		Total int `json:"total"`
		Rows  []struct {
			Text string `json:"text"`
		} `json:"rows"`
		Identity struct {
			Buffer struct {
				Kind     string `json:"kind"`
				Instance uint64 `json:"instance"`
			} `json:"buffer"`
			Generation int `json:"generation"`
		} `json:"identity"`
		Range struct{ Start, End int } `json:"range"`
	}
	if decodeErr := json.Unmarshal(body, &capture); decodeErr != nil {
		t.Fatalf("decode helper screen capture: %v\n%s", decodeErr, body)
	}
	if capture.Range.Start != capture.Floor || capture.Range.End != capture.Total || len(capture.Rows) != capture.Total-capture.Floor {
		t.Fatalf("capture range [%d,%d), floor %d, total %d, rows %d", capture.Range.Start, capture.Range.End, capture.Floor, capture.Total, len(capture.Rows))
	}
	if capture.Identity.Buffer.Kind != "normal" || capture.Identity.Buffer.Instance == 0 {
		t.Fatalf("capture lost the backend buffer identity: %+v", capture.Identity.Buffer)
	}
	fullText := make([]string, len(capture.Rows))
	for i, row := range capture.Rows {
		fullText[i] = row.Text
	}
	if !strings.Contains(strings.Join(fullText, "\n"), "CAPTURE-ROW-34") {
		t.Fatalf("backend capture omitted the program output: %q", fullText)
	}

	start, end := capture.Floor+2, capture.Floor+5
	partialBody, err := a.Transport.RequestScreen(context.Background(), string(opened.Session.ID()), &assistant.FrameRegion{Start: start, End: end})
	if err != nil {
		t.Fatalf("read an absolute subrange: %v", err)
	}
	var partial struct {
		Range struct{ Start, End int } `json:"range"`
		Rows  []struct {
			Text string `json:"text"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(partialBody, &partial); err != nil {
		t.Fatalf("decode absolute subrange: %v\n%s", err, partialBody)
	}
	if partial.Range.Start != start || partial.Range.End != end || len(partial.Rows) != end-start {
		t.Fatalf("absolute subrange = [%d,%d) with %d rows, want [%d,%d)", partial.Range.Start, partial.Range.End, len(partial.Rows), start, end)
	}
}

// The criterion beside it, also end to end: a client attaching mid-session
// receives a full snapshot at the current revision before any later frame —
// and the snapshot carries output produced BEFORE the attach.
func TestAMidSessionAttachReceivesTheBaselineBeforeAnyLaterFrame(t *testing.T) {
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	t.Cleanup(func() { endTheDaemon(t, filepath.Join(helperRoot(home, src.hash()), "nocx-helper")) })

	a := bootLocalAppOn(t, src)
	opened, err := a.Transport.OpenSession(context.Background(), transport.OpenSpec{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	sid := opened.Session.ID()
	if err := a.paneViews.Enrol(string(sid)); err != nil {
		t.Fatalf("watching the pane: %v", err)
	}

	// Output produced BEFORE the attach, proven to be on the runtime's
	// screen through the product's own read.
	const before = "DRAWN-BEFORE-THE-ATTACH"
	draw(t, opened, before)
	waitForMarkerOnAnyRow(t, a, string(sid), before)

	// The mid-session renderer attaches now — after the output exists, and
	// before anything else is drawn. Frames published while no renderer was
	// attached were dropped at the transport by design; what the attacher is
	// owed arrives with the NEXT revision: a full snapshot, which by being
	// whole carries the pre-attach output with it. That is the assertion —
	// the first frame this renderer is sent already knows what was drawn
	// before it attached, and it conforms to the contract.
	const after = "DRAWN-AFTER-THE-ATTACH"
	conn := dialRenderer(t, a)
	attachRenderer(t, conn, string(sid))
	draw(t, opened, after)

	first := nextScreenFrame(t, conn)
	var firstDoc any
	if err := json.Unmarshal(first.Payload, &firstDoc); err != nil {
		t.Fatalf("first frame payload is not JSON: %v", err)
	}
	if err := loadScreenContractSchema(t).Validate(firstDoc); err != nil {
		t.Fatalf("the first published frame does not satisfy the contract:\n%v", err)
	}
	if got := screenText(t, first.Payload); !strings.Contains(got, before) {
		t.Fatalf("the first frame a mid-session attacher received shows %q, want the pre-attach output %q", got, before)
	}

	// And every frame keeps being whole: the post-attach output arrives in
	// a frame that still carries the pre-attach output, so nothing spliced
	// or degraded on the way.
	for {
		frame := nextScreenFrame(t, conn)
		var doc any
		if err := json.Unmarshal(frame.Payload, &doc); err != nil {
			t.Fatalf("payload is not JSON: %v", err)
		}
		if got := screenText(t, frame.Payload); !strings.Contains(got, before) {
			t.Fatalf("a later frame shows %q, losing the pre-attach output: it is not a full snapshot", got)
		}
		if strings.Contains(screenText(t, frame.Payload), after) {
			break
		}
	}
}
