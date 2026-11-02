package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/log"
)

// ── Translation keys (localisation design Q2, Option B) ────────────────
// The notify.catalogue wire payload ships translation keys beside the
// English label and description; the renderer resolves key → catalogue
// entry → fallback. This pins the minted half over the real socket, against
// the same result the settings surface and the notification panel consume.

func TestNotifyCatalogue_EveryKindCarriesTranslationKeys(t *testing.T) {
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

	type catalogueKind struct {
		ID             string `json:"id"`
		LabelKey       string `json:"labelKey"`
		DescriptionKey string `json:"descriptionKey"`
	}
	var got struct {
		Kinds []catalogueKind `json:"kinds"`
	}
	if err := json.Unmarshal(envelope.Result, &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(got.Kinds) == 0 {
		t.Fatal("notify.catalogue returned no kinds")
	}
	for _, k := range got.Kinds {
		if want := "notify.kind." + k.ID + ".label"; k.LabelKey != want {
			t.Errorf("kind %q labelKey = %q, want %q", k.ID, k.LabelKey, want)
		}
		if want := "notify.kind." + k.ID + ".description"; k.DescriptionKey != want {
			t.Errorf("kind %q descriptionKey = %q, want %q", k.ID, k.DescriptionKey, want)
		}
		if strings.Contains(k.LabelKey, " ") {
			t.Errorf("kind %q labelKey = %q contains a space", k.ID, k.LabelKey)
		}
	}
}
