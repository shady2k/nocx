// diff compares a candidate's frame against the xterm.js ground truth.
//
// nocx-szb40.1 sets two different bars and this tool keeps them apart. For a
// FRAME, what matters is the chrome anchors — the input line, the prompt
// footer, the spinner, the menu rows — because that is all a driver reads to
// decide whether nocx may type. For BLOCK TEXT the bar is content fidelity,
// which is every cell. A candidate can fail the second and still carry the
// first, so a single "percentage correct" would hide the decision rather than
// inform it.
//
// Anchors are matched on the GROUND TRUTH and then checked in the candidate.
// That direction is deliberate: the question is never "did the candidate
// invent an input box" but "is the input box that is really there also there
// for the candidate".
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type cell struct {
	Ch string `json:"ch"`
	W  int    `json:"w"`
}

type row struct {
	Text  string  `json:"text"`
	Cells []*cell `json:"cells"`
}

type frame struct {
	Renderer string `json:"renderer"`
	Version  string `json:"version"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
	Cursor   struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"cursor"`
	Grid  []row    `json:"grid"`
	Notes []string `json:"notes"`
}

// anchor is a piece of chrome a driver would key on. The patterns are written
// against what these agents and TUIs actually print, not against a spec.
var anchors = []struct {
	name string
	re   *regexp.Regexp
}{
	{"input line", regexp.MustCompile(`(?i)(^\s*[>❯]\s|\bprompted\s*>|^\s*│\s*>)`)},
	{"menu row", regexp.MustCompile(`(?:^|\s)[❯>]?\s*\d\.\s+\S`)},
	{"prompt footer / rule", regexp.MustCompile(`[─━╌╍═_]{10,}`)},
	{"spinner glyph", regexp.MustCompile(`[\x{2800}-\x{28FF}✻✽✶✢·*◐◓◑◒⠋⠙⠹]`)},
	{"box corner", regexp.MustCompile(`[╭╮╰╯┌┐└┘│]`)},
}

func load(p string) (frame, error) {
	var f frame
	b, err := os.ReadFile(p)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(b, &f)
}

func norm(s string) string { return strings.TrimRight(s, " ") }

func main() {
	truth := flag.String("truth", "", "xterm.js ground-truth frame")
	quiet := flag.Bool("quiet", false, "one summary line per candidate, no row detail")
	maxRows := flag.Int("rows", 4, "how many differing rows to show")
	flag.Parse()
	if *truth == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: diff -truth GROUND.json CANDIDATE.json...")
		os.Exit(2)
	}
	gt, err := load(*truth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", *truth, err)
		os.Exit(1)
	}

	// Which ground-truth rows carry chrome, and what kind.
	anchorRows := map[int][]string{}
	for y, r := range gt.Grid {
		for _, a := range anchors {
			if a.re.MatchString(r.Text) {
				anchorRows[y] = append(anchorRows[y], a.name)
			}
		}
	}

	for _, path := range flag.Args() {
		cand, err := load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		if len(cand.Grid) == 0 {
			fmt.Printf("%-22s NO GRID  %s\n", cand.Renderer, strings.Join(cand.Notes, "; "))
			continue
		}
		var diffRows []int
		cellsSame, cellsTotal := 0, 0
		geomSame, geomTotal := 0, 0
		for y := 0; y < gt.Rows && y < len(cand.Grid); y++ {
			g, c := gt.Grid[y], cand.Grid[y]
			if norm(g.Text) != norm(c.Text) {
				diffRows = append(diffRows, y)
			}
			gr, cr := []rune(g.Text), []rune(c.Text)
			for x := 0; x < gt.Cols; x++ {
				cellsTotal++
				var gc, cc rune = ' ', ' '
				if x < len(gr) {
					gc = gr[x]
				}
				if x < len(cr) {
					cc = cr[x]
				}
				if gc == cc {
					cellsSame++
				}
			}
			// COLUMN GEOMETRY, which the text comparison above is blind to.
			// A library that packs a double-width character into one column
			// reads back identical TEXT and puts everything after it one
			// column to the left. Chrome anchors are positional, so that is
			// the failure the frame consumer actually cares about — and the
			// first version of this tool could not see it.
			for x := 0; x < gt.Cols; x++ {
				geomTotal++
				if occupancy(g.Cells, x) == occupancy(c.Cells, x) {
					geomSame++
				}
			}
		}
		anchorHit, anchorMiss := 0, map[string]int{}
		for y, kinds := range anchorRows {
			if y >= len(cand.Grid) {
				for _, k := range kinds {
					anchorMiss[k]++
				}
				continue
			}
			if norm(gt.Grid[y].Text) == norm(cand.Grid[y].Text) {
				anchorHit++
			} else {
				for _, k := range kinds {
					anchorMiss[k]++
				}
			}
		}
		curOK := "cursor MISMATCH"
		if cand.Cursor.X == gt.Cursor.X && cand.Cursor.Y == gt.Cursor.Y {
			curOK = "cursor ok"
		}
		pct := 100.0 * float64(cellsSame) / float64(cellsTotal)
		geo := 100.0 * float64(geomSame) / float64(geomTotal)
		fmt.Printf("%-22s text %6.2f%%  geometry %6.2f%%  rows differing %2d/%d  anchors ok %d/%d  %s\n",
			cand.Renderer, pct, geo, len(diffRows), gt.Rows, anchorHit, len(anchorRows), curOK)
		if len(anchorMiss) > 0 {
			var parts []string
			for k, n := range anchorMiss {
				parts = append(parts, fmt.Sprintf("%s×%d", k, n))
			}
			fmt.Printf("%-22s   ANCHORS MISSED: %s\n", "", strings.Join(parts, ", "))
		}
		for _, n := range cand.Notes {
			fmt.Printf("%-22s   note: %s\n", "", n)
		}
		if !*quiet {
			shown := 0
			for _, y := range diffRows {
				if shown >= *maxRows {
					fmt.Printf("%-22s   … %d more differing rows\n", "", len(diffRows)-shown)
					break
				}
				kinds := ""
				if k, ok := anchorRows[y]; ok {
					kinds = "  <- " + strings.Join(k, "+")
				}
				fmt.Printf("%-22s   row %2d%s\n", "", y, kinds)
				fmt.Printf("%-22s     truth |%s|\n", "", trunc(norm(gt.Grid[y].Text), 92))
				fmt.Printf("%-22s     cand  |%s|\n", "", trunc(norm(cand.Grid[y].Text), 92))
				shown++
			}
		}
	}
}

// occupancy answers what column x HOLDS, in a form comparable across
// renderers that disagree about how to spell "this is the second half of the
// character to my left". xterm.js writes an empty string there; x/vt writes a
// space; both mark it width 0, and that is the part that means something.
func occupancy(cells []*cell, x int) string {
	if x >= len(cells) || cells[x] == nil {
		return "?"
	}
	c := cells[x]
	if c.W == 0 {
		return "\x00CONT"
	}
	ch := c.Ch
	if ch == "" {
		ch = " "
	}
	return fmt.Sprintf("%s|%d", ch, c.W)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
