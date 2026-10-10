package downloadsave

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/transfer"
)

const (
	maxHandles = 64
	handleTTL  = 60 * time.Second
	readIdle   = 30 * time.Second
)

var ticketPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Picker is the platform's save destination dialog.
type Picker interface {
	SaveFile(context.Context, string) (string, error)
}

// Destination owns the selected directory and its atomic sink.
// Close releases it only after Put has returned.
type Destination interface {
	Put(context.Context, int64, io.Reader) (transfer.Outcome, error)
	Close() error
}

// Config contains the receiver's platform and transport dependencies.
type Config struct {
	Picker             Picker
	PrepareDestination func(string) (Destination, error)
	Address            func() (string, error)
	Client             *http.Client
	Logger             *slog.Logger
	Now                func() time.Time
	Random             io.Reader
}

// Result is the only information returned from a native save operation.
type Result struct {
	Outcome string `json:"outcome"`
}

type entry struct {
	destination Destination
	state       string
	expires     time.Time
	cancel      context.CancelFunc
	body        io.Closer
	discarded   bool
}

// Service owns one-shot, in-memory save destinations and their active streams.
type Service struct {
	picker             Picker
	prepareDestination func(string) (Destination, error)
	addr               func() (string, error)
	client             *http.Client
	logger             *slog.Logger
	now                func() time.Time
	random             io.Reader

	randomMu  sync.Mutex
	mu        sync.Mutex
	closed    bool
	entries   map[string]*entry
	cleanup   sync.WaitGroup
	closeDone chan struct{}
}

// New constructs a receiver. Clock and randomness are mandatory so expiry
// and capability creation cannot silently become nondeterministic.
func New(cfg Config) (*Service, error) {
	if cfg.Picker == nil || cfg.PrepareDestination == nil || cfg.Address == nil || cfg.Now == nil || cfg.Random == nil {
		return nil, errors.New("download save service is not configured")
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	transport, ok := copyClient.Transport.(*http.Transport)
	if copyClient.Transport == nil {
		transport = &http.Transport{}
		ok = true
	}
	if !ok {
		return nil, errors.New("download HTTP client transport is unsupported")
	}
	transport = transport.Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.DisableKeepAlives = true
	transport.ResponseHeaderTimeout = 10 * time.Second
	dial := transport.DialContext
	if dial == nil {
		dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}
		dial = dialer.DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		conn, err := dial(dialCtx, network, address)
		if err != nil {
			return nil, err
		}
		return readIdleConn{Conn: conn}, nil
	}
	copyClient.Transport = transport
	copyClient.Timeout = 0
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Service{
		picker: cfg.Picker, prepareDestination: cfg.PrepareDestination, addr: cfg.Address, client: &copyClient,
		logger: cfg.Logger, now: cfg.Now, random: cfg.Random, entries: make(map[string]*entry),
		closeDone: make(chan struct{}),
	}, nil
}

