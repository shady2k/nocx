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
	"os/exec"
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

// Ensure connects to the endpoint for gen, starting a helper from exe when
// nothing is serving it yet.
//
// exeGen is the generation exe IS. Starting a helper for a generation this
// binary is not would publish somebody else's name over our sessions, so it is
// refused rather than approximated — and the refusal is ErrNoEndpoint, because
// that is what is true: no helper of that generation is serving, and this
// process is not the one that can change it.
//
// Two processes may reach here at once; both may start a helper. The one that
// loses the bind answers ErrAlreadyServing to itself and exits, and both then
// dial the winner. The race is resolved by the socket, which is the only
// authority present on both sides of it.
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
//
// What is left over is therefore at most one extra helper process of our own
// generation, and the design already has an answer for that: a second helper
// finding one serving exits 0 without changing anything. What it does NOT
// cover is the daemon nobody ends up wanting — nothing retires a generation
// yet, so it runs until it is signalled. That is D2's, unimplemented, and it
// is stated here rather than papered over.
func Ensure(ctx context.Context, dir string, gen, exeGen proto.GenerationID, exe string) (net.Conn, error) {
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

	cmd := exec.Command(exe, ServeCommand) // #nosec G204 — exe is os.Executable(), never a caller's string
	// Detached from this process's streams and its terminal: an ssh exec lane
	// does not return while a child holds its stdout, so a helper that
	// inherited it would keep the lane open for as long as it serves — which
	// is the whole point of it, forever.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("endpoint: start a helper for %s: %w", short(gen), err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Waiting on two observable facts — the socket accepting, and the child
	// ending — and never on a duration. A child that ends first is not
	// necessarily a failure: the race loser exits 0 while the winner serves,
	// so the socket is asked once more before its exit is believed.
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case werr := <-exited:
			conn, derr := Dial(ctx, dir, gen)
			if derr == nil {
				return conn, nil
			}
			if werr != nil {
				return nil, fmt.Errorf("endpoint: the helper for %s exited before it served: %w", short(gen), werr)
			}
			return nil, derr
		case <-timer.C:
			conn, derr := Dial(ctx, dir, gen)
			if derr == nil {
				return conn, nil
			}
			if !errors.Is(derr, ErrNoEndpoint) {
				return nil, derr
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
