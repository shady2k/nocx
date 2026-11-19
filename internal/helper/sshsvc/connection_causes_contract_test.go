//go:build nocx_local_ssh

package sshsvc_test

import (
	"fmt"
	"testing"
)

func TestConnectionStateContractRejectsImpossibleCauses(t *testing.T) {
	schema := loadHelperSchema(t, "ssh.connection-state.schema.json")
	allowed := map[string]map[string]bool{
		"reconnecting": {"lost-link": true, "silence": true},
		"failed":       {"host-key-changed": true, "auth-rejected": true, "no-auth-method": true},
		"ended":        {"lost-link": true, "released": true, "silence": true, "tainted": true},
	}
	for state, causes := range allowed {
		for _, cause := range []string{"host-key-changed", "auth-rejected", "no-auth-method", "lost-link", "released", "silence", "tainted"} {
			t.Run(state+"/"+cause, func(t *testing.T) {
				attempt := ""
				if state == "reconnecting" {
					attempt = `,"attempt":1`
				}
				raw := []byte(fmt.Sprintf(`{"connectionId":"connection","seq":1,"generation":1,"state":%q,"stateMs":0,"cause":%q%s}`, state, cause, attempt))
				err := validateHelperJSON(schema, raw)
				if (err == nil) != causes[cause] {
					t.Fatalf("state %s cause %s: validation=%v, allowed=%v", state, cause, err, causes[cause])
				}
			})
		}
	}
}
