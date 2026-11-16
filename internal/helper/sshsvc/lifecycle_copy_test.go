//go:build nocx_local_ssh

package sshsvc

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

type partialLifecycleWriter struct{}

func (partialLifecycleWriter) Write([]byte) (int, error) { return 2, io.ErrClosedPipe }

func TestLifecycleBridgeCountsCompletedWritesWithoutLoggingPayload(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial_failure"}[fail], func(t *testing.T) {
			sl := logtest.Slog(t)
			ctx := nocxlog.WithLogger(context.Background(), nocxlog.NewSlogAdapter(sl))
			var dst io.Writer = io.Discard
			if fail {
				dst = partialLifecycleWriter{}
			}
			w := &lifecycleCopyWriter{ctx: ctx, dst: dst, port: 1234, direction: "carrier_to_shell"}
			const payload = "secret-capability"
			n, err := w.Write([]byte(payload))
			want := len(payload)
			if fail {
				want = 2
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if n != want || w.total != int64(want) {
				t.Fatalf("written=%d total=%d want=%d", n, w.total, want)
			}
			records := logtest.RecordsSlog(sl)
			if len(records) != 1 {
				t.Fatalf("records=%v", records)
			}
			attrs := map[string]string{}
			for _, a := range records[0].Attrs {
				attrs[a.Key] = a.Value.String()
			}
			if attrs["direction"] != "carrier_to_shell" || attrs["lifecycle_port"] != "1234" {
				t.Fatalf("attrs=%v", attrs)
			}
			if strings.Contains(logtest.TextSlog(sl), payload) {
				t.Fatal("payload leaked into log")
			}
		})
	}
}
