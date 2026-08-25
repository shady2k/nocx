package apisend

// Design §11.1's three states, and the property that makes them safe: a
// secret VALUE never appears in what crosses, in any of them. Every
// assertion here that matters is made against the MARSHALLED JSON rather
// than the struct — the struct is not what the renderer receives, and a
// value that leaks through a field nobody thought about would pass a
// field-by-field check.

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

// liveToken is deliberately long and distinctive: a prefix of it must be
// searchable in a whole payload without matching by accident. It is
// invented for this file and reaches no service.
//
//nolint:gosec // G101: this is the fake credential the tests search for. It is the SUBJECT of every assertion here — "no span carries a secret value" cannot be tested without a value to look for — so there is nothing to move elsewhere and nothing to shorten.
const liveToken = "sk-live-QZ1x9v7Kb3Np2Rt6Yw0Ec4Ha8Ju5Md1Sf7Gk3Lz"

// marshalled is the payload as the renderer would receive it. Every leak
// assertion goes through here.
func marshalled(t *testing.T, r Raw) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal Raw: %v", err)
	}
	return string(b)
}

// TestMarkRequest_AnIntactSpanIsNamedAndItsValueDoesNotCross is the first
// row of §11.1: the bytes still equal the secret, so the span says exactly
// which secret this is — and says it by NAME.
func TestMarkRequest_AnIntactSpanIsNamedAndItsValueDoesNotCross(t *testing.T) {
	text := "Authorization: Bearer " + liveToken + "\n"
	raw := MarkRequest(text, []Placement{{
		From: len("Authorization: Bearer "),
		To:   len("Authorization: Bearer ") + len(liveToken),
		Name: "API_TOKEN",
		Want: liveToken,
	}})

	var secret *Span
	for i := range raw.Spans {
		if raw.Spans[i].Kind == SpanSecret {
			secret = &raw.Spans[i]
		}
	}
	if secret == nil {
		t.Fatalf("no %q span in %+v", SpanSecret, raw.Spans)
	}
	if secret.Name != "API_TOKEN" {
		t.Errorf("Name = %q, want API_TOKEN", secret.Name)
	}
	if secret.Damage != "" {
		t.Errorf("Damage = %q on an intact span, want empty", secret.Damage)
	}
	if got := marshalled(t, raw); strings.Contains(got, liveToken) {
		t.Fatalf("the token crossed in the payload: %s", got)
	}
}

// TestMarkRequest_ADamagedSpanNamesTheShapeAndTheSurvivingBytesAppearNowhere
// is the second row, and the one that makes the whole thing safe. A
// truncated token is a PREFIX OF A LIVE TOKEN, so showing "the text when it
// does not match" would print the beginning of a real credential.
func TestMarkRequest_ADamagedSpanNamesTheShapeAndTheSurvivingBytesAppearNowhere(t *testing.T) {
	full := "Authorization: Bearer " + liveToken + "\n"
	cut := full[:len("Authorization: Bearer ")+12] // 12 bytes of the token survive
	survivors := liveToken[:12]

	raw := MarkRequest(cut, []Placement{{
		From: len("Authorization: Bearer "),
		To:   len("Authorization: Bearer ") + len(liveToken),
		Name: "API_TOKEN",
		Want: liveToken,
	}})

	var damaged *Span
	for i := range raw.Spans {
		if raw.Spans[i].Kind == SpanSecretDamaged {
			damaged = &raw.Spans[i]
		}
	}
	if damaged == nil {
		t.Fatalf("no %q span in %+v", SpanSecretDamaged, raw.Spans)
	}
	if damaged.Name != "API_TOKEN" {
		t.Errorf("Name = %q, want API_TOKEN", damaged.Name)
	}
	want := "truncated, 12 of 47 bytes"
	if damaged.Damage != want {
		t.Errorf("Damage = %q, want %q — the SHAPE of the damage, never its bytes", damaged.Damage, want)
	}

	// The whole payload, not just the span: eliding the bytes from the span
	// while leaving them in Text would be the leak wearing a badge.
	got := marshalled(t, raw)
	if strings.Contains(got, survivors) {
		t.Fatalf("the surviving %d bytes of a live token crossed in the payload: %s", len(survivors), got)
	}
	if strings.Contains(raw.Text, survivors) {
		t.Fatalf("the surviving bytes are still in Text: %q", raw.Text)
	}
}

