package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Production argon2id parameters as package-level variables so a test can
// lower them without weakening the build (no env var, no build tag). They are
// read at every call site, never from a constant inside the KDF path — that
// is what lets the Envelope carry its own params and what lets a future
// version raise the cost without invalidating stored envelopes.
var (
	argon2Memory  uint32 = 64 * 1024 // KiB → 64 MiB
	argon2Time    uint32 = 3
	argon2Threads uint8  = 4
)

const (
	argon2SaltLen  = 16
	argon2KeyLen   = 32
	aesNonceLen    = 12
	crockfordGroup = 4
)

// crockfordAlphabet is Crockford base32, excluding I/L/O/U to eliminate
// visual ambiguity (0/O, 1/I/l). Lowercase for readability.
const crockfordAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// Envelope wraps the root key with an argon2id-derived KEK. Every parameter
// is stored alongside the ciphertext so raising the KDF cost later does not
// invalidate what is already persisted (§5.1).
type Envelope struct {
	Salt       []byte
	Ciphertext []byte // nonce (12) || ciphertext || GCM tag
	Memory     uint32 // KiB
	Time       uint32
	Threads    uint8
}

// newRootKey returns a fresh 32-byte vault root key.
func newRootKey() ([]byte, error) {
	k := make([]byte, argon2KeyLen)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("new root key: %w", err)
	}
	return k, nil
}

// wrapWithPassphrase derives a KEK from pass via argon2id using the current
// package-level cost parameters, then encrypts root with AES-256-GCM. The
// KDF parameters are bound as AAD so a stored-envelope tamper that alters
// Memory/Time/Threads makes decryption fail.
func wrapWithPassphrase(root []byte, pass string) (Envelope, error) {
	mem, tm, thr := argon2Memory, argon2Time, argon2Threads

	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return Envelope{}, fmt.Errorf("wrap: %w", err)
	}

	kek := argon2.IDKey([]byte(pass), salt, tm, mem, thr, argon2KeyLen)
	aad := buildAAD(salt, mem, tm, thr)

	block, err := aes.NewCipher(kek)
	if err != nil {
		return Envelope{}, fmt.Errorf("wrap: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, fmt.Errorf("wrap: %w", err)
	}

	nonce := make([]byte, aesNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, fmt.Errorf("wrap: %w", err)
	}

	ct := gcm.Seal(nil, nonce, root, aad)

	return Envelope{
		Salt:       salt,
		Ciphertext: append(nonce, ct...),
		Memory:     mem,
		Time:       tm,
		Threads:    thr,
	}, nil
}

// unwrapWithPassphrase derives a KEK from pass using the parameters stored in
// e (not from package variables), then decrypts. A wrong passphrase, a
// tampered envelope, or any invalid parameter value all produce
// ErrUnsealFailed — they are deliberately indistinguishable (spec §5.1
// consequence 3).
func unwrapWithPassphrase(e Envelope, pass string) ([]byte, error) {
	if err := validateEnvelope(e); err != nil {
		return nil, err
	}

	kek := argon2.IDKey([]byte(pass), e.Salt, e.Time, e.Memory, e.Threads, argon2KeyLen)
	aad := buildAAD(e.Salt, e.Memory, e.Time, e.Threads)

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, ErrUnsealFailed
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnsealFailed
	}

	nonce := e.Ciphertext[:aesNonceLen]
	ct := e.Ciphertext[aesNonceLen:]

	plaintext, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrUnsealFailed
	}
	return plaintext, nil
}

// newRecoveryCode generates a fresh recovery code, wraps a freshly minted
// root key with it, and returns both. The plaintext code is returned exactly
// once and is never stored — only the Envelope persists.
func newRecoveryCode() (code string, e Envelope, err error) {
	var raw [16]byte // 128 bits
	if _, rerr := rand.Read(raw[:]); rerr != nil {
		return "", Envelope{}, fmt.Errorf("new recovery code: %w", rerr)
	}
	code = crockfordEncode(raw[:])

	root, err := newRootKey()
	if err != nil {
		return "", Envelope{}, err
	}

	e, err = wrapWithPassphrase(root, code)
	if err != nil {
		return "", Envelope{}, err
	}
	return code, e, nil
}

// --- helpers ---

// buildAAD encodes the KDF parameters as additional authenticated data so a
// stored-envelope modification is detected by the GCM tag check.
func buildAAD(salt []byte, mem, tm uint32, thr uint8) []byte {
	n := len(salt) + 4 + 4 + 1
	aad := make([]byte, 0, n)
	aad = append(aad, salt...)
	aad = binary.LittleEndian.AppendUint32(aad, mem)
	aad = binary.LittleEndian.AppendUint32(aad, tm)
	aad = append(aad, thr)
	return aad
}

// validateEnvelope returns ErrUnsealFailed if any parameter is missing or
// degenerate, before the KDF or AES machinery runs. This prevents a
// malformed persisted envelope from consuming unbounded resources or
// panicking in the GCM library.
func validateEnvelope(e Envelope) error {
	switch {
	case len(e.Salt) < argon2SaltLen:
		return ErrUnsealFailed
	case e.Memory < 1:
		return ErrUnsealFailed
	case e.Time < 1:
		return ErrUnsealFailed
	case e.Threads < 1:
		return ErrUnsealFailed
	case len(e.Ciphertext) <= aesNonceLen:
		return ErrUnsealFailed
	}
	return nil
}

// crockfordEncode encodes src as Crockford base32 with hyphens every
// crockfordGroup characters for human transcription.
func crockfordEncode(src []byte) string {
	var buf [26]byte // 16 bytes → 26 Crockford chars (128 bits, ceil(128/5)=26)
	bits := 0
	acc := uint(0)
	pos := 0

	for _, b := range src {
		acc = (acc << 8) | uint(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			buf[pos] = crockfordAlphabet[(acc>>bits)&0x1F]
			pos++
		}
	}
	if bits > 0 {
		buf[pos] = crockfordAlphabet[(acc<<(5-bits))&0x1F]
		pos++
	}

	// Insert hyphen every crockfordGroup chars
	var sb strings.Builder
	sb.Grow(pos + (pos-1)/crockfordGroup)
	for i := range pos {
		if i > 0 && i%crockfordGroup == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(buf[i])
	}
	return sb.String()
}
