package webpush_test

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"testing"

	"irrlicht/core/pkg/webpush"
)

// RFC 8291 Appendix A's worked example, transcribed from the RFC text. The
// expected body pins the entire pipeline byte for byte — ECDH, both HKDF
// stages, record layout, header layout, AES-128-GCM — against numbers this
// package did not produce. A known-answer test that computes its own
// expectation would only prove the code agrees with itself.
const (
	katPlaintext = "When I grow up, I want to be a watermelon"

	katUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	katUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	katASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	katASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	katSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	katAuth      = "BTBZMqHH6r4Tts7J_aSIgg"

	// The complete encrypted message from the end of Appendix A.
	katBody = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlm" +
		"lMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK" +
		"4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func TestEncrypt_RFC8291AppendixA(t *testing.T) {
	ephemeral, err := ecdh.P256().NewPrivateKey(mustB64(t, katASPrivate))
	if err != nil {
		t.Fatalf("as_private from the RFC does not parse: %v", err)
	}
	// Sanity-pin the transcription itself: the RFC's as_public must be the
	// public half of its as_private, or a typo in either constant would
	// masquerade as an encryption defect.
	if got := ephemeral.PublicKey().Bytes(); !bytes.Equal(got, mustB64(t, katASPublic)) {
		t.Fatalf("as_private does not derive as_public — the transcribed constants disagree with each other")
	}

	// The example pads with the delimiter only, so padTo is exactly
	// plaintext+delimiter.
	body, err := webpush.Encrypt(ephemeral, mustB64(t, katSalt), mustB64(t, katUAPublic), mustB64(t, katAuth), []byte(katPlaintext), len(katPlaintext)+1)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if got, want := base64.RawURLEncoding.EncodeToString(body), katBody; got != want {
		t.Fatalf("encrypted body differs from RFC 8291 Appendix A\n got: %s\nwant: %s", got, want)
	}
}

// TestDecryptBody_RFC8291AppendixA runs the test package's own decryptor
// against the RFC's ciphertext. The round-trip property test leans on that
// decryptor as its oracle, so the oracle gets its own known answer — two
// implementations that only ever validate each other could share a bug.
func TestDecryptBody_RFC8291AppendixA(t *testing.T) {
	uaPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, katUAPrivate))
	if err != nil {
		t.Fatalf("ua_private from the RFC does not parse: %v", err)
	}
	if got := uaPriv.PublicKey().Bytes(); !bytes.Equal(got, mustB64(t, katUAPublic)) {
		t.Fatalf("ua_private does not derive ua_public — the transcribed constants disagree with each other")
	}
	got, err := decryptBody(uaPriv, mustB64(t, katAuth), mustB64(t, katBody))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != katPlaintext {
		t.Fatalf("decrypted %q, want %q", got, katPlaintext)
	}
}
