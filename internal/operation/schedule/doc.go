// Package schedule orders and bounds an operation's execution on the resource
// it changes and the failure domain it draws on.
//
// ADR-8eej3e66 §5 and §6: order is per resource — a checkout, not a binding —
// and execution is bounded per failure domain, so a hung mount holds its own
// permits while a healthy mount's work goes on. This package is the mechanism
// those two rules need; the declarations that name the resources and the
// domains stay in internal/operation/catalog.
//
// It deliberately does not reuse internal/transport/control's keyed
// submission. That submission is fire-and-forget: the read loop hands it work,
// nothing waits, and its queue has no withdrawal. An operation that must wait
// for its turn, give up at the declaration's turn deadline, and never start a
// cancelled mutation needs a synchronous acquisition with a release, which a
// Submission does not express. Extending it would give its session callers a
// blocking shape they never asked for. The rules are the same; the interface
// is the one the enforcing stage can hold.
package schedule
