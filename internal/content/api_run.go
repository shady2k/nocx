package content

import "context"

// APIRunOutcome is the durable state of one API exchange. Pending is used
// between Begin and Complete; the other values are the sender's closed
// outcomes. A pending row has no ended interval and is never an eviction
// candidate.
type APIRunOutcome string

const (
	APIRunPending  APIRunOutcome = "pending"
	APIRunAnswered APIRunOutcome = "answered"
	APIRunFailed   APIRunOutcome = "failed"
	APIRunStopped  APIRunOutcome = "stopped"
)

// APIRunRoute is the route actually used by the exchange, not a copy of the
// environment selection. ProfileID is empty for direct routes.
type APIRunRoute struct {
	Kind        string
	ProfileID   string
	InsecureTLS bool
}

// APIRunSpan names a secret reference or an ordinary text span. It contains
// only the sender's already-redacted representation; the secret value has no
// field in this package and cannot be persisted accidentally.
type APIRunSpan struct {
	From   int
	To     int
	Kind   string
	Name   string
	Damage string
}

// APIRaw is one segmented side of an API exchange. Text is kept in an
// api_run_artifacts chunk stream rather than in the run row.
type APIRaw struct {
	Text  string
	Spans []APIRunSpan
}

// APIRunHeader is one response header as observed by the sender.
type APIRunHeader struct {
	Name    string
	Value   string
	Enabled bool
}

// APIRunTrust records what TLS verification said about the chain.
type APIRunTrust struct {
	State  string
	Reason string
}

// APIRunCertificate is the described certificate chain; raw DER never crosses
// the sender seam and is not accepted by this repository.
type APIRunCertificate struct {
	Subject     string
	Issuer      string
	NotBefore   string
	NotAfter    string
	DNSNames    []string
	IPAddresses []string
	SelfSigned  bool
	Fingerprint string
}

// APIRunTimings records the sender's phase durations in milliseconds.
type APIRunTimings struct {
	DNSMs     float64
	ConnectMs float64
	TLSMs     float64
	TTFBMs    float64
	TotalMs   float64
}

// APIRunFailure is the closed phase and human-readable reason for a failed or
// stopped exchange.
type APIRunFailure struct {
	Phase  string
	Reason string
}

// APIRunResponse is the response side of an answered exchange. Text and Raw
// are reconstructed from ordered artifact chunks; they are intentionally not
// columns on api_runs or api_run_artifacts.
type APIRunResponse struct {
	Status         int
	Headers        []APIRunHeader
	Text           string
	Binary         bool
	Lossy          bool
	Truncated      bool
	Size           int64
	TLSVersion     string
	Raw            APIRaw
	TLSCipherSuite string
	Trust          APIRunTrust
}

// APIRunStart is the durable identity and request snapshot created before the
// network call. CollectionPath and RequestRelPath are the durable key. The
// renderer's collection handle is explicitly absent: it is minted per
// session and means nothing after restart.
type APIRunStart struct {
	CollectionPath string
	RequestRelPath string
	RepeatedFrom   *int64
	Method         string
	URL            string
	Request        APIRaw
	StartedAt      int64
}

// APIRunResult is the settled exchange supplied to Complete. Request belongs
// to APIRunStart because it exists before dialing; Response is nil unless the
// outcome is answered, and Failure is present for failed and stopped outcomes.
type APIRunResult struct {
	Outcome      APIRunOutcome
	Environment  string
	Route        APIRunRoute
	RemoteAddr   string
	DNSAddresses []string
	Timings      APIRunTimings
	Certificates []APIRunCertificate
	Response     *APIRunResponse
	Failure      *APIRunFailure
	EndedAt      int64
}

// APIRun is one durable exchange. Its ID is store-owned and stable across
// restarts; the collection path and request relative path are the durable
// address, not the ephemeral collection handle.
type APIRun struct {
	ID             int64
	CollectionPath string
	RequestRelPath string
	RepeatedFrom   *int64
	Method         string
	URL            string
	Outcome        APIRunOutcome
	Environment    string
	Route          APIRunRoute
	Request        APIRaw
	RemoteAddr     string
	DNSAddresses   []string
	Timings        APIRunTimings
	Certificates   []APIRunCertificate
	Response       *APIRunResponse
	Failure        *APIRunFailure
	StartedAt      int64
	EndedAt        *int64
}

// APIRunRepository owns api_runs and its artifact tables. It is separate from
// LedgerRepository because the ledger's artifacts.execution_id are ledger
// captures and that table has one writer. API runs therefore have their own
// api_run_artifacts and api_run_artifact_chunks tables: generalising the
// ledger owner would make one table answer two unrelated identities and would
// silently give it a second writer.
type APIRunRepository interface {
	// Begin records the request before the network call. The returned pending
	// row exists until Complete succeeds or Delete removes it.
	Begin(ctx context.Context, start APIRunStart) (APIRun, error)
	// Complete atomically appends response artifacts, closes the run interval,
	// and evicts oldest completed runs until the configured logical budget is
	// satisfied. A failed transaction leaves the pending row unchanged.
	Complete(ctx context.Context, id int64, result APIRunResult) (APIRun, error)
	Get(ctx context.Context, id int64) (APIRun, error)
	// List returns runs for the durable collection/request pair, newest first.
	List(ctx context.Context, collectionPath, requestRelPath string) ([]APIRun, error)
	Delete(ctx context.Context, id int64) error
}
