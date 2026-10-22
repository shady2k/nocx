package fishchannel

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/lifecycle"
)

// These are conversion bounds, not new protocol bounds. Frame bytes remain
// lifecycle-owned. Aggregate slots prevent many mostly empty arrays from
// amplifying a small input; cumulative work/output bounds prevent repeated
// ancestor paths from amplifying a deep JSON document.
const (
	maxInputBytes      = 8 * lifecycle.MaxFrameBytes
	maxNodes           = 16384
	maxArraySlots      = 16384
	maxDepth           = 64
	maxConversionBytes = 16 * lifecycle.MaxFrameBytes
	maxWorkBytes       = 32 * lifecycle.MaxFrameBytes
)

type budget struct {
	nodes int
	slots int
	work  int
}

func (b *budget) charge(n int) error {
	if n < 0 || n > maxWorkBytes-b.work {
		return errData
	}
	b.work += n
	return nil
}

func (b *budget) node() error {
	if b.nodes >= maxNodes {
		return errData
	}
	b.nodes++
	return nil
}

func (b *budget) array(n int) error {
	if n < 0 || n > maxArraySlots-b.slots {
		return errData
	}
	b.slots += n
	return nil
}

type tree struct {
	value  any
	object map[string]*tree
	array  []*tree
}

func objectTree() *tree {
	return &tree{value: make(map[string]any), object: make(map[string]*tree)}
}

func scalarOrContainer(typ, value string, b *budget) (*tree, error) {
	if err := b.node(); err != nil {
		return nil, err
	}
	t := &tree{}
	switch typ {
	case "object":
		if value != "" {
			return nil, errData
		}
		return objectTree(), nil
	case "array":
		n, err := decimalIndex(value)
		if err != nil || b.array(n) != nil {
			return nil, errData
		}
		t.array = make([]*tree, n)
		t.value = make([]any, n)
	case "string":
		t.value = value
	case "number":
		if value == "" || (value[0] != '-' && (value[0] < '0' || value[0] > '9')) || !json.Valid([]byte(value)) || strings.TrimSpace(value) != value {
			return nil, errData
		}
		t.value = json.Number(value)
	case "bool":
		if value != "true" && value != "false" {
			return nil, errData
		}
		t.value = value == "true"
	case "null":
		if value != "" {
			return nil, errData
		}
	default:
		return nil, errData
	}
	return t, nil
}

func decimalIndex(s string) (int, error) {
	if s == "" || len(s) > 5 || len(s) > 1 && s[0] == '0' {
		return 0, errData
	}
	n := 0
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, errData
		}
		n = n*10 + int(s[i]-'0')
	}
	if n > maxArraySlots {
		return 0, errData
	}
	return n, nil
}

func pointerParts(pointer string, b *budget) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if pointer[0] != '/' || strings.Count(pointer, "/") > maxDepth {
		return nil, errData
	}
	if err := b.charge(len(pointer)); err != nil {
		return nil, err
	}
	parts := strings.Split(pointer[1:], "/")
	for i, part := range parts {
		// RFC 6901 replacements are simultaneous: ~01 is the key ~1,
		// not /. Reject every unrecognized/incomplete escape.
		if strings.Contains(part, "~") {
			var decoded strings.Builder
			decoded.Grow(len(part))
			for j := 0; j < len(part); j++ {
				if part[j] != '~' {
					decoded.WriteByte(part[j])
					continue
				}
				j++
				if j == len(part) || part[j] != '0' && part[j] != '1' {
					return nil, errData
				}
				if part[j] == '0' {
					decoded.WriteByte('~')
				} else {
					decoded.WriteByte('/')
				}
			}
			parts[i] = decoded.String()
		}
	}
	return parts, nil
}

func (t *tree) child(key string) (*tree, error) {
	if t.object != nil {
		child := t.object[key]
		if child == nil {
			return nil, errData
		}
		return child, nil
	}
	if t.array != nil {
		i, err := decimalIndex(key)
		if err != nil || i >= len(t.array) || t.array[i] == nil {
			return nil, errData
		}
		return t.array[i], nil
	}
	return nil, errData
}

