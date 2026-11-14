package agentrecord

// # The agents the assistant can run, as detection knows them
//
// ADR-anntnxdj decision 6: Settings lists what is installed on this computer.
// The five agents nocx's assistant can run are Claude Code, Codex, pi, omp and
// prime-agent, and the facts detection needs about each — the command to look
// for, the flag that prints its version, and, where the agent has one, the
// probe that says whether it can answer — are stated here.
//
// THIS IS THE ONLY PLACE IN THE TREE THAT NAMES AN AGENT. A surface, the
// detection code and the transport learn an agent by iterating this table and
// see only the id, the display name and the answers; a connector is the one
// other place that may know an agent's name, and there is none of those in
// this build yet. Adding an agent is a row here, not a branch anywhere else.
//
// It is deliberately NOT the launch record beside it (Document, in
// agentrecord.go): a launch record says how to start an agent and what its
// conversation resumes under, and it is a document a person edits. These are
// facts about finding an agent this build supports, and a person does not
// edit them. The two meet only at the command — claude's launch record and its
// detection record name the same binary, because it is the same binary.

// Readiness is what detection can say about one agent on this machine. It is a
// closed set for the reason every closed set here is: a state nobody wrote a
// branch for would land in whichever branch was written last, and this one is
// what a person reads before trusting the assistant.
type Readiness string

const (
	// ReadinessNotInstalled means the command was not found on the login
	// shell's PATH.
	ReadinessNotInstalled Readiness = "not_installed"
	// ReadinessInstalled means the agent is there and states no readiness of
	// its own, so "it is installed" is the whole answer.
	ReadinessInstalled Readiness = "installed"
	// ReadinessReady means the agent's own probe said it can answer.
	ReadinessReady Readiness = "ready"
	// ReadinessNotReady means the agent is installed and its own probe said
	// it cannot answer yet — pi with no provider configured is the case.
	ReadinessNotReady Readiness = "not_ready"
	// ReadinessInvalid means the agent's own probe reported that what it is
	// configured with is not usable.
	ReadinessInvalid Readiness = "invalid"
	// ReadinessUnknown means the agent is installed and its probe did not
	// answer: it hung past its bound, it exited with a status its record does
	// not interpret, or it could not be started. It is never read as ready.
	ReadinessUnknown Readiness = "unknown"
)

// ProbeAnswer is one exit status a readiness probe answers with and what it
// means. A status the record does not list is ReadinessUnknown rather than a
// guess, which is why this is a list rather than an assumption that 0 is
// ready and everything else is not.
type ProbeAnswer struct {
	Exit      int
	Readiness Readiness
}

// ReadinessProbe is the command an agent answers "can you run" with, appended
// to its command, and what each exit status it gives means.
type ReadinessProbe struct {
	Args    []string
	Answers []ProbeAnswer
}

// SupportedAgent is one agent the assistant can run, as detection needs it.
type SupportedAgent struct {
	// ID is the agent's name, and it is the same id every other package
	// names the agent by — the enrolment act's, the launch record's.
	ID string
	// DisplayName is what a surface calls it.
	DisplayName string
	// Command is the bare program to look for on the login shell's PATH. It
	// is a program and not a shell line, exactly as Document.Command is.
	Command string
	// VersionArgs are the arguments that make the command print its version.
	VersionArgs []string
	// Readiness is nil where the agent has no way to say whether it can
	// answer; detection then reports installed or not installed, and nothing
	// more.
	Readiness *ReadinessProbe
}

// supportedAgents is the set, in the order the connectors are built
// (ADR-anntnxdj decision 1's table). The order is the product's, not
// alphabetical: it is the order a person meets them in the section.
var supportedAgents = []SupportedAgent{
	{
		ID:          "claude",
		DisplayName: "Claude Code",
		Command:     "claude",
		VersionArgs: []string{"--version"},
	},
	{
		ID:          "codex",
		DisplayName: "Codex",
		Command:     "codex",
		VersionArgs: []string{"--version"},
	},
	{
		ID:          "pi",
		DisplayName: "pi",
		Command:     "pi",
		VersionArgs: []string{"--version"},
		// pi is the one agent here that states whether it can answer:
		// `pi auth check` exits 0 when it is ready, 1 when it is not, and 2
		// when what it is configured with is not usable (measured on 1.5.2,
		// ADR-anntnxdj decision 6).
		Readiness: &ReadinessProbe{
			Args: []string{"auth", "check"},
			Answers: []ProbeAnswer{
				{Exit: 0, Readiness: ReadinessReady},
				{Exit: 1, Readiness: ReadinessNotReady},
				{Exit: 2, Readiness: ReadinessInvalid},
			},
		},
	},
	{
		ID:          "omp",
		DisplayName: "omp",
		Command:     "omp",
		VersionArgs: []string{"--version"},
	},
	{
		ID:          "prime-agent",
		DisplayName: "prime-agent",
		Command:     "prime-agent",
		VersionArgs: []string{"--version"},
	},
}

// SupportedAgents lists the agents this build's assistant can run, in the
// product's order. The slice is a copy: the table is the one owner and no
// caller gets to edit it.
func SupportedAgents() []SupportedAgent {
	return append([]SupportedAgent(nil), supportedAgents...)
}

// Detection is one agent as detection found it on this machine.
type Detection struct {
	ID          string
	DisplayName string
	// Found is whether the command was resolved on the login shell's PATH.
	Found bool
	// Version is the first line the command printed for its version flag,
	// trimmed. Empty means it printed nothing usable, or was not found.
	Version string
	// Readiness is the answer above. It is never empty: a row that was not
	// found is ReadinessNotInstalled.
	Readiness Readiness
}
