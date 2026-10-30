package pydantic

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// Runtime names an already staged runtime. Installation verifies shipped file
// hashes; the locator reads its manifest. This owner never installs software.
type Runtime struct {
	Interpreter    string
	Entrypoint     string
	Directory      string
	ManifestDigest string
	Version        string
	// Lease is held by the coordinator and inherited by the child. Closing the
	// parent's descriptor must not make a live worker's tree prunable.
	Lease *os.File
}

// RuntimeLocator reads verified package metadata without starting Python.
type RuntimeLocator interface {
	Locate(context.Context) (Runtime, error)
}

type process struct {
	ctx         context.Context
	cmd         *exec.Cmd
	input       io.WriteCloser
	output      io.ReadCloser
	operationID string
	epoch       string
	manifest    string
	writeMu     sync.Mutex
	seq         uint64
	objectSeq   uint64
	requestSeq  atomic.Uint64
	mu          sync.Mutex
	pending     map[string]string
	incoming    map[string]bool
	hello       chan error
	ready       bool
	intentional atomic.Bool
	stopOnce    sync.Once
	failOnce    sync.Once
	reaped      chan struct{}
	readDone    chan struct{}
	onFrame     func(envelope) error
	onLoss      func(error)
}

func identity() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func startProcess(ctx context.Context, runtime Runtime, onFrame func(envelope) error, onLoss func(error)) (*process, error) {
	if !filepath.IsAbs(runtime.Interpreter) || !filepath.IsAbs(runtime.Entrypoint) || !filepath.IsAbs(runtime.Directory) || runtime.Version == "" {
		return nil, errors.New("pydantic runtime is unavailable; disable Pydantic agent (Experimental) in Settings or repair the installation")
	}
	digest, err := hex.DecodeString(runtime.ManifestDigest)
	if err != nil || len(digest) != sha256.Size {
		return nil, errors.New("pydantic: runtime manifest digest is invalid")
	}
	p := &process{ctx: ctx, operationID: identity(), epoch: identity(), manifest: runtime.ManifestDigest, pending: make(map[string]string), incoming: make(map[string]bool), hello: make(chan error, 1), reaped: make(chan struct{}), readDone: make(chan struct{}), onFrame: onFrame, onLoss: onLoss}
	// #nosec G204 -- RuntimeLocator supplies verified, pinned bundle paths; argv contains only fixed flags and locally generated identities, never model input.
	p.cmd = exec.Command(runtime.Interpreter, "-I", "-B", runtime.Entrypoint, "--operation-id", p.operationID, "--epoch", p.epoch, "--manifest-digest", runtime.ManifestDigest)
	p.cmd.Dir = runtime.Directory
	p.cmd.Env = []string{"PATH=" + filepath.Dir(runtime.Interpreter), "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	if runtime.Lease != nil {
		p.cmd.ExtraFiles = []*os.File{runtime.Lease}
	}
	if p.input, err = p.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if p.output, err = p.cmd.StdoutPipe(); err != nil {
		_ = p.input.Close()
		return nil, err
	}
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		_ = p.input.Close()
		_ = p.output.Close()
		return nil, err
	}
	if err = p.cmd.Start(); err != nil {
		_ = p.input.Close()
		_ = p.output.Close()
		_ = stderr.Close()
		return nil, err
	}
	go p.diagnostics(stderr)
	go p.readLoop()
	go func() {
		waitErr := p.cmd.Wait()
		close(p.reaped)
		if waitErr == nil {
			waitErr = io.EOF
		}
		p.fail(waitErr)
	}()
	id := p.reserve("hello")
	if err = p.send("hello", &id, helloPayload{runtime.ManifestDigest, BridgeVersion}); err != nil {
		p.stop()
		return nil, err
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case err = <-p.hello:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = errors.New("pydantic: runtime handshake timed out")
	}
	if err != nil {
		p.stop()
		return nil, err
	}
	return p, nil
}

func (p *process) reserve(reply string) string {
	id := "g:" + strconv.FormatUint(p.requestSeq.Add(1), 10)
	p.mu.Lock()
	p.pending[id] = reply
	p.mu.Unlock()
	return id
}

