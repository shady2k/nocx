package transport

// The installed fact's WRITER (nocx-ak2d). The store was constructed, wired
// and READ by shell.footprint.status, and nothing anywhere filled it: the
// original writer was shell.environmentObserved, severed with the P7
// stream-and-passport surface (nocx-292k, ADR-0024 §1 — a passport is tty
// bytes and cannot activate a domain). The read half stayed and the write
// half went, so the footprint surface reported an empty list on every machine
// and installed-facts.json existed nowhere. deadcode cannot see it: a
// reachable read path keeps the package reachable while its write path is
// dead, the same shape as nocx-rtg0's ContentDB.Add.
//
// What replaces the passport is the authenticated hello. The far shell knows
// which bundle it was brought up from — the publish prelude exports
// NOCX_GENERATION — and now says so in the hello frame, which is a
// control-plane frame carrying the capability in its envelope. That is the
// distinction ADR-0024 §1 draws: the passport was inferred from the byte
// stream, this is stated on the authenticated channel.
//
// Recorded at ESTABLISHMENT rather than at launch, because launching proves
// nothing: the command may be refused by the far sshd, the publish may skip
// on a read-only home, the shell may never source the bundle. A domain that
// reaches Established through a launcher we built is the event that means
// "the integration is on that host and works".

import (
	"context"
	"time"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/ssh"
)

// installedFactProtocol is the protocol version this build speaks, as the
// wire spells it. The manifest the prelude writes carries the same number;
// the fact records it so a host installed by an older nocx can be told apart
// from one this build could talk to.
const installedFactProtocol = "1"

// recordInstalledFact persists what the far shell reported about its own
// installation, once per domain.
//
// Silent no-ops, each deliberate:
//   - no store wired (a test server, or a build without one);
//   - a fact that names no ssh destination — a local domain installs nothing;
//   - a shell that named no generation, which is every shell not brought up
//     from a committed bundle. Recording "installed" for one of those is the
//     lie the whole surface exists to avoid;
//   - a domain already recorded, so a chatty lane does not rewrite the
//     document on every prompt.
//
// A failed write is logged and dropped: the store's own contract is that a
// lost fact degrades to "not observed", which costs the footprint surface
// one row and never a claim that cannot be proven. Nothing else reads the
// fact — the remote command is unconditional (2026-08-20 carrier design
// §4.1) — so a lost row changes no delivery decision.
func (s *WSServer) recordInstalledFact(f lifecyclepub.Fact) {
	if s.installedFacts == nil || s.sshConfigResolver == nil || s.lifecyclePub == nil {
		return
	}
	if f.Destination == nil || f.Domain == "" {
		return
	}
	dom, ok := s.lifecyclePub.Domain(lifecycle.DomainID(f.Domain))
	if !ok || dom.BundleGeneration == "" {
		return
	}
	if s.installedFactRecorded(f.Domain) {
		return
	}

	// Off the publish path from here, and this is the one `go` in this file.
	//
	// Everything above is a map lookup; everything below runs `ssh -G` as a
	// subprocess on a cache miss, and PublishLifecycle is called
	// synchronously by the publisher from whatever goroutine caused the
	// transition. Resolving inline would put a fork+exec between a shell's
	// prompt and the renderer being told about it, for a write that concerns
	// a remote disk and nothing the user is waiting on.
	//
	// It does not escape what ADR-0026 protects. This is not control-handler
	// work: no request is admitted, no lane is held, no domain ownership is
	// taken, and nothing answers a caller — the only effect is one document
	// written and a log line. The spawn is bounded to once per domain by the
	// latch above, and bounded in time by the context below, so neither a
	// hung oracle nor a chatty lane can accumulate goroutines.
	go s.resolveAndRecordInstalledFact(f.Destination.Host, f.Destination.User, f.Destination.Port, dom.BundleGeneration)
}

// installedFactResolveTimeout bounds the oracle call. `ssh -G` is a
// subprocess against the user's config; it is normally instant and can hang
// on a pathological Include. The fact is a convenience, so the honest bound
// is short: a destination that cannot be resolved in this long is recorded
// as nothing rather than under a guessed key.
const installedFactResolveTimeout = 10 * time.Second

func (s *WSServer) resolveAndRecordInstalledFact(host, user string, port int, generation string) {
	// Owner: this goroutine, and nothing else — the work belongs to no
	// request and no session, so there is no caller's context to inherit and
	// a session's would be the wrong lifetime anyway (the host stays
	// integrated after the session that reached it closes).
	//
	// Closing event: installedFactResolveTimeout elapsing, or this function
	// returning — whichever comes first, both through the deferred cancel.
	// There is no third way out: the only blocking call inside is the
	// oracle, and it takes this context.
	ctx, cancel := context.WithTimeout(context.Background(), installedFactResolveTimeout)
	defer cancel()

	// The identity is the RESOLVED destination, through the same oracle and
	// the same argv builder the footprint surface uses to map a saved
	// connection (profileOracleArgv). Two answers to "which host is this"
	// would agree everywhere anyone looked and disagree on the one host
	// reached two ways — and the whole point of the store is that a
	// destination reached by a typed line and by a profile is one entry.
	argv := profileOracleArgv(host, user, port)
	hc, err := s.sshConfigResolver.ResolveArgv(ctx, argv)
	if err != nil {
		// The oracle could not answer, so the key would be a guess. A fact
		// under the wrong key is worse than no fact: it makes the surface
		// name a host it cannot act on.
		s.log.Debug("installed fact: the ssh -G oracle could not resolve the destination",
			"host", host, "error", err)
		return
	}

	// ScriptVersion is deliberately EMPTY. The hello reports the generation
	// and nothing else, and the generation is not the version: they coincide
	// as "v<version>" only when this build did the publishing, and diverge
	// the moment the far host already carried a newer bundle and the prelude
	// adopted the generation named in ITS manifest. Filling the field by
	// stripping the "v" would be right until the first adopted host and
	// silently wrong after it — which is the whole reason the generation is
	// reported rather than derived. An empty field says "not observed"; a
	// derived one would say something false with confidence.
	fact := ssh.InstalledFact{
		Identity:   ssh.IdentityKey(hc),
		Protocol:   installedFactProtocol,
		Generation: generation,
		ObservedAt: time.Now().UTC(),
	}
	if err := s.installedFacts.Record(fact); err != nil {
		s.log.Warn("installed fact: the observation could not be persisted, so the footprint surface will not list this destination",
			"identity", fact.Identity, "error", err)
		return
	}
	s.log.Info("installed fact recorded",
		"identity", fact.Identity, "generation", fact.Generation)
}

// installedFactRecorded reports whether this domain's fact has already been
// written, and marks it as written when it has not. One domain is one remote
// shell, so this is once per connection — the fact is upserted by a later
// connection to the same host, which is how a rotated bundle is noticed.
func (s *WSServer) installedFactRecorded(domain string) bool {
	s.installedFactMu.Lock()
	defer s.installedFactMu.Unlock()
	if s.installedFactSeen == nil {
		s.installedFactSeen = make(map[string]struct{})
	}
	if _, ok := s.installedFactSeen[domain]; ok {
		return true
	}
	s.installedFactSeen[domain] = struct{}{}
	return false
}
