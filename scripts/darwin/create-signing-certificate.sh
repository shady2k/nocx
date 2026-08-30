#!/usr/bin/env bash
# Generate the project's code-signing certificate (spec D1).
#
# The signature exists to give nocx-server a designated requirement the
# keychain recognises across releases (ADR-0050 step 3) — NOT to satisfy
# Gatekeeper, which needs a Developer ID we do not have (ADR-0003). The
# certificate is therefore self-signed and long-lived: a renewal is an
# IDENTITY CHANGE, so it is a decision, not a chore (spec D8).
#
# Output: base64 of the .p12 on stdout, for the MACOS_SIGNING_P12 secret.
# The private key and the .p12 stay in $OUT_DIR; back them up offline,
# beside RELEASE_SIGNING_KEY, and delete them from the machine afterwards.
set -euo pipefail

OUT_DIR="${1:-./signing}"
DAYS=7300 # 20 years

mkdir -p "$OUT_DIR"
key="$OUT_DIR/nocx-signing.key"
crt="$OUT_DIR/nocx-signing.crt"
p12="$OUT_DIR/nocx-signing.p12"

if [ -e "$key" ]; then
  echo "refusing to overwrite $key — an existing key is an existing identity" >&2
  exit 1
fi

pass="$(openssl rand -base64 24)"

# codeSigning EKU is what makes `security find-identity -p codesigning`
# list it; without it the certificate imports and codesign never sees it.
openssl req -x509 -newkey rsa:4096 -sha256 -days "$DAYS" -nodes \
  -keyout "$key" -out "$crt" \
  -subj "/CN=nocx Project Code Signing/O=nocx" \
  -addext "basicConstraints=critical,CA:FALSE" \
  -addext "keyUsage=critical,digitalSignature" \
  -addext "extendedKeyUsage=critical,codeSigning" >/dev/null 2>&1

openssl pkcs12 -export -inkey "$key" -in "$crt" -out "$p12" \
  -name "nocx Project Code Signing" -passout "pass:$pass"

chmod 600 "$key" "$p12"

echo "certificate: $crt" >&2
echo "private key: $key  (back this up offline; losing it is losing the identity)" >&2
echo "password for MACOS_SIGNING_P12_PASSWORD: $pass" >&2
echo "--- base64 of the .p12 for MACOS_SIGNING_P12 ---" >&2
base64 < "$p12"
