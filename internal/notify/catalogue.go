package notify

import (
	"errors"
	"fmt"
)

// The catalogue is the one place that names what can be routed where: every
// routable Kind with a stable id and a human label, every channel (sink
// surface) with the same, and which trust classes each kind can carry. The
// settings registration and the table builder both read it and neither
// restates a kind or a channel as a literal — one owner per behaviour (AD-8).
//
// It carries no sinks. A Sink is an implementation the composition root binds
// at startup; the catalogue is read BEFORE that, at package init, to declare
// the settings the user ticks. Keeping the two apart is what lets the settings
// declarations exist on a host that never builds a router at all.
//
// The trust bound is expressed here by ABSENCE (D3, ADR-0047 §3). A pair a
// trust class may never reach — heuristic to anything that leaves the machine
// — is not offered by the catalogue, so no toggle for it exists, so no table
// built from the toggles can contain it. The router still re-checks every
// table it is handed; this is the outer of two fences, not a replacement for
// the inner one.

// Channel ids. These are ALSO the resolved Destination.Target of each route
// (Destination), which is what makes a failed delivery able to say which
// channel failed in the same word the router used to reach it. They sit
// beside ChannelPipeline in the same vocabulary for the same reason.
const (
	// ChannelBanner is the OS notification banner (HostSink → AttentionHost).
	ChannelBanner = "banner"
	// ChannelToast is the in-window transient message (ToastSink →
	// ToastPresenter).
	ChannelToast = "toast"
)

// routeSettingPrefix is the dotted namespace every routing toggle's key sits
// under. The key is persisted in the settings document, so it is a stable
// identifier rather than a display string: it is built from the catalogue's
// ids, never from a label.
const routeSettingPrefix = "notifications.route."

// RouteSettingSection is the settings section the routing matrix is declared
// in. Named here, beside the keys, so the composition root's registration and
// anything that has to find the section again agree by construction.
const RouteSettingSection = "Notifications"

// RouteSettingKey is the settings key of one (kind, channel) cell. It is the
// single derivation of that key: the composition root registers the toggle
// under it and reads the toggle back through it, so there is no second
// spelling to drift.
//
// The renderer PARSES this shape. The settings matrix derives its rows and
// columns from the declared keys rather than from a list of its own, which is
// what stops the surface drifting from the backend — so this function is a
// contract between two packages in two languages, not a private helper.
// frontend/src/settings-domain.ts's parseRouteSettingKey cites it back. Changing the shape here
// breaks the grid there, and a key that does not parse must stay visible as
// an ordinary setting rather than vanishing from both.
func RouteSettingKey(kindID, channelID string) string {
	return routeSettingPrefix + kindID + "." + channelID
}

const centreSettingPrefix = "notifications.centre."

// CentreSettingKey is the persisted key of a kind's notification-centre
// visibility toggle.
func CentreSettingKey(kindID string) string {
	return centreSettingPrefix + kindID
}

// ErrCatalogue is returned by NewCatalogue when a declaration is malformed or
// a default names a pair the catalogue does not offer.
var ErrCatalogue = errors.New("notify: catalogue")

// RoutableKind is one routable event kind: the Kind the pipeline stamps, a
// stable id for keys, a human label and description for the settings surface,
// and the trust classes this kind can carry.
type RoutableKind struct {
	// Kind is the value the source adapter stamps on the Event.
	Kind Kind

	// ID is the stable identifier used in a setting key. It is not the Kind
	// value: Kind is dotted ("program.notify") and a key built from it would
	// be ambiguous about where the kind ends and the channel begins.
	ID string

	// Label and Description are what the settings surface renders.
	Label       string
	Description string

	// Trusts are the trust classes this kind can carry — stamped by its
	// source adapter, never chosen by a user. A table row is keyed by
	// (Kind, Trust), so one toggle turned on writes one row per class here.
	Trusts []Trust

	// DefaultChannels are the channel ids this kind reaches with no user
	// choice at all. Every other pair is OFF: default-deny is what the
	// absence of a channel id here means.
	DefaultChannels []string
}

