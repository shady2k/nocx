package proto

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSSHHostKeyPinParamsConformToContract(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	const dir = "../../../contracts/helper"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		doc, parseErr := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if addErr := compiler.AddResource("https://nocx.local/contracts/helper/"+entry.Name(), doc); addErr != nil {
			t.Fatal(addErr)
		}
	}
	for _, tc := range []struct {
		name   string
		params any
		base   string
	}{
		{"lease", &LeaseParams{}, `{"destination":{"host":"host","port":22,"user":"user","identity":{"auth":"interactive"}},"acceptOnTrust":false}`},
		{"open", &OpenChannelParams{}, `{"destination":{"host":"host","port":22,"user":"user","identity":{"auth":"interactive"}},"kind":"sftp","acceptOnTrust":false}`},
		{"lane", &LaneParams{}, `{"destination":{"host":"host","port":22,"user":"user","identity":{"auth":"interactive"}},"machine":{"dir":"/home/user/.nocx/helper/build"},"generation":"abc123"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, compileErr := compiler.Compile("https://nocx.local/contracts/helper/ssh." + tc.name + ".params.schema.json")
			if compileErr != nil {
				t.Fatal(compileErr)
			}
			for _, pin := range []string{"", "SHA256:accepted-host-key"} {
				raw := []byte(tc.base)
				if pin != "" {
					raw = []byte(strings.TrimSuffix(tc.base, "}") + `,"hostKeyFingerprint":"` + pin + `"}`)
				}
				doc, parseErr := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				if validateErr := schema.Validate(doc); validateErr != nil {
					t.Errorf("pin %q: %v", pin, validateErr)
				}
				if decodeErr := json.Unmarshal(raw, tc.params); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				encoded, encodeErr := json.Marshal(tc.params)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				var back map[string]any
				if decodeErr := json.Unmarshal(encoded, &back); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if pin == "" {
					if _, present := back["hostKeyFingerprint"]; present {
						t.Error("empty pin must be omitted")
					}
				} else if back["hostKeyFingerprint"] != pin {
					t.Errorf("pin lost on wire: %s", encoded)
				}
			}
			invalid := []byte(strings.TrimSuffix(tc.base, "}") + `,"hostKeyFingerprint":42}`)
			doc, parseErr := jsonschema.UnmarshalJSON(bytes.NewReader(invalid))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if schema.Validate(doc) == nil {
				t.Error("non-string pin accepted")
			}
		})
	}
}
