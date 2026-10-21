package fishchannel

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
)

func triples(fields ...string) []byte {
	return []byte(strings.Join(fields, "\x00") + "\x00")
}

func TestConversionPreservesExactValuesAndPointerEscapes(t *testing.T) {
	body := []byte(`{"":"","a/b":{"~1":[],"~":{},"line":"魚\n🌊\t\"\\"},"array":[null,false,true,-0,123456789012345678901234567890.00e+999]}`)
	want := triples(
		"", "object", "",
		"/", "string", "",
		"/a~1b", "object", "",
		"/a~1b/line", "string", "魚\n🌊\t\"\\",
		"/a~1b/~0", "object", "",
		"/a~1b/~01", "array", "0",
		"/array", "array", "5",
		"/array/0", "null", "",
		"/array/1", "bool", "false",
		"/array/2", "bool", "true",
		"/array/3", "number", "-0",
		"/array/4", "number", "123456789012345678901234567890.00e+999",
	)
	got, err := jsonToTriples(body)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("triples = %q, want %q (%v)", got, want, err)
	}
	back, err := triplesToJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(back))
	decoder.UseNumber()
	var roundtrip map[string]any
	if err := decoder.Decode(&roundtrip); err != nil {
		t.Fatalf("invalid roundtrip JSON: %v", err)
	}
	wantObject := map[string]any{
		"": "",
		"a/b": map[string]any{
			"~1":   []any{},
			"~":    map[string]any{},
			"line": "魚\n🌊\t\"\\",
		},
		"array": []any{nil, false, true, json.Number("-0"), json.Number("123456789012345678901234567890.00e+999")},
	}
	if !reflect.DeepEqual(roundtrip, wantObject) {
		t.Fatalf("roundtrip semantics = %#v, want %#v", roundtrip, wantObject)
	}
}

func TestSendRejectsConflictingIncompleteAndExcessiveTrees(t *testing.T) {
	cases := map[string][]byte{
		"empty input":             nil,
		"unterminated":            []byte("/s\x00string\x00text"),
		"unfinished triple":       []byte("/s\x00string\x00"),
		"duplicate":               triples("/s", "string", "first", "/s", "string", "second"),
		"scalar parent":           triples("/s", "string", "first", "/s/x", "null", ""),
		"missing parent":          triples("/s/x", "null", ""),
		"bad escape":              triples("/s~2", "null", ""),
		"unfinished escape":       triples("/s~", "null", ""),
		"relative pointer":        triples("s", "null", ""),
		"root scalar":             triples("", "string", "s"),
		"late root":               triples("/s", "string", "s", "", "object", ""),
		"duplicate root":          triples("", "object", "", "", "object", ""),
		"null payload":            triples("/s", "null", "null"),
		"object payload":          triples("/s", "object", "{}"),
		"invalid bool":            triples("/s", "bool", "1"),
		"unknown type":            triples("/s", "secret-text", "never-print"),
		"invalid number":          triples("/s", "number", "01"),
		"number whitespace":       triples("/s", "number", "1 "),
		"number non-scalar":       triples("/s", "number", "[1]"),
		"sparse array":            triples("/a", "array", "2", "/a/1", "string", "x"),
		"huge array":              triples("/a", "array", "9999999999999999999999999999"),
		"array index alias":       triples("/a", "array", "1", "/a/00", "null", ""),
		"array append":            triples("/a", "array", "1", "/a/-", "null", ""),
		"array negative":          triples("/a", "array", "1", "/a/-1", "null", ""),
		"array out of range":      triples("/a", "array", "1", "/a/1", "null", ""),
		"aggregate array slots":   triples("/a", "array", "16384", "/b", "array", "1"),
		"deep pointer":            triples(strings.Repeat("/a", maxDepth+1), "null", ""),
		"invalid UTF8":            {'/', 's', 0, 's', 't', 'r', 'i', 'n', 'g', 0, 0xff, 0},
		"oversize input":          bytes.Repeat([]byte{'x'}, maxInputBytes+1),
		"oversize encoded string": triples("/s", "string", strings.Repeat("<", lifecycle.MaxFrameBytes/6)),
		"oversize hello":          triples("/evt", "string", "hello", "/s", "string", strings.Repeat("x", lifecycle.MaxHelloBytes)),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := triplesToJSON(input); err == nil || got != nil {
				t.Fatal("invalid tree was accepted")
			}
		})
	}
}

func TestReceiveRejectsInvalidJSONAndDecodedNUL(t *testing.T) {
	cases := []string{
		`[]`, `null`, `1`, `"string"`, ``, `{`, `{} {}`,
		`{"k":1,"k":2}`, `{"a":{"k":1,"k":2}}`,
		`{"k":"\u0000"}`, `{"\u0000":"k"}`, `{"k":["\u0000"]}`,
		`{"n":NaN}`, `{"n":01}`, `{"n":1e}`, `{"n":+1}`,
		`{"s":"` + string([]byte{0xff}) + `"}`,
		`{"evt":"hello","s":"` + strings.Repeat("x", lifecycle.MaxHelloBytes) + `"}`,
		`{"a":` + strings.Repeat("[", maxDepth+1) + `0` + strings.Repeat("]", maxDepth+1) + `}`,
		`{"a":[` + strings.Repeat("0,", maxArraySlots) + `0]}`,
	}
	for i, input := range cases {
		if got, err := jsonToTriples([]byte(input)); err == nil || got != nil {
			t.Fatalf("invalid JSON case %d accepted", i)
		}
	}
}

func TestPathExpansionAndWorkAreBounded(t *testing.T) {
	// Small enough to be a frame, but its repeated long ancestor path would
	// otherwise expand to several megabytes of NUL triples.
	body := []byte(`{"` + strings.Repeat("x", 32000) + `":[` + strings.Repeat(`"",`, 1000) + `""]}`)
	if len(body) >= lifecycle.MaxFrameBytes {
		t.Fatal("fixture no longer fits one frame")
	}
	if out, err := jsonToTriples(body); err == nil || out != nil {
		t.Fatal("path expansion was not bounded")
	}
	b := budget{work: maxWorkBytes}
	if _, err := childPath("/already", "large/key~", &b); err == nil {
		t.Fatal("path allocation ignored exhausted work budget")
	}
}

func TestJSONSizeMatchesEncodingBeforeAllocation(t *testing.T) {
	for _, text := range []string{"", "魚🌊", "<>&\u2028\u2029", "\x00\x01\b\f\n\r\t\"\\"} {
		for _, value := range []any{text, map[string]any{text: []any{text, nil, true, false, json.Number("-0.00e+9")}}} {
			body, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if got := jsonSize(value, lifecycle.MaxFrameBytes); got != len(body) {
				t.Fatalf("size = %d, encoded = %d", got, len(body))
			}
			if got := jsonSize(value, len(body)-1); got != len(body) {
				t.Fatalf("size did not saturate: %d", got)
			}
		}
	}
}

func TestEmptyContainersAndImplicitRoot(t *testing.T) {
	for _, input := range [][]byte{
		triples("", "object", ""),
		triples("/a", "array", "0", "/o", "object", ""),
	} {
		body, err := triplesToJSON(input)
		if err != nil {
			t.Fatal(err)
		}
		out, err := jsonToTriples(body)
		if err != nil || len(out) == 0 {
			t.Fatal("empty containers disappeared")
		}
	}
}