// RoutableChannel is one delivery channel: a sink surface a kind can be
// routed to.
type RoutableChannel struct {
	// ID is the stable identifier used in a setting key AND as the route's
	// resolved Destination.Target.
	ID string

	// Label and Description are what the settings surface renders.
	Label       string
	Description string

	// LeavesMachine declares whether delivering through this channel leaves
	// the machine. It is the catalogue's half of a fact the Sink also
	// declares; NewRoutingSource refuses a binding where the two disagree,
	// so the two answers cannot drift apart in silence (AD-8).
	LeavesMachine bool
}

// Pair is one offered (kind, channel) cell: one toggle, one row per trust
// class the kind carries.
type Pair struct {
	Kind    RoutableKind
	Channel RoutableChannel

	// DefaultOn is whether this cell ships on.
	DefaultOn bool
}

// SettingKey is the persisted key of this cell's toggle.
func (p Pair) SettingKey() string { return RouteSettingKey(p.Kind.ID, p.Channel.ID) }

// SettingLabel is the toggle's label: the cell, named as a cell, so a flat
// list still reads unambiguously before task 4's matrix arrives.
func (p Pair) SettingLabel() string { return p.Kind.Label + " → " + p.Channel.Label }

// SettingDescription is the toggle's description: what the kind is, and what
// turning it on makes it reach.
func (p Pair) SettingDescription() string {
	return p.Kind.Description + " When on, it reaches " + p.Channel.Description +
		". With every delivery channel off, this kind reaches no channel — it is still recorded in the notification centre."
}

// Catalogue is the immutable set of routable kinds and channels, and the
// pairs they offer.
type Catalogue struct {
	kinds    []RoutableKind
	channels []RoutableChannel
	pairs    []Pair
}

// NewCatalogue validates the declarations and computes the offered pairs. A
// pair is offered unless the trust bound forbids it: a channel that leaves the
// machine is offered only to a kind NONE of whose trust classes is heuristic.
//
// "None of", not "some of", on purpose. One toggle governs one (kind, channel)
// cell, so a pair offered because one of the kind's classes may reach the
// channel would apply to only part of what its label promises — and a routing
// control that grants less than it says is the same defect as one that grants
// more. Stricter is the only direction this bound may move (D3).
func NewCatalogue(kinds []RoutableKind, channels []RoutableChannel) (*Catalogue, error) {
	if len(kinds) == 0 {
		return nil, fmt.Errorf("%w: no kinds declared", ErrCatalogue)
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("%w: no channels declared", ErrCatalogue)
	}

	seenKindID := make(map[string]bool, len(kinds))
	seenKind := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		switch {
		case k.ID == "":
			return nil, fmt.Errorf("%w: kind %q has no id", ErrCatalogue, k.Kind)
		case k.Kind == "":
			return nil, fmt.Errorf("%w: kind %q has no Kind value", ErrCatalogue, k.ID)
		case k.Label == "" || k.Description == "":
			return nil, fmt.Errorf("%w: kind %q has no label or description", ErrCatalogue, k.ID)
		case len(k.Trusts) == 0:
			return nil, fmt.Errorf("%w: kind %q declares no trust class, so no row could ever be keyed for it", ErrCatalogue, k.ID)
		case seenKindID[k.ID]:
			return nil, fmt.Errorf("%w: duplicate kind id %q", ErrCatalogue, k.ID)
		case seenKind[k.Kind]:
			return nil, fmt.Errorf("%w: kind %q is declared twice under two ids", ErrCatalogue, k.Kind)
		}
		for _, tr := range k.Trusts {
			if tr != TrustAttested && tr != TrustProgramRequest && tr != TrustHeuristic {
				return nil, fmt.Errorf("%w: kind %q names unknown trust class %q", ErrCatalogue, k.ID, tr)
			}
		}
		seenKindID[k.ID] = true
		seenKind[k.Kind] = true
	}
	// Keep an owned copy of the declarations. PresentedKinds is the vocabulary
	// of every raised event, including kinds whose trust bound offers no pair.
	kinds = cloneRoutableKinds(kinds)

	byChannel := make(map[string]RoutableChannel, len(channels))
	for _, ch := range channels {
		switch {
		case ch.ID == "":
			return nil, fmt.Errorf("%w: a channel has no id", ErrCatalogue)
		case ch.Label == "" || ch.Description == "":
			return nil, fmt.Errorf("%w: channel %q has no label or description", ErrCatalogue, ch.ID)
		}
		if _, dup := byChannel[ch.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate channel id %q", ErrCatalogue, ch.ID)
		}
		byChannel[ch.ID] = ch
	}

	var pairs []Pair
	for _, k := range kinds {
		defaults := make(map[string]bool, len(k.DefaultChannels))
		for _, id := range k.DefaultChannels {
			ch, known := byChannel[id]
			if !known {
				return nil, fmt.Errorf("%w: kind %q defaults to unknown channel %q", ErrCatalogue, k.ID, id)
			}
			if !offers(k, ch) {
				return nil, fmt.Errorf("%w: kind %q defaults to channel %q, which its trust class may never reach", ErrCatalogue, k.ID, id)
			}
			defaults[id] = true
		}
		for _, ch := range channels {
			if !offers(k, ch) {
				continue
			}
			pairs = append(pairs, Pair{Kind: cloneRoutableKind(k), Channel: ch, DefaultOn: defaults[ch.ID]})
		}
	}

	return &Catalogue{
		kinds:    kinds,
		channels: append([]RoutableChannel(nil), channels...),
		pairs:    pairs,
	}, nil
}

