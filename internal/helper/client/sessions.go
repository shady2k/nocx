package client

import (
	"context"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// HostSessionID is the coordinator's view of a helper-owned session identity.
// It deliberately duplicates the wire shape instead of exposing proto types
// to callers above the helper client boundary.
type HostSessionID struct {
	Generation string `json:"generation"`
	Session    string `json:"session"`
}

type LaunchRecord struct {
	Shell       string `json:"shell"`
	Cwd         string `json:"cwd"`
	Pid         int    `json:"pid"`
	Pgid        int    `json:"pgid"`
	Cols        uint16 `json:"cols"`
	Rows        uint16 `json:"rows"`
	WindowBytes int64  `json:"windowBytes"`
}

type Observation struct {
	Source            string   `json:"source"`
	Cwd               string   `json:"cwd,omitempty"`
	Argv              []string `json:"argv"`
	ForegroundPgid    int      `json:"foregroundPgid,omitempty"`
	ForegroundCommand string   `json:"foregroundCommand,omitempty"`
	Unavailable       []string `json:"unavailable"`
}

type WindowSpan struct {
	Base    uint64 `json:"base"`
	Written uint64 `json:"written"`
}

type ExitStatus struct {
	Code   int    `json:"code"`
	Signal int    `json:"signal,omitempty"`
	At     string `json:"at"`
}

type SessionEntry struct {
	HostSessionID HostSessionID `json:"hostSessionId"`
	Workspace     string        `json:"workspace"`
	StartedAt     string        `json:"startedAt"`
	Launch        LaunchRecord  `json:"launch"`
	Observed      *Observation  `json:"observed"`
	Window        WindowSpan    `json:"window"`
	Writer        *string       `json:"writer"`
	WriterEpoch   uint64        `json:"writerEpoch"`
	Exit          *ExitStatus   `json:"exit"`
}

// Sessions asks one helper generation for the sessions it currently holds.
// An empty answer is an answer and is returned as a non-nil empty slice.
func (c *Client) Sessions(ctx context.Context) ([]SessionEntry, error) {
	var result proto.SessionsResult
	if err := c.Call(ctx, proto.ServiceSession, proto.OpSessions, proto.SessionsParams{}, &result); err != nil {
		return nil, err
	}
	entries := make([]SessionEntry, 0, len(result.Sessions))
	for _, entry := range result.Sessions {
		entries = append(entries, mapSessionEntry(entry))
	}
	return entries, nil
}

func mapSessionEntry(in proto.SessionEntry) SessionEntry {
	out := SessionEntry{
		HostSessionID: HostSessionID{Generation: string(in.Session.Generation), Session: in.Session.Session},
		Workspace:     string(in.Workspace),
		StartedAt:     in.StartedAt,
		Launch: LaunchRecord{
			Shell: in.Launch.Shell, Cwd: in.Launch.Cwd, Pid: in.Launch.Pid,
			Pgid: in.Launch.Pgid, Cols: in.Launch.Cols, Rows: in.Launch.Rows,
			WindowBytes: in.Launch.WindowBytes,
		},
		Window:      WindowSpan{Base: uint64(in.Window.Base), Written: uint64(in.Window.Written)},
		WriterEpoch: uint64(in.WriterEpoch),
	}
	if in.Writer != nil {
		writer := string(*in.Writer)
		out.Writer = &writer
	}
	if in.Observed != nil {
		out.Observed = &Observation{
			Source:            in.Observed.Source,
			Cwd:               in.Observed.Cwd,
			Argv:              make([]string, 0, len(in.Observed.Argv)),
			ForegroundPgid:    in.Observed.ForegroundPgid,
			ForegroundCommand: in.Observed.ForegroundCommand,
			Unavailable:       make([]string, 0, len(in.Observed.Unavailable)),
		}
		out.Observed.Argv = append(out.Observed.Argv, in.Observed.Argv...)
		for _, diagnostic := range in.Observed.Unavailable {
			out.Observed.Unavailable = append(out.Observed.Unavailable, string(diagnostic))
		}
	}
	if in.Exit != nil {
		out.Exit = &ExitStatus{Code: in.Exit.Code, Signal: in.Exit.Signal, At: in.Exit.At}
	}
	return out
}
