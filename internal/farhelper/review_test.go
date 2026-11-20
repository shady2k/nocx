package farhelper_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/helper/consent"
)

func TestReviewAutoModeGrantedConsent(t *testing.T) {
	h := newHarness(t)
	h.consents.Set(machine, consent.Granted)
	c := connected("review-auto", 1)
	c.HostKeyFingerprint = machine
	h.conns.Put(c)
	h.want("review-auto", farhelper.StateServing, "", farhelper.ActionNone)
}