// TestMarkRequest_TextOutsideAPlacementIsOrdinaryText is the third row.
// Anything we did not place is not a secret, and calling it one would make
// the badge meaningless.
func TestMarkRequest_TextOutsideAPlacementIsOrdinaryText(t *testing.T) {
	raw := MarkRequest("GET /users HTTP/1.1\nHost: api.internal\n", nil)
	if len(raw.Spans) != 1 || raw.Spans[0].Kind != SpanText {
		t.Fatalf("spans = %+v, want one text span", raw.Spans)
	}
	if raw.Text != "GET /users HTTP/1.1\nHost: api.internal\n" {
		t.Errorf("Text = %q, want it unchanged", raw.Text)
	}
	if raw.Spans[0].From != 0 || raw.Spans[0].To != len(raw.Text) {
		t.Errorf("span = %+v, want it to cover the whole text", raw.Spans[0])
	}
}

// TestRaw_NoSpanCarriesASecretValueInAnyOfTheThreeStates walks all three
// states in ONE payload. Two of them passing is the shape that ships.
func TestRaw_NoSpanCarriesASecretValueInAnyOfTheThreeStates(t *testing.T) {
	other := "pw-8834-hunter-correct-horse-battery"
	// The second secret sits at the very end and is cut short, which is the
	// only way the request side ever damages a span: the placement is what
	// the sender did, and the text is what fitted.
	text := "X-A: " + liveToken + "\nX-B: " + other[:9]
	raw := MarkRequest(text, []Placement{
		{From: 5, To: 5 + len(liveToken), Name: "API_TOKEN", Want: liveToken},
		{From: 5 + len(liveToken) + 6, To: 5 + len(liveToken) + 6 + len(other), Name: "PASSWORD", Want: other},
	})

	kinds := map[string]bool{}
	for _, s := range raw.Spans {
		kinds[s.Kind] = true
	}
	for _, k := range []string{SpanText, SpanSecret, SpanSecretDamaged} {
		if !kinds[k] {
			t.Fatalf("no %q span among %+v — this test only means something with all three", k, raw.Spans)
		}
	}
	got := marshalled(t, raw)
	for _, v := range []string{liveToken, other[:9]} {
		if strings.Contains(got, v) {
			t.Fatalf("a secret value crossed: %s", got)
		}
	}
	if !strings.Contains(got, "API_TOKEN") || !strings.Contains(got, "PASSWORD") {
		t.Fatalf("the NAMES must cross — that is what a badge is: %s", got)
	}
}

// TestRaw_SpansTileTheTextInOrder: a renderer draws the text by walking the
// spans, so a gap or an overlap is a rendering that silently drops or
// duplicates a run.
func TestRaw_SpansTileTheTextInOrder(t *testing.T) {
	text := "a=" + liveToken + "&b=2"
	raw := MarkRequest(text, []Placement{{From: 2, To: 2 + len(liveToken), Name: "T", Want: liveToken}})

	cursor := 0
	var rebuilt strings.Builder
	for _, s := range raw.Spans {
		if s.From != cursor {
			t.Fatalf("span %+v starts at %d, want %d — the spans do not tile", s, s.From, cursor)
		}
		if s.To < s.From || s.To > len(raw.Text) {
			t.Fatalf("span %+v is outside the text of %d bytes", s, len(raw.Text))
		}
		rebuilt.WriteString(raw.Text[s.From:s.To])
		cursor = s.To
	}
	if cursor != len(raw.Text) {
		t.Fatalf("the spans stop at %d of %d bytes", cursor, len(raw.Text))
	}
	if rebuilt.String() != raw.Text {
		t.Fatalf("walking the spans rebuilt %q, want %q", rebuilt.String(), raw.Text)
	}
}

// TestSearchResponse_FindsAnEchoedToken is §11.4: APIs echo credentials
// back in error text, and without this the response ships one to the
// renderer as ORDINARY TEXT in a view whose whole purpose is to show
// everything.
func TestSearchResponse_FindsAnEchoedToken(t *testing.T) {
	body := `{"error":"invalid token ` + liveToken + `"}`
	raw := SearchResponse(body, []NamedSecret{{Name: "API_TOKEN", Value: liveToken}})

	found := false
	for _, s := range raw.Spans {
		if s.Kind == SpanSecret && s.Name == "API_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the echoed token was not marked: %+v", raw.Spans)
	}
	if got := marshalled(t, raw); strings.Contains(got, liveToken) {
		t.Fatalf("the echoed token crossed as text: %s", got)
	}
}

// TestSearchResponse_DoesNotFindATransformedSpelling is the test that stops
// the one above being read as more than it is. The search is a bounded
// KNOWN-PLAINTEXT search: a base64-wrapped or URL-escaped token is missed
// ON PURPOSE, and the design says so rather than implying coverage it does
// not have (§11.3).
func TestSearchResponse_DoesNotFindATransformedSpelling(t *testing.T) {
	wrapped := base64Of(liveToken)
	escaped := strings.ReplaceAll(liveToken, "-", "%2D")
	body := `{"b64":"` + wrapped + `","url":"` + escaped + `"}`

	raw := SearchResponse(body, []NamedSecret{{Name: "API_TOKEN", Value: liveToken}})
	for _, s := range raw.Spans {
		if s.Kind != SpanText {
			t.Fatalf("a transformed spelling was marked %q — the search claims coverage it does not have: %+v",
				s.Kind, raw.Spans)
		}
	}
	if raw.Text != body {
		t.Fatalf("Text = %q, want the body unchanged: nothing matched, so nothing is elided", raw.Text)
	}
}

