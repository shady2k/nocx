package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// ---------------------------------------------------------------------------
// The row-handle translation seam (ADR-0017 §1)
// ---------------------------------------------------------------------------
//
// A profile's secret bindings are BACKEND-OWNED references (sec:v1:...) in
// storage. The renderer may not hold or name a reference (ADR-0011 §2), so
// every profile that crosses the wire carries the reference's row handle
// (secrow:...) instead. The seam has a pure half and a resolving half:
//
//	reference → row   vault.RowFor (one-way derivation, no vault state read)
//	row → reference   vault.ResolveRow (needs the vault catalogue)
//
// Every profile/group read path converts to rows before marshaling; every
// write path resolves rows before storing. A reference therefore never
// appears in a renderer response, and a row never survives into storage.

// secretRefToRow converts a stored reference to the renderer's row handle.
// Empty stays empty.
func secretRefToRow(ref string) string {
	if ref == "" {
		return ""
	}
	return vault.RowFor(credential.SecretID(ref))
}

// rowToSecretRef resolves a renderer row handle to the stored reference.
// Empty stays empty; an unknown row is an error — the renderer can only
// name rows the vault actually holds (nocx-jb20.1).
func (s *WSServer) rowToSecretRef(row string) (string, error) {
	if row == "" {
		return "", nil
	}
	if s.vaultLifecycle == nil {
		return "", errors.New("no vault: cannot resolve a secret row")
	}
	inputs := s.secretRowInputs()
	id, ok := s.vaultLifecycle.ResolveRow(row, inputs)
	if !ok {
		return "", fmt.Errorf("unknown secret row %q", row)
	}
	return string(id), nil
}

// optionsToWire replaces every stored secret reference in a profile's
// options with its row handle. Used on every read path that marshals a
// profile to the renderer.
func optionsToWire(o profile.StoredSSHProfileOptions) profile.StoredSSHProfileOptions {
	o.PasswordSecret = secretRefToRow(o.PasswordSecret)
	o.KeySecret = secretRefToRow(o.KeySecret)
	o.KeyPassphraseSecret = secretRefToRow(o.KeyPassphraseSecret)
	return o
}

// optionsFromWire resolves every row handle in a profile's options to its
// stored reference. Used on every write path that takes a profile from the
// renderer.
func (s *WSServer) optionsFromWire(o profile.StoredSSHProfileOptions) (profile.StoredSSHProfileOptions, error) {
	var err error
	if o.PasswordSecret, err = s.rowToSecretRef(o.PasswordSecret); err != nil {
		return o, err
	}
	if o.KeySecret, err = s.rowToSecretRef(o.KeySecret); err != nil {
		return o, err
	}
	if o.KeyPassphraseSecret, err = s.rowToSecretRef(o.KeyPassphraseSecret); err != nil {
		return o, err
	}
	return o, nil
}

// wireProfile converts a stored profile to its wire form: every secret
// reference replaced with the renderer's row handle.
func wireProfile(p profile.SSHProfile) profile.SSHProfile {
	p.Options = optionsToWire(p.Options)
	return p
}

// wireGroup converts a stored group to its wire form: every secret reference
// in the group's defaults replaced with the renderer's row handle.
func wireGroup(g profile.ProfileGroup) profile.ProfileGroup {
	if g.Defaults != nil {
		g.Defaults.SparseSSHOptions = sparseToWire(g.Defaults.SparseSSHOptions)
	}
	return g
}

// groupFromWire resolves every row handle in a group's defaults to its stored
// reference. Used on every write path that takes a group from the renderer.
func (s *WSServer) groupFromWire(g profile.ProfileGroup) (profile.ProfileGroup, error) {
	if g.Defaults == nil {
		return g, nil
	}
	var err error
	if g.Defaults.SparseSSHOptions, err = s.sparseFromWire(g.Defaults.SparseSSHOptions); err != nil {
		return g, err
	}
	return g, nil
}

// sparseToWire replaces the secret references in group/global defaults with
// row handles.
func sparseToWire(s profile.SparseSSHOptions) profile.SparseSSHOptions {
	rowPtr := func(p *string) *string {
		if p == nil {
			return nil
		}
		v := secretRefToRow(*p)
		return &v
	}
	s.PasswordSecret = rowPtr(s.PasswordSecret)
	s.KeySecret = rowPtr(s.KeySecret)
	s.KeyPassphraseSecret = rowPtr(s.KeyPassphraseSecret)
	return s
}

