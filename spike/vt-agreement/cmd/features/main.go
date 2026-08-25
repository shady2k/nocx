// features reports which VT constructs a capture actually exercises.
//
// nocx-szb40.1 names the things known to break naive parsers — alternate
// screen, scroll regions, wide characters, braille spinners — and says the
// bar differs by consumer. Before comparing renderers it is worth knowing
// what a given capture puts in front of them, because a capture that never
// enters the alternate screen cannot report whether a candidate handles it.
//
// This counts sequences. It does not render anything, on purpose: a summary
// produced by one of the parsers under test would be evidence about that
// parser.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"
)

type chunk struct {
	Data string `json:"data"`
}

func load(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var sb strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	first := true
	for sc.Scan() {
		if first { // header
			first = false
			continue
		}
		var c chunk
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			return "", err
		}
		sb.WriteString(c.Data)
	}
	return sb.String(), sc.Err()
}

// scan walks the stream once and tallies what it finds. It is a recogniser,
// not an emulator: it never maintains a grid, so nothing here can be wrong
// about rendering, only about counting.
func scan(s string) (map[string]int, map[rune]int) {
	seq := map[string]int{}
	wide := map[rune]int{}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == 0x1b {
			j := i + 1
			if j >= len(rs) {
				break
			}
			switch rs[j] {
			case '[': // CSI
				k := j + 1
				params := strings.Builder{}
				for k < len(rs) && (rs[k] < 0x40 || rs[k] > 0x7e) {
					params.WriteRune(rs[k])
					k++
				}
				if k < len(rs) {
					seq["CSI "+params.String()+string(rs[k])]++
					seq["_final:"+string(rs[k])]++
					i = k
				}
			case ']': // OSC
				k := j + 1
				for k < len(rs) && rs[k] != 0x07 && !(rs[k] == 0x1b && k+1 < len(rs) && rs[k+1] == '\\') {
					k++
				}
				head := string(rs[j+1 : min(k, j+6)])
				seq["OSC "+head]++
				i = k
			case 'P': // DCS
				seq["DCS"]++
				i = j
			case '7', '8':
				seq["ESC "+string(rs[j])]++
				i = j
			default:
				seq["ESC "+string(rs[j])]++
				i = j
			}
			continue
		}
		if r >= 0x2800 && r <= 0x28ff {
			wide[r]++
			seq["_braille"]++
		}
		if r == 0x00a0 {
			seq["_NBSP"]++
		}
		if r > 0x1100 && (unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)) {
			wide[r]++
			seq["_CJK"]++
		}
		if r >= 0x2500 && r <= 0x257f {
			seq["_boxdrawing"]++
		}
		if r >= 0x1f300 {
			wide[r]++
			seq["_emoji"]++
		}
	}
	return seq, wide
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// named is the checklist the bead cares about, expressed as the sequence that
// would have to appear for a capture to exercise it.
var named = []struct {
	label string
	keys  []string
}{
	{"alternate screen (?1049 / ?47)", []string{"CSI ?1049h", "CSI ?1049l", "CSI ?47h", "CSI ?47l"}},
	{"scroll region (DECSTBM, final r)", []string{"_final:r"}},
	{"cursor absolute column (CHA, final G)", []string{"_final:G"}},
	{"cursor position (CUP, final H)", []string{"_final:H"}},
	{"erase in display / line (J, K)", []string{"_final:J", "_final:K"}},
	{"scroll up / down (S, T)", []string{"_final:S", "_final:T"}},
	{"insert / delete lines (L, M)", []string{"_final:L", "_final:M"}},
	{"SGR (final m)", []string{"_final:m"}},
	{"bracketed paste (?2004)", []string{"CSI ?2004h", "CSI ?2004l"}},
	{"cursor visibility (?25)", []string{"CSI ?25h", "CSI ?25l"}},
	{"save / restore cursor (ESC 7/8)", []string{"ESC 7", "ESC 8"}},
	{"OSC (title and friends)", []string{"OSC 0", "OSC 1", "OSC 2", "OSC 8", "OSC 9"}},
	{"braille glyphs (spinner)", []string{"_braille"}},
	{"box drawing", []string{"_boxdrawing"}},
	{"NBSP U+00A0", []string{"_NBSP"}},
	{"CJK wide characters", []string{"_CJK"}},
	{"emoji", []string{"_emoji"}},
}

func main() {
	top := flag.Int("top", 20, "how many raw sequences to list")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: features CAPTURE.jsonl")
		os.Exit(2)
	}
	for _, path := range flag.Args() {
		s, err := load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
		seq, wide := scan(s)
		fmt.Printf("\n=== %s ===\n%d bytes, %d runes\n\n", path, len(s), len([]rune(s)))
		fmt.Println("CHECKLIST (what this capture can and cannot report on):")
		for _, n := range named {
			total := 0
			var hits []string
			for _, k := range n.keys {
				if seq[k] > 0 {
					total += seq[k]
					hits = append(hits, fmt.Sprintf("%s×%d", strings.TrimPrefix(k, "_"), seq[k]))
				}
			}
			mark := "  no"
			if total > 0 {
				mark = " YES"
			}
			fmt.Printf(" %s  %-38s %s\n", mark, n.label, strings.Join(hits, " "))
		}
		type kv struct {
			k string
			v int
		}
		var all []kv
		for k, v := range seq {
			if !strings.HasPrefix(k, "_") {
				all = append(all, kv{k, v})
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v })
		fmt.Printf("\nTOP %d SEQUENCES:\n", *top)
		for i, e := range all {
			if i >= *top {
				break
			}
			fmt.Printf("  %6d  %q\n", e.v, e.k)
		}
		if len(wide) > 0 {
			fmt.Printf("\nWIDE/SPECIAL RUNES: %d distinct\n", len(wide))
		}
	}
}
