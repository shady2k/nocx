package client_test

// THE HELLO FENCE, PROVEN IN BOTH DIRECTIONS.
//
// Mouse intents gained key-style modifier prefixes on their action token
// (nocx-if2d8, nocx-3y64b). A protocol-23 runtime rejects these payloads, so a
// coordinator must not recover an old generation and route the new vocabulary
// into it. These tests exercise the actual client/host hello fence, not a
// literal assertion about this build's version number.
//
// The old peer's version stays literal: deriving both peers' versions from
// proto.Version would let the compatibility boundary disappear unnoticed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// previousGeneration is the protocol version this bump fences off. It is a
// literal because that is the fact being asserted.
const previousGeneration = "23"

// previousHelperPeer answers a hello the way a helper SPEAKING version does: it
// compares what arrived with its own number, and where the two disagree it
// writes nothing to stdout and exits 42 (D5's one pre-sentinel exit, which is
// what a client maps to ErrVersionMismatch). received carries the version the
// hello carried, so a test can say what each side said rather than only that
// something failed.
func previousHelperPeer(version string, received chan<- string) func(io.Reader, io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		refused := false
		dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			if ty != proto.TypeHello {
				return
			}
			var h proto.Hello
			_ = json.Unmarshal(payload, &h)
			select {
			case received <- h.Version:
			default:
			}
			refused = h.Version != version
			if !refused {
				_, _ = fmt.Fprintf(out, "nocx-helper %s ready\n", version)
			}
		}, nil)
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				_ = dec.Feed(buf[:n])
				if refused {
					return host.ExitVersionMismatch
				}
			}
			if err != nil {
				if refused {
					return host.ExitVersionMismatch
				}
				return 0
			}
		}
	}
}

// TestThisCoordinatorAndThePreviousHelperRefuseEachOtherAtHello is the
// direction a person meets first: the coordinator is this build's, and the
// helper on the host is a generation behind. It says nothing on stdout and
// exits 42; the coordinator answers ErrVersionMismatch, which is a state the product
// renders ("this machine's helper is older than this app") rather than a
// generic launch failure.
func TestThisCoordinatorAndThePreviousHelperRefuseEachOtherAtHello(t *testing.T) {
	received := make(chan string, 1)
	conn := newFakeConn(previousHelperPeer(previousGeneration, received))

	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if !errors.Is(err, client.ErrVersionMismatch) {
		t.Fatalf("dialling a %s helper answered %v, want ErrVersionMismatch", previousGeneration, err)
	}
	if c != nil {
		t.Fatal("an incompatible helper returned an input-capable client")
	}
	if got := <-received; got != proto.Version {
		t.Fatalf("the %s helper read hello version %q, want %q — the coordinator must say which protocol it speaks",
			previousGeneration, got, proto.Version)
	}
}

// TestThePreviousCoordinatorAndThisHelperRefuseEachOtherAtHello is the reverse
// pair, and it drives the REAL host: the hello is the one a
// previous-generation coordinator sends, written by hand because the client is
// this build's and sends this build's number. The helper refuses it and writes
// NOTHING to stdout — the sentinel is written only after the version check
// passes (D5) — so a coordinator cannot read a sentinel and believe it found a
// helper it agrees with.
func TestThePreviousCoordinatorAndThisHelperRefuseEachOtherAtHello(t *testing.T) {
	hello := proto.Hello{Version: previousGeneration, Nonce: "n", Corr: "c"}
	raw, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	in := bytes.NewReader(proto.EncodeFrame(proto.TypeHello, 0, 0, raw))
	var out bytes.Buffer
	h := host.New(in, &out, "testhash", "instance-1", slog.New(slog.NewTextHandler(io.Discard, nil)))

	serveErr := h.Serve(context.Background())
	if !errors.Is(serveErr, host.ErrVersionMismatch) {
		t.Fatalf("this build's helper served a %s hello with %v, want ErrVersionMismatch", previousGeneration, serveErr)
	}
	if out.Len() != 0 {
		t.Fatalf("the helper wrote %q to stdout before refusing a %s hello, want nothing (D5)", out.String(), previousGeneration)
	}
	// WHAT EACH SIDE SAID. The refusal names the version it was given and its
	// own, which is the sentence a person reads when a host holds an older
	// helper.
	if !strings.Contains(serveErr.Error(), `"`+previousGeneration+`"`) || !strings.Contains(serveErr.Error(), `"`+proto.Version+`"`) {
		t.Fatalf("the refusal is %q, want it to name the %s hello it was given and its own %s", serveErr, previousGeneration, proto.Version)
	}
}

// TestThisCoordinatorRefusesAHelloOKThatNamesAnotherGeneration closes the third
// corner. The coordinator's own handshake has two version-bearing halves and
// both are checked: the SENTINEL line spells the protocol version, so a helper
// of another generation never matches it and is classified by its exit status
// (the case above), and the hello-ok ECHO names the version too — a peer that
// printed this build's sentinel and then answered with another generation's
// number is refused rather than believed.
func TestThisCoordinatorRefusesAHelloOKThatNamesAnotherGeneration(t *testing.T) {
	conn := newFakeConn(func(in io.Reader, out io.Writer) int {
		answered := false
		dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			if answered || ty != proto.TypeHello {
				return
			}
			answered = true
			var hello proto.Hello
			_ = json.Unmarshal(payload, &hello)
			// This build's SENTINEL — so the handshake gets past the line scan —
			// and the previous generation's number in the echo.
			_, _ = fmt.Fprintf(out, "nocx-helper %s ready\n", proto.Version)
			ok := proto.HelloOK{Version: previousGeneration, Nonce: hello.Nonce, ContentHash: "testhash", InstanceID: "i"}
			body, _ := json.Marshal(ok)
			_, _ = out.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, body))
		}, nil)
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				_ = dec.Feed(buf[:n])
			}
			if err != nil {
				return 0
			}
		}
	})

	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if !errors.Is(err, client.ErrVersionMismatch) {
		t.Fatalf("a hello-ok naming %s answered %v, want ErrVersionMismatch", previousGeneration, err)
	}
	if c != nil {
		t.Fatal("an incompatible hello-ok returned an input-capable client")
	}
}
