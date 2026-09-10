// Command manifest-sign is release tooling: it produces the detached ed25519
// signature that ships next to the update manifest (distribution design §5, §6).
//
// The signature format is deliberately raw so the compiled-in keyring can verify
// it with nothing but crypto/ed25519 (§6): a 64-byte ed25519 signature over the
// exact bytes of manifest.json, base64-encoded (standard encoding, one trailing
// newline that the verifier trims). Keys are raw too — the private key is the
// base64 of the 32-byte ed25519 seed, and -keygen prints both halves so the
// maintainer can set the RELEASE_SIGNING_KEY secret and paste the public key
// into the keyring.
//
// Usage:
//
//	manifest-sign -keygen                       # print a fresh seed + public key
//	manifest-sign -in manifest.json -out manifest.json.sig
//	manifest-sign -verify -in manifest.json -sig manifest.json.sig
//
// In sign mode the seed is read from the RELEASE_SIGNING_KEY environment
// variable (never a flag, so it stays out of process listings and CI logs).
//
// Verify mode takes no key: it uses the keyring compiled into this binary, so
// running it in the release workflow answers the only question signing cannot —
// whether the secret CI signed with and the public key the artefact ships with
// are the same pair (nocx-1d54).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/shady2k/nocx/internal/update"
)

const seedEnv = "RELEASE_SIGNING_KEY"

func main() {
	keygen := flag.Bool("keygen", false, "generate a new ed25519 keypair and print the seed and public key")
	verifyMode := flag.Bool("verify", false, "verify -sig against -in using the compiled-in release keyring")
	in := flag.String("in", "", "path to the manifest to sign or verify")
	out := flag.String("out", "", "path to write the base64 signature to (default stdout)")
	sig := flag.String("sig", "", "path to the detached signature to verify")
	flag.Parse()

	if err := run(*keygen, *verifyMode, *in, *out, *sig); err != nil {
		fmt.Fprintln(os.Stderr, "manifest-sign:", err)
		os.Exit(1)
	}
}

func run(keygen, verifyMode bool, in, out, sig string) error {
	if keygen {
		return generate()
	}
	if verifyMode {
		if in == "" || sig == "" {
			return fmt.Errorf("-in and -sig are both required in verify mode")
		}
		keyring, err := update.ReleaseKeyring()
		if err != nil {
			return fmt.Errorf("compiled-in release keyring: %w", err)
		}
		return verify(in, sig, keyring)
	}
	if in == "" {
		return fmt.Errorf("-in is required in sign mode")
	}
	return sign(in, out)
}

// verify checks a detached signature against the keyring the shipped binary
// carries, and is the only thing that proves the two halves of the release
// signing arrangement are a pair.
//
// Signing proves the CI secret is present and well-formed. It says nothing
// about whether anybody can check the result: if RELEASE_SIGNING_KEY and
// internal/update/keyring.go drift apart, every artefact still signs, still
// publishes, and every user's update check then fails against a manifest no
// key in their build validates. Both sides are individually green and the
// release is dead — so the workflow runs this between signing and publishing,
// against the keyring compiled from the same tree it is building (nocx-1d54).
func verify(in, sigPath string, keyring []ed25519.PublicKey) error {
	body, err := os.ReadFile(in) // #nosec -- in is an explicit CLI argument: the manifest path
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	raw, err := os.ReadFile(sigPath) // #nosec -- sigPath is an explicit CLI argument
	if err != nil {
		return fmt.Errorf("read signature: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	if len(keyring) == 0 {
		return fmt.Errorf("the compiled-in release keyring is empty: " +
			"this build can verify no update at all, whatever it publishes")
	}
	for _, key := range keyring {
		if ed25519.Verify(key, body, sig) {
			return nil
		}
	}
	return fmt.Errorf("no key in the compiled-in keyring validates this signature: "+
		"%s and internal/update/keyring.go are not a pair — "+
		"either the secret was rotated without adding its public key, or the key was added without rotating the secret",
		seedEnv)
}

// generate prints a fresh keypair. The seed goes into the RELEASE_SIGNING_KEY
// secret; the public key goes into the compiled keyring.
func generate() error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	enc := base64.StdEncoding
	fmt.Printf("seed (RELEASE_SIGNING_KEY secret): %s\n", enc.EncodeToString(priv.Seed()))
	fmt.Printf("public key (add to keyring):       %s\n", enc.EncodeToString(pub))
	return nil
}

// sign signs the manifest bytes with the seed from the environment and writes a
// base64 detached signature.
func sign(in, out string) error {
	raw := strings.TrimSpace(os.Getenv(seedEnv))
	if raw == "" {
		return fmt.Errorf("%s is empty; export the base64 seed before signing", seedEnv)
	}
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("decode %s: %w", seedEnv, err)
	}
	if len(seed) != ed25519.SeedSize {
		return fmt.Errorf("%s decodes to %d bytes, want %d", seedEnv, len(seed), ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	msg, err := os.ReadFile(in) // #nosec -- in is an explicit CLI argument: the manifest path
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)) + "\n"

	if out == "" {
		fmt.Print(sig)
		return nil
	}
	if err := os.WriteFile(out, []byte(sig), 0o600); err != nil { // #nosec G703 -- path is an explicit CLI input or validated repository path
		return fmt.Errorf("write signature: %w", err)
	}
	return nil
}
