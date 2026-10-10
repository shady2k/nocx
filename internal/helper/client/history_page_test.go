package client_test

// The coordinator's history-page call (nocx-zg3k3.10.3) against a real host
// half: the params go out spelled as the op declares them, the result comes
// back decoded, and a generation that predates the op answers the TYPED
// refusal — a fact about the generation, not about the session.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// historyStubService answers the session service's history-page op with a
// canned result, recording the params it was asked with.
type historyStubService struct {
	gotParams proto.HistoryPageParams
	result    proto.HistoryPageResult
}

func (historyStubService) Name() string  { return proto.ServiceSession }
func (historyStubService) Ops() []string { return []string{proto.OpHistoryPage} }

func (historyStubService) ParamsSchema(op string) *host.Schema {
	if op != proto.OpHistoryPage {
		return nil // unknown ops must reach the host's unknown_op refusal
	}
	return host.SchemaFor(proto.HistoryPageParams{})
}

func (s *historyStubService) Call(_ context.Context, _ string, params json.RawMessage) (any, error) {
	if err := json.Unmarshal(params, &s.gotParams); err != nil {
		return nil, err
	}
	return s.result, nil
}

func dialHistoryStub(t *testing.T, svc host.Service) *client.Client {
	t.Helper()
	conn := newFakeConn(hostPeer("testhash", svc))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestHistoryPageRoundTripsTheOpShape(t *testing.T) {
	svc := &historyStubService{result: proto.HistoryPageResult{
		Floor: 3, Start: 13, End: 23, More: true,
		Rows: json.RawMessage(`[{"text":"L000013"}]`),
	}}
	c := dialHistoryStub(t, svc)

	before := uint64(23)
	res, err := c.HistoryPage(context.Background(), client.HostSessionID{Generation: "testhash", Session: "abc"}, &before, 10)
	if err != nil {
		t.Fatalf("HistoryPage: %v", err)
	}
	if res.Floor != 3 || res.Start != 13 || res.End != 23 || !res.More {
		t.Fatalf("HistoryPage = %+v, want the stub's answer", res)
	}
	if string(res.Rows) != `[{"text":"L000013"}]` {
		t.Fatalf("rows = %s, want the stub's pre-encoded rows", res.Rows)
	}
	// The params reached the wire as the op declares them: the session,
	// the cursor and the bound.
	if svc.gotParams.Session.Session != "abc" || svc.gotParams.Session.Generation != "testhash" {
		t.Fatalf("params name %+v, want the caller's session", svc.gotParams.Session)
	}
	if svc.gotParams.Before == nil || *svc.gotParams.Before != 23 {
		t.Fatalf("params before = %v, want 23", svc.gotParams.Before)
	}
	if svc.gotParams.Limit != 10 {
		t.Fatalf("params limit = %d, want 10", svc.gotParams.Limit)
	}
}

// historylessService is an OLDER GENERATION: it serves the session service
// but declares no ops, so every op reaches the host's unknown_op refusal.
type historylessService struct{}

func (historylessService) Name() string                     { return proto.ServiceSession }
func (historylessService) Ops() []string                    { return nil }
func (historylessService) ParamsSchema(string) *host.Schema { return nil }
func (historylessService) Call(context.Context, string, json.RawMessage) (any, error) {
	return nil, errors.New("never reached: the host refuses the op before the service sees it")
}

func TestHistoryPageMapsUnknownOpToTheTypedRefusal(t *testing.T) {
	c := dialHistoryStub(t, historylessService{})
	_, err := c.HistoryPage(context.Background(), client.HostSessionID{Generation: "testhash", Session: "abc"}, nil, 10)
	if !errors.Is(err, client.ErrHistoryPageUnsupported) {
		t.Fatalf("want ErrHistoryPageUnsupported, got %v", err)
	}
	if errors.Is(err, client.ErrLost) {
		t.Fatal("an old generation must not read as transport loss")
	}
}

func TestHistoryPageRefusesAPathologicalLimitBeforeTheWire(t *testing.T) {
	c := dialHistoryStub(t, &historyStubService{})
	for _, limit := range []int{0, -1, sessionruntime.MaxHistoryPageRows + 1} {
		if _, err := c.HistoryPage(context.Background(), client.HostSessionID{Generation: "g", Session: "s"}, nil, limit); err == nil {
			t.Fatalf("limit %d was accepted, want the caller-side refusal", limit)
		}
	}
}
