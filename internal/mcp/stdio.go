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
	"syscall"
	"time"

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
	return stdioSessionConfig{
		transport: &ownedCommandTransport{
			lifetime: lifetime,
			command:  cfg.Command,
			argv:     append([]string(nil), cfg.Argv...),
			cwd:      cfg.Cwd,
			env:      env,
		},
		sensitive: sensitive,
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
		sensitive = append(sensitive, value)
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
}

func (t *ownedCommandTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// #nosec G204 -- command and argv are validated and passed without a shell.
	command := exec.CommandContext(t.lifetime, t.command, t.argv...)
	command.Dir = t.cwd
	command.Env = append([]string(nil), t.env...)
	proc.InOwnGroup(command)
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return nil
	}
	command.WaitDelay = stdioCloseGrace
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, errors.New("MCP stdio could not open stdout")
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("MCP stdio could not open stdin")
	}
	command.Stderr = &boundedStderr{max: stderrBound}
	if err := command.Start(); err != nil {
		return nil, errors.New("MCP stdio server could not start")
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
		_ = command.Wait()
		close(owner.done)
	}()
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
		select {
		case <-o.done:
		case <-time.After(stdioCloseGrace):
			proc.KillGroup(o.command, o.done, stdioCloseGrace)
			select {
			case <-o.done:
			case <-time.After(time.Second):
			}
		}
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
	maxFrame int
	maxTotal int
}

func (r *ownedFrameReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.frame) == 0 {
		chunk, err := r.reader.ReadSlice('\n')
		r.total += len(chunk)
		if r.total > r.maxTotal || len(r.frame)+len(chunk) > r.maxFrame {
			_ = r.owner.Close()
			return 0, ErrFrameTooLarge
		}
		r.frame = append(r.frame, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && len(r.frame) == 0 {
			return 0, err
		}
		break
	}
	n := copy(p, r.frame)
	r.frame = r.frame[n:]
	return n, nil
}

func (r *ownedFrameReader) Close() error { return r.owner.Close() }

type boundedStderr struct {
	mu    sync.Mutex
	max   int
	bytes int
}

func (b *boundedStderr) Write(p []byte) (int, error) {
	b.mu.Lock()
	if b.bytes < b.max {
		remaining := b.max - b.bytes
		if remaining > len(p) {
			remaining = len(p)
		}
		b.bytes += remaining
	}
	b.mu.Unlock()
	return len(p), nil
}

var _ sdk.Transport = (*ownedCommandTransport)(nil)
