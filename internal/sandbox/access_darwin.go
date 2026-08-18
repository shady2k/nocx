//go:build darwin

package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	macOSLogPath      = "/usr/bin/log"
	macOSLogLineMax   = 64 * 1024
	macOSMonitorReady = 75 * time.Millisecond
)

type darwinAccessMonitor struct {
	cancel    context.CancelFunc
	done      chan struct{}
	once      sync.Once
	onFailure func(error)
	closing   atomic.Bool
}

func startDarwinAccessMonitor(session *AccessSession, shell, token string, onFailure func(error)) (*darwinAccessMonitor, error) {
	if session == nil || token == "" {
		return nil, errors.New("invalid Seatbelt monitor identity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	predicate := `eventMessage CONTAINS "` + token + `"`
	cmd := exec.CommandContext(ctx, macOSLogPath, "stream", "--style", "json", "--predicate", predicate) //nolint:gosec // fixed binary and backend-minted token
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	monitor := &darwinAccessMonitor{cancel: cancel, done: make(chan struct{}), onFailure: onFailure}
	go monitor.consume(cmd, stdout, session, shell, token)
	select {
	case <-monitor.done:
		return nil, errors.New("macOS unified-log monitor exited during startup")
	case <-time.After(macOSMonitorReady):
		return monitor, nil
	}
}

func (m *darwinAccessMonitor) consume(cmd *exec.Cmd, stdout io.Reader, session *AccessSession, shell, token string) {
	defer close(m.done)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), macOSLogLineMax)
	for scanner.Scan() {
		var row struct {
			EventMessage     string `json:"eventMessage"`
			ProcessImagePath string `json:"processImagePath"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			if bytes.Contains(scanner.Bytes(), []byte(token)) {
				session.noteLost()
			}
			continue
		}
		if !strings.Contains(row.EventMessage, token) {
			continue
		}
		program, operation, path, access, ok := parseSeatbeltDenial(row.EventMessage)
		if !ok {
			session.noteLost()
			continue
		}
		executable := row.ProcessImagePath
		if executable == "" {
			executable = program
		}
		session.Record(AccessObservation{
			Shell: shell, Executable: executable, Path: path,
			Access: access, Operation: operation, Source: AccessSourceDarwinSeatbelt, At: time.Now().UTC(),
		})
	}
	if scanner.Err() != nil {
		session.noteLost()
	}
	waitErr := cmd.Wait()
	if !m.closing.Load() && m.onFailure != nil {
		if waitErr == nil {
			waitErr = errors.New("macOS unified-log monitor exited")
		}
		m.onFailure(waitErr)
	}
}

func (m *darwinAccessMonitor) Close() {
	if m == nil {
		return
	}
	m.once.Do(func() {
		m.closing.Store(true)
		m.cancel()
		<-m.done
	})
}
