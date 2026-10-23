package mcp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shady2k/nocx/internal/proc"
)

const (
	stdioCloseGrace = 100 * time.Millisecond
	stderrBound     = 32 << 10
)

type stdioSessionConfig struct {
	transport sdk.Transport
	sensitive []string
	stderr    *boundedStderr
}

func buildStdioTransport(ctx, lifetime context.Context, activation Activation, resolver SecretResolver) (stdioSessionConfig, error) {
	cfg := activation.Stdio
	if cfg == nil || cfg.Command == "" {
		return stdioSessionConfig{}, ErrInvalidActivation
	}
	env, sensitive, err := resolveEnvironment(ctx, cfg.Env, resolver)
	if err != nil {
		return stdioSessionConfig{}, err
	}
	stderr := &boundedStderr{max: stderrBound}
	return stdioSessionConfig{
		transport: &ownedCommandTransport{
			lifetime: lifetime,
			command:  cfg.Command,
			argv:     append([]string(nil), cfg.Argv...),
			cwd:      cfg.Cwd,
			env:      env,
			stderr:   stderr,
		},
		sensitive: sensitive,
		stderr:    stderr,
	}, nil
}

func resolveEnvironment(ctx context.Context, bindings []Binding, resolver SecretResolver) ([]string, []string, error) {
	baseNames := []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "LC_CTYPE", "SYSTEMROOT"}
	values := make(map[string]string, len(baseNames)+len(bindings))
	order := make([]string, 0, len(baseNames)+len(bindings))
	for _, name := range baseNames {
		if value, ok := os.LookupEnv(name); ok {
			values[name] = value
			order = append(order, name)
		}
	}
	sensitive := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		var value string
		switch {
		case binding.Literal != nil && binding.SecretRef == "":
			value = *binding.Literal
		case binding.Literal == nil && binding.SecretRef != "":
			secret, err := resolveSecret(ctx, resolver, binding.SecretRef)
			if err != nil {
				return nil, nil, err
			}
			if err := secret.Use(func(material []byte) error {
				value = string(material)
				return nil
			}); err != nil {
				return nil, nil, ErrSecretUnavailable
			}
		default:
			return nil, nil, ErrInvalidActivation
		}
		if strings.IndexByte(value, 0) >= 0 {
			return nil, nil, ErrInvalidActivation
		}
		if _, exists := values[binding.Name]; !exists {
			order = append(order, binding.Name)
		}
		values[binding.Name] = value
		if binding.SecretRef != "" {
			sensitive = append(sensitive, value)
		}
	}
	env := make([]string, 0, len(order))
	for _, name := range order {
		env = append(env, name+"="+values[name])
	}
	return env, sensitive, nil
}

type ownedCommandTransport struct {
	lifetime context.Context
	command  string
	argv     []string
	cwd      string
	env      []string
	stderr   *boundedStderr
}

func (t *ownedCommandTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// #nosec G204 -- command and argv are validated and passed without a shell.
	command := exec.Command(t.command, t.argv...)
	command.Dir = t.cwd
	command.Env = append([]string(nil), t.env...)
	proc.InOwnGroup(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, ErrServerUnavailable
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, ErrServerUnavailable
	}
	command.Stderr = t.stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, ErrServerUnavailable
	}
	owner := newProcessOwner(t.lifetime, command, stdin, stdout)
	reader := &ownedFrameReader{
		owner:    owner,
		reader:   bufio.NewReaderSize(stdout, 64<<10),
		maxFrame: maxFrameBytes,
		maxTotal: maxSessionBytes,
	}
	writer := &ownedWriter{owner: owner, writer: stdin}
	return (&sdk.IOTransport{Reader: reader, Writer: writer}).Connect(ctx)
}

type processOwner struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	done      chan struct{}
	closeOnce sync.Once
}

