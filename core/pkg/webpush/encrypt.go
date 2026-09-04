package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// DefaultPadTo is the fixed plaintext record size (payload + delimiter +
// zero padding) every message is padded to unless Options.PadTo overrides it.
// A constant record size means a constant ciphertext size, so the push
// service learns nothing about content from length —
// docs/mobile-notifications-arc42.md §8.2's "ciphertext padded to a fixed
// 2 KiB". 2048 leaves ample headroom under the 4096-byte payload cap
// RFC 8030 §7.2 lets services impose, while fitting every structured
// notification payload the relay composes (ids, labels, states — never
// prose).
const DefaultPadTo = 2048

const (
	// recordSize is the rs field of the aes128gcm content-coding header
	// (RFC 8188 §2.1): the maximum size of one encrypted record, tag
	// included. Everything this package sends is a single record, so rs
	// only has to be large enough — 4096 matches RFC 8291's own example
	// and the ceiling push services enforce anyway.
	recordSize = 4096

	saltLen       = 16 // RFC 8188 §2.1: the header's salt field
	authSecretLen = 16 // RFC 8291 §2: the subscription's auth secret
	gcmTagLen     = 16 // AES-128-GCM authentication tag
	pointLen      = 65 // uncompressed P-256 point: 0x04 || X || Y

	// headerLen is the aes128gcm header this package emits:
	// salt(16) || rs(4) || idlen(1) || keyid(65), with keyid carrying the
	// application server's ephemeral public key as RFC 8291 §4 requires.
	headerLen = saltLen + 4 + 1 + pointLen
)

// encrypt seals payload to the subscription's keys per RFC 8291 and returns
// the complete aes128gcm body: header (salt, rs, keyid = the ephemeral
// public key) followed by one sealed record of exactly padTo plaintext
// bytes. It is deterministic given its inputs — the caller supplies the
// ephemeral key and salt — which is what lets the RFC 8291 Appendix A
// known-answer test pin the whole pipeline byte for byte. Send wraps it
// with fresh randomness per message.
func encrypt(ephemeral *ecdh.PrivateKey, salt []byte, uaPublic, authSecret, payload []byte, padTo int) ([]byte, error) {
	if len(salt) != saltLen {
		return nil, fmt.Errorf("webpush: salt must be %d bytes, got %d", saltLen, len(salt))
	}
	if len(authSecret) != authSecretLen {
		return nil, fmt.Errorf("webpush: auth secret must be %d bytes, got %d", authSecretLen, len(authSecret))
	}
	uaKey, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh is not a valid uncompressed P-256 point: %w", err)
	}
	if padTo < 1 {
		return nil, fmt.Errorf("webpush: pad size must be at least 1 (the record delimiter), got %d", padTo)
	}
	// A single record, tag included, may not exceed the header's rs field
	// (RFC 8188 §2). Larger payloads would need multiple records, which
	// nothing in Web Push uses — push services cap the whole body at 4096.
	if padTo > recordSize-gcmTagLen {
		return nil, fmt.Errorf("webpush: pad size %d exceeds the %d-byte record (rs=%d minus the GCM tag)", padTo, recordSize-gcmTagLen, recordSize)
	}
	// One byte of the record is the delimiter, so padTo-1 is the hard
	// payload ceiling. The record must not grow to fit: a size-dependent
	// record is the leak DefaultPadTo exists to close.
	if len(payload) > padTo-1 {
		return nil, fmt.Errorf("webpush: %d-byte payload does not fit a %d-byte record (max %d): %w", len(payload), padTo, padTo-1, ErrPayloadTooLarge)
	}

	secret, err := ephemeral.ECDH(uaKey)
	if err != nil {
		return nil, fmt.Errorf("webpush: ECDH: %w", err)
	}
	asPublic := ephemeral.PublicKey().Bytes()

	// RFC 8291 §3.3–3.4: IKM = HKDF(salt=auth_secret, ikm=ecdh_secret,
	// info="WebPush: info"||0x00||ua_public||as_public). Binding both
	// public keys into the info is what ties the derived keys to this
	// exact key agreement.
	keyInfo := "WebPush: info\x00" + string(uaPublic) + string(asPublic)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, keyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("webpush: HKDF ikm: %w", err)
	}
	// RFC 8188 §2.2–2.3: content encryption key and nonce, derived from
	// the IKM with the header's salt. The info strings name the aes128gcm
	// coding — its predecessor "aesgcm" derived different keys, which is
	// how the two schemes stay incompatible instead of subtly wrong.
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("webpush: HKDF cek: %w", err)
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("webpush: HKDF nonce: %w", err)
	}

	// RFC 8188 §2: the record is payload, then the delimiter, then zero
	// padding out to exactly padTo. 0x02 marks the last (here: only)
	// record; padding sits after the delimiter, never before it.
	record := make([]byte, padTo)
	copy(record, payload)
	record[len(payload)] = 0x02

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("webpush: AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webpush: GCM: %w", err)
	}

	body := make([]byte, 0, headerLen+padTo+gcmTagLen)
	body = append(body, salt...)
	body = binary.BigEndian.AppendUint32(body, recordSize)
	body = append(body, byte(len(asPublic)))
	body = append(body, asPublic...)
	return gcm.Seal(body, nonce, record, nil), nil
}