// Prepare opens the native picker before any backend transfer is requested.
func (s *Service) Prepare(ctx context.Context, name string) (string, error) {
	if ctx == nil {
		return "", errors.New("download save is unavailable")
	}
	s.mu.Lock()
	expired := s.sweepLocked(s.now())
	defer func() { s.closeDestinations(expired) }()
	if s.closed || len(s.entries) >= maxHandles {
		s.mu.Unlock()
		return "", errors.New("download save is unavailable")
	}
	// Keep the slot reserved through both the prompt and destination preparation.
	handle := s.newID()
	if handle == "" {
		s.mu.Unlock()
		return "", errors.New("download save is unavailable")
	}
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	e := &entry{state: "preparing", cancel: cancelPrompt}
	s.entries[handle] = e
	s.mu.Unlock()
	s.closeDestinations(expired)
	expired = nil
	defer func() {
		cancelPrompt()
		s.mu.Lock()
		if s.entries[handle] == e && e.state == "preparing" {
			delete(s.entries, handle)
		}
		s.mu.Unlock()
	}()

	path, err := s.picker.SaveFile(promptCtx, name)
	promptReturned := s.now()
	if promptCtx.Err() != nil || ctx.Err() != nil {
		return "", nil
	}
	if err != nil {
		return "", errors.New("download save dialog failed")
	}
	if path == "" {
		return "", nil
	}
	s.mu.Lock()
	if s.closed || e.discarded || promptCtx.Err() != nil {
		s.mu.Unlock()
		return "", nil
	}
	s.cleanup.Add(1)
	s.mu.Unlock()
	defer s.cleanup.Done()
	destination, err := s.prepareDestination(path)
	if err != nil || destination == nil {
		return "", errors.New("download destination could not be prepared")
	}
	published := false
	defer func() {
		if !published {
			_ = destination.Close()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil || e.discarded {
		return "", nil
	}
	e.destination = destination
	e.state = "prepared"
	e.expires = promptReturned.Add(handleTTL)
	e.cancel = nil
	published = true
	return handle, nil
}

// Save consumes a prepared destination, streams the ticket directly into the
// atomic local sink, and confirms saved only after the sink returns success.
func (s *Service) Save(parent context.Context, handle, ticket string, size int64) (result Result) {
	active := false
	// Registered first so shutdown joins all cleanup and the final log.
	defer func() {
		if active {
			s.cleanup.Done()
		}
	}()
	defer func() {
		if s.logger != nil {
			s.logger.Debug("native download save finished", "outcome", result.Outcome)
		}
	}()
	if !validID(handle) {
		return Result{Outcome: "source-failed"}
	}
	s.mu.Lock()
	expired := s.sweepLocked(s.now())
	defer func() { s.closeDestinations(expired) }()
	e := s.entries[handle]
	if e != nil && e.state == "cancelled" {
		delete(s.entries, handle)
		s.mu.Unlock()
		return Result{Outcome: "cancelled"}
	}
	if s.closed || e == nil || e.state != "prepared" {
		s.mu.Unlock()
		return Result{Outcome: "source-failed"}
	}
	e.state = "running"
	s.cleanup.Add(1)
	active = true
	destination := e.destination
	var cancel context.CancelFunc
	defer func() {
		if cancel != nil {
			cancel()
		}
		_ = destination.Close()
		s.mu.Lock()
		delete(s.entries, handle)
		s.mu.Unlock()
	}()
	if parent == nil || !ticketPattern.MatchString(ticket) || size < 0 {
		s.mu.Unlock()
		return Result{Outcome: "source-failed"}
	}
	ctx, rawCancel := context.WithCancel(parent)
	var cancelOnce sync.Once
	cancel = func() { cancelOnce.Do(rawCancel) }
	e.cancel = cancel
	s.mu.Unlock()
	s.closeDestinations(expired)
	expired = nil

	address, err := s.addr()
	if err != nil {
		return Result{Outcome: "source-failed"}
	}
	host, port, err := loopbackHostPort(address)
	if err != nil {
		return Result{Outcome: "source-failed"}
	}
	url := "http://" + net.JoinHostPort(host, port) + "/download/" + ticket
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{Outcome: "source-failed"}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if ctx.Err() != nil {
			return Result{Outcome: "cancelled"}
		}
		return Result{Outcome: "source-failed"}
	}
	body := &onceReadCloser{ReadCloser: resp.Body}
	s.mu.Lock()
	if current := s.entries[handle]; current == e && !e.discarded {
		e.body = body
	}
	s.mu.Unlock()
	defer func() { _ = body.Close() }()
	if ctx.Err() != nil {
		return Result{Outcome: "cancelled"}
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Length") != "" || !declaresTrailer(resp.Header, resp.Trailer, "X-Nocx-Download-Status") {
		return Result{Outcome: "source-failed"}
	}
	reader := &statusReader{body: body, trailer: resp.Trailer}
	if _, err := destination.Put(ctx, size, reader); err != nil {
		var writeErr *transfer.WriteError
		if errors.As(err, &writeErr) {
			return Result{Outcome: "destination-failed"}
		}
		var cancelledErr *sourceCancelledError
		if errors.As(err, &cancelledErr) {
			return Result{Outcome: "cancelled"}
		}
		if ctx.Err() != nil {
			return Result{Outcome: "cancelled"}
		}
		var sourceErr *sourceError
		var sizeErr *transfer.SizeMismatchError
		if errors.As(err, &sourceErr) || errors.As(err, &sizeErr) {
			return Result{Outcome: "source-failed"}
		}
		return Result{Outcome: "destination-failed"}
	}
	return Result{Outcome: "saved"}
}

// Discard releases a prepared destination or interrupts an active save.
func (s *Service) Discard(handle string) {
	if !validID(handle) {
		return
	}
	s.mu.Lock()
	e := s.entries[handle]
	if e == nil || e.discarded {
		s.mu.Unlock()
		return
	}
	e.discarded = true
	var destination Destination
	if e.state == "prepared" {
		// Retain the bounded, expiring cancellation fact for a racing Save.
		e.state = "cancelled"
		destination, e.destination = e.destination, nil
	}
	if destination != nil {
		s.cleanup.Add(1)
	}
	cancel, body := e.cancel, e.body
	e.cancel, e.body = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if body != nil {
		_ = body.Close()
	}
	if destination != nil {
		_ = destination.Close()
		s.cleanup.Done()
	}
}

// Close interrupts streams and joins their cleanup, but never waits for an OS prompt.
func (s *Service) Close() {
	type pending struct {
		cancel      context.CancelFunc
		body        io.Closer
		destination Destination
	}
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return
	}
	s.closed = true
	active := make([]pending, 0, len(s.entries))
	for _, e := range s.entries {
		if e.discarded {
			continue
		}
		e.discarded = true
		p := pending{cancel: e.cancel, body: e.body}
		if e.state == "prepared" {
			p.destination, e.destination = e.destination, nil
		}
		active = append(active, p)
		e.cancel, e.body = nil, nil
	}
	clear(s.entries)
	s.mu.Unlock()
	for _, e := range active {
		if e.cancel != nil {
			e.cancel()
		}
		if e.body != nil {
			_ = e.body.Close()
		}
		if e.destination != nil {
			_ = e.destination.Close()
		}
	}
	// Adds and the closed check share mu, so no work can start during Wait.
	// Also join destination releases already removed from entries.
	s.cleanup.Wait()
	close(s.closeDone)
}

