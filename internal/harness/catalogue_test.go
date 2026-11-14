package harness

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type catalogueConnector struct {
	readiness func(context.Context) Readiness
	models    func(context.Context) ([]Model, error)
}

func (c *catalogueConnector) Facts() Facts                                { return Facts{} }
func (c *catalogueConnector) Run(context.Context, TurnSpec) (Turn, error) { panic("unused") }
func (c *catalogueConnector) Readiness(ctx context.Context) Readiness     { return c.readiness(ctx) }
func (c *catalogueConnector) Models(ctx context.Context) ([]Model, error) { return c.models(ctx) }

type catalogueWithoutModels struct{ Harness }

func TestCatalogueReadinessAndOptionalModels(t *testing.T) {
	connectors := map[string]Harness{}
	for _, state := range []ReadinessState{NotInstalled, NotConfigured, Ready} {
		connectors[string(state)] = &catalogueConnector{
			readiness: func(context.Context) Readiness { return Readiness{State: state, Message: string(state)} },
			models: func(context.Context) ([]Model, error) {
				if state != Ready {
					t.Error("models requested while unready")
				}
				return []Model{{ID: "model", Name: "Model"}}, nil
			},
		}
	}
	connectors["without"] = catalogueWithoutModels{Harness: &catalogueConnector{readiness: func(context.Context) Readiness { return Readiness{State: Ready} }}}
	got, err := NewCatalogue(connectors).Catalogue(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []AgentCatalogue{
		{ID: "not_configured", Readiness: Readiness{State: NotConfigured, Message: "not_configured"}},
		{ID: "not_installed", Readiness: Readiness{State: NotInstalled, Message: "not_installed"}},
		{ID: "ready", Readiness: Readiness{State: Ready, Message: "ready"}, Models: []Model{{ID: "model", Name: "Model"}}},
		{ID: "without", Readiness: Readiness{State: Ready}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestCatalogueCacheRefreshAndCopies(t *testing.T) {
	calls := 0
	models := []Model{{ID: "original"}}
	connector := &catalogueConnector{readiness: func(context.Context) Readiness { calls++; return Readiness{State: Ready} }, models: func(context.Context) ([]Model, error) { return models, nil }}
	connectors := map[string]Harness{"agent": connector}
	service := NewCatalogue(connectors)
	delete(connectors, "agent")
	first, err := service.Catalogue(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	first[0].ID = "changed"
	first[0].Models[0].ID = "changed"
	models[0].ID = "source changed"
	cached, err := service.Catalogue(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || cached[0].ID != "agent" || cached[0].Models[0].ID != "original" {
		t.Fatalf("cache changed: %#v (%d calls)", cached, calls)
	}
	cached[0].Models[0].ID = "second change"
	again, _ := service.Catalogue(context.Background(), false)
	if again[0].Models[0].ID != "original" {
		t.Fatal("cached return aliases cache")
	}
	refreshed, err := service.Catalogue(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || refreshed[0].Models[0].ID != "source changed" {
		t.Fatalf("refresh: %#v (%d calls)", refreshed, calls)
	}
}

func TestCatalogueModelFailureIsVisibleAndNotCached(t *testing.T) {
	boom := errors.New("model list failed")
	calls := 0
	service := NewCatalogue(map[string]Harness{"agent": &catalogueConnector{
		readiness: func(context.Context) Readiness { return Readiness{State: Ready} },
		models: func(context.Context) ([]Model, error) {
			calls++
			if calls == 1 {
				return nil, boom
			}
			return []Model{{ID: "recovered"}}, nil
		},
	}})
	if rows, err := service.Catalogue(context.Background(), false); !errors.Is(err, boom) || rows != nil {
		t.Fatalf("failure hidden: %#v %v", rows, err)
	}
	rows, err := service.Catalogue(context.Background(), false)
	if err != nil || calls != 2 || rows[0].Models[0].ID != "recovered" {
		t.Fatalf("failed pass cached: %#v %v", rows, err)
	}
}

type catalogueWaitContext struct {
	context.Context
	waiting chan struct{}
}

func (c catalogueWaitContext) Done() <-chan struct{} { close(c.waiting); return c.Context.Done() }

func TestCatalogueCoalescesAndCancelledWaiterDoesNotCancelPass(t *testing.T) {
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	service := NewCatalogue(map[string]Harness{"agent": &catalogueConnector{
		readiness: func(ctx context.Context) Readiness {
			calls.Add(1)
			entered <- ctx
			<-release
			return Readiness{State: Ready}
		},
		models: func(context.Context) ([]Model, error) { return []Model{{ID: "model"}}, nil },
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := service.Catalogue(ctx, false); first <- err }()
	shared := <-entered
	deadline, ok := shared.Deadline()
	if !ok || time.Until(deadline) > 30*time.Second || time.Until(deadline) <= 0 {
		t.Fatal("shared pass must have a finite 30 second bound")
	}
	waiting := make(chan struct{})
	second := make(chan error, 1)
	go func() {
		rows, err := service.Catalogue(catalogueWaitContext{Context: context.Background(), waiting: waiting}, true)
		if err == nil && (len(rows) != 1 || rows[0].Models[0].ID != "model") {
			err = errors.New("missing shared result")
		}
		second <- err
	}()
	<-waiting
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	if err := shared.Err(); err != nil {
		t.Fatalf("waiter cancelled shared pass: %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("%d passes, want one", calls.Load())
	}
	if _, err := service.Catalogue(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("shared result not cached")
	}
}

func TestCatalogueCancelledCallerDoesNotStartPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := NewCatalogue(map[string]Harness{"agent": &catalogueConnector{readiness: func(context.Context) Readiness { t.Error("cancelled caller started work"); return Readiness{} }}})
	if _, err := service.Catalogue(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestCatalogueEmptyResultIsCached(t *testing.T) {
	service := NewCatalogue(nil)
	rows, err := service.Catalogue(context.Background(), false)
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty catalogue: %#v %v", rows, err)
	}
}

func TestCatalogueFailedRefreshPreservesLastGoodCache(t *testing.T) {
	calls := 0
	boom := errors.New("refresh failed")
	service := NewCatalogue(map[string]Harness{"agent": &catalogueConnector{
		readiness: func(context.Context) Readiness { return Readiness{State: Ready} },
		models: func(context.Context) ([]Model, error) {
			calls++
			if calls == 2 {
				return nil, boom
			}
			return []Model{{ID: "last good"}}, nil
		},
	}})
	if _, err := service.Catalogue(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if rows, err := service.Catalogue(context.Background(), true); !errors.Is(err, boom) || rows != nil {
		t.Fatalf("refresh failure hidden: %#v %v", rows, err)
	}
	rows, err := service.Catalogue(context.Background(), false)
	if err != nil || calls != 2 || rows[0].Models[0].ID != "last good" {
		t.Fatalf("last good cache lost: %#v %v, calls=%d", rows, err, calls)
	}
	if _, err := service.Catalogue(context.Background(), true); err != nil || calls != 3 {
		t.Fatalf("refresh did not recover: %v, calls=%d", err, calls)
	}
}

func TestCatalogueRefreshDropsModelsWhenConnectorBecomesUnready(t *testing.T) {
	state := Ready
	modelCalls := 0
	service := NewCatalogue(map[string]Harness{"agent": &catalogueConnector{
		readiness: func(context.Context) Readiness { return Readiness{State: state, Message: string(state)} },
		models:    func(context.Context) ([]Model, error) { modelCalls++; return []Model{{ID: "previous model"}}, nil },
	}})
	rows, err := service.Catalogue(context.Background(), false)
	if err != nil || len(rows[0].Models) != 1 {
		t.Fatalf("ready catalogue: %#v %v", rows, err)
	}
	state = NotConfigured
	rows, err = service.Catalogue(context.Background(), true)
	if err != nil || rows[0].Readiness.State != NotConfigured || len(rows[0].Models) != 0 || modelCalls != 1 {
		t.Fatalf("refresh retained ready facts: %#v %v, model calls=%d", rows, err, modelCalls)
	}
	rows, err = service.Catalogue(context.Background(), false)
	if err != nil || rows[0].Readiness.State != NotConfigured || len(rows[0].Models) != 0 {
		t.Fatalf("unready refresh not cached: %#v %v", rows, err)
	}
}
