package endpoint

// The bridge and the start it may have to do first.
//
// Remotely NOTHING IS FORWARDED: `nocx-helper bridge <generation>` runs over
// the pty-less ssh exec lane, connects to the endpoint on the far side and
// copies bytes. No port forwarding is configured and none is required. The
// bridge is stateless and disposable — it holds no session, no window and no
// lock — so its death is one attachment ending (D2), never a session ending.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// Bridge copies bytes between a carrier's two halves and a connection to the
// endpoint, until the endpoint's side of the stream ends or the carrier dies.
//
// The direction that ENDS it is the endpoint's, and that is D6's "EOF ends the
// bridge rather than the daemon" read the only way that keeps both promises:
// when the coordinator goes away, in reaches EOF and the write half is closed,
// which is the EOF the helper's read loop needs to release that connection —
// and this side goes on draining whatever the helper had already written,
// rather than truncating an answer that was in flight. When the ssh channel
// dies instead, the copy out of the endpoint fails on its write and the bridge
// ends on that. Either way the helper keeps its sessions, its windows and its
// processes: what died was an attachment.
func Bridge(ctx context.Context, in io.Reader, out io.Writer, conn net.Conn) error {
	upstream := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, in)
		// Half-close, so the helper's read loop sees the end of THIS
		// connection without the connection being torn out from under the
		// bytes it is still writing back.
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		upstream <- err
	}()

	downstream := make(chan error, 1)
	go func() {
		_, err := io.Copy(out, conn)
		downstream <- err
	}()

	select {
	case err := <-downstream:
		return err
	case err := <-upstream:
		// The carrier's input died with an error rather than reaching EOF:
		// the lane is gone, so there is nobody left to hand the endpoint's
		// answer to. An EOF is not this case — it falls through to the
		// downstream wait above, which is what drains the answer.
		if err == nil {
			select {
			case derr := <-downstream:
				return derr
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startRetry is how often Ensure re-dials while a helper it started comes up.
// It is not a bound on anything: the bound is the caller's context, and the
// two facts Ensure waits on are the socket accepting and the child exiting,
// both observable. This is only how often it looks.
const startRetry = 10 * time.Millisecond

// startAttempts bounds how many helpers ONE Ensure call may start.
//
// A helper that finds another of its generation already serving yields to it
// and exits 0, and that is right: the socket is the only authority present on
// both sides of the race, and starting twice is not an error to a person. But
// the socket is NOT an authority on whether that peer is still there a moment
// later. A daemon that owns no session retires after its startup grace, and one
// that has just lost its last session retires at once; either way its endpoint
// is unlinked as it goes (D2). A caller that believed the first yield would
// then report "no helper is serving" for a generation it is perfectly able to
// serve — which is exactly what a pane said on CI run 39057679352 (webkit-1):
// "Nocx installed this machine's helper and it did not start", over a socket a
// retiring daemon had removed between the yield and the dial that followed it.
//
// Three, because each attempt costs a process and the events are rare. The
// first is the ordinary path; the second covers a helper that yielded to a peer
// which then left; the third covers a second peer appearing while the
// replacement comes up. Past that the caller is told what actually happened
// rather than handed a bare "nothing is serving".
const startAttempts = 3

// Ensure connects to the endpoint for gen, starting a helper from exe when
// nothing is serving it yet.
//
// exeGen is the generation exe IS. Starting a helper for a generation this
// binary is not would publish somebody else's name over our sessions, so it is
// refused rather than approximated — and the refusal is ErrNoEndpoint, because
// that is what is true: no helper of that generation is serving, and this
// process is not the one that can change it.
//
// # A start yields, and a yield is not a verdict
//
// Two processes may reach here at once; both may start a helper. The one that
// loses answers ErrAlreadyServing to itself — or finds the winner by handshake
// — and exits 0, having changed nothing. The race is resolved by the socket,
// which is the only authority present on both sides of it.
//
// WHAT THE LOSER'S EXIT DOES NOT SAY IS THAT THE ENDPOINT IS SERVED. The peer
// it yielded to may be a daemon that owns no session and is about to retire,
// and when such a daemon goes it unlinks the very socket the caller was about
// to dial. So a child that ends SUCCESSFULLY is treated as a YIELD: the dial
// after it is asked, and if the endpoint is unserved the whole start is made
// again, up to startAttempts. That is the caller waiting on the socket's
// creation event rather than on a duration; the two facts it waits on are still
// the socket accepting and the child ending.
//
// A child that ends UNSUCCESSFULLY stays a failure on the first attempt and is
// not retried: the binary did not come up, and starting it again would only
// repeat that. What is left when every attempt yields is ErrNoEndpoint with the
// yielding in the sentence, so the refusal a person meets names what happened.
//
// # A cancelled caller does not kill the child, and that is the right answer
//
// When ctx ends while a helper this call started is still coming up, Ensure
// returns and the child keeps going. WHAT THAT DAEMON IS: a legitimate helper
// of exactly this generation — it was started from a binary whose content hash
// IS gen, which is checked above — so if it comes up it serves the same
// endpoint every other caller wants. Killing it would be the wrong repair: by
// the time we noticed it may already hold somebody's PTY, and a process that
// gave up waiting has learned nothing that entitles it to end a helper.
//
// HOW A RETRY TELLS IT FROM A STALE SOCKET: it does not have to reason about
// it, because the distinction is made by ASKING rather than by inferring from
// a file. A retry dials. A socket that ACCEPTS is a live daemon and the
// handshake — not the file, and not this call's memory of having started
// something — decides whether what is behind it is our generation. A socket
// that REFUSES is stale by definition: net.Listen creates the file already
// bound and listening, so a daemon that is merely slow to come up has no
// socket yet, and one that is slow to reach its accept loop still accepts
// (the kernel queues the connection). A stale one is unlinked by the next
// helper about to bind (Listen → clearStale) and reported as ErrNoEndpoint by
// anyone else; nothing else repairs it, deliberately.
func Ensure(ctx context.Context, dir string, gen, exeGen proto.GenerationID, exe string) (net.Conn, error) {
	for attempt := 1; ; attempt++ {
		conn, err := Dial(ctx, dir, gen)
		if err == nil {
			return conn, nil
		}
		if !errors.Is(err, ErrNoEndpoint) {
			return nil, err
		}
		if gen != exeGen {
			return nil, fmt.Errorf("%w: this binary is generation %s and may not serve %s",
				ErrNoEndpoint, short(exeGen), short(gen))
		}
		conn, yielded, err := startAndAwait(ctx, dir, gen, exe)
		if err != nil {
			return nil, err
		}
		if !yielded {
			return conn, nil
		}
		if attempt == startAttempts {
			return nil, fmt.Errorf("%w: %d helpers were started for %s and each one yielded the endpoint to another that left it unserved",
				ErrNoEndpoint, attempt, short(gen))
		}
	}
}

// startAndAwait starts ONE helper and waits on the two observable facts — the
// endpoint accepting and the child ending — never on a duration.
//
// yielded is true when the child ended SUCCESSFULLY and left the endpoint
// unserved: it decided another helper of this generation serves, and that
// helper is not there any more. The caller owns what to do about that, and
// Ensure answers it by making the start again (see startAttempts). Every other
// outcome is a connection, or an error the caller keeps.
func startAndAwait(ctx context.Context, dir string, gen proto.GenerationID, exe string) (conn net.Conn, yielded bool, err error) {
	wait, err := startHelper(exe)
	if err != nil {
		return nil, false, fmt.Errorf("endpoint: start a helper for %s: %w", short(gen), err)
	}
	exited := make(chan error, 1)
	go func() { exited <- wait() }()

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case werr := <-exited:
			conn, derr := Dial(ctx, dir, gen)
			if derr == nil {
				return conn, false, nil
			}
			if werr != nil {
				return nil, false, fmt.Errorf("endpoint: the helper for %s exited before it served: %w", short(gen), werr)
			}
			// Ended successfully, and the socket it yielded to is gone: a
			// yield, not a failure. The endpoint is asked last so that a peer
			// still serving is answered with a connection rather than retried.
			return nil, true, nil
		case <-timer.C:
			conn, derr := Dial(ctx, dir, gen)
			if derr == nil {
				return conn, false, nil
			}
			if !errors.Is(derr, ErrNoEndpoint) {
				return nil, false, derr
			}
			timer.Reset(startRetry)
		}
	}
}

// short names a generation in a message. The whole content hash is 64
// characters and says nothing more than its head does to a reader.
func short(gen proto.GenerationID) string {
	if len(gen) <= genPrefix {
		return string(gen)
	}
	return string(gen[:genPrefix])
}
