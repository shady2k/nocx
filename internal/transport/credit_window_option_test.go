package transport

// The credit window is a fixed declaration in production: transport.credit
// advertises DefaultCreditWindow to every connection, and no setting names
// another value. Only tests need one, to fill a window with a known number of
// calls — so the option lives here rather than in the shipped file. It was in
// ws.go and the dead-code ratchet was right about it on both platforms: an
// exported option reachable from nothing but a test is not part of the
// product's surface.

// WithCreditWindow sets the ordinary credit window transport.credit
// advertises. Zero is DefaultCreditWindow, which is what production uses; a
// test names a smaller value to fill a window with a known number of calls.
// The value is per connection: every connection of this server is told the
// same window.
func WithCreditWindow(n int) WSServerOption {
	return func(s *WSServer) { s.creditWindow = n }
}
