package assistant

// session.keys (design §4.2, §6.4, §6.5, Task 9): one step under a target a
// prior session.read minted — a single key, one text atom pasted, or a
// named menu option — never a sequence. The wire carries the target's
// tokenId (session.read's own short, unsigned handle for it), not the
// signed token itself: the coordinator's own record of the mint (Task 8's
// targetRecord, internal/app/session_targets.go) already holds the signed
// token that session.intent needs, keyed by that same tokenId, so the model
// never has to round-trip the opaque signed blob.
//
// Mirroring Task 8's layering (internal/assistant/blocks.go's own note):
// this package owns the interface PaneKeys and the wire types the executor
// needs, exactly as it owns PaneReader; internal/app owns the one
// production implementation, which this package cannot name (app depends on
// assistant, not the reverse).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// KeyName is one physical key from design §4.2's closed vocabulary. ParseKey
// is the only way to produce one — nothing else may construct or widen the
// set, which is what keeps "one key, one text atom, or an option" a closed
// question rather than "whatever string arrived".
type KeyName string

// bareKeyNames is the vocabulary's unmodified half: every name design §4.2
// lists on its own, with no chord. F1 through F12 are added in init rather
// than spelled twelve times.
var bareKeyNames = map[string]bool{
	"Enter": true, "Esc": true, "Tab": true, "BackTab": true,
	"Backspace": true, "Delete": true,
	"Up": true, "Down": true, "Left": true, "Right": true,
	"Home": true, "End": true, "PageUp": true, "PageDown": true,
	"Insert": true, "Space": true,
}

func init() {
	for n := 1; n <= 12; n++ {
		bareKeyNames[fmt.Sprintf("F%d", n)] = true
	}
}