func (t *tree) insert(key string, child *tree) error {
	if t.object != nil {
		if _, exists := t.object[key]; exists {
			return errData
		}
		value, ok := t.value.(map[string]any)
		if !ok {
			return errData
		}
		t.object[key] = child
		value[key] = child.value
		return nil
	}
	if t.array != nil {
		i, err := decimalIndex(key)
		if err != nil || i >= len(t.array) || t.array[i] != nil {
			return errData
		}
		value, ok := t.value.([]any)
		if !ok {
			return errData
		}
		t.array[i] = child
		value[i] = child.value
		return nil
	}
	return errData
}

func (t *tree) complete() bool {
	for _, child := range t.object {
		if !child.complete() {
			return false
		}
	}
	for _, child := range t.array {
		if child == nil || !child.complete() {
			return false
		}
	}
	return true
}

func parseTriples(input []byte) (*tree, error) {
	if len(input) == 0 || len(input) > maxInputBytes || !utf8.Valid(input) {
		return nil, errData
	}
	b := budget{nodes: 1}
	root := objectTree()
	rootMarker := false
	triples := 0
	for len(input) > 0 {
		var fields [3]string
		for i := range fields {
			end := bytes.IndexByte(input, 0)
			if end < 0 || b.charge(end+1) != nil {
				return nil, errData
			}
			fields[i] = string(input[:end])
			input = input[end+1:]
		}
		parts, err := pointerParts(fields[0], &b)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			if triples != 0 || rootMarker || fields[1] != "object" || fields[2] != "" {
				return nil, errData
			}
			rootMarker = true
		} else {
			parent := root
			for _, part := range parts[:len(parts)-1] {
				parent, err = parent.child(part)
				if err != nil {
					return nil, err
				}
			}
			child, err := scalarOrContainer(fields[1], fields[2], &b)
			if err != nil || parent.insert(parts[len(parts)-1], child) != nil {
				return nil, errData
			}
		}
		triples++
	}
	if !root.complete() || jsonSize(root.value, lifecycle.MaxFrameBytes) > lifecycle.MaxFrameBytes {
		return nil, errData
	}
	return root, nil
}

func triplesToJSON(input []byte) ([]byte, error) {
	root, err := parseTriples(input)
	if err != nil {
		return nil, err
	}
	var event any
	if child := root.object["evt"]; child != nil {
		event = child.value
	}
	if event == "hello" && jsonSize(root.value, lifecycle.MaxHelloBytes) > lifecycle.MaxHelloBytes {
		return nil, errData
	}
	return json.Marshal(root.value)
}

func validateBodySize(body []byte, event any) error {
	if len(body) > lifecycle.MaxFrameBytes || event == "hello" && len(body) > lifecycle.MaxHelloBytes {
		return errData
	}
	return nil
}

// jsonSize computes the exact encoding/json size without allocating encoded
// strings or a body larger than the lifecycle bound. Work is linear in the
// already bounded tree, with early saturation at limit+1.
func jsonSize(value any, limit int) int {
	size := 0
	switch value := value.(type) {
	case map[string]any:
		size = 2
		for key, child := range value {
			size += jsonStringSize(key) + 1 + jsonSize(child, limit) + 1
			if size > limit+1 {
				return limit + 1
			}
		}
		if len(value) > 0 {
			size--
		}
	case []any:
		size = 2
		for _, child := range value {
			size += jsonSize(child, limit) + 1
			if size > limit+1 {
				return limit + 1
			}
		}
		if len(value) > 0 {
			size--
		}
	case string:
		size = jsonStringSize(value)
	case json.Number:
		size = len(value)
	case bool:
		size = 4
		if !value {
			size++
		}
	case nil:
		size = 4
	}
	if size > limit {
		return limit + 1
	}
	return size
}

func jsonStringSize(value string) int {
	size := 2
	for _, r := range value {
		switch r {
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			size += 2
		case '<', '>', '&', '\u2028', '\u2029':
			size += 6
		default:
			if r < 0x20 {
				size += 6
			} else {
				size += utf8.RuneLen(r)
			}
		}
	}
	return size
}

