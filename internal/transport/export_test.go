package transport

import "time"

// The two transfer timeouts are TEST SEAMS, and this file is where the
// dead-code ratchet says a test seam lives: "a `_test.go` file, which
// deadcode never compiles without -test and so never reports"
// (.githooks/check-deadcode.mjs).
//
// Both were in ws_upload.go beside the registry they configure, and neither
// has ever had a caller outside a test — production takes
// `defaultUploadTicketTTL` and the default stall bound every time.
// WithTransferTicketTTL's own comment says as much: "the tests use it to
// reach the expiry path through the SAME code production takes, rather than
// by sleeping". That is the description of a seam, not of an option somebody
// operating nocx can set, and a file that offers it beside real WSServer
// options claims otherwise (nocx-9le.5.25).
//
// Names and signatures are unchanged, so the upload, download and notify
// tests compile untouched.

// WithTransferStallTimeout bounds how long ONE read of an upload body — or
// ONE write of a download's response — may go
// without progress. It is a stall rule and never a rule about the
// transfer's total duration (D2): a 2 GB upload over a slow link is a
// working upload, and only a body that has stopped is a broken one.
func WithTransferStallTimeout(d time.Duration) WSServerOption {
	return func(s *WSServer) { s.transfers.stall = d }
}

// WithTransferTicketTTL bounds how long an unclaimed ticket lives, in
// either direction. Zero
// is legitimate and means "expire as soon as the mint-side timer can run" —
// the tests use it to reach the expiry path through the SAME code
// production takes, rather than by sleeping.
func WithTransferTicketTTL(d time.Duration) WSServerOption {
	return func(s *WSServer) {
		s.transfers.ttl = d
		s.transfers.ttlSet = true
	}
}
