package transport

import (
	"context"

	"github.com/shady2k/nocx/internal/ssh"
)

// ---------------------------------------------------------------------------
// sshConfig.aliases — JSON-RPC types
// ---------------------------------------------------------------------------

// sshConfigAliasesResponse is the wire format for sshConfig.aliases.
type sshConfigAliasesResponse struct {
	Aliases     []ssh.AliasEntry     `json:"aliases"`
	Unavailable *ssh.UnavailableInfo `json:"unavailable"`
}

// sshConfigPathResponse is the wire format for sshConfig.path.
type sshConfigPathResponse struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// handleSSHConfigPath reports which file sshConfig.aliases reads, and whether
// reading it is possible at all.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"sshConfig.path","params":{}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"path":"/home/u/.ssh/config","available":true}}
//
// It exists because the renderer was naming the file in its own UI text. The
// path is composed in the composition root from the user's home directory, so
// a label that spells "~/.ssh/config" is a guess that happens to agree with it
// — and would keep reading as the truth on the day the two diverge. This is
// cheap on purpose: it stats nothing and resolves nothing, so a dialog may ask
// merely to draw itself, which sshConfig.aliases (an `ssh -G` per host) is far
// too expensive for.
func (s *WSServer) handleSSHConfigPath(wconn *wsConn, req jsonrpcRequest) {
	resp := sshConfigPathResponse{
		Path:      s.sshConfigPath,
		Available: s.sshConfigResolver != nil && s.sshConfigPath != "",
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
}

// handleSSHConfigAliases returns SSH aliases from ~/.ssh/config with their
// resolved values. The handler enumerates Host patterns directly from the
// config file (ssh -G does not enumerate — it answers only for a named host)
// and resolves values through the wired ConfigResolver.
//
//	--> {"jsonrpc":"2.0","id":1,"method":"sshConfig.aliases","params":{}}
//	<-- {"jsonrpc":"2.0","id":1,"result":{"aliases":[{"alias":"prod-db","hostName":"10.0.0.1","user":"deploy","port":2222}],"unavailable":null}}
//
// When the resolver is not wired, returns success with unavailable set
// so the frontend can handle the condition uniformly.
// When resolution fails, entries are returned with hostName=alias and
// unavailable conveys the reason.
func (s *WSServer) handleSSHConfigAliases(wconn *wsConn, req jsonrpcRequest) {
	if s.sshConfigResolver == nil || s.sshConfigPath == "" {
		resp := sshConfigAliasesResponse{
			Aliases: nil,
			Unavailable: &ssh.UnavailableInfo{
				Reason: "parse-failure",
				Detail: "SSH config resolver not wired",
			},
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
		return
	}

	// Enumerate host patterns from the config file.
	patterns, err := ssh.EnumerateHostPatterns(s.sshConfigPath)
	if err != nil {
		resp := sshConfigAliasesResponse{
			Aliases: nil,
			Unavailable: &ssh.UnavailableInfo{
				Reason: "parse-failure",
				Detail: err.Error(),
			},
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
		return
	}

	// No aliases at all — valid state, not degradation.
	if len(patterns) == 0 {
		resp := sshConfigAliasesResponse{
			Aliases:     []ssh.AliasEntry{},
			Unavailable: nil,
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
		return
	}

	// Resolve each pattern through the ConfigResolver (ssh -G).
	// Use a background context with no deadline — the resolver has its own
	// internal timeout per call (10s) and caches results.
	resolved := ssh.ResolveAliases(context.Background(), s.sshConfigResolver, patterns)

	resp := sshConfigAliasesResponse{
		Aliases:     resolved.Aliases,
		Unavailable: resolved.Unavailable,
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
}
