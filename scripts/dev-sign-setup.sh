#!/usr/bin/env bash
# Generate a self-signed code signing certificate ("Irrlicht Dev") in the
# login keychain so dev builds keep a stable code identity across rebuilds.
#
# Why: macOS TCC permissions (Accessibility, Automation) key on the app's
# designated requirement, which is derived from the code signature. Ad-hoc
# signing (`codesign --sign -`) produces a fresh hash-based identity every
# time, so every rebuild invalidates granted TCC permissions. A persistent
# self-signed cert keeps the identity stable.
#
# Idempotent: re-running after the identity already exists is a no-op.
# Reversible: `security delete-certificate -c "Irrlicht Dev"` to undo.

set -euo pipefail

IDENTITY="Irrlicht Dev"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"

if security find-identity -v -p codesigning "$KEYCHAIN" 2>/dev/null | grep -q "$IDENTITY"; then
    echo "Identity \"$IDENTITY\" already present in login keychain — nothing to do."
    exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Minimal OpenSSL config with a Code Signing EKU — `codesign` requires this
# extension on the signing cert.
cat > "$TMP/cert.cnf" <<'EOF'
[req]
distinguished_name = dn
prompt = no
[dn]
CN = Irrlicht Dev
[v3]
basicConstraints = critical, CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = codeSigning
EOF

openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -keyout "$TMP/key.pem" \
    -out "$TMP/cert.pem" \
    -config "$TMP/cert.cnf" \
    -extensions v3

# macOS's `security import` only accepts the older PKCS12 MAC/cipher combo.
# OpenSSL 3 changed defaults; force the legacy provider *and* the SHA1 MAC
# with 3DES encryption that the Keychain APIs know how to read. Without
# this import fails with "MAC verification failed".
# A non-empty password is required — empty passwords also trip import.
P12_PASS="irrlicht-dev"
openssl pkcs12 -export -legacy \
    -keypbe PBE-SHA1-3DES \
    -certpbe PBE-SHA1-3DES \
    -macalg SHA1 \
    -inkey "$TMP/key.pem" \
    -in "$TMP/cert.pem" \
    -name "$IDENTITY" \
    -out "$TMP/bundle.p12" \
    -passout pass:"$P12_PASS"

security import "$TMP/bundle.p12" \
    -k "$KEYCHAIN" \
    -P "$P12_PASS" \
    -T /usr/bin/codesign \
    -A

# Trust the cert for code signing in this user's trust settings. macOS 12+
# requires this for codesign to accept the identity even though the private
# key is in the same keychain.
security add-trusted-cert \
    -d -r trustRoot -p codeSign \
    -k "$KEYCHAIN" \
    "$TMP/cert.pem" 2>/dev/null || \
    echo "note: user-level trust setting failed (may prompt for keychain password); \
signing will still work but codesign may warn about unknown trust."

echo "Installed code signing identity \"$IDENTITY\"."
echo ""
echo "Next steps:"
echo "  1. Re-sign installed app:  codesign --force --sign \"$IDENTITY\" --deep /Applications/Irrlicht.app"
echo "  2. Re-grant Accessibility once in System Settings → Privacy & Security."
echo "  3. Future rebuilds using this identity keep the grant intact."
