package capability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// THE STORE SET: one barrier, one key per store, and what happens when a
// restore cannot be finished (ADR-8eej3e66 §5).
//
// The record says: "Restore and reset take a barrier exclusively, and every
// writer and coherent reader of those stores takes it shared. A waiting
// exclusive holder admits no new shared ones, so continuous reads cannot
// starve restore. Config and vault stay ordered per store until the audit of
// each store shows its writes are atomic or ordered."
//
// The audit, written up in the stage's report, found both stores atomic PER
// DOCUMENT and ordered WITHIN one process, and neither atomic ACROSS the
// documents one operation writes (config is two documents plus the vault row
// references; the vault is a document plus provider material behind a
// journal). So the per-store key stays, and the barrier is what spans them.
//
// # One plan, derived from the catalogue
//
// The barrier names and the stores each barrier protects are READ OFF the
// declarations, never restated here. A phase that takes a declared barrier
// exclusively and names store resources is saying "these stores are the
// generation that barrier protects", and that is exactly the membership this
// guard needs. A declaration that names a barrier or a store the guard does
// not carry is refused at construction rather than silently unprotected: the
// alternative is a store nobody ordered, which is how mixed generations get
// exposed.
//
// # Acquisition order, and why there is no cycle
//
// Every admission this guard builds acquires BARRIERS FIRST, then store keys in
// one fixed order (config, vault, content). That is the whole deadlock
// argument:
//
//   - an exclusive holder obtains the barrier only when no shared holder
//     exists, so while it holds a key no shared holder holds one. Store-key
//     contention therefore only ever happens BETWEEN shared holders, and they
//     take the keys in the same order — no cycle.
//   - a shared holder never acquires a barrier inside a held scope: the
//     composite is built whole and acquired once, which is the record's
//     "no acquisition is nested inside a held scope".
//
// # Quarantine
//
// A committed restore whose rollback does not finish inside its recovery
// budget cannot be left readable: part of the set is the new generation and
// part the old one, and nothing on disk says which. The stores it was
// rewriting are QUARANTINED: every admission over them is refused with a
// typed unavailable rejection naming the store and the reason, and stays so
// until restart. Restart runs the journal recovery before anything reopens
// access, which is the other half of the trade (§5). Availability is what is
// spent; consistency is what is bought.
const (
	// StoreConfig is the config store: the profile/group/endpoint document and
	// the settings document, which one config operation can write in sequence.
	StoreConfig = "config"
	// StoreVault is the vault: one document plus provider material behind a
	// journal.
	StoreVault = "vault"
	// StoreContent is the content store family restore rewrites (snippets,
	// notes, skills). Its own domain stage owns the operations; the barrier is
	// what makes restore's exclusive claim over it hold.
	StoreContent = "content"
)

// Barrier names, as the catalogue declares them.
const (
	BarrierStoreSet   = "store-set"
	BarrierVaultReset = "vault-reset"
)

// storeOrder is the one acquisition order for store keys. It is fixed here,
// not derived, because an order that could differ between two constructions is
// not an order.
var storeOrder = []string{StoreConfig, StoreVault, StoreContent}

// StoreGuard owns the barrier, the per-store keys and the quarantine. It is
// built once at the composition root and shared by every operation over the
// store set.
type StoreGuard struct {
	barriers     map[string]*control.Barrier
	barrierOrder []string
	keys         map[string]control.Admission
	members      map[string][]string // store -> barrier names, in barrierOrder
	stores       []string

	mu         sync.RWMutex
	quarantine map[string]string // store -> reason it is unavailable
}

