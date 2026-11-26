package main

import (
	"errors"

	"github.com/shady2k/nocx/internal/filesystem/hostsvc"
	"github.com/shady2k/nocx/internal/helper/session"
)

// daemonAdmission is the fs service's way to the daemon's lifecycle, which is
// the SESSION service's (nocx-oalo1.12.5).
//
// The two services are constructed in the order they are for two reasons that
// pull in opposite directions, and this type is where they meet. The JOURNAL —
// the fallible half of the fs service — must be opened before the endpoint
// exists, so a helper whose journal cannot be opened never announces itself
// (serve's own comment). The SESSION service must not be constructed before the
// endpoint either, because its startup grace starts counting the moment it is
// built, and a bound of 250 ms that began while nobody could connect would
// retire a helper that was merely opening a journal.
//
// So the fs service is given this binding at construction and the session
// service fills it in afterwards, still on the constructing goroutine and
// before endpoint.Serve can accept a connection: nothing reads sessions until
// a request is being served, which happens after Serve began.
type daemonAdmission struct {
	sessions *session.Service
}

// Admit reserves the daemon's right to keep serving: see session.Service.Admit
// and hostsvc.Admission.
//
// A retirement is not a failure, and the wire says which one it is (review
// m1). The session service's own code for it is `draining`, and the fs service
// cannot know that sentinel — hostsvc may not import internal/helper/session
// (C30) — so the refusal is wrapped here, in the one package that has both.
// errors.Is against session.ErrDraining still holds through the wrapper, and
// hostsvc's Refusal answers `draining` for it instead of `internal`, which is
// the difference between a coordinator that re-acquires a lease and one that
// reports a broken helper and replays into a daemon that is leaving.
func (a *daemonAdmission) Admit() (func(), error) {
	release, err := a.sessions.Admit()
	if err != nil && errors.Is(err, session.ErrDraining) {
		return nil, &hostsvc.DrainingRefusal{Err: err}
	}
	return release, err
}
