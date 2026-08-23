// Command e2e-sshd runs an in-process SSH server for the nocx e2e suite
// (e2e/shell-mode.spec.ts, e2e/nocxify-journey.spec.ts,
// e2e/api-import-url.spec.ts) that executes REAL commands on a REAL PTY with
// the REAL shell. The nocx integration path needs the far side to actually run
// `exec bash --rcfile <(...) -i` (or a plain `bash -i` shell) and emit OSC 133
// markers — an echo server cannot. Hermetic and deterministic: keys are minted
// at startup, the address is ephemeral, and everything the spec needs is
// printed machine-readable.
//
// It also carries TCP, in both directions: `tcpip-forward` with the
// `forwarded-tcpip` channels that answer it (the remote lifecycle channel,
// ADR-0024), and `direct-tcpip` (an API request routed through a connection,
// design §7.1). Everything else is refused by name.
//
// Dev-only; never shipped. Usage:
//
//	go run ./cmd/e2e-sshd [-banner <text>] [-password <pass>]
//
// Flags:
//
//	-banner <text>     send an sshd banner before authentication (the
//	                   journey's frozen local block must contain it)
//	-password <pass>   require password auth: the fixture's own key is
//	                   REFUSED, and the callback accepts only <pass>. This is
//	                   what makes a hand-typed `ssh` prompt for a password;
//	                   without it the server is public-key-only. A wrong
//	                   password (or a mismatched <pass> on a second fixture)
//	                   is the journey's authentication-failure host.
//
// Output:
//
//	ADDR=127.0.0.1:<port>
//	USERKEY=<path to the user private key, in the OpenSSH encoding>
//	KNOWNHOSTS=<one known_hosts line for the host key>
//	CONN=<client address>   printed once per client, when its first userauth
//	                        attempt of a real method (publickey or password —
//	                        never the "none" probe) reaches the server. Key
//	                        exchange is done and the client is one response from
//	                        rendering the password prompt. The journey waits for
//	                        this line before typing the password, so the run is
//	                        deterministic, not timed.
//	TCPIP=<host:port>       printed once per direct-tcpip channel, after the
//	                        server has connected to the address the channel
//	                        named and accepted the channel. A spec routing an
//	                        HTTP request through this server waits for it to
//	                        know the bytes went over the connection rather
//	                        than out of this machine's own interface.
//	READY
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-sshd:", err)
		os.Exit(1)
	}
}

func run() error {
	banner := flag.String("banner", "", "sshd banner sent before authentication")
	password := flag.String("password", "", "require password auth; accepts exactly this password and refuses every key")
	flag.Parse()

	hostSigner, _, _, err := signer()
	if err != nil {
		return err
	}
	userSigner, userKey, _, err := signer()
	if err != nil {
		return err
	}

	userKeyPath, err := writeUserKey(userKey)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = ln.Close() }()

	printLine("ADDR=" + ln.Addr().String())
	printLine("USERKEY=" + userKeyPath)
	printLine("KNOWNHOSTS=" + knownhosts.Line([]string{ln.Addr().String()}, hostSigner.PublicKey()))
	printLine("READY")

	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil // listener closed
		}
		// CONN= is per connection: printed when the client's first userauth
		// attempt reaches the server — key exchange done, one response before
		// the client renders the password prompt. The journey waits for it
		// before typing the password; without it the run is timed, not
		// deterministic.
		var once sync.Once
		config := buildConfig(userSigner, hostSigner, *banner, *password, func() {
			once.Do(func() { printLine("CONN=" + conn.RemoteAddr().String()) })
		})
		go serveConn(conn, config)
	}
}

