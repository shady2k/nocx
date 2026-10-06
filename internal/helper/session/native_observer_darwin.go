//go:build darwin

package session

import (
	"bufio"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/shady2k/nocx/internal/sandbox"
)

type darwinNativeObserver struct {
	config    nativeObserverConfig
	command   *exec.Cmd
	stdout    io.ReadCloser
	done      chan struct{}
	closeOnce sync.Once
	closing   atomic.Bool
}

func startNativeObserver(config nativeObserverConfig) nativeObserver {
	observer := &darwinNativeObserver{config: config, done: make(chan struct{})}
	if !validSeatbeltNonce(config.Nonce) {
		observer.setStatus(sandbox.ObserverUnavailable)
		return observer
	}
	predicate := `(eventMessage ENDSWITH "` + config.Nonce + `")`
	observer.command = exec.Command("/usr/bin/log", "stream", "--predicate", predicate, "--style", "compact")
	observer.command.Stderr = io.Discard
	stdout, err := observer.command.StdoutPipe()
	if err != nil {
		observer.setStatus(sandbox.ObserverUnavailable)
		return observer
	}
	if err := observer.command.Start(); err != nil {
		_ = stdout.Close()
		observer.setStatus(sandbox.ObserverUnavailable)
		return observer
	}
	observer.stdout = stdout
	observer.setStatus(sandbox.ObserverActive)
	go observer.read()
	return observer
}

func (o *darwinNativeObserver) setStatus(status sandbox.ObserverStatus) {
	if o.config.Sink != nil {
		o.config.Sink.SetObserver(status)
	}
}

func (o *darwinNativeObserver) read() {
	defer close(o.done)
	reader := bufio.NewReaderSize(o.stdout, seatbeltLogReadChunk)
	var storage [seatbeltLogLineLimit]byte
	batchBytes, batchRecords := 0, 0
	parser := seatbeltLogParser{nonce: o.config.Nonce}
	for {
		line, size, err := readBoundedSeatbeltLine(reader, storage[:])
		batchBytes += size
		if observation, ok := parser.consume(string(line)); ok && o.config.Sink != nil {
			// Emit only after the immediately following private tag completes
			// this compact-log record; sparse denies never wait for a batch.
			o.config.Sink.Observe(observation)
			batchRecords++
		} else if size > seatbeltLogLineLimit && o.config.Sink != nil {
			o.config.Sink.Drop(1)
		}
		if batchRecords >= seatbeltLogBatchRecords || batchBytes >= seatbeltLogBatchLimit {
			batchBytes, batchRecords = 0, 0
			runtime.Gosched()
		}
		if err != nil {
			_ = o.command.Wait()
			if o.closing.Load() {
				o.setStatus(sandbox.ObserverUnavailable)
			} else {
				o.setStatus(sandbox.ObserverFailed)
			}
			return
		}
	}
}

func (o *darwinNativeObserver) Close() {
	o.closeOnce.Do(func() {
		o.closing.Store(true)
		if o.command == nil || o.command.Process == nil {
			if o.config.Listener != nil {
				_ = o.config.Listener.Close()
			}
			return
		}
		_ = o.command.Process.Kill()
		if o.stdout != nil {
			_ = o.stdout.Close()
		}
		<-o.done
		if o.config.Listener != nil {
			_ = o.config.Listener.Close()
		}
	})
}
