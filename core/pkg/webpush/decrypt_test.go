package webpush_test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// decryptBody is the device side of RFC 8291, implemented independently in
// the test package: parse the aes128gcm header, ECDH from the user agent's
// private key against the header's keyid, re-derive IKM/CEK/NONCE, open the
// record, and strip delimiter plus padding. It rejects a wrong delimiter and
// non-zero padding the way a real service worker's decryptor would, which is
// what lets the round-trip property test detect a sender that pads in the
// wrong place or marks the record with the wrong delimiter.
func decryptBody(uaPriv *ecdh.PrivateKey, authSecret, body []byte) ([]byte, error) {
	const headerLen = 16 + 4 + 1 + 65
	if len(body) < headerLen+16 {
		return nil, fmt.Errorf("body too short: %d bytes", len(body))
	}
	salt := body[:16]
	if rs := binary.BigEndian.Uint32(body[16:20]); rs != 4096 {
		return nil, fmt.Errorf("rs = %d, want 4096", rs)
	}
	if idlen := body[20]; idlen != 65 {
		return nil, fmt.Errorf("keyid length = %d, want 65", idlen)
	}
	asPublic := body[21:headerLen]
	asKey, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, fmt.Errorf("keyid is not a valid P-256 point: %w", err)
	}
	secret, err := uaPriv.ECDH(asKey)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(uaPriv.PublicKey().Bytes()) + string(asPublic)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record, err := gcm.Open(nil, nonce, body[headerLen:], nil)
	if err != nil {
		return nil, fmt.Errorf("GCM open: %w", err)
	}
	// RFC 8188 §2: strip the zero padding, then the 0x02 last-record
	// delimiter. Any non-zero byte in the padding position shows up here
	// as a wrong delimiter and is rejected, exactly like a delimiter that
	// is not 0x02 at all.
	i := len(record) - 1
	for i >= 0 && record[i] == 0 {
		i--
	}
	if i < 0 {
		return nil, errors.New("record has no delimiter (all zeros)")
	}
	if record[i] != 0x02 {
		return nil, fmt.Errorf("last non-padding byte is 0x%02x, want the 0x02 last-record delimiter", record[i])
	}
	return record[:i], nil
}

// mustB64 decodes the base64url (unpadded or padded) constants tests carry.
func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		t.Fatalf("bad base64url constant %q: %v", s, err)
	}
	return b
}

// randomECDHKey derives a P-256 key deterministically from the seeded rng —
// unlike ecdh.GenerateKey, whose consumption of the reader is not guaranteed
// to be a pure function of it, drawing scalar bytes ourselves keeps a failing
// property-test iteration reproducible from its seed alone.
func randomECDHKey(t *testing.T, rng *rand.Rand) *ecdh.PrivateKey {
	t.Helper()
	for {
		b := make([]byte, 32)
		rng.Read(b)
		key, err := ecdh.P256().NewPrivateKey(b)
		if err == nil {
			return key
		}
		// Out-of-range scalar (probability ~2^-32): redraw.
	}
}
