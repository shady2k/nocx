package agenttools

// Executes says where a tool's work happens: in Go, against an owner
// package, or as a request to the renderer (design §2.2, §5). The field is
// the seam the headless-VT revisit of design §8 lands on — a tool that today
// executes in the renderer can execute in Go without touching anything else
// in the row.
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
)

var allExecutes = []Executes{InGo, InRenderer}

func supportedExecutes(e Executes) bool {
	switch e {
	case InGo, InRenderer:
		return true
	default:
		return false
	}
}
