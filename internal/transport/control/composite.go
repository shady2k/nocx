package control

import (
	"context"
	"strings"
	"sync"
)

// composite acquires several Admissions in a fixed order and releases all of
// them on failure of any.
type composite struct {
	name       string
	admissions []Admission
}

// NewComposite returns an Admission that acquires each of the given admissions
// in order and, if any of them refuses, releases every permit acquired so far
// before returning that rejection.
//
// The order is the contract, not an accident: conflict order is acquired
// BEFORE execution capacity, so waiting conflict work never occupies a
// worker (AD-8, constraint 4 of the executor contract).
func NewComposite(admissions ...Admission) Admission {
	names := make([]string, len(admissions))
	for i, a := range admissions {
		names[i] = a.Name()
	}
	return &composite{name: strings.Join(names, "+"), admissions: admissions}
}

func (c *composite) Name() string { return c.name }

func (c *composite) TryAcquire(ctx context.Context) (Permit, *Rejection) {
	permits := make([]Permit, 0, len(c.admissions))
	for _, a := range c.admissions {
		p, rej := a.TryAcquire(ctx)
		if rej != nil {
			for i := len(permits) - 1; i >= 0; i-- {
				permits[i].Release()
			}
			return nil, rej
		}
		permits = append(permits, p)
	}
	return &compositePermit{permits: permits}, nil
}

// compositePermit releases its parts in reverse acquisition order, exactly
// once. The once guards the composite's own contract rather than trusting
// every part to be idempotent: the parts can come from outside this package.
type compositePermit struct {
	permits []Permit
	once    sync.Once
}

func (p *compositePermit) Release() {
	p.once.Do(func() {
		for i := len(p.permits) - 1; i >= 0; i-- {
			p.permits[i].Release()
		}
	})
}

// Parts reports the admissions a composite is made of, flattened and in
// acquisition order; any other Admission reports itself.
//
// It exists so a caller that was handed one Admission can ask a question
// about the PARTS it is made of — internal/capability asks whether one of
// them is the gate the answer to a vault unlock would itself need — without
// branching on Name(), which is metrics-only and may not be branched on
// (AD-8). The question is asked once, at construction; nothing here is on an
// acquisition path.
//
// Flattened because a composite may hold a composite, and a caller asking
// "is this part in here" means it at any depth. The slice is fresh, so a
// caller cannot reorder the acquisition contract by writing into it.
func Parts(a Admission) []Admission {
	c, ok := a.(*composite)
	if !ok {
		return []Admission{a}
	}
	out := make([]Admission, 0, len(c.admissions))
	for _, part := range c.admissions {
		out = append(out, Parts(part)...)
	}
	return out
}
