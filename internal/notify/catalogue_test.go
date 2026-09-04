package notify_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/shady2k/nocx/internal/notify"
)

// ── the catalogue's own fixtures ───────────────────────────────────────
//
// Built here rather than reused from the shipped catalogue: the rules under
// test are about pairs the shipped catalogue deliberately cannot express (a
// heuristic kind and a channel that leaves the machine), so a test that could
// only reach the shipped one could not exercise them at all.

func localChannel(id string) notify.RoutableChannel {
	return notify.RoutableChannel{ID: id, Label: id, Description: "a local surface"}
}

func networkChannel(id string) notify.RoutableChannel {
	return notify.RoutableChannel{ID: id, Label: id, Description: "a network destination", LeavesMachine: true}
}

func catKind(id string, trust notify.Trust, defaults ...string) notify.RoutableKind {
	return notify.RoutableKind{
		Kind:            notify.Kind("test." + id),
		ID:              id,
		Label:           id,
		Description:     "a test kind",
		Trusts:          []notify.Trust{trust},
		DefaultChannels: defaults,
	}
}

// ── every declared Kind is routable ────────────────────────────────────

// declaredKinds reads the Kind constants out of notify.go itself. Parsing the
// source is the point: a test that enumerated the kinds in its own literal
// would agree with itself forever, and the criterion is that a kind added to
// notify.go tomorrow cannot be silently unroutable.
func declaredKinds(t *testing.T) []notify.Kind {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "notify.go", nil, 0)
	if err != nil {
		t.Fatalf("parse notify.go: %v", err)
	}
	var kinds []notify.Kind
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "Kind" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				unquoted, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					t.Fatalf("unquote %s: %v", lit.Value, uerr)
				}
				kinds = append(kinds, notify.Kind(unquoted))
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no Kind constants found in notify.go — the parser stopped matching the source")
	}
	return kinds
}

// The assertion is over the OFFERED pairs rather than a kind list, and that is
// the stronger statement: a kind the catalogue names but offers to no channel
// is exactly as unroutable as one nobody catalogued.
func TestCatalogueListsEveryDeclaredKind(t *testing.T) {
	listed := map[notify.Kind]bool{}
	for _, p := range notify.DefaultCatalogue().Pairs() {
		listed[p.Kind.Kind] = true
	}
	for _, k := range declaredKinds(t) {
		if !listed[k] {
			t.Errorf("kind %q is declared in notify.go and absent from the catalogue — it can never be routed anywhere", k)
		}
	}
}

func TestCatalogueGivesEveryKindAndChannelAStableIDAndLabel(t *testing.T) {
	c := notify.DefaultCatalogue()
	for _, p := range c.Pairs() {
		if k := p.Kind; k.ID == "" || k.Label == "" || k.Description == "" || len(k.Trusts) == 0 {
			t.Errorf("kind %q is incompletely catalogued: %+v", k.Kind, k)
		}
	}
	for _, ch := range c.Channels() {
		if ch.ID == "" || ch.Label == "" || ch.Description == "" {
			t.Errorf("channel %q is incompletely catalogued: %+v", ch.ID, ch)
		}
	}
}

// ── the trust bound: the forbidden pair is ABSENT, not refused ─────────

func TestCatalogueDoesNotOfferHeuristicToANetworkChannel(t *testing.T) {
	c, err := notify.NewCatalogue(
		[]notify.RoutableKind{
			catKind("guessed", notify.TrustHeuristic),
			catKind("attested", notify.TrustAttested),
		},
		[]notify.RoutableChannel{localChannel("local"), networkChannel("remote")},
	)
	if err != nil {
		t.Fatalf("NewCatalogue: %v", err)
	}

	offered := map[string]bool{}
	for _, p := range c.Pairs() {
		offered[p.Kind.ID+"/"+p.Channel.ID] = true
	}
	if offered["guessed/remote"] {
		t.Error("the catalogue offers heuristic → a network channel; the impossible choice must be absent, not refused")
	}
	for _, want := range []string{"guessed/local", "attested/local", "attested/remote"} {
		if !offered[want] {
			t.Errorf("pair %q is missing from the catalogue", want)
		}
	}
}

// A kind carrying SEVERAL trust classes loses the pair if ANY of them is
// barred. One toggle governs one (kind, channel) cell, so a pair offered for
// half a kind's trust classes would silently apply to half of what the label
// says — stricter is the only direction this bound may move.
func TestCatalogueDropsAPairWhenAnyOfTheKindsTrustClassesIsBarred(t *testing.T) {
	mixed := notify.RoutableKind{
		Kind: "test.mixed", ID: "mixed", Label: "mixed", Description: "two trust classes",
		Trusts: []notify.Trust{notify.TrustAttested, notify.TrustHeuristic},
	}
	c, err := notify.NewCatalogue([]notify.RoutableKind{mixed},
		[]notify.RoutableChannel{localChannel("local"), networkChannel("remote")})
	if err != nil {
		t.Fatalf("NewCatalogue: %v", err)
	}
	for _, p := range c.Pairs() {
		if p.Channel.ID == "remote" {
			t.Error("a kind carrying heuristic was offered a network channel because one of its other classes may reach it")
		}
	}
}

