package transport

// The pane's opening is announced before the opener is asked anything
// (design §5.5, Q1; nocx-oalo1.5.3).
//
// The connection supervisor's own `Bind` happens inside the SSH opener, after
// the helper has been asked to name the connection and after the stand has
// started a helper on a cold boot — measured over a second on the e2e stand.
// The renderer is owed the first stage on the frame its tab exists, so the
// transport (which already knows the pane and has bound its subscriber just
// above) tells the policy owner to paint the opening now. The opener's real
// bind replaces it before the spawn; this notice carries no events and makes
// no claim about a connection id.
func (s *WSServer) notePaneOpening(paneID string) {
	if paneID == "" || s.paneConnectionPolicy == nil {
		return
	}
	if owner, ok := s.paneConnectionPolicy.(interface{ PaneOpening(string) }); ok {
		owner.PaneOpening(paneID)
	}
}

// settlePaneOpening closes the provisional opening when the opener failed
// before its real bind. It is a no-op for a policy owner that never published
// one.
func (s *WSServer) settlePaneOpening(paneID string) {
	if paneID == "" || s.paneConnectionPolicy == nil {
		return
	}
	if owner, ok := s.paneConnectionPolicy.(interface{ PaneOpeningFailed(string) }); ok {
		owner.PaneOpeningFailed(paneID)
	}
}
