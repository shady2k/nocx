package app

import (
	"sync/atomic"

	"github.com/shady2k/nocx/internal/settings"
)

// sessionLifetimeSetting carries the configured absence limit to new sessions.
// The helper retains the value for the session's lifetime, like its buffer bound.
type sessionLifetimeSetting struct{ days atomic.Int64 }

func newSessionLifetimeSetting(reg *settings.Registry) *sessionLifetimeSetting {
	s := &sessionLifetimeSetting{}
	apply := func() {
		v, err := reg.GetNumber(settings.SessionIdleDays)
		if err != nil {
			v = settings.SessionIdleDays.DefaultValue()
		}
		s.days.Store(int64(v))
	}
	apply()
	reg.AddNotifier(func(_ int, keys []string) {
		for _, key := range keys {
			if key == settings.SessionIdleDays.Key() {
				apply()
				return
			}
		}
	})
	return s
}

func (s *sessionLifetimeSetting) daysPtr() *int {
	if s == nil {
		return nil
	}
	days := int(s.days.Load())
	return &days
}
