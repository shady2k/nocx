package apiimport

// Telegram puts the bot token in the PATH — `/bot<TOKEN>/sendMessage` — so an
// export of one carries a credential-shaped collection variable. The import
// keeps the reference and an empty ordinary variable, reports the dropped
// value, and writes the credential nowhere.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

//nolint:gosec // a test fixture, and the same string the e2e export carries
const telegramToken = "e2e-telegram-bot-token-77a1c39fbe0284d5619c"

func TestImport_ATokenInThePathBecomesAnEmptyVariableAndReportsIt(t *testing.T) {
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
	unsupported, err := ImportInto(t.Context(), NewOSFS(), dest, strings.NewReader(doc), apicoll.Route{})
	if err != nil {
		t.Fatalf("ImportInto: %v", err)
	}

	files := walkFiles(t, dest)
	var requestText string
	for name, body := range files {
		if strings.HasSuffix(name, ".json") && strings.Contains(body, `"method"`) {
			requestText = body
		}
		if strings.Contains(body, telegramToken) {
			t.Errorf("%s carries the token", name)
		}
	}
	if requestText == "" {
		t.Fatal("the import wrote no request file")
	}
	if !strings.Contains(requestText, "{{baseUrl}}/bot{{token}}/sendMessage") {
		t.Errorf("the request's URL is not the export's:\n%s", requestText)
	}
	env := files["environments/default.json"]
	if !strings.Contains(env, `"token": ""`) {
		t.Errorf("environment does not carry an empty token variable: %s", env)
	}
	if strings.Contains(env, `"secretVars"`) {
		t.Errorf("environment declares a secret variable: %s", env)
	}
	found := false
	for _, item := range unsupported {
		if strings.Contains(item.What, "token") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unsupported = %+v, want token named", unsupported)
	}
}
