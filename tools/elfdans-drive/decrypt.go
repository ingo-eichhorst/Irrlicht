package main

// Reading a push back: the decode half of core/pkg/webpush's encrypt, which
// is what turns "the relay POSTed 2 KiB of ciphertext" into "the relay
// decided a summary naming four sessions". Nothing in the product reads a
// Web Push body — the phone's browser does that — so this is written against
// the RFCs rather than shared with the writer: a decoder derived from the
// encoder would agree with it about a mistake they both made.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
)

// decryptPush reverses core/pkg/webpush's encrypt: RFC 8188 §2.1's
// aes128gcm header, RFC 8291 §3.4's key derivation, one record, and §2's
// padding delimiter. Every error is a finding rather than a dropped push —
// the caller records it, and a scenario then fails on "a delivered push could
// not be read" instead of on a silence that never happened.
func decryptPush(body []byte, priv *ecdh.PrivateKey, authSecret []byte) ([]byte, error) {
	const headStatic = saltLen + 4 + 1 // salt || rs || idlen
	if len(body) < headStatic {
		return nil, fmt.Errorf("push body is %d bytes, too short for an aes128gcm header", len(body))
	}
	salt := body[:saltLen]
	idLen := int(body[saltLen+4])
	if len(body) < headStatic+idLen {
		return nil, fmt.Errorf("push body is %d bytes, too short for its %d-byte keyid", len(body), idLen)
	}
	asPublic := body[headStatic : headStatic+idLen]
	ciphertext := body[headStatic+idLen:]

	asKey, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, fmt.Errorf("the header's keyid is not a P-256 point: %w", err)
	}
	secret, err := priv.ECDH(asKey)
	if err != nil {
		return nil, fmt.Errorf("ECDH with the sender's ephemeral key: %w", err)
	}

	// RFC 8291 §3.4: both public keys are bound into the info string, which
	// is what ties the derived keys to this exact agreement.
	keyInfo := "WebPush: info\x00" + string(priv.PublicKey().Bytes()) + string(asPublic)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, keyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("HKDF ikm: %w", err)
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("HKDF cek: %w", err)
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("HKDF nonce: %w", err)
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("the record did not authenticate — this push was encrypted to different keys: %w", err)
	}

	// RFC 8188 §2: payload, then the 0x02 delimiter marking the last record,
	// then zero padding out to the fixed record size (§8.2's constant
	// ciphertext length). JSON never ends in a NUL, so trimming them back to
	// the delimiter recovers the payload exactly.
	end := len(record)
	for end > 0 && record[end-1] == 0x00 {
		end--
	}
	if end == 0 || record[end-1] != 0x02 {
		return nil, errors.New("the record carries no 0x02 delimiter — not an RFC 8188 payload")
	}
	return record[:end-1], nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