// isSingleLetter/isSingleDigit are the two chord bases design §4.2 names —
// "Ctrl+<letter>" is the letter alone; "Alt+<key>" / "Shift+<key>" also
// admit a digit or one of the bare names above (an arrow, Tab, Enter, …),
// which is what lets Shift+Tab and Alt+Up parse without a second table.
func isSingleLetter(s string) bool {
	return len(s) == 1 && ((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z'))
}

func isSingleDigit(s string) bool {
	return len(s) == 1 && s[0] >= '0' && s[0] <= '9'
}

// ParseKey validates s against design §4.2's closed vocabulary and is the
// vocabulary's only door: a plain name from bareKeyNames, or one chord
// Ctrl+<letter> / Alt+<key> / Shift+<key> — never a sequence ("Down,Enter"
// is two calls, or an option) and never a modifier this vocabulary does not
// name.
func ParseKey(s string) (KeyName, error) {
	if s == "" {
		return "", errors.New("session.keys: key must not be empty")
	}
	mod, base, hasMod := strings.Cut(s, "+")
	if !hasMod {
		if !bareKeyNames[s] {
			return "", fmt.Errorf("session.keys: %q is not a key this vocabulary names", s)
		}
		return KeyName(s), nil
	}
	if strings.Contains(base, "+") {
		return "", fmt.Errorf("session.keys: %q names more than one modifier, which this vocabulary does not allow", s)
	}
	switch mod {
	case "Ctrl":
		if !isSingleLetter(base) {
			return "", fmt.Errorf("session.keys: Ctrl only combines with a single letter (design §4.2), got %q", base)
		}
	case "Alt", "Shift":
		if !isSingleLetter(base) && !isSingleDigit(base) && !bareKeyNames[base] {
			return "", fmt.Errorf("session.keys: %q is not a key %s can combine with", base, mod)
		}
	default:
		return "", fmt.Errorf("session.keys: %q is not a modifier this vocabulary knows (Ctrl, Alt, Shift)", mod)
	}
	return KeyName(s), nil
}

// KeysRequest is one session.keys call: exactly one of Key, Text or Option
// set (design §4.2). TokenID names the target a prior session.read minted —
// the coordinator's own record of that mint carries the signed token,
// authority and menu identity a step is judged against (design §6.2).
type KeysRequest struct {
	SessionID string
	TokenID   string
	Key       *KeyName
	Text      *string
	Option    *string
}

// KeysResult is what became of one session.keys call (design §6.5): the
// helper's own closed state set for a plain key or text atom — executed,
// refused, failed_partial, delivery_unknown, cancelled, in_progress — plus
// "indeterminate" for a step whose outcome could not be learned even after
// its commitBy passed (design §7.2's transport-timeout rule: never
// "cancelled" for that case, which would claim more than is known). Steps
// counts every mint-and-send this call actually performed: 1 for a plain
// key or text atom, more for the option loop (design §6.4).
type KeysResult struct {
	State        string
	BytesWritten int
	Refusal      *proto.IntentRefusal
	Steps        int
}

// PaneKeys is session.keys' write path. access is RunContext.PaneAccess,
// carried as `any` for the identical layering reason PaneReader.Read
// receives it that way (this package sits below internal/app, which binds
// the concrete DescendantPaneAccess and holds the one production
// implementation, wrapping PaneReader's own record of each mint and the
// authority hub, Task 7).
type PaneKeys interface {
	Send(ctx context.Context, access any, req KeysRequest) (KeysResult, error)
}

// sessionKeysParams is session.keys' wire request. Key/Text/Option are
// pointers so "absent" and "empty string" stay distinct facts: an absent
// field is not this call's chosen one, an empty string is a text atom that
// happens to be empty (and refused elsewhere, on its own terms) or an
// option named "" (never on a real screen, refused as not found).
type sessionKeysParams struct {
	SessionID string  `json:"sessionId"`
	TokenID   string  `json:"tokenId"`
	Key       *string `json:"key,omitempty"`
	Text      *string `json:"text,omitempty"`
	Option    *string `json:"option,omitempty"`
}

type sessionKeysRefusalWire struct {
	Cause           string `json:"cause"`
	RegionNow       string `json:"regionNow,omitempty"`
	RegionTruncated bool   `json:"regionTruncated,omitempty"`
	RegionOmitted   bool   `json:"regionOmitted,omitempty"`
}

type sessionKeysResultWire struct {
	SessionID    string                  `json:"sessionId"`
	State        string                  `json:"state"`
	BytesWritten int                     `json:"bytesWritten,omitempty"`
	Steps        int                     `json:"steps,omitempty"`
	Refusal      *sessionKeysRefusalWire `json:"refusal,omitempty"`
}

// executeSessionKeys is session.keys' whole InGo execution: parse the wire
// request, validate exactly one of key/text/option is set (a sequence such
// as `key: ["Down","Enter"]` never reaches here at all — the schema's `key`
// is a bare string, so an array fails schema validation before dispatch,
// design §4.2's "TestASequenceIsNotAccepted"), and hand it to the run's
// PaneKeys.
func executeSessionKeys(ctx context.Context, cap *agenttools.SessionDescendantCapability, args json.RawMessage) (string, error) {
	if cap == nil {
		return "", errors.New("session.keys: capability carries no session access")
	}
	var p sessionKeysParams
	if unmarshalErr := json.Unmarshal(args, &p); unmarshalErr != nil {
		return "", fmt.Errorf("session.keys: args: %w", unmarshalErr)
	}
	if p.SessionID == "" {
		return "", errors.New("session.keys: name the session whose pane to write to")
	}
	if p.TokenID == "" {
		return "", errors.New("session.keys: name the target's tokenId, from a prior session.read")
	}
	set := 0
	if p.Key != nil {
		set++
	}
	if p.Text != nil {
		set++
	}
	if p.Option != nil {
		set++
	}
	if set != 1 {
		return "", errors.New("session.keys: exactly one of key, text or option is required")
	}
	if cap.PaneAccess == nil || cap.SessionKeys == nil {
		return "", fmt.Errorf("session.keys: %q is not this run's own session and no descendant write authority is wired for this run", p.SessionID)
	}
	keys, ok := cap.SessionKeys.(PaneKeys)
	if !ok {
		return "", fmt.Errorf("session.keys: session keys capability is %T, not a PaneKeys", cap.SessionKeys)
	}
	req := KeysRequest{SessionID: p.SessionID, TokenID: p.TokenID}
	if p.Key != nil {
		k, parseErr := ParseKey(*p.Key)
		if parseErr != nil {
			return "", fmt.Errorf("session.keys: %w", parseErr)
		}
		req.Key = &k
	}
	if p.Text != nil {
		req.Text = p.Text
	}
	if p.Option != nil {
		req.Option = p.Option
	}
	result, sendErr := keys.Send(ctx, cap.PaneAccess, req)
	if sendErr != nil {
		return "", fmt.Errorf("session.keys: %w", sendErr)
	}
	out := sessionKeysResultWire{
		SessionID:    p.SessionID,
		State:        result.State,
		BytesWritten: result.BytesWritten,
		Steps:        result.Steps,
	}
	if result.Refusal != nil {
		out.Refusal = &sessionKeysRefusalWire{
			Cause:           result.Refusal.Cause,
			RegionNow:       result.Refusal.RegionNow,
			RegionTruncated: result.Refusal.RegionTruncated,
			RegionOmitted:   result.Refusal.RegionOmitted,
		}
	}
	b, marshalErr := json.Marshal(out)
	if marshalErr != nil {
		return "", fmt.Errorf("session.keys: marshal result: %w", marshalErr)
	}
	return string(b), nil
}
