package session

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	nocxlog "github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
)

type observedLifecycleProcess struct {
	fakeProcess
	carrier io.ReadWriteCloser
}

func (p *observedLifecycleProcess) Lifecycle() io.ReadWriteCloser { return p.carrier }

type observedLifecycleCarrier struct{ fail bool }

func (c observedLifecycleCarrier) Read([]byte) (int, error) { return 0, io.EOF }
func (c observedLifecycleCarrier) Close() error             { return nil }
func (c observedLifecycleCarrier) Write(b []byte) (int, error) {
	if c.fail {
		return 2, io.ErrClosedPipe
	}
	return len(b), nil
}

func TestLifecycleWriteReportsCompletedBytesNotPayload(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial_failure"}[fail], func(t *testing.T) {
			sl := logtest.Slog(t)
			ctx := nocxlog.WithLogger(context.Background(), nocxlog.NewSlogAdapter(sl))
			id := proto.SubscriberID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			s := &hostSession{id: proto.HostSessionID{Generation: "gen", Session: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, proc: &observedLifecycleProcess{carrier: observedLifecycleCarrier{fail: fail}}, subs: map[proto.SubscriberID]*subscriber{id: {id: id}}}
			const secret = "secret-capability"
			raw, parseErr := proto.SessionBytes(string(id))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			err := s.writeLifecycle(ctx, nil, proto.SessionFrame{Subscriber: raw, Payload: []byte(secret)})
			if fail && !errors.Is(err, io.ErrClosedPipe) || !fail && err != nil {
				t.Fatalf("error=%v", err)
			}
			records := logtest.RecordsSlog(sl)
			if len(records) != 1 {
				t.Fatalf("records=%v", records)
			}
			attrs := map[string]string{}
			for _, a := range records[0].Attrs {
				attrs[a.Key] = a.Value.String()
			}
			want := "17"
			if fail {
				want = "2"
			}
			if attrs["bytes"] != want || attrs["requested_bytes"] != "17" || attrs["session"] != s.id.Session {
				t.Fatalf("attrs=%v", attrs)
			}
			if strings.Contains(logtest.TextSlog(sl), secret) {
				t.Fatal("payload leaked")
			}
		})
	}
}
