package client_test

// THE HELLO FENCE, PROVEN IN BOTH DIRECTIONS (nocx-xn63t.6.56).
//
// `screenCell` gained a field — whether a cell was drawn faint — and a field on
// a shared shape is the half of a wire change that cannot be added politely:
// both ends are `additionalProperties: false`, so a peer that does not know the
// field does not ignore it, and a peer that knows it cannot tell an older
// answer from a malformed one. `proto.Version` therefore moved to 23, and this
// file is the evidence that the numbers on either side of this one refuse each
// other rather than decode a shape they do not know.
//
// The two peers below are deliberately NOT the same as this build's: one speaks
// the previous generation's number, and one is this build's real host fed the
// hello a previous-generation coordinator sends. Both numbers are written out
// LITERALLY here — a test that read proto.Version on both sides would pass at
// every version and fence nothing.

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

// previousGeneration is the protocol version this bump fences off — the one
// immediately before it, which is the pair a person meets: a helper installed
// before this app. It is a literal because that is the fact being asserted.
const previousGeneration = "25"

// olderProtocolGeneration is a generation two bumps back (the frame-set bump
// before the keepalive type was removed). It is here as well so the fence is
// pinned for a peer that is not merely one version behind: the refusal must
// not depend on the distance.
const olderProtocolGeneration = "22"

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

// TestThisBuildsCoordinatorAndThePreviousHelperRefuseEachOtherAtHello is the
// direction a person meets first: the coordinator is this build's (26), and
// the helper on the host is the previous generation (25). The helper says
// nothing on stdout and exits 42; the coordinator answers ErrVersionMismatch,
// which is a state the product renders ("this machine's helper is older than
// this app") rather than a generic launch failure. The OLDER generation is
// run through the same assertion: the fence must not depend on the distance.
func TestThisBuildsCoordinatorAndThePreviousHelperRefuseEachOtherAtHello(t *testing.T) {
	for _, version := range []string{previousGeneration, olderProtocolGeneration} {
		t.Run(version, func(t *testing.T) {
			received := make(chan string, 1)
			conn := newFakeConn(previousHelperPeer(version, received))

			_, err := client.Dial(context.Background(), client.Config{
				Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
			})
			if !errors.Is(err, client.ErrVersionMismatch) {
				t.Fatalf("dialling a %s helper answered %v, want ErrVersionMismatch", version, err)
			}
			if got := <-received; got != proto.Version {
				t.Fatalf("the %s helper read hello version %q, want %q — the coordinator must say which protocol it speaks",
					version, got, proto.Version)
			}
		})
	}
}

// TestA22CoordinatorAndThisBuildsHelperRefuseEachOtherAtHello is the reverse
// pair, and it drives the REAL host: the hello is the one a
// previous-generation coordinator sends, written by hand because the client is
// this build's and sends this build's number. The helper refuses it and writes
// NOTHING to stdout — the sentinel is written only after the version check
// passes (D5) — so a coordinator cannot read a sentinel and believe it found a
// helper it agrees with.
func TestThePreviousCoordinatorAndThisBuildsHelperRefuseEachOtherAtHello(t *testing.T) {
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

// TestA23CoordinatorRefusesAHelloOKThatNamesAnotherGeneration closes the third
// corner. The coordinator's own handshake has two version-bearing halves and
// both are checked: the SENTINEL line spells the protocol version, so a helper
// of another generation never matches it and is classified by its exit status
// (the case above), and the hello-ok ECHO names the version too — a peer that
// printed this build's sentinel and then answered with another generation's
// number is refused rather than believed.
func TestThisBuildsCoordinatorRefusesAHelloOKThatNamesAnotherGeneration(t *testing.T) {
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

	_, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if !errors.Is(err, client.ErrVersionMismatch) {
		t.Fatalf("a hello-ok naming %s answered %v, want ErrVersionMismatch", previousGeneration, err)
	}
}
