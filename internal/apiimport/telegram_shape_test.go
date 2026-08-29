package apiimport

// Telegram puts the bot token in the PATH — `/bot<TOKEN>/sendMessage` — so an
// export of one carries the credential as an ordinary collection variable
// marked `secret`. The import keeps the reference AND the value, and names
// the variable as one the person may move into the vault (nocx-zn386): the
// URL is `{{baseUrl}}/bot{{token}}/sendMessage` either way, and the
// difference between the two rules is whether the first Send works.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
)

//nolint:gosec // a test fixture, and the same string the e2e export carries
const telegramToken = "e2e-telegram-bot-token-77a1c39fbe0284d5619c"

func TestImport_ATokenInThePathIsCarriedAndOffered(t *testing.T) {
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
	unsupported, err := ImportInto(t.Context(), NewOSFS(), dest, strings.NewReader(doc), apicoll.Route{}, nil)
	if err != nil {
		t.Fatalf("ImportInto: %v", err)
	}

	files := walkFiles(t, dest)
	var requestText string
	for name, body := range files {
		if strings.HasSuffix(name, ".json") && strings.Contains(body, `"method"`) {
			requestText = body
		}
		_ = name
	}
	if requestText == "" {
		t.Fatal("the import wrote no request file")
	}
	if !strings.Contains(requestText, "{{baseUrl}}/bot{{token}}/sendMessage") {
		t.Errorf("the request's URL is not the export's:\n%s", requestText)
	}
	env := files["environments/default.json"]
	if !strings.Contains(env, telegramToken) {
		t.Errorf("the environment does not carry the token the export held: %s", env)
	}
	for _, item := range unsupported {
		if strings.Contains(item.What, "token") {
			t.Fatalf("the carried variable was reported as a loss: %+v", unsupported)
		}
	}
}
