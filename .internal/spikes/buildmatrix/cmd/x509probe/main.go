// Command x509probe exists for one check, and it is not a probe of ghostty.
//
// nocx-sf1: a darwin binary that imports crypto/x509 gets `-framework Security`
// and `-framework CoreFoundation` on its link line, because
// crypto/x509/internal/macos carries those `//go:cgo_ldflag` directives — and it
// carries them whether or not any symbol of either framework survives
// dead-code elimination. On a Linux host both frameworks come from the `.tbd`
// stubs in third_party/libghostty-vt/stubs, and a `.tbd` is a promise made to
// the LINKER: the only place it is cashed is dyld on a Mac.
//
// So this program is the smallest thing that makes Go *reference* the symbols,
// which is what tells a binary that merely links apart from one whose calls
// work:
//
//   - `x509.Certificate.Verify` on darwin calls the platform verifier directly
//     when Roots is nil (crypto/x509/verify.go), so the call reaches
//     SecTrustCreateWithCertificates and the CoreFoundation calls beside it;
//   - the certificate is generated here rather than fetched or embedded, so the
//     check needs no network and no fixture;
//   - it must be PARSED. `Verify` returns errNotParsed for a zero Certificate
//     before it looks at the platform at all — which is how a probe that proves
//     nothing gets written by accident.
//
// Go emits these imports as LAZY binds, so the stub's promise is cashed at the
// first CALL rather than at load. The rejection this program prints is therefore
// the evidence: it is only reachable if the framework calls ran.
//
// Run it on a Mac (`mac-check.sh` check C10) after building it for darwin with
// the stubs on the link line; a dyld "Symbol not found" is the failure this
// check exists to catch. From this directory:
//
//	cc="$(cd ../../.. && go run ./cmd/vtfetch cc --target darwin/arm64 --zig "$ZIG")"
//	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CC="$cc" \
//	  go build -trimpath -ldflags="-s -w -linkmode=external" -o bin/x509-arm64 ./cmd/x509probe
//	./bin/x509-arm64
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"os"
	"time"
)

func main() {
	cert, err := selfSigned()
	if err != nil {
		fmt.Fprintln(os.Stderr, "x509probe: building a certificate:", err)
		os.Exit(1)
	}

	_, err = cert.Verify(x509.VerifyOptions{DNSName: "example.com"})
	if err == nil {
		fmt.Fprintln(os.Stderr, "x509probe: a self-signed certificate verified against the system roots")
		os.Exit(1)
	}
	fmt.Println("verify:", err)
}

// selfSigned returns a parsed certificate, which is the whole requirement: a
// parsed leaf with no Roots is what routes Verify into the platform verifier on
// darwin. Whether the verifier trusts it is the next question, and the answer
// there is expected to be no.
func selfSigned() (*x509.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "nocx cross-link probe"},
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}
