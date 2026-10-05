package transport

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/transport/control"
	"github.com/shady2k/nocx/internal/waittest"
)

func orderedWorkerCount() int {
	for size := 1 << 20; ; size *= 2 {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte("control.(*orderedSubmission).worker"))
		}
		if size >= 1<<28 {
			panic("goroutine stack dump exceeded 256 MiB")
		}
	}
}

func TestWSServerStopStopsOrderedSubmissionWorkers(t *testing.T) {
	ws, db, stop := newAgentWSServer(t)
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()

	baseline := orderedWorkerCount()
	var sub control.Submission
	for _, spec := range ws.methods {
		if _, ok := spec.submission.(control.Shutdownable); ok {
			sub = spec.submission
			break
		}
	}
	if sub == nil {
		t.Fatal("server has no ordered submission")
	}
	run := make(chan struct{})
	if rejection := sub.TrySubmit(context.Background(), control.Task{Run: func(context.Context) { close(run) }}); rejection != nil {
		t.Fatalf("TrySubmit: %v", rejection)
	}
	<-run
	afterSubmit := orderedWorkerCount()
	t.Logf("ordered worker family before=%d after-submit=%d", baseline, afterSubmit)
	if afterSubmit != baseline+1 {
		t.Fatalf("worker family count after submission = %d, want %d", afterSubmit, baseline+1)
	}

	if err := ws.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("content db close: %v", err)
	}
	stopped = true
	waittest.WaitForTimeoutDetail(t, "ordered worker to exit after Stop", 5*time.Second,
		func() string { return "worker family count " + fmt.Sprint(orderedWorkerCount()) },
		func() bool { return orderedWorkerCount() == baseline })
	t.Logf("ordered worker family after-stop=%d", orderedWorkerCount())
}