// offers reports whether the trust bound permits this (kind, channel) cell to
// be offered at all.
func offers(k RoutableKind, ch RoutableChannel) bool {
	if !ch.LeavesMachine {
		return true
	}
	for _, tr := range k.Trusts {
		if tr == TrustHeuristic {
			return false
		}
	}
	return true
}

// Channels returns the declared channels, in declaration order. The slice is a
// copy: the catalogue is immutable and a caller may not rewrite it through an
// accessor.
func (c *Catalogue) Channels() []RoutableChannel {
	return append([]RoutableChannel(nil), c.channels...)
}

// PresentedKinds returns every declared kind, in declaration order. This is
// the vocabulary of events that may be raised, including a kind whose trust
// bound leaves it with no offered pair. Pairs answers what can be routed;
// PresentedKinds answers what may be named in the notification centre.
func (c *Catalogue) PresentedKinds() []RoutableKind {
	return cloneRoutableKinds(c.kinds)
}

// Pairs returns every OFFERED (kind, channel) cell, in kind-then-channel
// order, as a deep copy. A cell the trust bound forbids is absent — the
// impossible choice is not offered rather than offered and declined.
func (c *Catalogue) Pairs() []Pair {
	out := make([]Pair, len(c.pairs))
	for i, pair := range c.pairs {
		out[i] = clonePair(pair)
	}
	return out
}

func cloneRoutableKind(k RoutableKind) RoutableKind {
	k.Trusts = append([]Trust(nil), k.Trusts...)
	k.DefaultChannels = append([]string(nil), k.DefaultChannels...)
	return k
}

func cloneRoutableKinds(kinds []RoutableKind) []RoutableKind {
	out := make([]RoutableKind, len(kinds))
	for i, kind := range kinds {
		out[i] = cloneRoutableKind(kind)
	}
	return out
}

func clonePair(pair Pair) Pair {
	pair.Kind = cloneRoutableKind(pair.Kind)
	return pair
}

// defaultCatalogue is the shipped catalogue, built once at package init. It
// panics on a malformed declaration because a malformed catalogue is a
// programming error in this file, not a runtime condition: there is no value a
// caller could pass that would change the outcome.
var defaultCatalogue = mustCatalogue()

// DefaultCatalogue is the catalogue nocx ships.
//
// The DEFAULTS are the load-bearing part (D2). They are exactly the rows the
// composition root's hand-written table carried before this task existed —
// program.notify and session.ended, each to the banner and the toast — so
// nobody's notifications change on the day the matrix lands. Every other cell
// is off, which is default-deny stated as data: a kind with no channel here
// reaches nothing, and a kind nobody declares has no toggle to turn on.
func DefaultCatalogue() *Catalogue { return defaultCatalogue }

