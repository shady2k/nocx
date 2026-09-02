package apifetch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apifetch"
)

func TestFetchTextUsesXMLDeclarationWhenHeaderCharsetIsSilent(t *testing.T) {
	body := append([]byte(`<?xml version="1.0" encoding="windows-1251"?><message>`), []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}...)
	body = append(body, []byte(`</message>`)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil || !strings.Contains(got.Text, "Привет") {
		t.Fatalf("result = %+v, err = %v; want declaration-decoded Cyrillic", got, err)
	}
}

func TestFetchTextHeaderCharsetWinsOverXMLDeclaration(t *testing.T) {
	body := append([]byte(`<?xml version="1.0" encoding="utf-8"?><message>`), []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}...)
	body = append(body, []byte(`</message>`)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=windows-1251")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	got, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if err != nil || !strings.Contains(got.Text, "Привет") {
		t.Fatalf("result = %+v, err = %v; want header-decoded Cyrillic", got, err)
	}
}

func TestFetchTextRefusesUnknownXMLDeclarationCharset(t *testing.T) {
	const body = `<?xml version="1.0" encoding="x-unknown-feed-charset"?><message>text</message>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	_, err := apifetch.New(directTextRoutes(), nil).FetchText(context.Background(), apifetch.TextRequest{URL: srv.URL, MaxBytes: 64 << 10})
	if !errors.Is(err, apifetch.ErrNotText) || !strings.Contains(err.Error(), "x-unknown-feed-charset") {
		t.Fatalf("error = %v, want named ErrNotText refusal", err)
	}
}
