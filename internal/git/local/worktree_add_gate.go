package local

import (
	"context"
	"sync"
)

// worktreeAddGate serializes worktree additions that target the same path in
// the same Git repository. Different paths remain independent. The process-wide
// registry lets distinct Factories and Repo values share each reservation.
// processWorktreeAdds is shared across every local Factory in this process.
// Separate NewFactory values can still open the same repository and target.
var processWorktreeAdds worktreeAddGate

type worktreeAddGate struct {
	mu      sync.Mutex
	entries map[string]*worktreeAddGateEntry
}

type worktreeAddGateEntry struct {
	token chan struct{}
	refs  int
}

// lock reserves repo/path until unlock is called. A caller whose own context
// expires while it waits leaves the queue without entering Git, so it cannot
// mistake another add's partial checkout for one it created.
func (g *worktreeAddGate) lock(ctx context.Context, repo, path string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key := worktreePathIdentity(repo) + "\x00" + worktreePathIdentity(path)

	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[string]*worktreeAddGateEntry)
	}
	entry := g.entries[key]
	if entry == nil {
		entry = &worktreeAddGateEntry{token: make(chan struct{}, 1)}
		g.entries[key] = entry
	}
	entry.refs++
	g.mu.Unlock()

	select {
	case entry.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-entry.token
			g.release(key, entry)
			return nil, err
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				<-entry.token
				g.release(key, entry)
			})
		}, nil
	case <-ctx.Done():
		g.release(key, entry)
		return nil, ctx.Err()
	}
}

func (g *worktreeAddGate) release(key string, entry *worktreeAddGateEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(g.entries, key)
	}
}
