// Command compare diffs two observation files key by key.
//
// Both drivers emit {"probe","case","key","value"} lines, so the comparison is
// a lookup: for every key the two files share, are the values equal? A key
// only one side emits is reported separately rather than counted as a
// difference — the wasm build legitimately reports things a native process
// cannot (its own linear memory) and cannot report things it does not carry.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

type record struct {
	Probe string `json:"probe"`
	Case  string `json:"case"`
	Key   string `json:"key"`
	Value any    `json:"value"`
}

func (r record) id() string { return r.Probe + "\x00" + r.Case + "\x00" + r.Key }

func load(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]any{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[r.id()] = r.Value
	}
	return out, sc.Err()
}

func fmtVal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func main() {
	wasmPath := flag.String("wasm", "results/probes.jsonl", "observations from the wasm build")
	nativePath := flag.String("native", "../emulator/results/ghostty.jsonl", "observations from the native build")
	flag.Parse()

	wasm, err := load(*wasmPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		os.Exit(1)
	}
	native, err := load(*nativePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare:", err)
		os.Exit(1)
	}

	var shared, same int
	var diffs, onlyWasm, onlyNative []string
	for id, wv := range wasm {
		nv, ok := native[id]
		if !ok {
			onlyWasm = append(onlyWasm, id+" = "+fmtVal(wv))
			continue
		}
		shared++
		if fmtVal(wv) == fmtVal(nv) {
			same++
			continue
		}
		diffs = append(diffs, fmt.Sprintf("%s\n    wasm:   %s\n    native: %s",
			strings.ReplaceAll(id, "\x00", " / "), fmtVal(wv), fmtVal(nv)))
	}
	for id, nv := range native {
		if _, ok := wasm[id]; !ok {
			onlyNative = append(onlyNative, id+" = "+fmtVal(nv))
		}
	}
	sort.Strings(diffs)
	sort.Strings(onlyWasm)
	sort.Strings(onlyNative)

	fmt.Printf("shared keys: %d   identical: %d   different: %d\n", shared, same, len(diffs))
	fmt.Printf("only in wasm: %d   only in native: %d\n\n", len(onlyWasm), len(onlyNative))

	if len(diffs) > 0 {
		fmt.Println("=== DIFFERENCES ===")
		for _, d := range diffs {
			fmt.Println(strings.ReplaceAll(d, "\x00", " / "))
		}
		fmt.Println()
	}
	if len(onlyWasm) > 0 {
		fmt.Println("=== ONLY IN WASM ===")
		for _, s := range onlyWasm {
			fmt.Println("  " + strings.ReplaceAll(s, "\x00", " / "))
		}
		fmt.Println()
	}
	if len(onlyNative) > 0 {
		fmt.Println("=== ONLY IN NATIVE ===")
		for _, s := range onlyNative {
			fmt.Println("  " + strings.ReplaceAll(s, "\x00", " / "))
		}
	}
	if len(diffs) > 0 {
		os.Exit(1)
	}
}
