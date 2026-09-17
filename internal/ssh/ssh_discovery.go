package ssh

import "context"

// DiscoveryConn is the ONE exec-shaped ssh seam left in the coordinator, and it
// is one command wide.
//
// # What it is now
//
// It used to be port discovery's lease: a pooled reference this process held,
// with Exec running whatever command a caller composed. Discovery does not use
// it any more — discovery names a PROBE and this machine's helper runs it
// (nocx-50w7p.9), because D3 refuses a free-form command on the helper wire and
// a lease that could run one would be the thing being refused. What is left is
// its single remaining caller: the helper-install path's platform probe (D20),
// which converts this shape into the deploy package's own one-command seam.
//
// It is kept, rather than retyped along with the others, because that caller
// lives in internal/app/helper_git.go — a file another task owns — and because
// the conversion is honest as it stands:
//
//   - the ONLY command that reaches `Exec` is `uname -s -m`, spelled once in
//     internal/remoteprobe, and the implementation refuses anything else before
//     it could reach a wire (app.helperProbes' platformLease);
//   - the wire carries a lease id and an op name and never a command, so the
//     refusal is a local programming-error check and not a policy;
//   - what a caller observes is unchanged: the captured stdout, the remote exit
//     status, the connection-loss signal.
//
// # The loss signal
//
// Done closes when the connection under the lease is gone and LostErr says why,
// which is what a consumer watching a long-lived lease needs. It does NOT close
// on an explicit Close: an intentional stop while the connection is still
// shared must not read as connection loss.
type DiscoveryConn interface {
	// Exec runs ONE named command — the platform probe — on the pooled
	// connection, capturing stdout and stderr separately and returning the
	// remote exit status. Anything else is refused by the implementation
	// rather than sent.
	Exec(ctx context.Context, cmd string) (*ExecResult, error)
	// HostKeyFingerprint is the target host's public-key fingerprint as
	// observed at dial time — the machine's identity, which the install path
	// keys a consent decision by (ADR-0023).
	HostKeyFingerprint() string
	// Done closes when the underlying connection shuts down.
	Done() <-chan struct{}
	// LostErr reports why the connection shut down. Meaningful once Done
	// has closed; nil when the connection closed cleanly.
	LostErr() error
	// Close releases this lease's pooled reference. The connection stays open
	// for every other reference — tabs and other leases alike.
	Close() error
}

// ExecResult is the outcome of one auxiliary command: the captured stdout and
// stderr, the remote exit status, and whether a capture bound was hit
// (Truncated — the output is not complete).
type ExecResult struct {
	Stdout     []byte
	Stderr     []byte
	ExitStatus int
	Truncated  bool
}