// buildConfig assembles the ServerConfig for one connection. onAuthAttempt
// fires on the client's first userauth attempt by any method, after key
// exchange and before the password prompt.
func buildConfig(userSigner, hostSigner gossh.Signer, banner, password string, onAuthAttempt func()) *gossh.ServerConfig {
	config := &gossh.ServerConfig{}
	if password != "" {
		// Password-auth fixture (the journey's hand-typed `ssh` must be
		// prompted): the fixture's own key is REFUSED so the client has no
		// publickey path, and the callback accepts exactly the one password.
		// A wrong password is an auth failure with the client's own exit
		// status — the journey's fail-open assertion.
		config.PasswordCallback = func(_ gossh.ConnMetadata, pass []byte) (*gossh.Permissions, error) {
			if string(pass) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("e2e-sshd: wrong password")
		}
		config.PublicKeyCallback = func(_ gossh.ConnMetadata, _ gossh.PublicKey) (*gossh.Permissions, error) {
			return nil, fmt.Errorf("e2e-sshd: public key auth disabled")
		}
	} else {
		config.PublicKeyCallback = func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			// Compare the wire blob (algorithm + key), not the raw key: the
			// client sends key.Marshal(), which for ed25519 carries the
			// algorithm string ahead of the 32-byte key.
			if string(key.Marshal()) == string(userSigner.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("e2e-sshd: unknown public key")
		}
	}
	// The CONN= signal, from ONE place and for every authentication method.
	//
	// It used to hang off PublicKeyCallback, which made it a statement about
	// how the client chose to authenticate rather than about whether it
	// arrived. A client that offers no key — because its key would not load,
	// because it has none, because it was told not to — connected, authenticated
	// by password and completed the whole session while the fixture stayed
	// silent, and the waiting spec reported "saw 0/1 CONN= lines in 30000ms":
	// a timeout naming the signal instead of the cause (nocx-z9s9.12).
	//
	// "none" is excluded deliberately, and that exclusion is the timing
	// contract. Every OpenSSH client opens with a `none` probe purely to learn
	// which methods exist; firing on it would signal roughly a whole round trip
	// before the client has rendered anything, and the journey types its
	// password the moment this line arrives. Measured: firing on `none` got the
	// journey past its old timeout and then failed it at the SECOND connection
	// with two entered blocks instead of three — a password typed into a prompt
	// that was not up yet.
	//
	// The first attempt of a real method is what the old publickey callback
	// happened to catch, and it is what the waiter actually needs: key exchange
	// done, the server's method list delivered, the client one response away
	// from a prompt.
	config.AuthLogCallback = func(_ gossh.ConnMetadata, method string, _ error) {
		if method == "none" {
			return
		}
		onAuthAttempt()
	}
	if banner != "" {
		b := banner
		config.BannerCallback = func(_ gossh.ConnMetadata) string { return b }
	}
	config.AddHostKey(hostSigner)
	return config
}

func signer() (gossh.Signer, ed25519.PrivateKey, ed25519.PublicKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate key: %w", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("signer: %w", err)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, nil, nil, fmt.Errorf("signer: unexpected public key type")
	}
	return signer, priv, pub, nil
}

func writeUserKey(priv ed25519.PrivateKey) (string, error) {
	dir, err := os.MkdirTemp("", "nocx-e2e-sshd-*")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	path := dir + "/id_e2e"
	// The OpenSSH encoding, because the reader is the OpenSSH CLIENT.
	//
	// This used to write PKCS#8, which OpenSSH cannot load for ed25519 at all:
	// `ssh -i` reports `Load key "…": invalid format`, offers no key, and then
	// authenticates by password as though nothing were wrong. Go's own
	// ssh.ParsePrivateKey reads both encodings, so every Go-side check of this
	// file passed throughout (nocx-z9s9.12).
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", fmt.Errorf("marshal key: %w", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", fmt.Errorf("write key: %w", err)
	}
	return path, nil
}

func serveConn(conn net.Conn, config *gossh.ServerConfig) {
	defer func() { _ = conn.Close() }()
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()
	fwd := &forwards{conn: sshConn, listeners: map[string]net.Listener{}}
	defer fwd.closeAll()
	go fwd.serveGlobalRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "session":
			ch, reqs, err := newChan.Accept()
			if err != nil {
				return
			}
			go handleSession(ch, reqs)
		case "direct-tcpip":
			go serveDirectTCPIP(newChan)
		default:
			// Everything else is still refused BY NAME. The fixture serves
			// what nocx asks of a far side and nothing more: a channel type
			// it does not implement must fail as a real sshd fails it, not
			// be accepted and quietly dropped.
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
		}
	}
}

// directTCPIPDialTimeout bounds the fixture's own dial to the address a
// channel names. A refusal is a legitimate answer and arrives on its own —
// a closed port answers RST at once — but a filtered or blackholed address
// answers nothing at all, and a fixture that waits forever on it turns a
// wrong address in a spec into a timeout somewhere else entirely. Bounded
// here, so the far side always says something.
const directTCPIPDialTimeout = 10 * time.Second