// sparseFromWire resolves the row handles in group/global defaults to stored
// references.
func (s *WSServer) sparseFromWire(sp profile.SparseSSHOptions) (profile.SparseSSHOptions, error) {
	var err error
	if sp.PasswordSecret != nil {
		if *sp.PasswordSecret, err = s.rowToSecretRef(*sp.PasswordSecret); err != nil {
			return sp, err
		}
	}
	if sp.KeySecret != nil {
		if *sp.KeySecret, err = s.rowToSecretRef(*sp.KeySecret); err != nil {
			return sp, err
		}
	}
	if sp.KeyPassphraseSecret != nil {
		if *sp.KeyPassphraseSecret, err = s.rowToSecretRef(*sp.KeyPassphraseSecret); err != nil {
			return sp, err
		}
	}
	return sp, nil
}

// secretRowInputs returns the row set ResolveRow checks beyond the vault's
// own catalogue records: the secret references bound to stored profiles.
// Every secret the renderer can address is created through the vault
// (ADR-0016), so the catalogue covers it — this set exists for the
// unrecorded reference that predates a catalogue entry. Group-default
// bindings resolve through the catalogue like any other; an inherited
// binding is resolved onto its profile before storage, so it is present
// here in its profile's own options.
func (s *WSServer) secretRowInputs() []vault.CredentialInventory {
	if s.profiles == nil {
		return nil
	}
	profiles, err := s.profiles.LoadProfiles()
	if err != nil {
		return nil
	}
	inputs := make([]vault.CredentialInventory, 0, len(profiles))
	for _, p := range profiles {
		o := p.Options
		if o.PasswordSecret == "" && o.KeySecret == "" && o.KeyPassphraseSecret == "" {
			continue
		}
		inputs = append(inputs, vault.CredentialInventory{
			ID:                  p.ID,
			SecretID:            o.PasswordSecret,
			PassphraseSecretID:  o.KeyPassphraseSecret,
			KeyMaterialSecretID: o.KeySecret,
		})
	}
	return inputs
}

// wireEffectiveSecretFields replaces the secret references in an effective
// DTO's fields with row handles, so the renderer never receives a reference
// (ADR-0011 §2).
func wireEffectiveSecretFields(dto *profile.EffectiveProfileDTO) {
	for _, name := range []string{"passwordSecret", "keySecret", "keyPassphraseSecret"} {
		f, ok := dto.Fields[name]
		if !ok {
			continue
		}
		s, isStr := f.Value.(string)
		if isStr {
			f.Value = secretRefToRow(s)
			dto.Fields[name] = f
		}
	}
}