func mustCatalogue() *Catalogue {
	c, err := NewCatalogue(
		[]RoutableKind{
			{
				// No DefaultChannels, and that is a CHOICE rather than an
				// omission (nocx-n3nfg). Every other kind here fires a
				// handful of times a session; this one fires once per
				// command, which in a terminal is hundreds of times an hour
				// — shipping it to the banner would make `ls` interrupt the
				// user and would train them to turn the whole matrix off.
				// The surface the design gives this kind is the ONE-SHOT
				// completion subscription (§A3, "уведомить, когда
				// закончится"), which is why it is attested: only an
				// attested event may match one, and a subscription is
				// reached through the same table without needing a default
				// row here.
				//
				// It is still raised for every closed block, and that is the
				// point: ingress records the occurrence before the router
				// decides anything (ingress.go), so with no channel at all
				// the notification centre can finally answer "what did I
				// miss" — which is the defect this bead was filed for. The
				// toggles beside this row now govern a real event; they
				// simply start off.
				Kind: KindBlockFinished, ID: "blockFinished",
				Label:       "Command finished",
				Description: "nocx's own block ledger recorded that a command finished.",
				Trusts:      []Trust{TrustAttested},
			},
			{
				// Attested, because it is nocx's own registry that observed
				// it, so it may reach every sink (ADR-0047 §3). It ships on
				// for the same reason a program's own request does: the user
				// is not looking at the tab, which is the only moment either
				// event matters.
				Kind: KindSessionEnded, ID: "sessionEnded",
				Label:           "Session ended",
				Description:     "nocx's own session registry recorded that a session ended.",
				Trusts:          []Trust{TrustAttested},
				DefaultChannels: []string{ChannelBanner, ChannelToast},
			},
			{
				// ONE kind for both directions, because "transfer" is
				// already the word the backend uses for the concept —
				// runningTransfer, transferRegistry, deliverTransferDone,
				// files.uploadProgress and files.downloadProgress share one
				// params shape — and because a toggle governs a KIND: two
				// kinds would ask a person to answer twice a question they
				// have one answer to. "finished" rather than "done" so it
				// reads beside block.finished and session.ended, which are
				// the other two attested endings.
				//
				// TOAST AND NOT BANNER, and that is the whole of this row
				// (nocx-zlxmm). A transfer finishing is rare, the person
				// walked away from it and it is exactly what they come back
				// for — which is what earns it a default at all, where
				// block.finished has none. It is still not worth taking the
				// focus off whatever they walked away TO: a completed
				// download is news inside nocx and an interruption anywhere
				// else. Someone who wants the banner has a toggle for it.
				//
				// Attested because the fact is nocx's own: settleUpload and
				// settleDownload are the points where the outcome becomes
				// known to the backend, and nothing here reads a renderer's
				// claim about it.
				Kind: KindTransferFinished, ID: "transferFinished",
				Label: "File transfer finished",
				Description: "nocx's own transfer registry recorded that an upload or " +
					"a download reached its end.",
				Trusts:          []Trust{TrustAttested},
				DefaultChannels: []string{ChannelToast},
			},
			{
				Kind: KindProgramNotify, ID: "programNotify",
				Label:           "Program notification request",
				Description:     "A program printed OSC 9 or OSC 777 to ask for one.",
				Trusts:          []Trust{TrustProgramRequest},
				DefaultChannels: []string{ChannelBanner, ChannelToast},
			},
			{
				Kind: KindBell, ID: "bell",
				Label:       "Terminal bell",
				Description: "A program printed BEL.",
				Trusts:      []Trust{TrustProgramRequest},
			},
			{
				Kind: KindPaneWorkFinished, ID: "paneWorkFinished",
				Label: "Work seems to have finished",
				Description: "nocx inferred from a pane's title that its work finished. " +
					"It is an inference, so it may never leave this machine.",
				Trusts: []Trust{TrustHeuristic},
			},
		},
		[]RoutableChannel{
			{
				ID: ChannelBanner, Label: "OS banner",
				Description: "the desktop notification banner",
			},
			{
				ID: ChannelToast, Label: "In-app toast",
				Description: "a transient message in the nocx window",
			},
		},
	)
	if err != nil {
		panic("notify: the shipped catalogue is malformed: " + err.Error())
	}
	return c
}