func TestCatalogueRefusesADefaultOnAPairItDoesNotOffer(t *testing.T) {
	_, err := notify.NewCatalogue(
		[]notify.RoutableKind{catKind("guessed", notify.TrustHeuristic, "remote")},
		[]notify.RoutableChannel{networkChannel("remote")},
	)
	if err == nil {
		t.Fatal("NewCatalogue accepted a default naming a pair the trust bound forbids")
	}
}

// ── construction refusals ──────────────────────────────────────────────

func TestCatalogueRefusesMalformedDeclarations(t *testing.T) {
	local := localChannel("local")
	cases := map[string]struct {
		kinds    []notify.RoutableKind
		channels []notify.RoutableChannel
	}{
		"no kinds":    {nil, []notify.RoutableChannel{local}},
		"no channels": {[]notify.RoutableKind{catKind("a", notify.TrustAttested)}, nil},
		"duplicate kind id": {
			[]notify.RoutableKind{catKind("a", notify.TrustAttested), catKind("a", notify.TrustAttested)},
			[]notify.RoutableChannel{local},
		},
		"duplicate channel id": {
			[]notify.RoutableKind{catKind("a", notify.TrustAttested)},
			[]notify.RoutableChannel{local, localChannel("local")},
		},
		"empty kind id": {
			[]notify.RoutableKind{{Kind: "test.x", Label: "x", Description: "d", Trusts: []notify.Trust{notify.TrustAttested}}},
			[]notify.RoutableChannel{local},
		},
		"empty channel id": {
			[]notify.RoutableKind{catKind("a", notify.TrustAttested)},
			[]notify.RoutableChannel{{Label: "l", Description: "d"}},
		},
		"kind with no trust class": {
			[]notify.RoutableKind{{Kind: "test.x", ID: "x", Label: "x", Description: "d"}},
			[]notify.RoutableChannel{local},
		},
		"default names an unknown channel": {
			[]notify.RoutableKind{catKind("a", notify.TrustAttested, "nowhere")},
			[]notify.RoutableChannel{local},
		},
		"duplicate Kind value under two ids": {
			[]notify.RoutableKind{
				{Kind: "test.same", ID: "a", Label: "a", Description: "d", Trusts: []notify.Trust{notify.TrustAttested}},
				{Kind: "test.same", ID: "b", Label: "b", Description: "d", Trusts: []notify.Trust{notify.TrustAttested}},
			},
			[]notify.RoutableChannel{local},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := notify.NewCatalogue(tc.kinds, tc.channels); err == nil {
				t.Fatalf("NewCatalogue accepted %s", name)
			}
		})
	}
}

// ── the catalogue is the one owner of these words ──────────────────────

func TestCatalogueSettingKeyIsDerivedFromTheIDs(t *testing.T) {
	c, err := notify.NewCatalogue(
		[]notify.RoutableKind{catKind("programNotify", notify.TrustProgramRequest)},
		[]notify.RoutableChannel{localChannel("banner")},
	)
	if err != nil {
		t.Fatalf("NewCatalogue: %v", err)
	}
	pairs := c.Pairs()
	if len(pairs) != 1 {
		t.Fatalf("catalogue offers %d pairs, want 1", len(pairs))
	}
	want := notify.RouteSettingKey("programNotify", "banner")
	if got := pairs[0].SettingKey(); got != want {
		t.Errorf("pair setting key = %q, want %q", got, want)
	}
	if want != "notifications.route.programNotify.banner" {
		t.Errorf("RouteSettingKey spelled %q; the key is persisted, so it is not free to move", want)
	}
	if pairs[0].SettingLabel() == "" || pairs[0].SettingDescription() == "" {
		t.Error("a pair must carry the label and description its settings declaration is built from")
	}
}

func TestCatalogueAccessorsHandOutCopies(t *testing.T) {
	c := notify.DefaultCatalogue()
	before := len(c.Pairs())

	pairs := c.Pairs()
	pairs[0].Kind.ID = "clobbered"
	pairs[0].Kind.Trusts[0] = notify.TrustHeuristic
	pairs[0].Kind.DefaultChannels = append(pairs[0].Kind.DefaultChannels, "clobbered")
	pairs[0].DefaultOn = !pairs[0].DefaultOn
	if got := c.Pairs()[0]; got.Kind.ID == "clobbered" ||
		got.Kind.Trusts[0] == notify.TrustHeuristic ||
		len(got.Kind.DefaultChannels) != len(pairs[0].Kind.DefaultChannels)-1 ||
		len(c.Pairs()) != before {
		t.Error("Pairs() hands out the catalogue's own data; a caller can rewrite the catalogue through it")
	}

	presented := c.PresentedKinds()
	presented[0].Trusts[0] = notify.TrustHeuristic
	presented[0].DefaultChannels = append(presented[0].DefaultChannels, "clobbered")
	if got := c.PresentedKinds()[0]; got.Trusts[0] == notify.TrustHeuristic ||
		len(got.DefaultChannels) != len(presented[0].DefaultChannels)-1 {
		t.Error("PresentedKinds() hands out the catalogue's nested data")
	}

	channels := c.Channels()
	channels[0].ID = "clobbered"
	if c.Channels()[0].ID == "clobbered" {
		t.Error("Channels() hands out the catalogue's own slice")
	}
}

