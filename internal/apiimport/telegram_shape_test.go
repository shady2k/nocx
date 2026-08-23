package apiimport

// The shape this epic exists for, at the level that can prove it cheaply
// (nocx-ew3uv.4's premise).
//
// Telegram puts the bot token in the PATH — `/bot<TOKEN>/sendMessage` — so an
// export of one carries a SECRET collection variable that the URL references
// mid-segment. Three things have to come out of that import together, and the
// e2e above it is expensive enough that they are worth asserting here first:
// the reference survives in the address, the NAME reaches the environment
// file, and the VALUE goes to the binder and nowhere else.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

//nolint:gosec // a test fixture, and the same string the e2e export carries
const telegramToken = "e2e-telegram-bot-token-77a1c39fbe0284d5619c"

func TestImport_ATokenInThePathBecomesAReferenceAndABinding(t *testing.T) {
	doc := `{
      "info": {"name": "telegram-api", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
      "variable": [
        {"key": "baseUrl", "value": "https://api.telegram.org", "type": "string"},
        {"key": "token", "value": "` + telegramToken + `", "type": "secret"}
      ],
      "item": [{
        "name": "send message",
        "request": {
          "method": "GET",
          "url": {"raw": "{{baseUrl}}/bot{{token}}/sendMessage", "host": ["{{baseUrl}}"], "path": ["bot{{token}}", "sendMessage"]}
        }
      }]
    }`

	dest := destUnder(t)
	binder := &recordingBinder{}
	if _, err := ImportInto(t.Context(), NewOSFS(), binder, dest, strings.NewReader(doc), apicoll.Route{}); err != nil {
		t.Fatalf("ImportInto: %v", err)
	}

	// 1. THE VALUE WENT TO THE BINDER, and to nothing else.
	value, ok := binder.valueFor("token")
	if !ok {
		t.Fatal("the secret variable was not bound at all")
	}
	if value != telegramToken {
		t.Errorf("bound %q, want the token from the export", value)
	}

	// 2. THE REFERENCE SURVIVES IN THE ADDRESS, mid-segment. An importer
	// that split the path on `{{` — or url-escaped the braces — would leave
	// an address that can never resolve, and the send would fail at compose
	// with nothing to say about why.
	files := walkFiles(t, dest)
	var requestText string
	for name, body := range files {
		if strings.HasSuffix(name, ".json") && strings.Contains(body, `"method"`) {
			requestText = body
		}
	}
	if requestText == "" {
		t.Fatal("the import wrote no request file")
	}
	if !strings.Contains(requestText, "{{baseUrl}}/bot{{token}}/sendMessage") {
		t.Errorf("the request's URL is not the export's:\n%s", requestText)
	}

	// 3. AND NO FILE UNDER THE ROOT CARRIES THE VALUE — the claim §8 makes
	// about the whole folder, asserted over the whole folder.
	for name, body := range files {
		if strings.Contains(body, telegramToken) {
			t.Errorf("%s carries the token", name)
		}
	}
	// The environment DECLARES the name, or the absence above would pass on
	// an import that dropped the variable entirely.
	var declared bool
	for _, body := range files {
		if strings.Contains(body, `"secretVars"`) && strings.Contains(body, `"token"`) {
			declared = true
		}
	}
	if !declared {
		t.Error("no environment declares the secret variable by name")
	}
}