// NewStoreGuard derives the plan from the catalogue and builds one barrier per
// declared barrier name and one key per store. maxQueue and waitTimeout are the
// bounds every acquisition carries; the composition root names them, the same
// way it names the domain gates' bounds.
func NewStoreGuard(decls []catalog.Declaration, maxQueue int, waitTimeout time.Duration) *StoreGuard {
	g := &StoreGuard{
		barriers:   make(map[string]*control.Barrier),
		keys:       make(map[string]control.Admission),
		members:    make(map[string][]string),
		quarantine: make(map[string]string),
	}
	owned := map[string]bool{BarrierStoreSet: true, BarrierVaultReset: true}
	storeSet := map[string]bool{StoreConfig: true, StoreVault: true, StoreContent: true}
	barrierSeen := map[string]bool{}

	for _, d := range decls {
		for _, phase := range d.Phases {
			// Only the barriers this guard carries take part. A declaration
			// may name a barrier that belongs to another resource family (the
			// native picker is one); the guard has nothing to say about it.
			var phaseBarriers []string
			for _, r := range phase.Resources {
				if r.Kind != catalog.ResourceBarrier || !owned[r.Name] {
					continue
				}
				barrierSeen[r.Name] = true
				phaseBarriers = append(phaseBarriers, r.Name)
			}
			for _, r := range phase.Resources {
				if r.Kind != catalog.ResourceStore {
					continue
				}
				if !storeSet[r.Name] {
					panic(fmt.Sprintf("capability: store guard does not own store %q (declared by %s)", r.Name, d.ID))
				}
				if r.Mode == catalog.ModeExclusive && len(phaseBarriers) > 0 {
					for _, name := range phaseBarriers {
						g.addMember(r.Name, name)
					}
				}
			}
		}
	}
	if !barrierSeen[BarrierStoreSet] || !barrierSeen[BarrierVaultReset] {
		panic("capability: a store barrier has no declaration; the plan would guard nothing")
	}
	for name := range barrierSeen {
		g.barrierOrder = append(g.barrierOrder, name)
	}
	sort.Strings(g.barrierOrder)
	for _, name := range g.barrierOrder {
		g.barriers[name] = control.NewBarrier(name, maxQueue, waitTimeout)
	}
	for _, store := range storeOrder {
		g.stores = append(g.stores, store)
		g.keys[store] = control.NewWaitingSemaphore("store:"+store, 1, maxQueue, waitTimeout)
		// Rebuild the member list in barrier order from the names collected
		// above: the acquisition order may not depend on which declaration
		// happened to be walked first.
		ordered := make([]string, 0, len(g.members[store]))
		for _, name := range g.barrierOrder {
			if contains(g.members[store], name) {
				ordered = append(ordered, name)
			}
		}
		g.members[store] = ordered
	}
	// A store that no barrier protects (content joins store-set through
	// backup.restore; config through both; vault through vault-reset) would be
	// silently unguarded, which is the defect this guard exists to prevent.
	for _, store := range g.stores {
		if len(g.members[store]) == 0 {
			panic(fmt.Sprintf("capability: store %q is protected by no declared barrier", store))
		}
	}
	return g
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func (g *StoreGuard) addMember(store, barrier string) {
	if !contains(g.members[store], barrier) {
		g.members[store] = append(g.members[store], barrier)
	}
}

// Barriers returns the barrier names in acquisition order. Diagnostics and the
// stage's tests read the plan through it rather than restating it.
func (g *StoreGuard) Barriers() []string {
	out := make([]string, len(g.barrierOrder))
	copy(out, g.barrierOrder)
	return out
}

// Stores returns the store names in key-acquisition order.
func (g *StoreGuard) Stores() []string {
	out := make([]string, len(g.stores))
	copy(out, g.stores)
	return out
}

// BarriersOf returns the barriers that protect a store, in acquisition order.
func (g *StoreGuard) BarriersOf(store string) []string {
	names, ok := g.members[store]
	if !ok {
		panic("capability: no store named " + store)
	}
	out := make([]string, len(names))
	copy(out, names)
	return out
}

// Shared returns the admission a reader or a single-store writer takes: every
// barrier that protects the store, shared, then the store's key. It panics on
// an unknown store — that is a wiring mistake, not a runtime condition.
func (g *StoreGuard) Shared(store string) control.Admission {
	members, ok := g.members[store]
	if !ok {
		panic("capability: no store named " + store)
	}
	parts := make([]control.Admission, 0, len(members)+1)
	for _, name := range g.barrierOrder {
		if contains(members, name) {
			parts = append(parts, g.barriers[name].Shared())
		}
	}
	parts = append(parts, g.keys[store])
	name := "store:" + store + "+shared"
	return g.watch([]string{store}, control.NewComposite(parts...), name)
}

// SharedBarrier returns the barrier half of the shared plan: every barrier
// that protects the store, taken shared, with the quarantine check and without
// the store's own key. It is for a store whose ORDER belongs to another
// admission. Content is that store: migration stage 6 replaced its domain gate
// with a per-resource FIFO, so taking Shared(StoreContent) would put every
// content write back behind one capacity-one key and undo that grain. What
// stage 7 still owes content is the exclusion half — a restore that rewrites
// the store set must see no content writer — and a barrier alone is exactly
// that. A caller composes it with its own ordering admission, barrier first,
// which is the order every holder here takes.
func (g *StoreGuard) SharedBarrier(store string) control.Admission {
	members, ok := g.members[store]
	if !ok {
		panic("capability: no store named " + store)
	}
	parts := make([]control.Admission, 0, len(members))
	for _, name := range g.barrierOrder {
		if contains(members, name) {
			parts = append(parts, g.barriers[name].Shared())
		}
	}
	if len(parts) == 0 {
		panic("capability: store " + store + " is protected by no declared barrier")
	}
	return g.watch([]string{store}, control.NewComposite(parts...), "store:"+store+"+barrier")
}

// Exclusive returns the admission an operation that rewrites the stores of a
// declaration takes: every barrier the declaration names, exclusive, then every
// store it names, in the key order. It panics on a resource the guard does not
// own, because an operation wired here without a declared resource would be
// unguarded and its correctness would be an accident.
func (g *StoreGuard) Exclusive(decl catalog.Declaration) control.Admission {
	var stores []string
	barriers := map[string]bool{}
	for _, r := range decl.Resources() {
		switch r.Kind {
		case catalog.ResourceBarrier:
			if _, ok := g.barriers[r.Name]; !ok {
				panic(fmt.Sprintf("capability: %s takes barrier %q, which the store guard does not own", decl.ID, r.Name))
			}
			if r.Mode != catalog.ModeExclusive {
				panic(fmt.Sprintf("capability: %s takes barrier %q shared; the guard's exclusive plan is for the operations that rewrite the set", decl.ID, r.Name))
			}
			barriers[r.Name] = true
		case catalog.ResourceStore:
			if _, ok := g.keys[r.Name]; !ok {
				panic(fmt.Sprintf("capability: %s takes store %q, which the store guard does not own", decl.ID, r.Name))
			}
			if r.Mode != catalog.ModeExclusive {
				panic(fmt.Sprintf("capability: %s takes store %q %q; the exclusive plan follows the declaration's exclusive reservation", decl.ID, r.Name, r.Mode))
			}
			stores = append(stores, r.Name)
		}
	}
	if len(stores) == 0 {
		panic(fmt.Sprintf("capability: %s declares no store for its exclusive admission", decl.ID))
	}
	parts := make([]control.Admission, 0, len(barriers)+len(stores))
	for _, name := range g.barrierOrder {
		if barriers[name] {
			parts = append(parts, g.barriers[name].Exclusive())
		}
	}
	ordered := make([]string, 0, len(stores))
	for _, store := range g.stores {
		if contains(stores, store) {
			ordered = append(ordered, store)
			parts = append(parts, g.keys[store])
		}
	}
	return g.watch(ordered, control.NewComposite(parts...), string(decl.ID)+"+exclusive")
}

// StoresOf returns the store names a declaration takes, in key order. It is how
// a caller learns WHICH stores a failed recovery must quarantine without
// restating the declaration.
func (g *StoreGuard) StoresOf(decl catalog.Declaration) []string {
	var out []string
	for _, store := range g.stores {
		for _, r := range decl.Resources() {
			if r.Kind == catalog.ResourceStore && r.Name == store {
				out = append(out, store)
				break
			}
		}
	}
	return out
}

// watch wraps an admission in the quarantine check. The check is first, so a
// quarantined store is refused even while the barrier and key are free: the
// refusal is about what the store CONTAINS, not about capacity.
//
// The check is on BOTH sides of the wait, and the second one is the one the
// quarantine exists for. A restore holds the store set exclusively, its
// rollback fails, and it quarantines before it releases the permit — the
// permit is what keeps readers out while the set holds two generations
// (restoreOperation.Run). A caller that was already queued behind that
// barrier passed the check while the quarantine did not exist yet, so the
// permit it is finally granted has to be checked again: otherwise the save is
// admitted to, and acknowledged into, the generation the quarantine hides, and
// the rollback or the next start's recovery overwrites what it was told had
// been written.
//
// A check made while the permit is held cannot be overtaken, which is why the
// second one is decisive. Quarantine is set only by an operation holding an
// EXCLUSIVE plan, and every admission this guard builds for a shared holder
// takes every barrier that protects the store first — so the exclusive plan
// cannot even start while the permit is held, and no quarantine can appear
// between the second check and the callback.
func (g *StoreGuard) watch(stores []string, inner control.Admission, name string) control.Admission {
	return &guardedAdmission{guard: g, stores: stores, inner: inner, name: name}
}

// Quarantine marks every named store unavailable until restart. It never
// clears: a second call with another reason is a second store.
func (g *StoreGuard) Quarantine(stores []string, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, store := range stores {
		if _, ok := g.keys[store]; !ok {
			panic("capability: cannot quarantine unknown store " + store)
		}
		if _, held := g.quarantine[store]; !held {
			g.quarantine[store] = reason
		}
	}
}

// Quarantined reports the reason a store is unavailable, if it is.
func (g *StoreGuard) Quarantined(store string) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	reason, ok := g.quarantine[store]
	return reason, ok
}