func TestCataloguePresentedKindsIncludesUnroutableKind(t *testing.T) {
	c, err := notify.NewCatalogue(
		[]notify.RoutableKind{catKind("guessed", notify.TrustHeuristic)},
		[]notify.RoutableChannel{networkChannel("remote")},
	)
	if err != nil {
		t.Fatalf("NewCatalogue: %v", err)
	}
	if got := c.Pairs(); len(got) != 0 {
		t.Fatalf("Pairs() returned %d offered cells, want none", len(got))
	}
	kinds := c.PresentedKinds()
	if len(kinds) != 1 || kinds[0].ID != "guessed" {
		t.Fatalf("PresentedKinds() = %+v, want the unroutable guessed kind", kinds)
	}
}

func TestCatalogueCopiesNestedKindSlicesOnConstruction(t *testing.T) {
	kinds := []notify.RoutableKind{catKind("original", notify.TrustAttested, "local")}
	c, err := notify.NewCatalogue(kinds, []notify.RoutableChannel{localChannel("local")})
	if err != nil {
		t.Fatalf("NewCatalogue: %v", err)
	}
	kinds[0].Trusts[0] = notify.TrustHeuristic
	kinds[0].DefaultChannels[0] = "changed"
	got := c.PresentedKinds()[0]
	if got.Trusts[0] != notify.TrustAttested || got.DefaultChannels[0] != "local" {
		t.Errorf("catalogue retained caller-owned nested slices: %+v", got)
	}
}

// ── the shipped default ────────────────────────────────────────────────

// Default-deny, asserted as an EXACT set. Four of these five are the rows the
// composition root's hand-written table carried before the matrix landed, so
// nobody's notifications changed the day it did. The fifth is
// transferFinished -> toast (nocx-zlxmm): a background transfer's outcome is
// the one ending a person deliberately walks away from, so it ships reaching
// something, and it ships reaching the toast alone — a completed download is
// not worth taking the focus off whatever they walked away to.
//
// The set is exact in both directions on purpose: a default is a decision
// somebody made about a stranger's attention, and adding one silently is how
// a notification surface becomes something people switch off wholesale.
func TestCatalogueDefaultsAreExactlyTodaysTable(t *testing.T) {
	want := map[string]bool{
		"programNotify/" + notify.ChannelBanner:   true,
		"programNotify/" + notify.ChannelToast:    true,
		"sessionEnded/" + notify.ChannelBanner:    true,
		"sessionEnded/" + notify.ChannelToast:     true,
		"transferFinished/" + notify.ChannelToast: true,
		// The wave backstop: the coordinator was not reached about a
		// worker's result, so the person is the only one left who can act.
		// Both channels, for session.ended's reason — it fires a handful of
		// times a wave and the only moment it matters is the one where
		// nobody is looking at the tab.
		"waveUndispatched/" + notify.ChannelBanner: true,
		"waveUndispatched/" + notify.ChannelToast:  true,
	}
	got := map[string]bool{}
	for _, p := range notify.DefaultCatalogue().Pairs() {
		if p.DefaultOn {
			got[p.Kind.ID+"/"+p.Channel.ID] = true
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("pair %q is meant to ship on and is off by default", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("pair %q is on by default and nobody decided it should be", k)
		}
	}
}

// And the half of that decision a set of strings cannot state: the transfer
// kind reaches the toast and NOT the banner. Asserted separately because the
// exact-set test above would go on passing if somebody swapped one id for the
// other, and the swap is precisely the thing this row was argued about.
func TestCatalogueDoesNotDefaultATransferOutcomeToTheBanner(t *testing.T) {
	for _, p := range notify.DefaultCatalogue().Pairs() {
		if p.Kind.Kind != notify.KindTransferFinished {
			continue
		}
		if p.Channel.ID == notify.ChannelBanner && p.DefaultOn {
			t.Error("a finished transfer defaults to the OS banner; it must default to the toast alone")
		}
		if p.Channel.ID == notify.ChannelToast && !p.DefaultOn {
			t.Error("a finished transfer does not default to the toast, which is the whole reason it is catalogued")
		}
	}
}
