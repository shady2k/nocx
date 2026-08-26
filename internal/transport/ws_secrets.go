package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/transport/control"
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
//	row → reference   the capability services (SecretService.ResolveRow,
//	                  ConfigService) — the transport no longer resolves rows
//	                  (migration map: the resolution lives in the services)
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

// optionsToWire replaces every stored secret reference in a profile's
// options with its row handle. Used on every read path that marshals a
// profile to the renderer.
func optionsToWire(o profile.StoredSSHProfileOptions) profile.StoredSSHProfileOptions {
	o.PasswordSecret = secretRefToRow(o.PasswordSecret)
	o.KeySecret = secretRefToRow(o.KeySecret)
	o.KeyPassphraseSecret = secretRefToRow(o.KeyPassphraseSecret)
	return o
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

// ---------------------------------------------------------------------------
// The secrets.* handlers (migration map, "secrets.*")
// ---------------------------------------------------------------------------

// secretsHandlers answers the vault-secret methods: secrets.usage and the
// three mint methods (ADR-0017 §1, zqce.3). It holds the SecretOperation
// (config, vault gates — built once in secretSpecs) and the Responder; every
// store access goes through the operation's service. The wired flags
// reproduce the old handlers' answers on the unwired paths: secrets.usage
// reported "not available" without the vault or the profile/group stores,
// secrets.saveKeyPassphrase refused a row without a vault, and the other mint
// methods fell back to the plain store exactly like the old createSecret
// (MintSecret mirrors it in the service).
type secretsHandlers struct {
	op          capability.SecretOperation
	r           Responder
	vaultWired  bool // vaultLifecycle != nil at construction
	configWired bool // profiles != nil && groups != nil at construction
	// secrets is the stanced material seam, held by the HANDLER rather
	// than reached through the operation's service: an operation-stance
	// read blocks on the unlock, and no admission may be held across that
	// wait (nocx-o3606). nil when no store is wired.
	secrets credential.Resolver
}

// secretsUsageParams is the wire format for secrets.usage: one row handle.
type secretsUsageParams struct {
	Row string `json:"row"`
}

// secretsSavePasswordParams is the wire format for secrets.savePassword.
type secretsSavePasswordParams struct {
	Password string `json:"password"`
	Name     string `json:"name,omitempty"`
}

// secretsSaveKeyMaterialParams is the wire format for secrets.saveKeyMaterial.
type secretsSaveKeyMaterialParams struct {
	KeyText string `json:"keyText"`
	Name    string `json:"name,omitempty"`
}

// secretsSaveKeyPassphraseParams is the wire format for secrets.saveKeyPassphrase.
type secretsSaveKeyPassphraseParams struct {
	KeyRow     string `json:"keyRow"`
	Passphrase string `json:"passphrase"`
	Name       string `json:"name,omitempty"`
}

// handleUsage answers secrets.usage: for one vault row, the profiles that use
// the secret behind it (ADR-0017: the count is the number of profiles whose
// effective secret is this one). The renderer addresses the secret by its row
// handle; the reference never leaves the backend. An unknown row or an unused
// secret answers an empty profile list.
func (h secretsHandlers) handleUsage(ctx context.Context, req jsonrpcRequest) {
	var params secretsUsageParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Row == "" {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: row required"})
		return
	}
	if !h.vaultWired || !h.configWired {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "secrets.usage not available"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.SecretService) error {
		profiles, err := svc.Usage(ctx, params.Row)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		if profiles == nil {
			profiles = []profile.ProfileRef{}
		}
		_ = h.r.TryResult(req.ID, mustMarshal(struct {
			Profiles []profile.ProfileRef `json:"profiles"`
		}{Profiles: profiles}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// secretMintResult is the result of the password and passphrase mint
// methods: the row handle the editor names on the profile's options.
type secretMintResult struct {
	Row string `json:"row"`
}

// handleMint answers the three secrets.save* methods. Key and passphrase
// PARSING stays here (pure); only the store access goes through the service
// — MintSecret for the create, ResolveRow and GetSecret for the passphrase's
// verify-read.
func (h secretsHandlers) handleMint(ctx context.Context, req jsonrpcRequest) {
	switch req.Method {
	case "secrets.savePassword":
		var params secretsSavePasswordParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Password == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: password required"})
			return
		}
		err := h.op.Run(ctx, func(ctx context.Context, svc capability.SecretService) error {
			id, err := svc.MintSecret(ctx, credential.NewSecret(params.Password),
				vault.SecretMeta{Name: params.Name, Kind: vault.KindPassword})
			if err != nil {
				_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "store password: ", err))
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(secretMintResult{Row: vault.RowFor(id)}))
			return nil
		})
		if err != nil {
			answerOperationRefusal(h.r, req, err)
		}

	case "secrets.saveKeyMaterial":
		var params secretsSaveKeyMaterialParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.KeyText == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: keyText required"})
			return
		}
		fingerprint, passphraseWanted, parseErr := parsePrivateKeyMaterial(params.KeyText)
		if parseErr != nil {
			var invalidKey *errInvalidKeyMaterial
			if errors.As(parseErr, &invalidKey) {
				_ = h.r.TryError(req.ID, RPCError{
					Code:    -32603,
					Message: parseErr.Error(),
					Data:    &vaultErrorData{Reason: "invalid-key"},
				})
				return
			}
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "store key material: ", parseErr))
			return
		}
		runErr := h.op.Run(ctx, func(ctx context.Context, svc capability.SecretService) error {
			id, err := svc.MintSecret(ctx, credential.NewSecret(params.KeyText),
				vault.SecretMeta{Name: params.Name, Kind: vault.KindPrivateKey})
			if err != nil {
				_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "store key material: ", err))
				return nil
			}
			_ = h.r.TryResult(req.ID, mustMarshal(struct {
				secretMintResult
				Fingerprint      string `json:"fingerprint"`
				PassphraseWanted bool   `json:"passphraseWanted"`
			}{
				secretMintResult: secretMintResult{Row: vault.RowFor(id)},
				Fingerprint:      fingerprint,
				PassphraseWanted: passphraseWanted,
			}))
			return nil
		})
		if runErr != nil {
			answerOperationRefusal(h.r, req, runErr)
		}

	case "secrets.saveKeyPassphrase":
		var params secretsSaveKeyPassphraseParams
		if err := json.Unmarshal(req.Params, &params); err != nil || params.KeyRow == "" {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: keyRow required"})
			return
		}
		if !h.vaultWired {
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "no vault: cannot resolve a secret row"})
			return
		}
		h.handleSaveKeyPassphrase(ctx, req, params)
	}
}

