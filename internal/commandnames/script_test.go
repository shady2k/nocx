package commandnames

import (
	"strings"
	"testing"
)

// A banner in front of the frame is not part of the answer, and an answer
// that never closes its frame is a prefix — which is the partial result that
// may never be published.
func TestFramed_RejectsPollutedAndUnclosedAnswers(t *testing.T) {
	nonce := "abc123"
	body := "NOCX_CN abc123 BEGIN\nN ls\nNOCX_CN abc123 END\n"

	got, err := framed([]byte("Welcome to example.\nLast login: ...\n"+body+"you have mail\n"), nonce)
	if err != nil {
		t.Fatalf("a banner-wrapped answer was rejected: %v", err)
	}
	if strings.Join(got, ",") != "N ls" {
		t.Fatalf("lines = %v", got)
	}

	if _, err := framed([]byte("NOCX_CN abc123 BEGIN\nN ls\n"), nonce); err == nil {
		t.Fatalf("an unclosed frame was accepted")
	}
	if _, err := framed([]byte(body), "other"); err == nil {
		t.Fatalf("a frame carrying somebody else's nonce was accepted")
	}
}

func TestParseProbe_ReadsEveryFieldAndBoundsTheStamps(t *testing.T) {
	var b strings.Builder
	b.WriteString("NOCX_CN n BEGIN\nV 1\nU deploy\nF zsh\nP /usr/bin:/bin\n")
	for i := 0; i < 100; i++ {
		b.WriteString("D /d\nS 1\n")
	}
	b.WriteString("NOCX_CN n END\n")

	p, err := parseProbe([]byte(b.String()), "n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.User != "deploy" || p.ShellFamily != "zsh" || p.Path != "/usr/bin:/bin" {
		t.Fatalf("probe = %+v", p)
	}
	if len(p.Stamps) != MaxPathDirs {
		t.Fatalf("stamps = %d, want the %d bound", len(p.Stamps), MaxPathDirs)
	}
}

// A directory whose name contains a space still pairs with its own stamp:
// the two ride on separate lines precisely so no separator has to survive a
// path.
func TestParseProbe_PairsDirectoriesWithSpacesToTheirStamps(t *testing.T) {
	in := "NOCX_CN n BEGIN\nV 1\nU u\nF sh\nP /a b:/c\nD /a b\nS 7\nD /c\nS 8\nNOCX_CN n END\n"
	p, err := parseProbe([]byte(in), "n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.Stamps) != 2 || p.Stamps[0] != (DirStamp{Dir: "/a b", Stamp: "7"}) {
		t.Fatalf("stamps = %+v", p.Stamps)
	}
}

func TestParseProbe_RefusesAnUnknownProtocolAndAMissingPath(t *testing.T) {
	if _, err := parseProbe([]byte("NOCX_CN n BEGIN\nV 2\nP /bin\nNOCX_CN n END\n"), "n"); err == nil {
		t.Fatalf("protocol 2 was accepted")
	}
	if _, err := parseProbe([]byte("NOCX_CN n BEGIN\nV 1\nU u\nNOCX_CN n END\n"), "n"); err == nil {
		t.Fatalf("a probe with no PATH was accepted")
	}
}

// An rc file that prints inside the frame contributes no names, and an empty
// enumeration is refused rather than published as "this shell can run
// nothing".
func TestParseScan_TakesOnlyTaggedNamesAndRefusesAnEmptySet(t *testing.T) {
	s, err := parseScan([]byte("NOCX_CN n BEGIN\nN ls\nnot a name\nN grep\nNOCX_CN n END\n"), "n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Join(s.Names, ",") != "ls,grep" {
		t.Fatalf("names = %v", s.Names)
	}
	if _, err := parseScan([]byte("NOCX_CN n BEGIN\nNOCX_CN n END\n"), "n"); err == nil {
		t.Fatalf("an empty enumeration was accepted")
	}
}
