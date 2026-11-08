package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// A burst must fit by its retained content, not by unused grid padding. The
// receiver cannot confirm anything until the whole burst has been ingested.
// The replacement case then reaches the same rows from the resend window,
// with live history disabled and before the command's own fence arrives.
func TestZeroRetentionBurstSurvivesBlockedDeliveryAndReplay(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprintf("replace=%v", replace), func(t *testing.T) {
			proc := newRawReaderFakeProcess()
			rt, screen, err := newSessionRuntime(defaultScreen, proc, "00000000000000000000000000000000", 136, 40, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer screen.Close()
			if err = rt.ApplyScrollback(0); err != nil {
				t.Fatal(err)
			}
			first := &blockedTailSink{rowsSink: newRowsSink(), entered: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(first.release) }) }
			defer unblock()
			hs := &hostSession{
				id:  proto.HostSessionID{Generation: "testhash", Session: "0123456789abcdef0123456789abcdef"},
				raw: mintRaw(t), proc: proc, runtime: rt, log: logtest.Slog(t), now: time.Now,
				win:     newWindow(2 * creditLimit),
				subs:    map[proto.SubscriberID]*subscriber{"coord-1": {id: "coord-1", sink: first}},
				rowWake: make(chan struct{}, 1), rowsDone: make(chan struct{}),
			}
			rt.SetRowStream(&rowBridge{hs: hs})
			go hs.serveRows()
			defer close(hs.rowsDone)
			var feed strings.Builder
			for i := 1; i <= 5000; i++ {
				fmt.Fprintf(&feed, "transcript-zero-retention-%03d\r\n", i)
			}
			b := []byte(feed.String())
			for offset := 0; offset < len(b); {
				n := min(len(b)-offset, 32<<10)
				if err = rt.Ingest(b[offset : offset+n]); err != nil {
					t.Fatal(err)
				}
				if offset == 0 {
					<-first.entered
				}
				offset += n
			}
			retained := poolBytes(t, hs)
			if retained > DefaultRowBufferBytes {
				t.Fatalf("retained %d bytes beyond the existing %d-byte pool", retained, DefaultRowBufferBytes)
			}
			t.Logf("input=%d retained=%d pool=%d", len(b), retained, DefaultRowBufferBytes)
			unblock()
			<-hs.requestRowsDrain()
			if overflows := hs.rowBufferOverflows.Load(); overflows != 0 {
				t.Fatalf("the marker burst exhausted the default row pool: %d overflow(s)", overflows)
			}

			sink := first.rowsSink
			if replace {
				hs.mu.Lock()
				delete(hs.subs, "coord-1")
				hs.mu.Unlock()
				sink = newRowsSink()
				rowsAttachReader(t, hs, "22222222222222222222222222222222", sink)
				// A drain request can bypass pending replay. Wait for the
				// replacement receiver's departed cursor before adding the fence.
				wantCursor := rt.DepartedRowCount()
				for {
					frames := sink.rowFrames()
					if len(frames) > 0 {
						last := decodeResentRows(t, frames[len(frames)-1:])[0]
						cursor := last.from + uint64(len(last.texts)) //nolint:gosec // a row count
						if cursor >= wantCursor {
							if cursor != wantCursor {
								t.Fatalf("replay cursor=%d, want %d before the fence", cursor, wantCursor)
							}
							break
						}
					}
					<-sink.changed
				}
			}
			nonce := endNonce(0x41)
			rt.Completed(rt.Incarnation(), nonce, 0)
			if err = rt.Ingest([]byte("\x1b]1337;NOCX_FENCE;" + strings.Repeat("41", 32) + "\a")); err != nil {
				t.Fatal(err)
			}
			<-hs.requestRowsDrain()
			assertTailMarkers(t, sink)
			page, err := hs.historyPage(nil, sessionruntime.MaxHistoryPageRows)
			if err != nil {
				t.Fatal(err)
			}
			var history []json.RawMessage
			if err := json.Unmarshal(page.Rows, &history); err != nil {
				t.Fatal(err)
			}
			if len(history) != 0 || page.More || page.Start != page.End {
				t.Fatalf("zero retention exposed live history: rows=%d more=%v start=%d end=%d", len(history), page.More, page.Start, page.End)
			}
		})
	}
}

type blockedTailSink struct {
	*rowsSink
	once             sync.Once
	entered, release chan struct{}
}

func (s *blockedTailSink) SendOutputRows(f proto.OutputRowsFrame) error {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return s.rowsSink.SendOutputRows(f)
}

func assertTailMarkers(t *testing.T, sink *rowsSink) {
	t.Helper()
	var got []string
	var next uint64
	for _, batch := range decodeResentRows(t, sink.rowFrames()) {
		if batch.from != next || batch.lost != 0 || batch.incomplete {
			t.Fatalf("noncontiguous or incomplete delivery at %d: %+v", next, batch)
		}
		got = append(got, batch.texts...)
		next += uint64(len(batch.texts)) //nolint:gosec // a row count
	}
	ends := sink.endFrames()
	if len(ends) != 1 || ends[0].EndRow != next {
		t.Fatalf("end markers=%d, departed=%d; want one end at the delivered cursor", len(ends), next)
	}
	var end struct {
		NoFence bool `json:"noFence"`
		Closing []struct {
			Text string `json:"text"`
		} `json:"closing"`
	}
	if err := json.Unmarshal(ends[0].Payload, &end); err != nil {
		t.Fatal(err)
	}
	if end.NoFence {
		t.Fatal("the sighted fence was reported missing")
	}
	for _, row := range end.Closing {
		got = append(got, row.Text)
	}
	if len(got) != 5000 {
		t.Fatalf("received %d markers including closing screen, want 5000", len(got))
	}
	for i, text := range got {
		if want := fmt.Sprintf("transcript-zero-retention-%03d", i+1); text != want {
			t.Fatalf("marker %d = %q, want %q", i, text, want)
		}
	}
}
