package hostconn

import "context"

type attemptReporterKey struct{}

// WithAttemptReporter attaches the attempt owner's progress sink.
func WithAttemptReporter(ctx context.Context, sink EventSink) context.Context {
	return context.WithValue(ctx, attemptReporterKey{}, sink)
}

// ReportAttempt reports an actual handshake boundary, including a wait for a person.
func ReportAttempt(ctx context.Context, event Event) {
	if sink, ok := ctx.Value(attemptReporterKey{}).(EventSink); ok {
		sink(event)
	}
}
