package fishchannel

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var (
	errExecUsage    = errors.New("usage: agent-exec --data-fd N [--stdin-fd M]")
	errExecNotFound = errors.New("agent-exec: executable not found")
	errExecDenied   = errors.New("agent-exec: cannot execute target")
)

// ExecAgent consumes the same typed NUL triples as fish-channel send from a
// separate anonymous pipe. /argv is a nonempty string array; /env is an object
// of string environment overrides. The target, not Fish or this process's
// environment, receives those overrides. Success replaces this process, keeping
// its PID, stdout/stderr and inherited lifecycle descriptors. --stdin-fd M
// restores an explicitly saved original stdin after request delivery; otherwise
// stdin is preserved. Only the data fd and optional saved stdin fd close.
// Data may occupy fd0 only when --stdin-fd supplies its replacement.
func ExecAgent(ctx context.Context, args []string) (int, error) {
	if len(args) != 2 && len(args) != 4 || args[0] != "--data-fd" {
		return 2, errExecUsage
	}
	n, err := strconv.ParseUint(args[1], 10, 31)
	if err != nil || n == 1 || n == 2 {
		return 2, errExecUsage
	}
	fd := int(n)
	stdinFD := -1
	if len(args) == 4 {
		if args[2] != "--stdin-fd" {
			return 2, errExecUsage
		}
		saved, parseErr := strconv.ParseUint(args[3], 10, 31)
		if parseErr != nil || saved < 3 || int(saved) == fd {
			return 2, errExecUsage
		}
		stdinFD = int(saved)
	}
	if fd == 0 && stdinFD < 0 {
		return 2, errExecUsage
	}
	var stat unix.Stat_t
	if statErr := unix.Fstat(fd, &stat); statErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return 1, errTransport
	}
	// This explicit descriptor belongs only to request delivery. All return
	// paths close it, including cancellation and validation failure.
	open := true
	defer func() {
		if open {
			_ = unix.Close(fd)
		}
		if stdinFD >= 0 {
			_ = unix.Close(stdinFD)
		}
	}()
	if ctx.Err() != nil {
		return 130, errCanceled
	}
	op, err := newOperation(ctx)
	if err != nil {
		return 1, errTransport
	}
	op.deadline = time.Now().Add(handshakeTimeout)
	input, err := op.readInput(fd)
	var argv, env []string
	var path string
	if err == nil {
		var root *tree
		root, err = parseTriples(input)
		if err == nil {
			argv, env, err = agentRequest(root, os.Environ())
		}
		if err == nil {
			path, err = agentExecutable(op, argv[0], env)
		}
	}
	if err == nil {
		err = op.check()
	}
	op.close()
	_ = unix.Close(fd)
	open = false
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return 130, errCanceled
	}
	if err != nil {
		return agentExecFailure(err)
	}
	if stdinFD >= 0 {
		// Fish may alias virtual descriptor redirections before exec. Restore
		// stdin here, after the separate request pipe has been fully drained.
		if err := unix.Dup2(stdinFD, 0); err != nil {
			return 1, errTransport
		}
		_ = unix.Close(stdinFD)
		stdinFD = -1
	}
	// execve resets caught signal dispositions, and there is no intermediate
	// waiter to translate the target's exit status or signal termination.
	if err := syscall.Exec(path, argv, env); err != nil { //nolint:gosec // G204: execute validated configured argv directly, without shell interpolation
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return 127, errExecNotFound
		}
		return 126, errExecDenied
	}
	panic("unreachable exec return")
}

func agentExecFailure(err error) (int, error) {
	switch {
	case errors.Is(err, errData):
		return 1, errData
	case errors.Is(err, errTimeout):
		return 1, errTimeout
	case errors.Is(err, errExecNotFound), errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
		return 127, errExecNotFound
	case errors.Is(err, errExecDenied), errors.Is(err, unix.EACCES), errors.Is(err, unix.ENOEXEC), errors.Is(err, unix.EPERM), errors.Is(err, unix.E2BIG):
		return 126, errExecDenied
	default:
		return 1, errTransport
	}
}

func agentRequest(root *tree, inherited []string) ([]string, []string, error) {
	if len(root.object) != 2 || root.object["argv"] == nil || root.object["env"] == nil {
		return nil, nil, errData
	}
	args, ok := root.object["argv"].value.([]any)
	if !ok || len(args) == 0 {
		return nil, nil, errData
	}
	overrides, ok := root.object["env"].value.(map[string]any)
	if !ok {
		return nil, nil, errData
	}
	argv := make([]string, len(args))
	for i, value := range args {
		arg, ok := value.(string)
		if !ok || i == 0 && arg == "" {
			return nil, nil, errData
		}
		argv[i] = arg
	}
	if len(inherited) > maxNodes-len(overrides) {
		return nil, nil, errData
	}
	// Validate every value/name and the total environment before allocating
	// entries. Names are execve data, not shell identifiers: no Fish blacklist.
	total := 0
	for name, value := range overrides {
		text, ok := value.(string)
		if !ok || name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(text, 0) {
			return nil, nil, errData
		}
		total += len(name) + 1 + len(text) + 1
		if total > maxInputBytes {
			return nil, nil, errData
		}
	}
	for _, entry := range inherited {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; !replaced {
			total += len(entry) + 1
			if total > maxInputBytes {
				return nil, nil, errData
			}
		}
	}
	env := make([]string, 0, len(inherited)+len(overrides))
	for _, entry := range inherited {
		name, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; !replaced {
			env = append(env, entry)
		}
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		text, ok := overrides[name].(string)
		if !ok {
			return nil, nil, errData
		}
		env = append(env, name+"="+text)
	}
	return argv, env, nil
}

// There is no existing generic environment-aware executable lookup owner.
// exec.LookPath reads this process's PATH, which is deliberately unchanged.
func agentExecutable(op *operation, command string, env []string) (string, error) {
	if strings.ContainsRune(command, '/') {
		return command, nil
	}
	path := ""
	found := false
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path, found = value, true
		}
	}
	if !found {
		return "", errExecNotFound
	}
	if strings.Count(path, ":") >= maxNodes {
		return "", errData
	}
	b := budget{}
	denied := false
	for {
		if err := op.check(); err != nil {
			return "", err
		}
		dir, rest, more := strings.Cut(path, ":")
		if err := b.charge(len(dir) + 1 + len(command)); err != nil {
			return "", err
		}
		// An empty/relative target PATH component is intentionally relative
		// to the preserved cwd, just as the agent's native exec search is.
		candidate := command
		if dir != "" {
			// Preserve symlink/.. resolution; lexical cleaning can select a
			// different program from the target's native PATH search.
			candidate = dir + "/" + command
		}
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			if accessErr := unix.Access(candidate, unix.X_OK); accessErr == nil {
				return candidate, nil
			} else {
				denied = true
			}
		} else if err == nil || !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENOTDIR) {
			denied = true
		}
		if !more {
			break
		}
		path = rest
	}
	if denied {
		return "", errExecDenied
	}
	return "", errExecNotFound
}
