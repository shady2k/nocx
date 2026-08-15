package profile

import "testing"

// ---------------------------------------------------------------------------
// `auto` is the name for "not yet answered" — ADR-0033, nocx-7iisi.
//
// The delivery axis carries a fourth value whose whole job is to be
// distinguishable from an answer. These tests pin the two halves of that:
// silence resolves to auto, and an explicit script is a different value the
// resolver can honour — which is what D8's "script is an answer, not a gap"
// needs in order to bite on anything.
// ---------------------------------------------------------------------------

// TestHardcodedDefault_IsAuto pins the root of the cascade. Before ADR-0033
// silence resolved to script, so the product could not tell a user who chose
// script from one who chose nothing — and honouring D8 then meant never
// offering the helper on any connection nobody had hand-edited.
func TestHardcodedDefault_IsAuto(t *testing.T) {
	eff, err := ResolveEffectiveProfile(
		SSHProfile{
			Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
			Options: StoredSSHProfileOptions{Host: "h"},
		},
		nil,
		SparseSSHOptions{},
	)
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if got := eff.ResolvedOptions.DesiredMode; got != DesiredAuto {
		t.Errorf("desiredMode with nothing set = %q, want %q", got, DesiredAuto)
	}
	if got := eff.Source["desiredMode"]; got != FieldSourceDefault {
		t.Errorf("desiredMode source = %q, want %q", got, FieldSourceDefault)
	}
}

// TestExplicitScript_IsDistinguishableFromSilence is the point of the whole
// ADR: two connections, one that answered "script" and one that answered
// nothing, must not resolve to the same value. If they do, no downstream rule
// can honour "an explicit script is never silently upgraded".
func TestExplicitScript_IsDistinguishableFromSilence(t *testing.T) {
	silent, err := ResolveEffectiveProfile(
		SSHProfile{
			Base:    Base{ID: "p1", Type: "ssh", Name: "silent"},
			Options: StoredSSHProfileOptions{Host: "h"},
		},
		nil,
		SparseSSHOptions{},
	)
	if err != nil {
		t.Fatalf("resolve silent: %v", err)
	}
	answered, err := ResolveEffectiveProfile(
		SSHProfile{
			Base:    Base{ID: "p2", Type: "ssh", Name: "answered"},
			Options: StoredSSHProfileOptions{Host: "h", DesiredMode: new(DesiredScript)},
		},
		nil,
		SparseSSHOptions{},
	)
	if err != nil {
		t.Fatalf("resolve answered: %v", err)
	}
	if silent.ResolvedOptions.DesiredMode == answered.ResolvedOptions.DesiredMode {
		t.Fatalf("silence and an explicit script both resolved to %q — "+
			"D8's no-silent-upgrade rule has nothing to distinguish",
			silent.ResolvedOptions.DesiredMode)
	}
	if got := answered.ResolvedOptions.DesiredMode; got != DesiredScript {
		t.Errorf("explicit script resolved to %q, want %q", got, DesiredScript)
	}
	if got := answered.Source["desiredMode"]; got != FieldSourceProfile {
		t.Errorf("explicit script source = %q, want %q", got, FieldSourceProfile)
	}
}

// TestAuto_IsExpressibleAtEveryLayer: auto is a stored value, not merely the
// absence of one. A profile must be able to say auto over a group that says
// raw — otherwise "I have not answered for this host" is inexpressible
// wherever an ancestor has answered, and the user is forced into a mode they
// did not pick.
func TestAuto_IsExpressibleAtEveryLayer(t *testing.T) {
	eff, err := ResolveEffectiveProfile(
		SSHProfile{
			Base:    Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
			Options: StoredSSHProfileOptions{Host: "h", DesiredMode: new(DesiredAuto)},
		},
		[]ProfileGroup{
			{ID: "g1", Name: "Root", Defaults: &ProfileDefaults{
				SparseSSHOptions: SparseSSHOptions{DesiredMode: new(DesiredRaw)},
			}},
		},
		SparseSSHOptions{},
	)
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if got := eff.ResolvedOptions.DesiredMode; got != DesiredAuto {
		t.Errorf("explicit auto over a raw group = %q, want %q", got, DesiredAuto)
	}
	if got := eff.Source["desiredMode"]; got != FieldSourceProfile {
		t.Errorf("explicit auto source = %q, want %q", got, FieldSourceProfile)
	}
}

// TestValidDesiredMode_AcceptsAuto: a stored auto must survive validation.
// An unrecognised value falls back to the default at resolution, so an auto
// the validator rejected would silently become the default instead — which is
// the same value here, and would therefore hide the defect rather than show
// it. Pinned directly.
func TestValidDesiredMode_AcceptsAuto(t *testing.T) {
	for _, m := range []DesiredMode{DesiredAuto, DesiredRaw, DesiredScript, DesiredRelay} {
		if !validDesiredMode(m) {
			t.Errorf("validDesiredMode(%q) = false, want true", m)
		}
	}
	if validDesiredMode(DesiredMode("ask")) {
		t.Error("validDesiredMode(\"ask\") = true — ask is not a mode (ADR-0033); " +
			"asking is what auto does when it has no stored answer")
	}
}

// TestDeliversScripts is the open-time gate, stated once because it is read
// twice: internal/ssh gates the session open on it and internal/transport
// decides from it whether a session even REQUESTED integration. Those two
// were separate literals until ADR-0033, and they disagreed the moment the
// default moved — every unconfigured session integrated and then reported
// nothing, because the gate said yes and the reporter said no. The rule now
// lives on the axis; this table is what a future edit has to argue with.
func TestDeliversScripts(t *testing.T) {
	cases := []struct {
		mode DesiredMode
		want bool
		why  string
	}{
		{DesiredAuto, true, "the default: the scripts are automatic and unasked (N3)"},
		{DesiredScript, true, "the same delivery, chosen explicitly"},
		{DesiredMode(""), true, "no profile spoke — a direct host or an ad-hoc open"},
		{DesiredRaw, false, "the user's opt-out: nothing is written"},
		{DesiredRelay, true, "additive, not alternative: allowing the helper never withholds the scripts (§5.2)"},
		{DesiredMode("ask"), false, "not a mode; and an unknown value fails closed"},
	}
	for _, tc := range cases {
		if got := tc.mode.DeliversScripts(); got != tc.want {
			t.Errorf("DesiredMode(%q).DeliversScripts() = %v, want %v — %s",
				tc.mode, got, tc.want, tc.why)
		}
	}
}

// TestDefaultDesiredMode_IsTheCascadeDefault: the exported default and the
// cascade's base layer are one value, not two literals that agree today.
// The open ack reads the exported one; the resolver reads the cascade.
func TestDefaultDesiredMode_IsTheCascadeDefault(t *testing.T) {
	eff, err := ResolveEffectiveProfile(
		SSHProfile{
			Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
			Options: StoredSSHProfileOptions{Host: "h"},
		},
		nil,
		SparseSSHOptions{},
	)
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if got, want := DefaultDesiredMode(), eff.ResolvedOptions.DesiredMode; got != want {
		t.Errorf("DefaultDesiredMode() = %q but the cascade resolves an unset mode to %q — "+
			"two defaults for one absent value is the defect ADR-0033 closed", got, want)
	}
}
