package webpush

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// VAPIDKey is the relay's ES256 application-server identity (RFC 8292). The
// browser binds every subscription to the public half at subscribe time, so
// this key is long-lived state, not a per-message artifact: losing it means
// no phone is pushable until its PWA next opens and re-subscribes
// (docs/mobile-notifications-arc42.md §8.6).
type VAPIDKey struct {
	signer *ecdsa.PrivateKey
	public []byte // 65-byte uncompressed P-256 point, derived from signer once
}

// GenerateVAPIDKey mints a fresh P-256 identity.
func GenerateVAPIDKey() (*VAPIDKey, error) {
	signer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: generate VAPID key: %w", err)
	}
	pub, err := signer.PublicKey.ECDH()
	if err != nil {
		return nil, fmt.Errorf("webpush: encode VAPID public key: %w", err)
	}
	return &VAPIDKey{signer: signer, public: pub.Bytes()}, nil
}

// vapidKeyJSON is the vapid-keys.json shape
// (docs/mobile-notifications-arc42.md §8.6): the 32-byte private scalar and
// the 65-byte uncompressed public point, both base64url. The public half is
// stored even though it is derivable — it is the value every subscription
// was bound to, and writing it down is what lets UnmarshalJSON detect a
// corrupted or hand-edited file instead of trusting whichever half survived.
type vapidKeyJSON struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

func (k *VAPIDKey) MarshalJSON() ([]byte, error) {
	if k.signer == nil {
		return nil, errors.New("webpush: cannot marshal a zero VAPIDKey")
	}
	d := k.signer.D.FillBytes(make([]byte, 32))
	return json.Marshal(vapidKeyJSON{
		PrivateKey: base64.RawURLEncoding.EncodeToString(d),
		PublicKey:  base64.RawURLEncoding.EncodeToString(k.public),
	})
}

// UnmarshalJSON re-derives the public key from the private scalar and fails
// loudly when the stored public half disagrees. A corrupted vapid-keys.json
// must not sign with a key whose public half every subscription was bound
// to: the pushes would be rejected (or worse, silently misattributed) with
// nothing pointing at the file. Refusing here surfaces the corruption at
// load time, where re-pairing is the obvious fix.
func (k *VAPIDKey) UnmarshalJSON(b []byte) error {
	var raw vapidKeyJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("webpush: vapid key: %w", err)
	}
	d, err := decodeB64("vapid private_key", raw.PrivateKey)
	if err != nil {
		return err
	}
	if len(d) != 32 {
		return fmt.Errorf("webpush: vapid private_key must be 32 bytes, got %d", len(d))
	}
	// NewPrivateKey validates the scalar range; its public key is the
	// ground truth the stored public half is checked against.
	priv, err := ecdh.P256().NewPrivateKey(d)
	if err != nil {
		return fmt.Errorf("webpush: vapid private_key: %w", err)
	}
	derived := priv.PublicKey().Bytes()
	declared, err := decodeB64("vapid public_key", raw.PublicKey)
	if err != nil {
		return err
	}
	if !bytes.Equal(declared, derived) {
		return errors.New("webpush: vapid public_key does not match the key derived from private_key — refusing a corrupted identity (subscriptions are bound to the public half)")
	}
	k.signer = &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(derived[1:33]),
			Y:     new(big.Int).SetBytes(derived[33:]),
		},
		D: new(big.Int).SetBytes(d),
	}
	k.public = derived
	return nil
}

// PublicKey returns the base64url (unpadded) uncompressed public point — the
// applicationServerKey a browser passes to pushManager.subscribe(), and the
// k parameter of every Authorization header this key signs (RFC 8292 §3.2).
func (k *VAPIDKey) PublicKey() string {
	return base64.RawURLEncoding.EncodeToString(k.public)
}

// signJWT builds and signs the RFC 8292 JWT for one push service origin.
//
// aud is the ORIGIN of the push resource (RFC 8292 §2: scheme://host[:port]),
// never the full endpoint URL — the endpoint's path is the per-subscription
// capability secret (RFC 8030 §8.3), and the JWT travels in a header that
// proxies and logs see. exp is capped well under the 24 hours RFC 8292 §2
// allows; 12h means a token is fresh for any plausible retry while clock
// skew at the service never pushes it over the cap.
func (k *VAPIDKey) signJWT(aud, subject string, now time.Time) (string, error) {
	if k.signer == nil {
		return "", errors.New("webpush: cannot sign with a zero VAPIDKey")
	}
	claims := struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub,omitempty"`
	}{
		Aud: aud,
		Exp: now.Add(12 * time.Hour).Unix(),
		Sub: subject,
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("webpush: vapid claims: %w", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) +
		"." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.signer, digest[:])
	if err != nil {
		return "", fmt.Errorf("webpush: vapid signature: %w", err)
	}
	// JWS ES256 (RFC 7518 §3.4) wants the raw 64-byte R||S concatenation,
	// not the ASN.1 DER that crypto/ecdsa's SignASN1 would emit — push
	// services reject DER outright.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
