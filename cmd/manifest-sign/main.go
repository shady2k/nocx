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
//
// In sign mode the seed is read from the RELEASE_SIGNING_KEY environment
// variable (never a flag, so it stays out of process listings and CI logs).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
)

const seedEnv = "RELEASE_SIGNING_KEY"

func main() {
	keygen := flag.Bool("keygen", false, "generate a new ed25519 keypair and print the seed and public key")
	in := flag.String("in", "", "path to the manifest to sign")
	out := flag.String("out", "", "path to write the base64 signature to (default stdout)")
	flag.Parse()

	if err := run(*keygen, *in, *out); err != nil {
		fmt.Fprintln(os.Stderr, "manifest-sign:", err)
		os.Exit(1)
	}
}

func run(keygen bool, in, out string) error {
	if keygen {
		return generate()
	}
	if in == "" {
		return fmt.Errorf("-in is required in sign mode")
	}
	return sign(in, out)
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

	msg, err := os.ReadFile(in) //nolint:gosec // in is an explicit CLI argument: the manifest path
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)) + "\n"

	if out == "" {
		fmt.Print(sig)
		return nil
	}
	if err := os.WriteFile(out, []byte(sig), 0o600); err != nil {
		return fmt.Errorf("write signature: %w", err)
	}
	return nil
}