// handleSaveKeyPassphrase stores a key passphrase once it has been verified
// against the key material it is supposed to open (nocx-dze3).
//
// IT IS THREE PHASES, AND THE MIDDLE ONE HOLDS NO GATE. Reading the key
// material is an operation-stance read: a sealed vault raises the unlock and
// the read WAITS for the person to answer it. The answer is vault.unseal,
// which runs under the vault gate — so a read done inside this operation
// holds the gate its own prompt needs, and the dialog comes back "Control
// plane busy" no matter what the person types (nocx-o3606). The row lookup
// and the mint each take the operation for as long as they need it; the wait
// happens between them, holding nothing.
//
// It is the same split endpoints.probe has, and the same reason PHASE TWO of
// the open dials outside its domain gate.
func (h secretsHandlers) handleSaveKeyPassphrase(ctx context.Context, req jsonrpcRequest, params secretsSaveKeyPassphraseParams) {
	var keyID credential.SecretID
	answered := false
	err := h.op.Run(ctx, func(_ context.Context, svc capability.SecretService) error {
		id, ok := svc.ResolveRow(params.KeyRow)
		switch {
		case !ok:
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: fmt.Sprintf("unknown secret row %q", params.KeyRow)})
			answered = true
		case id == "":
			_ = h.r.TryError(req.ID, RPCError{
				Code:    -32603,
				Message: "no stored key to verify against",
				Data:    &vaultErrorData{Reason: "invalid-key-passphrase"},
			})
			answered = true
		default:
			keyID = id
		}
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
		return
	}
	if answered {
		return
	}
	if h.secrets == nil {
		_ = h.r.TryError(req.ID, RPCError{
			Code:    -32603,
			Message: "secret store not available",
			Data:    &vaultErrorData{Reason: "invalid-key-passphrase"},
		})
		return
	}

	// No gate is held here. A sealed vault raises one coalesced unlock and
	// this read continues when it is answered; a dismissed one comes back as
	// the cancellation, not as a failure to find the key.
	secret, err := h.secrets.Resolve(ctx, keyID, credential.Operation("verify the key passphrase"))
	if err != nil {
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "store passphrase: ", fmt.Errorf("load key material: %w", err)))
		return
	}
	if verr := verifyPassphraseSecret(secret, []byte(params.Passphrase)); verr != nil {
		var invalidPass *errInvalidKeyPassphrase
		if errors.As(verr, &invalidPass) {
			_ = h.r.TryError(req.ID, RPCError{
				Code:    -32603,
				Message: verr.Error(),
				Data:    &vaultErrorData{Reason: "invalid-key-passphrase"},
			})
			return
		}
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "store passphrase: ", verr))
		return
	}

	err = h.op.Run(ctx, func(ctx context.Context, svc capability.SecretService) error {
		id, mintErr := svc.MintSecret(ctx, credential.NewSecret(params.Passphrase),
			vault.SecretMeta{Name: params.Name, Kind: vault.KindKeyPassphrase})
		if mintErr != nil {
			_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "store passphrase: ", mintErr))
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(secretMintResult{Row: vault.RowFor(id)}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// verifyPassphraseSecret answers whether the passphrase opens the stored key
// material behind the secret. Refuses when it does not (nocx-dze3). The
// store read happened before (GetSecret); this is the pure parse half and
// stays in the handler.
func verifyPassphraseSecret(secret credential.Secret, passphrase []byte) error {
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

// ── per-method validators ──────────────────────────────────────────────

// validateSecretsUsageRaw: the row is a renderer-addressable handle — a
// SecretID is never an identifier (nocx-jb20.1) — so it must have the
// secrow: grammar, not merely be non-empty.
func validateSecretsUsageRaw(raw json.RawMessage) string {
	var p secretsUsageParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if !isRowHandle(p.Row) {
		return "row must be a secrow handle"
	}
	return ""
}

// validateSecretsSavePasswordRaw: the password is required and bounded; the
// name is optional (absent, the backend derives one per ADR-0016).
func validateSecretsSavePasswordRaw(raw json.RawMessage) string {
	var p secretsSavePasswordParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.Password == "" {
		return "password is required"
	}
	if n := utf8.RuneCountInString(p.Password); n > maxSecretMaterialRunes {
		return fmt.Sprintf("password exceeds %d characters", maxSecretMaterialRunes)
	}
	if p.Name != "" {
		if msg := secretNameProblem(p.Name); msg != "" {
			return msg
		}
	}
	return ""
}

// validateSecretsSaveKeyMaterialRaw: the key text is required and bounded;
// whether it parses as a private key is the handler's check (it shapes the
// invalid-key error the renderer acts on).
func validateSecretsSaveKeyMaterialRaw(raw json.RawMessage) string {
	var p secretsSaveKeyMaterialParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if p.KeyText == "" {
		return "keyText is required"
	}
	if n := utf8.RuneCountInString(p.KeyText); n > maxSecretMaterialRunes {
		return fmt.Sprintf("keyText exceeds %d characters", maxSecretMaterialRunes)
	}
	if p.Name != "" {
		if msg := secretNameProblem(p.Name); msg != "" {
			return msg
		}
	}
	return ""
}

// validateSecretsSaveKeyPassphraseRaw: the key row is a handle; the
// passphrase is bounded and MAY be empty — verifying the empty passphrase
// against an unencrypted key is a legitimate check.
func validateSecretsSaveKeyPassphraseRaw(raw json.RawMessage) string {
	var p secretsSaveKeyPassphraseParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if !isRowHandle(p.KeyRow) {
		return "keyRow must be a secrow handle"
	}
	if n := utf8.RuneCountInString(p.Passphrase); n > maxSecretMaterialRunes {
		return fmt.Sprintf("passphrase exceeds %d characters", maxSecretMaterialRunes)
	}
	if p.Name != "" {
		if msg := secretNameProblem(p.Name); msg != "" {
			return msg
		}
	}
	return ""
}

// validateSecretsDetectRaw: the line is bounded; an empty line is a
// legitimate "nothing to detect" query.
func validateSecretsDetectRaw(raw json.RawMessage) string {
	var p secretsDetectParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return "params must be a JSON object"
		}
	}
	if n := utf8.RuneCountInString(p.Line); n > maxVaultLineRunes {
		return fmt.Sprintf("line exceeds %d characters", maxVaultLineRunes)
	}
	return ""
}

// validateSecretsCaptureSaveRaw: the capture id is the idempotency key and a
// capability token — minted by the registry, so it has the cap_ grammar. The
// optional name is a vault name (it becomes the {{secret:NAME}} reference).
func validateSecretsCaptureSaveRaw(raw json.RawMessage) string {
	var p captureSaveParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if !isCaptureID(p.CaptureID) {
		return "captureId must be a capture handle"
	}
	if p.Name != "" {
		if msg := secretNameProblem(p.Name); msg != "" {
			return msg
		}
	}
	return ""
}

// validateSecretsCaptureDismissRaw: the capture id is a capability token with
// the cap_ grammar.
func validateSecretsCaptureDismissRaw(raw json.RawMessage) string {
	var p captureDismissParams
	if len(raw) == 0 {
		return "params are required"
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return "params must be a JSON object"
	}
	if !isCaptureID(p.CaptureID) {
		return "captureId must be a capture handle"
	}
	return ""
}

// secretSpecs declares the secrets.* control methods: the vault-secret mint
// and usage surface under one SecretOperation ([config, vault] gates), the
// pure detector (no capability), and the capture settlement under the
// CaptureSaveOperation ([vault, content] gates — the capture operation needs
// the content gate, so this builder takes four gates). The operations are
// built once here from the wired stores (composition root for this domain);
// the handlers carry the wired flags that reproduce the old dispatchers'
// unwired answers.
func (s *WSServer) secretSpecs(lane control.Admission, configGate, vaultGate, contentGate control.Admission) []methodSpec {
	secretOp := capability.NewSecretOperation(configGate, vaultGate, lane, s.profiles, s.groups, s.vaultLifecycle, s.credentials)
	secretResolver := s.credentialResolver()
	captureOp := capability.NewCaptureSaveOperation(vaultGate, contentGate, lane, s.vaultLifecycle, s.contentDB)
	vaultWired := s.vaultLifecycle != nil
	configWired := s.profiles != nil && s.groups != nil
	contentWired := s.contentDB != nil
	secretSub := s.operationQueue("secrets")
	// captureSub is ORDERED, not the ordinary bounded queue: captureSave and
	// secrets.paneClosed share it, and their arrival order is load-bearing. A pane's
	// destruction is the same registry operation family as a save, and a
	// save submitted after the pane's close must observe the destruction
	// (nocx-tsajw) — the single FIFO worker guarantees it, where a bounded
	// queue would race the two goroutines.
	captureSub := control.NewOrderedSubmission("capture", s.domainQueueDepth)
	return []methodSpec{
		whenAvailable(regResponder(secretSub, "secrets.usage", params(validateSecretsUsageRaw), func(r Responder) handlerFunc {
			h := secretsHandlers{op: secretOp, r: r, vaultWired: vaultWired, configWired: configWired, secrets: secretResolver}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleUsage(ctx, req) }
		}), func() bool { return vaultWired && configWired }, "secrets.usage not available"),
		whenAvailable(regResponder(secretSub, "secrets.savePassword", params(validateSecretsSavePasswordRaw), func(r Responder) handlerFunc {
			h := secretsHandlers{op: secretOp, r: r, vaultWired: vaultWired, configWired: configWired, secrets: secretResolver}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMint(ctx, req) }
		}), func() bool { return secretOp != nil }, "vault not available"),
		whenAvailable(regResponder(secretSub, "secrets.saveKeyMaterial", params(validateSecretsSaveKeyMaterialRaw), func(r Responder) handlerFunc {
			h := secretsHandlers{op: secretOp, r: r, vaultWired: vaultWired, configWired: configWired, secrets: secretResolver}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMint(ctx, req) }
		}), func() bool { return secretOp != nil }, "vault not available"),
		whenAvailable(regResponder(secretSub, "secrets.saveKeyPassphrase", params(validateSecretsSaveKeyPassphraseRaw), func(r Responder) handlerFunc {
			h := secretsHandlers{op: secretOp, r: r, vaultWired: vaultWired, configWired: configWired, secrets: secretResolver}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleMint(ctx, req) }
		}), func() bool { return secretOp != nil }, "vault not available"),
		regResponder(s.lane, "secrets.detect", params(validateSecretsDetectRaw), func(r Responder) handlerFunc {
			h := secretsDetectHandlers{log: s.log, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleDetect(req) }
		}),
		whenAvailable(regResponder(captureSub, "secrets.captureSave", params(validateSecretsCaptureSaveRaw), func(r Responder) handlerFunc {
			h := captureSaveHandlers{op: captureOp, captures: s.captures, r: r, vaultWired: vaultWired, contentWired: contentWired}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleCaptureSave(ctx, req) }
		}), func() bool { return captureOp != nil }, "vault not available"),
		regResponder(s.lane, "secrets.captureDismiss", params(validateSecretsCaptureDismissRaw), func(r Responder) handlerFunc {
			h := captureDismissHandlers{captures: s.captures, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleCaptureDismiss(ctx, req) }
		}),
		// secrets.paneClosed is the renderer's announcement that a pane died
		// (nocx-tsajw): its pending captures die with it, keyed on
		// (connection, pane). reg rather than regResponder — the handler
		// needs the connection as the destruction key's other half. It
		// shares the ORDERED capture queue with captureSave so a pane's
		// destruction is applied before any later save from the same
		// connection settles: a save in flight when the pane dies is left
		// to settle (capture contract), but a save submitted after the
		// close must see the destruction.
		reg(captureSub, "secrets.paneClosed", params(validatePaneClosedRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := paneClosedHandlers{captures: s.captures, log: s.log}
			return func(ctx context.Context, req jsonrpcRequest) { h.handlePaneClosed(ctx, w, req) }
		}),
	}
}
