package shellintegration

import "github.com/shady2k/nocx/internal/log"

type CwdInfo struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

type PromptMarker struct {
	Kind     string `json:"kind"`     // A, B, C, D
	ExitCode int    `json:"exitCode"` // only for D
}

type ShellIntegration interface {
	ParseCwd(data []byte) (*CwdInfo, error)
	ParsePromptMarker(data []byte) (*PromptMarker, error)
}

type Stub struct {
	log log.Logger
}

func NewStub(logger log.Logger) *Stub {
	return &Stub{log: logger}
}

func (s *Stub) ParseCwd(data []byte) (*CwdInfo, error) {
	s.log.Debug("shellintegration stub: ParseCwd called", "data", string(data))
	return &CwdInfo{Path: string(data)}, nil
}

func (s *Stub) ParsePromptMarker(data []byte) (*PromptMarker, error) {
	s.log.Debug("shellintegration stub: ParsePromptMarker called", "data", string(data))
	return &PromptMarker{Kind: "D"}, nil
}
