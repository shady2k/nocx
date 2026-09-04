package transport

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

func TestNotifyCatalogue_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "notify.catalogue.schema.json")
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "notify.catalogue", map[string]any{})
	var envelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, resp)
	}
	if envelope.Error != nil {
		t.Fatalf("notify.catalogue: %+v", envelope.Error)
	}
	validateJSON(t, schema, envelope.Result, "notify.catalogue result")

	type catalogueKind struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	var got struct {
		Kinds []catalogueKind `json:"kinds"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	want := []catalogueKind{
		{ID: "blockFinished", Kind: "block.finished", Label: "Command finished", Description: "nocx's own block ledger recorded that a command finished."},
		{ID: "sessionEnded", Kind: "session.ended", Label: "Session ended", Description: "nocx's own session registry recorded that a session ended."},
		{ID: "transferFinished", Kind: "transfer.finished", Label: "File transfer finished", Description: "nocx's own transfer registry recorded that an upload or a download reached its end."},
		{ID: "programNotify", Kind: "program.notify", Label: "Program notification request", Description: "A program printed OSC 9 or OSC 777 to ask for one."},
		{ID: "bell", Kind: "bell", Label: "Terminal bell", Description: "A program printed BEL."},
		{ID: "waveUndispatched", Kind: "wave.undispatched", Label: "A worker is waiting for judgement", Description: "nocx's own wave record has a worker's result that its coordinator was not reached about."},
		{ID: "paneWorkFinished", Kind: "pane.workFinished", Label: "Work seems to have finished", Description: "nocx inferred from a pane's title that its work finished. It is an inference, so it may never leave this machine."},
	}
	if !reflect.DeepEqual(got.Kinds, want) {
		t.Errorf("notify.catalogue kinds = %+v, want shipped presented kinds %+v", got.Kinds, want)
	}
}