// TestSearchResponse_OverlappingMatchesCollapseToTheLongest: two secrets
// where one contains the other produce one span, and it is the longer one.
// Two overlapping spans would be a rendering with duplicated bytes.
func TestSearchResponse_OverlappingMatchesCollapseToTheLongest(t *testing.T) {
	short := liveToken[:20]
	raw := SearchResponse("prefix "+liveToken+" suffix", []NamedSecret{
		{Name: "SHORT", Value: short},
		{Name: "LONG", Value: liveToken},
	})

	var marked []Span
	for _, s := range raw.Spans {
		if s.Kind != SpanText {
			marked = append(marked, s)
		}
	}
	if len(marked) != 1 {
		t.Fatalf("marked = %+v, want exactly one span", marked)
	}
	if marked[0].Name != "LONG" {
		t.Errorf("Name = %q, want LONG — overlapping matches collapse to the longest", marked[0].Name)
	}
	if got := marshalled(t, raw); strings.Contains(got, short) {
		t.Fatalf("the shorter secret's bytes crossed: %s", got)
	}
}

// TestSearchResponse_AnEmptySecretMatchesNothing: an unbound variable
// resolved to "" would otherwise match at every offset and turn the whole
// body into badges.
func TestSearchResponse_AnEmptySecretMatchesNothing(t *testing.T) {
	raw := SearchResponse("hello", []NamedSecret{{Name: "EMPTY", Value: ""}})
	if len(raw.Spans) != 1 || raw.Spans[0].Kind != SpanText {
		t.Fatalf("spans = %+v, want one text span", raw.Spans)
	}
	if raw.Text != "hello" {
		t.Errorf("Text = %q, want hello", raw.Text)
	}
}

// TestSearchResponse_EmptyTextHasNoSpans: an empty body has nothing to
// segment, and a zero-length text span would be a rendering artefact.
func TestSearchResponse_EmptyTextHasNoSpans(t *testing.T) {
	raw := SearchResponse("", []NamedSecret{{Name: "T", Value: liveToken}})
	if len(raw.Spans) != 0 {
		t.Fatalf("spans = %+v, want none", raw.Spans)
	}
	if raw.Spans == nil {
		t.Fatal("Spans is nil — a result with no spans is [], never null, or the wire carries null")
	}
}

// base64Of is the transformed spelling the search deliberately misses.
func base64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// The same three states, now off a real exchange rather than a unit call.
// The raw text rides on the send result because it belongs to THIS run: a
// second round trip could only fetch the raw of a different send.

// TestSend_RawRequestNamesTheSecretAndNeverCarriesIt: the request side is a
// verification — the sender placed the value, so it knows where it is.
func TestSend_RawRequestNamesTheSecretAndNeverCarriesIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	r := apicoll.Request{
		Method:  http.MethodPost,
		URL:     srv.URL + "/users",
		Headers: []apicoll.Header{{Name: "Authorization", Value: "Bearer " + liveToken, Enabled: true}},
		Body:    apicoll.Body{Kind: apicoll.BodyRaw, Text: `{"email":"a@b.c"}`},
	}
	ex, err := New().Send(context.Background(), r, Key{}, NamedSecret{Name: "API_TOKEN", Value: liveToken})
	answered(t, ex, err)

	req := ex.Request
	for _, want := range []string{"POST /users HTTP/1.1", "Host: ", "Authorization: Bearer ", `{"email":"a@b.c"}`} {
		if !strings.Contains(req.Text, want) {
			t.Errorf("the raw request does not show %q:\n%s", want, req.Text)
		}
	}
	if !hasSpan(req, SpanSecret, "API_TOKEN") {
		t.Errorf("no named secret span in %+v", req.Spans)
	}
	if s := marshalled(t, req); strings.Contains(s, liveToken) {
		t.Fatalf("the token crossed on the request side: %s", s)
	}
}