// readJSON uses Token/UseNumber so bounds are checked before growing containers,
// duplicate object keys are not silently overwritten, and number text is exact.
func readJSON(d *json.Decoder, b *budget, depth int) (any, error) {
	if depth > maxDepth || b.node() != nil {
		return nil, errData
	}
	token, err := d.Token()
	if err != nil {
		return nil, errData
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			obj := make(map[string]any)
			for d.More() {
				keyToken, err := d.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok || strings.ContainsRune(key, 0) || b.charge(len(key)) != nil {
					return nil, errData
				}
				if _, exists := obj[key]; exists {
					return nil, errData
				}
				child, err := readJSON(d, b, depth+1)
				if err != nil {
					return nil, err
				}
				obj[key] = child
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, errData
			}
			return obj, nil
		case '[':
			arr := make([]any, 0)
			for d.More() {
				if b.array(1) != nil {
					return nil, errData
				}
				child, err := readJSON(d, b, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, child)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, errData
			}
			return arr, nil
		}
	case string:
		if strings.ContainsRune(value, 0) || b.charge(len(value)) != nil {
			return nil, errData
		}
		return value, nil
	case json.Number:
		if b.charge(len(value)) != nil {
			return nil, errData
		}
		return value, nil
	case bool, nil:
		return value, nil
	}
	return nil, errData
}

func appendTriple(out []byte, path, typ, value string, b *budget) ([]byte, error) {
	n := len(path) + len(typ) + len(value) + 3
	if n > maxConversionBytes-len(out) || b.charge(n) != nil {
		return nil, errData
	}
	out = append(out, path...)
	out = append(out, 0)
	out = append(out, typ...)
	out = append(out, 0)
	out = append(out, value...)
	return append(out, 0), nil
}

func childPath(path, key string, b *budget) (string, error) {
	// Charge the exact escaped length before allocating the new path.
	n := len(path) + 1 + len(key) + strings.Count(key, "~") + strings.Count(key, "/")
	if n > maxConversionBytes || b.charge(n) != nil {
		return "", errData
	}
	var result strings.Builder
	result.Grow(n)
	result.WriteString(path)
	result.WriteByte('/')
	for i := range len(key) {
		switch key[i] {
		case '~':
			result.WriteString("~0")
		case '/':
			result.WriteString("~1")
		default:
			result.WriteByte(key[i])
		}
	}
	return result.String(), nil
}

func emitJSON(out []byte, path string, value any, b *budget) ([]byte, error) {
	var err error
	switch value := value.(type) {
	case map[string]any:
		out, err = appendTriple(out, path, "object", "", b)
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child, pathErr := childPath(path, key, b)
			if pathErr != nil {
				return nil, pathErr
			}
			out, err = emitJSON(out, child, value[key], b)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case []any:
		out, err = appendTriple(out, path, "array", strconv.Itoa(len(value)), b)
		if err != nil {
			return nil, err
		}
		for i, item := range value {
			child, pathErr := childPath(path, strconv.Itoa(i), b)
			if pathErr != nil {
				return nil, pathErr
			}
			out, err = emitJSON(out, child, item, b)
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	case string:
		return appendTriple(out, path, "string", value, b)
	case json.Number:
		return appendTriple(out, path, "number", string(value), b)
	case bool:
		return appendTriple(out, path, "bool", strconv.FormatBool(value), b)
	case nil:
		return appendTriple(out, path, "null", "", b)
	}
	return nil, errData
}

func jsonToTriples(body []byte) ([]byte, error) {
	if len(body) == 0 || len(body) > lifecycle.MaxFrameBytes || !utf8.Valid(body) {
		return nil, errData
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	b := budget{}
	value, err := readJSON(d, &b, 0)
	root, ok := value.(map[string]any)
	if err != nil || !ok || validateBodySize(body, root["evt"]) != nil {
		return nil, errData
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errData
	}
	return emitJSON(nil, "", value, &b)
}