func (g *StoreGuard) quarantineReason(stores []string) (string, string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, store := range stores {
		if reason, ok := g.quarantine[store]; ok {
			return store, reason, true
		}
	}
	return "", "", false
}

// guardedAdmission refuses a quarantined store before it touches capacity.
type guardedAdmission struct {
	guard  *StoreGuard
	stores []string
	inner  control.Admission
	name   string
}

func (a *guardedAdmission) Name() string { return a.name }

// Holders answers the diagnostics question through the wrapped admission, so
// app.diagnostics still names the requests holding a store's barrier and key.
// The wrapper exists to refuse a quarantined store, not to hide who holds it.
func (a *guardedAdmission) Holders() []control.Holder { return control.HoldersOf(a.inner) }

func (a *guardedAdmission) TryAcquire(ctx context.Context) (control.Permit, *control.Rejection) {
	if refusal := a.quarantineRefusal(); refusal != nil {
		return nil, refusal
	}
	permit, rej := a.inner.TryAcquire(ctx)
	if rej != nil {
		return nil, rej
	}
	// The wait above is where a restore can quarantine the set it was already
	// holding, so the answer is about the store's contents NOW, not when the
	// caller arrived. Release before refusing: the reason is the contents, and
	// keeping the shared half of the barrier would keep a later restore from
	// ever draining a set it must be able to rewrite.
	if refusal := a.quarantineRefusal(); refusal != nil {
		permit.Release()
		return nil, refusal
	}
	return permit, nil
}

// quarantineRefusal is the unavailable answer, or nil while the stores are
// serving. One copy so the two checks cannot drift into two sentences.
func (a *guardedAdmission) quarantineRefusal() *control.Rejection {
	store, reason, ok := a.guard.quarantineReason(a.stores)
	if !ok {
		return nil
	}
	return &control.Rejection{
		Kind:   control.RejectionUnavailable,
		Reason: unavailableSentence(store, reason),
		Scope:  "store:" + store,
	}
}

// unavailableSentence is what a person reads when a store is quarantined. It
// names the store, says what happened in their terms, and says what ends it —
// because the answer is a restart, not a retry.
func unavailableSentence(store, reason string) string {
	return fmt.Sprintf("Your %s data is unavailable because an interrupted restore could not be completed (%s). Restart nocx to repair it.",
		storeName(store), strings.TrimSpace(reason))
}

func storeName(store string) string {
	switch store {
	case StoreConfig:
		return "settings and connection"
	case StoreVault:
		return "vault"
	case StoreContent:
		return "snippets, notes and skill"
	default:
		return store
	}
}