// TestSend_RawResponseFindsATokenTheServerEchoed is §11.4 through a real
// socket: without the search this arrives as ordinary text.
func TestSend_RawResponseFindsATokenTheServerEchoed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"token `+liveToken+` is expired"}`)
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(), apicollGet(srv.URL), Key{},
		NamedSecret{Name: "API_TOKEN", Value: liveToken})
	got := answered(t, ex, err)
	if !hasSpan(got.Raw, SpanSecret, "API_TOKEN") {
		t.Errorf("the echoed token was not marked: %+v", got.Raw.Spans)
	}
	if s := marshalled(t, got.Raw); strings.Contains(s, liveToken) {
		t.Fatalf("the echoed token crossed as text: %s", s)
	}
	if !strings.Contains(got.Raw.Text, "401") {
		t.Errorf("the raw response does not show the status line:\n%s", got.Raw.Text)
	}
	// Response.Text is the body the run shows; the search elides nothing
	// there, so the raw view and the body view are two answers to two
	// questions rather than one field doing both.
	if !strings.Contains(got.Text, liveToken) {
		t.Errorf("Response.Text should still be what the server sent; it is the raw view that is segmented")
	}
}

// TestSend_RawResponseSearchesTheDecodedBody: a compressed frame is
// searched AFTER decoding, never before. Without this the search reports
// nothing for every gzip-encoded error body, which is most of them.
func TestSend_RawResponseSearchesTheDecodedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, `{"error":"token `+liveToken+`"}`)
		_ = zw.Close()
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(), apicollGet(srv.URL), Key{},
		NamedSecret{Name: "API_TOKEN", Value: liveToken})
	got := answered(t, ex, err)
	if !hasSpan(got.Raw, SpanSecret, "API_TOKEN") {
		t.Fatalf("a token echoed inside a gzip frame was not found: %+v", got.Raw.Spans)
	}
	if s := marshalled(t, got.Raw); strings.Contains(s, liveToken) {
		t.Fatalf("the token crossed: %s", s)
	}
}

// TestSend_ARawCutShortDamagesTheSpanRatherThanShowingItsBytes is the whole
// reason the request side keeps placements instead of searching the text it
// finally sends: the placement is what the sender DID, the text is what
// fitted, and the difference between them is the damage.
func TestSend_ARawCutShortDamagesTheSpanRatherThanShowingItsBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	// "GET /?t=" is 8 bytes, so a 30-byte ceiling leaves 22 bytes of a
	// 44-byte token — a prefix of a live credential.
	ex, err := newBounded(30).Send(context.Background(),
		apicollGet(srv.URL+"/?t="+liveToken), Key{}, NamedSecret{Name: "API_TOKEN", Value: liveToken})
	answered(t, ex, err)

	var damaged *Span
	for i := range ex.Request.Spans {
		if ex.Request.Spans[i].Kind == SpanSecretDamaged {
			damaged = &ex.Request.Spans[i]
		}
	}
	if damaged == nil {
		t.Fatalf("no damaged span in %+v (text %q)", ex.Request.Spans, ex.Request.Text)
	}
	if want := "truncated, 22 of 47 bytes"; damaged.Damage != want {
		t.Errorf("Damage = %q, want %q", damaged.Damage, want)
	}
	if s := marshalled(t, ex.Request); strings.Contains(s, liveToken[:22]) {
		t.Fatalf("the surviving 22 bytes of a live token crossed: %s", s)
	}
}

// TestSend_RawIsAlwaysPresentAndNeverNull: a send with no secrets at all
// still carries both sides, and the span lists are [] rather than null —
// null is what a renderer walking spans crashes on.
func TestSend_RawIsAlwaysPresentAndNeverNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	got := answered(t, ex, err)
	for what, raw := range map[string]Raw{"request": ex.Request, "response": got.Raw} {
		if raw.Text == "" {
			t.Errorf("the raw %s is empty", what)
		}
		if raw.Spans == nil {
			t.Errorf("the raw %s has null spans", what)
		}
		for _, s := range raw.Spans {
			if s.Kind != SpanText {
				t.Errorf("the raw %s marked %+v with no secret in the request", what, s)
			}
		}
	}
}

// TestSend_ABinaryRawSaysSoAndCarriesNoBase64: §12.3's rule reaches the raw
// view too. A base64 body here would be the bulk payload in JSON that AD-1
// prohibits, arriving through a side door.
func TestSend_ABinaryRawSaysSoAndCarriesNoBase64(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x02, 0x03, 0xff, 0xfe}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	ex, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	got := answered(t, ex, err)
	if !got.Binary {
		t.Fatalf("the body was not read as binary")
	}
	if want := "binary body, 6 bytes"; !strings.Contains(got.Raw.Text, want) {
		t.Errorf("the raw response does not say %q:\n%q", want, got.Raw.Text)
	}
	if b64 := base64Of(string(payload)); strings.Contains(got.Raw.Text, b64) {
		t.Errorf("the raw response carries the body as base64")
	}
}

// hasSpan reports whether raw carries a span of this kind under this name.
func hasSpan(raw Raw, kind, name string) bool {
	for _, s := range raw.Spans {
		if s.Kind == kind && s.Name == name {
			return true
		}
	}
	return false
}
