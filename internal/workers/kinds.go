package workers

// The kinds of row a mailbox holds (nocx-luqz9.4; design §4.2, §5.1; mesh
// design P1–P7).
//
// ONE VOCABULARY AND FOUR VALUES, because a mailbox has one order and one
// reader: what a coordinator reads is "what my workers said and what they were
// seen to be", and a kind that said which of two vocabularies it came from
// would be a second axis for a reader that does not have one. The three a
// worker's tool can produce are the design's own words — `done`, `question`,
// `progress` — and the fourth is what nocx SAW.
//
// The wake's rule is one comparison over them rather than a table beside them,
// which is what makes "progress never wakes" a property of the vocabulary
// instead of a case somebody has to add each time a kind is.

// MessageKind is what a row of a mailbox IS, for the one reader whose behaviour
// depends on it: the wake. It is read off the ROW and never inferred from prose
// — a kind derived from a body is a second derivation of a fact the producer
// already had.
type MessageKind string

const (
	// KindObservation is a settled state nocx saw: idle, blocked or exited.
	KindObservation MessageKind = "observation"
	// KindDone is a worker saying it has finished. What came of it is in the
	// worker's own words and nowhere else: nocx records no outcome, and this
	// kind is not one.
	KindDone MessageKind = "done"
	// KindQuestion is a worker saying it cannot continue without an answer.
	// It wakes the coordinator and it holds up nothing: the answer arrives as
	// the worker's next message (ADR-0070's "why not a blocking ask").
	KindQuestion MessageKind = "question"
	// KindProgress is a CHECKPOINT (mesh design P1, P4): a milestone the
	// worker reports as reached. It never wakes — a report of "still going"
	// is exactly the traffic the batch mechanism exists to keep out of a
	// coordinator's turn — and it is read at the coordinator's next call.
	KindProgress MessageKind = "progress"
)

// reportKinds is the three a WORKER's tool can send, which is a different set
// from the four a row can BE. It is written out because "is this a kind a
// report can carry" is asked of a value that arrived as a string, and a switch
// in the writer would leave the vocabulary without a place to say which of its
// members a producer may use.
var reportKinds = []MessageKind{KindDone, KindQuestion, KindProgress}

// Wakes reports whether mail of this kind starts the coordinator's turn.
//
// ONE COMPARISON, which is design §5.1's sentence read as the rule it is: mail
// of kinds done, question, or an observed idle, blocked or exited wakes an idle
// coordinator, and a checkpoint never does. An EMPTY kind wakes too, and that
// is the conservative direction rather than an oversight: empty is text
// nobody's reporting tool wrote — a coordinator's own message — which only ever
// sits in a WORKER's mailbox, and the wake counts no box the record did not
// admit a coordinator for.
func (k MessageKind) Wakes() bool { return k != KindProgress }

// kindOf reads one row's kind off the row itself.
//
// A row carrying a state IS an observation whatever its Kind field says: the
// state is the stronger fact, and a producer that set both would be describing
// one row twice. Everything else is the kind its writer stamped — which is why
// the writers stamp it (`Say` leaves it empty, `Report` refuses a kind that is
// not one of the three, `placeObservation` writes the state instead).
func kindOf(m Message) MessageKind {
	if m.Observed != nil {
		return KindObservation
	}
	return m.Kind
}
