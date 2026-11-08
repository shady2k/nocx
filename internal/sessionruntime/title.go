package sessionruntime

// A PROGRAM'S TITLE BELONGS TO ITS RUN (nocx-ouru5.1.1).
//
// OSC 0/2 sets the tab's title, and the runtime keeps it as latest-value state
// so a frame can restore it without replaying an effect. What that state did
// not say is WHOSE title it is: a program that named itself on the way in —
// omp announcing "π > tmp" — left the tab reading that name long after the
// program had exited and the prompt was back, beside tabs that read their
// directory. The title is the program's for as long as the program's run is,
// and when the command ends the tab goes back to what the shell shows.
//
// WHERE THE BOUNDARY IS, AND WHY IT IS THE 133 C MARK. The two parties that
// write a title share one byte stream with the markers that delimit a run, and
// the order between them is the whole rule:
//
//	fence  …  C  …the command's own output, including its OSC 0…  fence
//	                then the next prompt: the user's PROMPT_COMMAND title,
//	                OSC 133 D/A, OSC 7, and PS1 with the 133 B marker
//
// The shell's own title therefore arrives AFTER the fence that closed the run,
// and the program's arrives between that run's C and its fence. So the window
// is C→fence, and it is read from the SAME ordered stream that carries both —
// no clock, no lifecycle fact from another transport, and nothing inferred
// from the title's text. A shell that draws its title in the prompt, or in a
// prompt_command, keeps it: it arrives outside the window, and the runtime
// never touches it.
//
// A FENCE WITH NO RUN OPEN clears nothing: `runOutputSeen` is false until a C
// has been sighted in this interval, and a program that forged a fence can
// only clear its own title. A session whose shell has no integration writes no
// C and no fence at all, and its title stays exactly as it always did — there
// is no boundary in its stream to end a run at, and inventing one would be
// guessing.

// sightTitleLocked records one OSC 0/2 report as the latest title, noting
// whether it arrived inside a run's own output window.
func (s *Session) sightTitleLocked(body []byte) {
	title := string(body)
	s.title = &title
	s.titleFromRun = s.runOutputSeen
}

// commandRunEndedLocked ends the window a command's own output opened. A title
// that arrived inside it was the program's, so the sticky title is cleared —
// an EXPLICIT empty string, which is the wire's own way of saying "a clear
// happened", rather than the absence that means no title was ever reported.
// The next frame therefore carries it, and every consumer already knows what
// an empty title means: the tab falls back to the shell's own title, or to the
// working directory.
func (s *Session) commandRunEndedLocked() {
	fromRun := s.titleFromRun
	s.runOutputSeen = false
	s.titleFromRun = false
	if !fromRun {
		return
	}
	cleared := ""
	s.title = &cleared
}
