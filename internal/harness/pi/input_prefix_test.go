package pi

import (
	"encoding/json"
	"reflect"
	"testing"

	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
)

func piInputPrefixPreserved(before, after fakemodel.Request) bool {
	if before.Model != after.Model || len(after.Messages) < len(before.Messages) {
		return false
	}
	// Decode whole messages, not extracted text: roles, tool calls, IDs and all
	// provider fields must be identical and in the same order.
	equalJSON := func(a, b any) bool {
		left, err := json.Marshal(a)
		if err != nil {
			return false
		}
		right, err := json.Marshal(b)
		if err != nil {
			return false
		}
		var x, y any
		if json.Unmarshal(left, &x) != nil || json.Unmarshal(right, &y) != nil {
			return false
		}
		return reflect.DeepEqual(x, y)
	}
	return equalJSON(before.Messages, after.Messages[:len(before.Messages)]) && equalJSON(before.Tools, after.Tools)
}

func TestPiInputPrefixRejectsChangedInputs(t *testing.T) {
	request := func(messages, tools string) fakemodel.Request {
		raw := []byte(`{"model":"test-model","messages":` + messages + `,"tools":` + tools + `}`)
		var r fakemodel.Request
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		r.Body = raw
		return r
	}
	tools := `[{"type":"function","function":{"name":"probe","parameters":{"type":"object"}}}]`
	first := `{"role":"system","content":"instructions"}`
	user := `{"role":"user","content":"question"}`
	answer := `{"role":"assistant","content":"answer"}`
	before := request(`[`+first+`,`+user+`]`, tools)
	for name, after := range map[string]fakemodel.Request{
		"reordered":     request(`[`+user+`,`+first+`,`+answer+`]`, tools),
		"duplicated":    request(`[`+first+`,`+first+`,`+user+`,`+answer+`]`, tools),
		"altered":       request(`[`+first+`,{"role":"user","content":"changed question"},`+answer+`]`, tools),
		"tools changed": request(`[`+first+`,`+user+`,`+answer+`]`, `[]`),
	} {
		t.Run(name, func(t *testing.T) {
			if piInputPrefixPreserved(before, after) {
				t.Fatal("accepted changed model input prefix")
			}
		})
	}
	if !piInputPrefixPreserved(before, request(`[`+first+`,`+user+`,`+answer+`]`, tools)) {
		t.Fatal("rejected unchanged model input prefix")
	}
}