// serveDirectTCPIP answers the channel a `-L`-shaped dial opens: the client
// names an address, the SERVER connects to it, and the two are spliced.
//
// This is the traffic an API request routed through a connection is made of.
// `apisend`'s route leases the pooled SSH connection and dials through it
// (apisend/routes.go → apisend/ssh_dialer.go → ssh.tunnelConn.Dial →
// gossh.Client.Dial), which is exactly one direct-tcpip channel per HTTP
// connection. While this fixture rejected the type, the only SSH server the
// e2e suite can start forwarded no TCP at all, so the connection half of the
// import — the half the feature exists for — could not be watched end to end
// by anything: a routed fetch failed here for a reason that has nothing to
// do with the product (nocx-n4rep).
//
// Same principle as the denied `tcpip-forward` above: a fixture may be
// small, it may not be dishonest about the protocol.
//
// THE DIAL HAPPENS BEFORE THE ACCEPT, deliberately. RFC 4254 §7.2 says the
// server opens the connection and answers the open with success or failure,
// and gossh's client turns a rejection into an error from Dial. Accepting
// first and closing the channel afterwards would report a connection that
// briefly existed instead of one that was refused — and the caller above it
// (`apisend`) distinguishes exactly those two.
//
// It reaches only what the channel names, and every spec that starts this
// fixture names a loopback address it bound itself. Nothing here consults
// the developer's ssh agent, ~/.ssh or any host outside the run.
func serveDirectTCPIP(newChan gossh.NewChannel) {
	// RFC 4254 §7.2: the address the SERVER should connect to, then the
	// originator's. gossh's channelOpenDirectMsg marshals them in that order.
	var p struct {
		DestAddr   string
		DestPort   uint32
		OriginAddr string
		OriginPort uint32
	}
	if err := gossh.Unmarshal(newChan.ExtraData(), &p); err != nil {
		_ = newChan.Reject(gossh.ConnectionFailed, "direct-tcpip: unreadable payload")
		return
	}
	dest := net.JoinHostPort(p.DestAddr, strconv.FormatUint(uint64(p.DestPort), 10))
	c, err := net.DialTimeout("tcp", dest, directTCPIPDialTimeout)
	if err != nil {
		_ = newChan.Reject(gossh.ConnectionFailed, fmt.Sprintf("direct-tcpip: dial %s: %v", dest, err))
		return
	}
	defer func() { _ = c.Close() }()

	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go gossh.DiscardRequests(reqs)

	// TCPIP= is the fixture's account of having carried the traffic, and it
	// is printed only once the channel is open and the far end connected —
	// so a spec waiting for it is waiting on a state and not on a duration.
	// Without it, a routed fetch and a direct one look identical from the
	// outside when both endpoints are on this machine's loopback, and the
	// spec would be asserting that the import worked rather than that it
	// went where the person sent it.
	printLine(fmt.Sprintf("TCPIP=%s", dest))

	pipe(ch, c)
}

// stdoutMu serialises the machine-readable lines. Until direct-tcpip there
// was one writer per connection and one at startup; a forwarded channel is
// opened per HTTP connection and several can be in flight at once, so the
// reader — which splits on newlines — needs the writes not to interleave.
var stdoutMu sync.Mutex

// printLine writes one machine-readable line to stdout and flushes it. It is
// the ONE place any of them is printed: a second spelling would be a second
// answer to "what does this server say when it does something", and the
// flush is what makes a spec waiting on a line see it rather than wait out a
// buffer.
func printLine(s string) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	fmt.Println(s)
	_ = os.Stdout.Sync()
}

// pipe splices an SSH channel and a TCP connection, returning when either
// direction ends. The caller closes both, which unblocks the copy that is
// still running.
//
// One owner for "join these two", used by the direct-tcpip channel above and
// by the forwarded-tcpip one below: the two differ only in which side opened
// the channel, and a second copy of the loop would be a second answer to one
// question.
func pipe(ch gossh.Channel, c net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(ch, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, ch); done <- struct{}{} }()
	<-done
}

// forwards implements remote port forwarding — the `tcpip-forward` global
// request and the `forwarded-tcpip` channels that answer it.
//
// This fixture used to hand every global request to gossh.DiscardRequests,
// which replies "denied" to anything wanting an answer. The product's remote
// lifecycle channel is built on remote forwarding (ADR-0024), so every SSH
// session this fixture served came up conventional — "lifecycle channel
// refused; session stays conventional" — and the specs that watch a remote
// shell produce blocks could never have passed. They were failing on main for
// this, not for anything in the product (nocx-cbtc).
//
// A fixture may be small; it may not be dishonest about the protocol. Denying
// a request the real server grants makes every test above it prove the wrong
// thing.
type forwards struct {
	conn      gossh.Conn
	mu        sync.Mutex
	listeners map[string]net.Listener
}