func newProcessOwner(lifetime context.Context, command *exec.Cmd, stdin io.WriteCloser, stdout io.ReadCloser) *processOwner {
	owner := &processOwner{command: command, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	go func() {
		select {
		case <-lifetime.Done():
			_ = owner.Close()
		case <-owner.done:
		}
	}()
	return owner
}

func (o *processOwner) Close() error {
	o.closeOnce.Do(func() {
		_ = o.stdin.Close()
		// Do not reap the leader ahead of escalation: it may exit on EOF while
		// descendants with redirected streams continue using its credentials.
		<-time.After(stdioCloseGrace)
		_ = proc.TerminateGroupAndWait(o.command, stdioCloseGrace)
		close(o.done)
		_ = o.stdout.Close()
	})
	return nil
}

type ownedWriter struct {
	owner  *processOwner
	writer io.Writer
}

func (w *ownedWriter) Write(p []byte) (int, error) { return w.writer.Write(p) }
func (w *ownedWriter) Close() error                { return w.owner.Close() }

type ownedFrameReader struct {
	owner    *processOwner
	reader   *bufio.Reader
	mu       sync.Mutex
	frame    []byte
	total    int
	offset   int
	maxFrame int
	maxTotal int
	failure  error
}

func (r *ownedFrameReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	if r.failure != nil {
		return 0, r.failure
	}
	if len(r.frame) == 0 {
		for {
			chunk, err := r.reader.ReadSlice('\n')
			r.total += len(chunk)
			if r.total > r.maxTotal || len(r.frame)+len(chunk) > r.maxFrame {
				r.failure = ErrFrameTooLarge
				r.frame = nil
				_ = r.Close()
				return 0, r.failure
			}
			r.frame = append(r.frame, chunk...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				r.failure = err
				if len(r.frame) != 0 {
					r.failure = ErrProtocol
				}
				r.frame = nil
				_ = r.Close()
				return 0, r.failure
			}
			break // Only a complete, bounded newline frame reaches the decoder.
		}
	}
	n := copy(p, r.frame[r.offset:])
	r.offset += n
	if r.offset == len(r.frame) {
		r.frame = r.frame[:0]
		r.offset = 0
	}
	return n, nil
}

func (r *ownedFrameReader) Close() error {
	if r.owner != nil {
		return r.owner.Close()
	}
	return nil
}

type boundedStderr struct {
	mu        sync.Mutex
	max       int
	data      []byte
	truncated bool
}

func (b *boundedStderr) Write(p []byte) (int, error) {
	b.mu.Lock()
	if len(p) >= b.max {
		if len(p) > b.max || len(b.data) > 0 {
			b.truncated = true
		}
		b.data = append(b.data[:0], p[len(p)-b.max:]...)
	} else if overflow := len(b.data) + len(p) - b.max; overflow > 0 {
		b.truncated = true
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
		b.data = append(b.data, p...)
	} else {
		b.data = append(b.data, p...)
	}
	b.mu.Unlock()
	return len(p), nil
}

func (b *boundedStderr) safeTail(sensitive []string) string {
	b.mu.Lock()
	raw := append([]byte(nil), b.data...)
	truncated := b.truncated
	b.mu.Unlock()
	rawText := string(raw)
	for _, value := range sensitive {
		if value == "" {
			continue
		}
		rawText = strings.ReplaceAll(rawText, value, "[redacted]")
		if truncated && len(value) > b.max {
			// A token longer than the retention window may be present in full
			// only across discarded bytes; no retained fragment is safe to expose.
			rawText = ""
			continue
		}
		if truncated {
			for overlap := min(len(rawText), len(value)-1); overlap > 0; overlap-- {
				if strings.HasPrefix(rawText, value[len(value)-overlap:]) {
					rawText = "[redacted]" + rawText[overlap:]
					break
				}
			}
		}
		for overlap := min(len(rawText), len(value)-1); overlap > 0; overlap-- {
			if strings.HasSuffix(rawText, value[:overlap]) {
				rawText = rawText[:len(rawText)-overlap] + "[redacted]"
				break
			}
		}
	}
	raw = []byte(rawText)
	var cleaned strings.Builder
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		if r == utf8.RuneError && size == 1 {
			raw = raw[1:]
			continue
		}
		if r == '\n' || r == '\r' || r == '\t' {
			cleaned.WriteByte(' ')
		} else if unicode.IsPrint(r) {
			cleaned.WriteRune(r)
		}
		raw = raw[size:]
	}
	result := strings.TrimSpace(cleaned.String())
	if len(result) > 1024 {
		result = result[len(result)-1024:]
		for !utf8.ValidString(result) {
			result = result[1:]
		}
	}
	return result
}

var _ sdk.Transport = (*ownedCommandTransport)(nil)