// handleSecretUsageMethod answers, for one vault row, the profiles that use
// the secret behind it (ADR-0017: the count is the number of profiles whose
// effective secret is this one). The renderer addresses the secret by its
// row handle; the reference never leaves the backend. An unknown row or an
// unused secret answers an empty profile list.
func (s *WSServer) handleSecretUsageMethod(wconn *wsConn, req jsonrpcRequest) {
	var params struct {
		Row string `json:"row"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Row == "" {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: row required"))
		return
	}
	if s.vaultLifecycle == nil || s.profiles == nil || s.groups == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "secrets.usage not available"))
		return
	}

	profiles, err := s.profiles.LoadProfiles()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
		return
	}
	groups, err := s.groups.LoadGroups()
	if err != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, err.Error()))
		return
	}

	ref, ok := s.vaultLifecycle.ResolveRow(params.Row, s.vaultInventoryInputs(profiles, groups))
	empty := struct {
		Profiles []profile.ProfileRef `json:"profiles"`
	}{Profiles: []profile.ProfileRef{}}
	if !ok {
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(empty)))
		return
	}

	usage := profile.ComputeSecretUsage(profiles, groups, profile.SparseSSHOptions{})
	for _, u := range usage {
		if u.SecretID == string(ref) {
			_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(struct {
				Profiles []profile.ProfileRef `json:"profiles"`
			}{Profiles: u.Profiles})))
			return
		}
	}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(empty)))
}

// ---------------------------------------------------------------------------
// Secret minting (ADR-0017 §1, zqce.3)
// ---------------------------------------------------------------------------
//
// These methods mint a secret into the vault and hand the editor the row
// handle it names on the profile's options. They replace the credential-
// keyed save methods: nothing here touches a credential record, because
// there is no credential record.

type secretMintResult struct {
	Row string `json:"row"`
}

func (s *WSServer) handleSecretMintMethod(wconn *wsConn, req jsonrpcRequest) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.configErr != nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32603, "Configuration recovery is required; restart nocx"))
		return
	}

	switch req.Method {
	case "secrets.savePassword":
		var params struct {
			Password string `json:"password"`
			Name     string `json:"name,omitempty"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Password == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: password required"))
			return
		}
		id, err := s.createSecret(context.Background(), credential.NewSecret(params.Password),
			vault.SecretMeta{Name: params.Name, Kind: vault.KindPassword})
		if err != nil {
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "store password: ", err))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(secretMintResult{Row: vault.RowFor(id)})))

	case "secrets.saveKeyMaterial":
		var params struct {
			KeyText string `json:"keyText"`
			Name    string `json:"name,omitempty"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.KeyText == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: keyText required"))
			return
		}
		fingerprint, passphraseWanted, err := parsePrivateKeyMaterial(params.KeyText)
		if err != nil {
			var invalidKey *errInvalidKeyMaterial
			if errors.As(err, &invalidKey) {
				_ = wconn.writeJSON(jsonrpcResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &jsonrpcErrorObj{
						Code:    -32603,
						Message: err.Error(),
						Data:    &vaultErrorData{Reason: "invalid-key"},
					},
				})
				return
			}
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "store key material: ", err))
			return
		}
		id, err := s.createSecret(context.Background(), credential.NewSecret(params.KeyText),
			vault.SecretMeta{Name: params.Name, Kind: vault.KindPrivateKey})
		if err != nil {
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "store key material: ", err))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(struct {
			secretMintResult
			Fingerprint      string `json:"fingerprint"`
			PassphraseWanted bool   `json:"passphraseWanted"`
		}{
			secretMintResult: secretMintResult{Row: vault.RowFor(id)},
			Fingerprint:      fingerprint,
			PassphraseWanted: passphraseWanted,
		})))

	case "secrets.saveKeyPassphrase":
		var params struct {
			KeyRow     string `json:"keyRow"`
			Passphrase string `json:"passphrase"`
			Name       string `json:"name,omitempty"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.KeyRow == "" {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, "Invalid params: keyRow required"))
			return
		}
		keyRef, err := s.rowToSecretRef(params.KeyRow)
		if err != nil {
			_ = wconn.writeJSON(newJSONRPCError(req.ID, -32602, err.Error()))
			return
		}
		if verr := s.verifyPassphraseAgainst(credential.SecretID(keyRef), []byte(params.Passphrase)); verr != nil {
			var invalidPass *errInvalidKeyPassphrase
			if errors.As(verr, &invalidPass) {
				_ = wconn.writeJSON(jsonrpcResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &jsonrpcErrorObj{
						Code:    -32603,
						Message: verr.Error(),
						Data:    &vaultErrorData{Reason: "invalid-key-passphrase"},
					},
				})
				return
			}
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "store passphrase: ", verr))
			return
		}
		id, err := s.createSecret(context.Background(), credential.NewSecret(params.Passphrase),
			vault.SecretMeta{Name: params.Name, Kind: vault.KindKeyPassphrase})
		if err != nil {
			_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, "store passphrase: ", err))
			return
		}
		_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(secretMintResult{Row: vault.RowFor(id)})))
	}
}

// verifyPassphraseAgainst answers whether the passphrase opens the stored key
// material behind the reference. Refuses when it does not (nocx-dze3).
func (s *WSServer) verifyPassphraseAgainst(keyRef credential.SecretID, passphrase []byte) error {
	if s.credentials == nil {
		return &errInvalidKeyPassphrase{msg: "secret store not available"}
	}
	if keyRef == "" {
		return &errInvalidKeyPassphrase{msg: "no stored key to verify against"}
	}
	secret, err := s.credentials.Get(context.Background(), keyRef)
	if err != nil {
		return fmt.Errorf("load key material: %w", err)
	}
	if secret.IsEmpty() {
		return &errInvalidKeyPassphrase{msg: "stored key material is empty"}
	}
	var opens bool
	if err := secret.Use(func(keyBytes []byte) error {
		_, parseErr := gossh.ParsePrivateKeyWithPassphrase(keyBytes, passphrase)
		opens = parseErr == nil
		return nil
	}); err != nil {
		return fmt.Errorf("read key material: %w", err)
	}
	if !opens {
		return &errInvalidKeyPassphrase{msg: "that passphrase does not open this key"}
	}
	return nil
}