// serveGlobalRequests answers tcpip-forward and cancel-tcpip-forward, and
// refuses the rest the way DiscardRequests did.
func (f *forwards) serveGlobalRequests(reqs <-chan *gossh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			addr, port, err := f.listen(req.Payload)
			if err != nil {
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
				continue
			}
			if req.WantReply {
				// RFC 4254 §7.1: when the client asked for port 0 the reply
				// carries the port actually bound, and the client addresses
				// the forward by it.
				_ = req.Reply(true, gossh.Marshal(struct{ Port uint32 }{port}))
			}
			_ = addr
		case "cancel-tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			if err := gossh.Unmarshal(req.Payload, &p); err == nil {
				f.cancel(fmt.Sprintf("%s:%d", p.Addr, p.Port))
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// listen binds the requested address and serves it until cancelled.
func (f *forwards) listen(payload []byte) (string, uint32, error) {
	var p struct {
		Addr string
		Port uint32
	}
	if err := gossh.Unmarshal(payload, &p); err != nil {
		return "", 0, fmt.Errorf("tcpip-forward payload: %w", err)
	}
	// The bind address is honoured as asked, and 127.0.0.1 is what the
	// product requests (ADR-0024 pins the literal, never "localhost").
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", p.Addr, p.Port))
	if err != nil {
		return "", 0, fmt.Errorf("tcpip-forward listen: %w", err)
	}
	bound, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return "", 0, fmt.Errorf("tcpip-forward: not a tcp listener")
	}
	key := fmt.Sprintf("%s:%d", p.Addr, p.Port)
	f.mu.Lock()
	f.listeners[key] = ln
	f.mu.Unlock()

	go f.accept(ln, p.Addr, uint32(bound.Port)) // #nosec G115 -- a bound TCP port is 0..65535
	return p.Addr, uint32(bound.Port), nil      // #nosec G115 -- same
}

// accept opens one forwarded-tcpip channel per inbound connection and splices
// it to the client, which is the whole point of the forward.
func (f *forwards) accept(ln net.Listener, bindAddr string, bindPort uint32) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go f.splice(c, bindAddr, bindPort)
	}
}

func (f *forwards) splice(c net.Conn, bindAddr string, bindPort uint32) {
	defer func() { _ = c.Close() }()
	origin, _ := c.RemoteAddr().(*net.TCPAddr)
	var originAddr string
	var originPort uint32
	if origin != nil {
		originAddr = origin.IP.String()
		originPort = uint32(origin.Port) // #nosec G115 -- a TCP port is 0..65535
	}
	payload := gossh.Marshal(struct {
		Addr       string
		Port       uint32
		OriginAddr string
		OriginPort uint32
	}{bindAddr, bindPort, originAddr, originPort})

	ch, reqs, err := f.conn.OpenChannel("forwarded-tcpip", payload)
	if err != nil {
		return
	}
	defer func() { _ = ch.Close() }()
	go gossh.DiscardRequests(reqs)

	pipe(ch, c)
}

func (f *forwards) cancel(key string) {
	f.mu.Lock()
	ln, ok := f.listeners[key]
	delete(f.listeners, key)
	f.mu.Unlock()
	if ok {
		_ = ln.Close()
	}
}

func (f *forwards) closeAll() {
	f.mu.Lock()
	lns := make([]net.Listener, 0, len(f.listeners))
	for k, ln := range f.listeners {
		lns = append(lns, ln)
		delete(f.listeners, k)
	}
	f.mu.Unlock()
	for _, ln := range lns {
		_ = ln.Close()
	}
}

type sessionState struct {
	mu      sync.Mutex
	cols    uint16
	rows    uint16
	slave   *os.File
	started bool
}

// clampU16 bounds a window-size field before the narrowing conversion
// (gosec G115 wants the check, not a raw cast).
func clampU16(v uint32) uint16 {
	if v > 65535 {
		return 65535
	}
	return uint16(v)
}

func handleSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	st := &sessionState{cols: 80, rows: 24}
	done := make(chan struct{})

	go func() {
		defer close(done)
		for req := range reqs {
			switch req.Type {
			case "pty-req":
				// RFC 4254 §6.2: term, cols, rows, width, height, modes.
				var p struct {
					Term  string
					Cols  uint32
					Rows  uint32
					W     uint32
					H     uint32
					Modes string
				}
				if gossh.Unmarshal(req.Payload, &p) == nil {
					st.mu.Lock()
					st.cols = clampU16(p.Cols)
					st.rows = clampU16(p.Rows)
					st.mu.Unlock()
				}
				_ = req.Reply(true, nil)
			case "window-change":
				var w struct {
					Cols uint32
					Rows uint32
					W    uint32
					H    uint32
				}
				if gossh.Unmarshal(req.Payload, &w) == nil {
					st.mu.Lock()
					st.cols = clampU16(w.Cols)
					st.rows = clampU16(w.Rows)
					slave := st.slave
					st.mu.Unlock()
					if slave != nil {
						rows := clampU16(w.Rows)
						cols := clampU16(w.Cols)
						_ = pty.Setsize(slave, &pty.Winsize{Rows: rows, Cols: cols})
					}
				}
				_ = req.Reply(true, nil)
			case "shell":
				_ = req.Reply(true, nil)
				startCommand(ch, st, "exec bash -i")
			case "exec":
				var e struct{ Command string }
				if gossh.Unmarshal(req.Payload, &e) != nil {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				startCommand(ch, st, e.Command)
			case "subsystem":
				// RFC 4254 Â§6.5: one string, the subsystem name. OpenSSH
				// ships `internal-sftp` and is configured with it by default,
				// so a far side that refuses this request is not a smaller
				// sshd â it is a different one, and nocx publishes its
				// integration bundle over SFTP and nothing else (ADR-0035).
				// While this fell through to `default`, every connection to
				// the fixture came up "Not integrated", which is the same
				// class of dishonesty as the denied tcpip-forward above.
				var sub struct{ Name string }
				if gossh.Unmarshal(req.Payload, &sub) != nil || sub.Name != "sftp" {
					_ = req.Reply(false, nil)
					continue
				}
				_ = req.Reply(true, nil)
				startSFTP(ch, st)
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()
	<-done
	_ = ch.Close()
}

// startCommand runs the given command on a fresh PTY with the real bash and
// wires it to the channel. The command is wrapped in `bash -c` so launcher
// strings (`exec bash --rcfile <(...) -i`) execute as shell constructs. The
// parent's copy of the slave is closed after Start, mirroring pty.Start: the
// child alone holds the slave, so its exit propagates EOF to the master and
// the channel closes. stderr of the child is echoed to the fixture's stderr
// so a spawn failure is observable instead of a silent dead session.
// sessionEnv is the environment a session gets, and SHELL is why it exists.
//
// A real sshd sets SHELL from the account's passwd entry, and nocx's installed
// launcher carrier reads exactly that to choose its script:
//
//	case "${SHELL:-/bin/sh}" in */bash) … */zsh) … *) … posix
//
// This used to pass os.Environ() straight through, so SHELL was whatever had
// leaked in from whoever started the fixture — and in the e2e container nothing
// sets it. Every connection over the installed launcher therefore took the POSIX
// fallback and reported `tier=minimal`, while the first connection's argv-borne
// launcher — which the backend picks, not the remote — reported `tier=enhanced`.
// One host, two answers to "which shell is this", depending only on which path
// asked (nocx-z9s9.13).
//
// Set explicitly rather than inherited: this fixture already decides the login
// shell by exec'ing bash, and a decision it makes is a decision it should
// publish.
func sessionEnv(shell string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SHELL=") || strings.HasPrefix(kv, "TERM=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "SHELL="+shell, "TERM=xterm-256color")
}

// claim reports whether this channel is still free to start something. A
// session channel runs exactly one thing â a shell, an exec or a subsystem
// â and the second request to arrive is refused rather than raced.
func (st *sessionState) claim() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.started {
		return false
	}
	st.started = true
	return true
}

// startSFTP serves the SFTP subsystem on the channel, which is what OpenSSH's
// `internal-sftp` does and what the nocx publisher speaks to.
//
// pkg/sftp's server advertises posix-rename@openssh.com, and that matters
// rather than being incidental: sftpFS.Rename refuses to replace an existing
// destination on a server without it (there is deliberately no
// remove-then-rename fallback), so a fixture lacking the extension would take
// a first publish and refuse every manifest upgrade after it â a far side
// that half-works is worse than one that refuses, because the spec above it
// would prove the wrong thing.
//
// The bundle is written under the session's own $HOME, which the e2e home
// boundary has already moved into the run's disposable directory: this serves
// the real filesystem, exactly as a real sftp-server does, and the isolation
// stays where it already is.
func startSFTP(ch gossh.Channel, st *sessionState) {
	if !st.claim() {
		return
	}
	srv, err := sftp.NewServer(ch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e-sshd: sftp server:", err)
		_ = ch.Close()
		return
	}
	go func() {
		// Serve returns io.EOF when the client closes the session, which is
		// the ordinary end of a publish and not a failure.
		serveErr := srv.Serve()
		_ = srv.Close()
		status := uint32(0)
		if serveErr != nil && !errors.Is(serveErr, io.EOF) {
			fmt.Fprintln(os.Stderr, "e2e-sshd: sftp serve:", serveErr)
			status = 1
		}
		// The same contract as startCommand: an explicit exit-status and then
		// EOF, because a real ssh client waits for both.
		_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{Status: status}))
		_ = ch.Close()
	}()
}

