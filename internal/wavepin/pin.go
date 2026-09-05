// Package wavepin binds a caller to a process tree without treating a pid as
// a reusable identity.
package wavepin

import (
	"errors"
	"fmt"
	"time"
)

// Root is the kernel-stamped identity of the process tree that was enrolled.
// PID alone is intentionally insufficient because the kernel may reuse it.
type Root struct {
	PID       int
	StartTime time.Time
}

// Pinner binds process-tree membership to a non-reusable process identity.
type Pinner interface {
	Pin(pid int) (Root, error)
	Member(child int, root Root) (bool, error)
}

// SystemPinner reads process identity and ancestry from the host kernel.
//
// What this fence is for, and what it is not: it separates a stale pid, the
// wrong agent and an unrelated process that found the socket from the one that
// was enrolled. It is NOT a defence against a same-uid actor, who can read our
// descriptors or type into the session anyway. And the principal is the TREE,
// not the process — D14 of the 2026-08-15 design requires the approval to say
// so out loud: "allow this agent AND COMMANDS IT LAUNCHES".
type SystemPinner struct{}

// ErrGone means the pinned root no longer names the same live process.
var ErrGone = errors.New("wavepin: the pinned process is gone or was replaced")

// ErrChainBroken means a live caller cannot be traced to the pinned root.
var ErrChainBroken = errors.New("wavepin: ancestry to the pinned root is broken")

func (SystemPinner) Pin(pid int) (Root, error) {
	process, err := readProcess(pid)
	if err != nil || process.StartTime.IsZero() {
		return Root{}, fmt.Errorf("%w: pid %d", ErrGone, pid)
	}
	return Root{PID: pid, StartTime: process.StartTime}, nil
}

func (SystemPinner) Member(child int, root Root) (bool, error) {
	if root.PID <= 0 || root.StartTime.IsZero() {
		return false, ErrGone
	}

	// Check the root first. This distinguishes a dead enrollment from a live
	// root whose descendant was reparented and makes pid reuse fail closed.
	pinned, err := readProcess(root.PID)
	if err != nil || pinned.StartTime != root.StartTime {
		return false, ErrGone
	}
	if child <= 0 {
		return false, ErrChainBroken
	}

	seen := make(map[int]struct{})
	for current := child; ; {
		if _, ok := seen[current]; ok {
			return false, ErrChainBroken
		}
		seen[current] = struct{}{}

		process, err := readProcess(current)
		if err != nil {
			return false, ErrChainBroken
		}
		if current == root.PID {
			if process.StartTime != root.StartTime {
				return false, ErrGone
			}
			return true, nil
		}
		if process.Parent <= 0 || process.Parent == current {
			return false, ErrChainBroken
		}
		current = process.Parent
	}
}

type processSnapshot struct {
	StartTime time.Time
	Parent    int
}