func (p *process) send(kind string, id *string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > objectLimit(kind) {
		return errors.New("pydantic: bridge object exceeds its payload limit")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	write := func(k string, r *string, b json.RawMessage) error {
		if kind != "cancel" && (p.intentional.Load() || p.ctx != nil && p.ctx.Err() != nil) {
			return context.Canceled
		}
		p.seq++
		return writeEnvelope(p.input, envelope{BridgeVersion, k, p.operationID, p.epoch, p.seq, r, b})
	}
	// Keep envelope metadata safely inside the frame cap.
	if len(data) < maxFrame-1024 {
		return write(kind, id, data)
	}
	if !objectKind(kind) {
		return errProtocol
	}
	p.objectSeq++
	objectID := "g:o:" + strconv.FormatUint(p.objectSeq, 10)
	ordinal := 0
	for offset := 0; offset < len(data); offset += chunkBytes {
		end := min(offset+chunkBytes, len(data))
		chunk, _ := json.Marshal(objectChunk{objectID, kind, id, len(data), ordinal, data[offset:end]})
		if err := write("object.chunk", nil, chunk); err != nil {
			return err
		}
		ordinal++
	}
	digest := sha256.Sum256(data)
	commit, _ := json.Marshal(objectCommit{objectID, kind, id, len(data), ordinal, hex.EncodeToString(digest[:])})
	return write("object.commit", nil, commit)
}

func (p *process) readLoop() {
	defer close(p.readDone)
	var seq uint64
	var objects objectReader
	for {
		e, err := readEnvelope(p.output)
		if err != nil {
			p.fail(err)
			return
		}
		if e.OperationID != p.operationID || e.Epoch != p.epoch || e.Seq != seq+1 {
			p.fail(errProtocol)
			return
		}
		seq = e.Seq
		logical, err := objects.accept(e)
		if err != nil {
			p.fail(err)
			return
		}
		if logical == nil {
			continue
		}
		e = *logical
		if err = p.validate(e); err != nil {
			p.fail(err)
			return
		}
		if e.Kind == "hello" {
			var h helloPayload
			if err = decodeStrict(e.Payload, &h); err != nil || h.ManifestDigest != p.manifest || h.BridgeVersion != BridgeVersion {
				p.fail(errProtocol)
				return
			}
			p.ready = true
			p.hello <- nil
			continue
		}
		if !p.ready {
			p.fail(errProtocol)
			return
		}
		if err = p.onFrame(e); err != nil {
			p.fail(err)
			return
		}
	}
}

func (p *process) validate(e envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Kind {
	case "hello", "complete", "generated", "error":
		if e.RequestID == nil {
			return errProtocol
		}
		expected, ok := p.pending[*e.RequestID]
		if !ok || e.Kind != "error" && e.Kind != expected {
			return errProtocol
		}
		delete(p.pending, *e.RequestID)
	case "catalog.request", "tool.invoke", "model.open":
		if e.RequestID == nil {
			return errProtocol
		}
		if _, ok := requestNumber(*e.RequestID, "p:"); !ok || p.incoming[*e.RequestID] {
			return errProtocol
		}
		p.incoming[*e.RequestID] = true
	case "event", "model.chunk", "model.close":
		if e.RequestID != nil {
			return errProtocol
		}
	default:
		return errProtocol
	}
	return nil
}

func (p *process) fail(err error) {
	if p.intentional.Load() {
		return
	}
	p.failOnce.Do(func() {
		select {
		case p.hello <- err:
		default:
		}
		// Never invoke a callback from the waiter that Discard joins.
		go p.onLoss(err)
	})
}

func (p *process) stop() {
	p.stopOnce.Do(func() {
		p.intentional.Store(true)
		// A blocked write is released by closing the pipe at the grace bound.
		go func() { _ = p.send("cancel", nil, cancelPayload{}); _ = p.input.Close() }()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-p.reaped:
		case <-timer.C:
			_ = p.input.Close()
			_ = p.cmd.Process.Kill()
			<-p.reaped
		}
		_ = p.input.Close()
		_ = p.output.Close()
		<-p.readDone
	})
}

func (p *process) diagnostics(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 16<<10)
	for scanner.Scan() {
		var diagnostic struct {
			Level string `json:"level"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(scanner.Bytes(), &diagnostic) != nil || len(diagnostic.Code) > 64 {
			continue
		}
		valid := diagnostic.Code != ""
		for _, r := range diagnostic.Code {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
				valid = false
				break
			}
		}
		if valid {
			log.From(p.ctx).Debug("assistant runtime diagnostic", "code", diagnostic.Code)
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
