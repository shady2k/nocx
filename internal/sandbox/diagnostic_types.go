package sandbox

// ObserverStatus is independent of native enforcement and its immutable grant.
type ObserverStatus string

const (
	ObserverUnavailable ObserverStatus = "unavailable"
	ObserverActive      ObserverStatus = "active"
	ObserverUnsupported ObserverStatus = "unsupported"
	ObserverFailed      ObserverStatus = "failed"
)

type DiagnosticAccess string

const (
	DiagnosticRead    DiagnosticAccess = "read"
	DiagnosticWrite   DiagnosticAccess = "write"
	DiagnosticUnknown DiagnosticAccess = "unknown"
)

type DiagnosticSource string

const (
	DiagnosticLinuxSeccomp DiagnosticSource = "linux-seccomp"
	DiagnosticMacSeatbelt  DiagnosticSource = "macos-seatbelt"
)

type DiagnosticPrecision string

const (
	PrecisionAttempted      DiagnosticPrecision = "attempted"
	PrecisionReportedDenial DiagnosticPrecision = "reported-denial"
	PrecisionUnknown        DiagnosticPrecision = "unknown"
)

// DiagnosticObservation contains a bounded collector's facts, never an inferred
// errno or authority to retry. Policy prediction and proposals belong to the inbox.
type DiagnosticObservation struct {
	Executable string
	Path       string
	Operation  string
	Access     DiagnosticAccess
	PathKnown  bool
	Source     DiagnosticSource
	Precision  DiagnosticPrecision
}

// DiagnosticSink must return without waiting for prediction, profile IO or UI.
// Saturation is counted, never allowed to stall a tracee's syscall continuation.
type DiagnosticSink interface {
	Observe(DiagnosticObservation)
	Drop(uint64)
	SetObserver(ObserverStatus)
}

type DiagnosticPrediction string

const (
	PredictionDenied  DiagnosticPrediction = "denied"
	PredictionAllowed DiagnosticPrediction = "allowed"
	PredictionUnknown DiagnosticPrediction = "unknown"
)

type DiagnosticDecision string

const (
	DecisionDismiss DiagnosticDecision = "dismiss"
	DecisionAllowRO DiagnosticDecision = "allow-ro"
	DecisionAllowRW DiagnosticDecision = "allow-rw"
)

type DiagnosticState string

const (
	DiagnosticUnresolved   DiagnosticState = "unresolved"
	DiagnosticPending      DiagnosticState = "pending"
	DiagnosticDismissed    DiagnosticState = "dismissed"
	DiagnosticFuturePolicy DiagnosticState = "future-policy"
	DiagnosticUncertain    DiagnosticState = "uncertain"
)

// DiagnosticProposal names the exact directory displayed for confirmation.
// Its filesystem identity and observed target spelling remain helper-private.
type DiagnosticProposal struct {
	Directory     string `json:"directory"`
	Basis         string `json:"basis"`
	MissingTarget bool   `json:"missingTarget"`
}

type DiagnosticRecord struct {
	ID             string               `json:"id"`
	Revision       uint64               `json:"revision"`
	Executable     string               `json:"executable"`
	Path           string               `json:"path"`
	Operation      string               `json:"operation"`
	Access         DiagnosticAccess     `json:"access"`
	PathKnown      bool                 `json:"pathKnown"`
	Source         DiagnosticSource     `json:"source"`
	Precision      DiagnosticPrecision  `json:"precision"`
	Prediction     DiagnosticPrediction `json:"prediction"`
	Count          uint64               `json:"count"`
	State          DiagnosticState      `json:"state"`
	FutureRevision uint64               `json:"futureRevision"`
	Proposal       *DiagnosticProposal  `json:"proposal"`
}

// DiagnosticPage is a requested snapshot. Unsolicited notices carry only its
// revision/counters/identity, never records or paths.
type DiagnosticPage struct {
	Observer      ObserverStatus     `json:"observer"`
	Revision      uint64             `json:"revision"`
	Dropped       uint64             `json:"dropped"`
	Discontinuity bool               `json:"discontinuity"`
	Total         uint16             `json:"total"`
	NextCursor    uint16             `json:"nextCursor"`
	Records       []DiagnosticRecord `json:"records"`
}
