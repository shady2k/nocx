package config

import "github.com/shady2k/nocx/internal/log"

type Config interface {
	Load() error
	Save() error
	Get(key string) (any, error)
	Set(key string, value any) error
}

type Stub struct {
	log  log.Logger
	data map[string]any
}

func NewStub(logger log.Logger) *Stub {
	return &Stub{
		log:  logger,
		data: make(map[string]any),
	}
}

func (s *Stub) Load() error {
	s.log.Info("config stub: Load called (no-op)")
	return nil
}

func (s *Stub) Save() error {
	s.log.Info("config stub: Save called (no-op)")
	return nil
}

func (s *Stub) Get(key string) (any, error) {
	s.log.Debug("config stub: Get", "key", key)
	val, ok := s.data[key]
	if !ok {
		return nil, nil
	}
	return val, nil
}

func (s *Stub) Set(key string, value any) error {
	s.log.Debug("config stub: Set", "key", key)
	s.data[key] = value
	return nil
}