func startCommand(ch gossh.Channel, st *sessionState, command string) {
	if !st.claim() {
		return
	}

	master, slave, err := pty.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e-sshd: pty open:", err)
		return
	}
	st.mu.Lock()
	st.slave = slave
	cols, rows := st.cols, st.rows
	st.mu.Unlock()
	_ = pty.Setsize(slave, &pty.Winsize{Rows: rows, Cols: cols})

	bash, err := exec.LookPath("bash")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e-sshd: bash not found:", err)
		_ = slave.Close()
		_ = master.Close()
		return
	}
	//nolint:gosec // dev-only fixture: the command string is this binary's own contract.
	cmd := exec.Command(bash, "-c", command)
	cmd.Env = sessionEnv(bash)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = ptySetctty()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-sshd: spawn:", err)
		_ = slave.Close()
		_ = master.Close()
		return
	}
	// The child holds the slave through its stdio fds; the parent's copy is
	// closed so the child's exit produces EOF on the master.
	_ = slave.Close()

	go func() {
		_, _ = io.Copy(ch, master)
		_ = cmd.Wait()
		// A real ssh client terminates only on an explicit exit-status
		// followed by channel EOF (the journey's `exit` must end the remote
		// session cleanly and hand the real code to the local shell). The
		// shell's own `exit N` is the child's exit status; without this the
		// server waits for the client to close the channel while the client
		// waits for the server — a deadlock that looks like a hung ssh.
		code := 0
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		// A negative code means the process was signalled; the wire field is
		// unsigned, and the fixture only has to be faithful about ordinary
		// exits, so a signal reports as 255 the way a shell would.
		if code < 0 {
			code = 255
		}
		_, _ = ch.SendRequest(
			"exit-status",
			false,
			gossh.Marshal(struct{ Status uint32 }{Status: uint32(code)}), // #nosec G115 — clamped non-negative just above.
		)
		_ = master.Close()
		_ = ch.Close()
	}()
	go func() {
		_, _ = io.Copy(master, ch)
		// THE CLIENT HALF-CLOSING ITS STDIN IS NOT THE END OF THE COMMAND,
		// and closing the master here said it was.
		//
		// gossh sends channel EOF the moment a session with no Stdin starts,
		// which is every `sess.Output(...)` — so `exec` ran, the master was
		// closed under it, the child took SIGHUP from its own controlling
		// terminal and every exec on this fixture answered "Process exited
		// with status 255" with no output at all. Nothing noticed while the
		// integration bundle travelled in the ssh command line: the one
		// caller is GetRemoteHome, whose failure was fail-open and cost the
		// old path nothing. ADR-0035 moved the bundle onto SFTP, which needs
		// that home, so a fixture that cannot run `echo $HOME` now reports
		// itself to the user as "nocx could not copy its shell integration to
		// this host".
		//
		// So NOTHING HAPPENS HERE, which is also what a real sshd does: this
		// session has a PTY, a PTY cannot be half-closed, and OpenSSH answers
		// a client stdin EOF on a pty session by simply ceasing to write. The
		// master belongs to the exit goroutine above, which closes it once
		// the child is reaped.
		//
		// The cost is stated rather than hidden: a command that READS stdin
		// would now wait for input that can never arrive. Every caller of
		// exec here — GetRemoteHome's `echo $HOME`, the command-name probe
		// and scan — reads none, and injecting an EOT to cover the case that
		// does not exist is not faithfulness but invention: it also reaches
		// the interactive shell on this fixture, where ^D means "exit".
	}()
}