func (s *Service) sweepLocked(now time.Time) []Destination {
	var expired []Destination
	for id, e := range s.entries {
		if (e.state == "prepared" || e.state == "cancelled") && !now.Before(e.expires) {
			delete(s.entries, id)
			if e.destination != nil {
				expired = append(expired, e.destination)
				s.cleanup.Add(1)
			}
		}
	}
	return expired
}

func (s *Service) closeDestinations(destinations []Destination) {
	for _, destination := range destinations {
		_ = destination.Close()
		s.cleanup.Done()
	}
}

type sourceCancelledError struct{}

func (*sourceCancelledError) Error() string { return "native download cancelled" }

type sourceError struct{ Err error }

func (e *sourceError) Error() string { return "native download source failed" }
func (e *sourceError) Unwrap() error { return e.Err }

type onceReadCloser struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *onceReadCloser) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

type statusReader struct {
	body     io.Reader
	trailer  map[string][]string
	verified bool
}

func (r *statusReader) Read(p []byte) (int, error) {
	if r.verified {
		return 0, io.EOF
	}
	n, err := r.body.Read(p)
	if err == io.EOF {
		values := r.trailer[http.CanonicalHeaderKey("X-Nocx-Download-Status")]
		switch {
		case len(values) == 1 && values[0] == "sent":
			r.verified = true
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		case len(values) == 1 && values[0] == "cancelled":
			return n, &sourceCancelledError{}
		default:
			return n, &sourceError{Err: errors.New("invalid terminal status")}
		}
	}
	if err != nil {
		return n, &sourceError{Err: err}
	}
	return n, nil
}

func declaresTrailer(header, trailers http.Header, name string) bool {
	for _, value := range header.Values("Trailer") {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), name) {
				return true
			}
		}
	}
	values, declared := trailers[http.CanonicalHeaderKey(name)]
	return declared && values == nil
}

type readIdleConn struct{ net.Conn }

func (c readIdleConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(readIdle)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func loopbackHostPort(address string) (string, string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return "", "", errors.New("invalid backend address")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", "", errors.New("invalid backend port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", "", errors.New("backend address is not loopback")
	}
	return ip.String(), port, nil
}

func (s *Service) newID() string {
	s.randomMu.Lock()
	defer s.randomMu.Unlock()
	var raw [16]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(raw[:])
}

func validID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}
