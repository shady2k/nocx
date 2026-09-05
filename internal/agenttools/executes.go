package agenttools

// Executes says where a tool's work happens. Dynamic is a deliberate
// state-dependent dispatch: session.read chooses a durable ledger row for an
// ordinary exited item and the renderer broker for a running or
// renderer-owned automatic item.
// The backend never inspects terminal bytes to make that choice (AD-6);
// the item state and automatic provenance come from the run's authority,
// while the live result comes from the renderer.
//
// The field is the execution seam the headless-VT revisit of design §8
// lands on — a tool that today executes in the renderer can execute in Go
// without touching anything else in the row.
//
// Closed by the same construction as Effect: the typed field makes an
// unclassified tool not compile, and supportedExecutes is the value-level
// tripwire.
type Executes string

const (
	// InGo executes the tool's work in this process, against its owner
	// package (internal/git, internal/filesystem, ...).
	InGo Executes = "go"
	// InRenderer asks the renderer to produce the effect (a terminal tool:
	// submit, read the screen, send keys) and waits for the result.
	InRenderer Executes = "renderer"
	// Dynamic selects the owner from the ledger state of the addressed
	// session item. It is not a hidden third implementation: the executor
	// explicitly routes exited items to the ledger and live items to the
	// renderer broker.
	Dynamic Executes = "dynamic"
	// InMCP invokes a configured MCP runtime through an immutable activation
	// captured by the per-run registry. It remains a distinct site because
	// model names are dynamic and cannot be keys in the static Go executor map.
	InMCP Executes = "mcp"
)

var allExecutes = []Executes{InGo, InRenderer, Dynamic, InMCP}

func supportedExecutes(e Executes) bool {
	switch e {
	case InGo, InRenderer, Dynamic, InMCP:
		return true
	default:
		return false
	}
}
