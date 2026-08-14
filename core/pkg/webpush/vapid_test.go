package webpush_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"irrlicht/core/pkg/webpush"
)

func TestVAPIDKey_PublicKeyShape(t *testing.T) {
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := key.PublicKey()
	// 65 bytes of uncompressed point encode to exactly 87 unpadded
	// base64url characters — the applicationServerKey length every
	// browser's pushManager.subscribe() expects.
	if len(pub) != 87 {
		t.Fatalf("PublicKey() is %d chars, want 87", len(pub))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(pub) {
		t.Fatalf("PublicKey() %q is not base64url", pub)
	}
}

func TestVAPIDKey_JSONRoundTrip(t *testing.T) {
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"private_key"`, `"public_key"`} {
		if !strings.Contains(string(blob), field) {
			t.Fatalf("marshaled key %s lacks the %s field of vapid-keys.json", blob, field)
		}
	}
	var back webpush.VAPIDKey
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal of our own marshal: %v", err)
	}
	if back.PublicKey() != key.PublicKey() {
		t.Fatalf("round-trip changed the public key: %s -> %s", key.PublicKey(), back.PublicKey())
	}
	again, err := json.Marshal(&back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(blob) {
		t.Fatalf("second marshal differs:\n%s\n%s", blob, again)
	}
}

// TestVAPIDKey_TamperedPublicHalfFailsLoud swaps in a DIFFERENT valid public
// key — the strongest corruption, because every field still parses and only
// the private/public consistency check can notice. A key that loaded anyway
// would sign JWTs no subscription-bound public key verifies, failing at every
// push with nothing pointing back at vapid-keys.json.
func TestVAPIDKey_TamperedPublicHalfFailsLoud(t *testing.T) {
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(blob), key.PublicKey(), other.PublicKey(), 1)
	if tampered == string(blob) {
		t.Fatal("tamper did not change the document")
	}
	var back webpush.VAPIDKey
	if err := json.Unmarshal([]byte(tampered), &back); err == nil {
		t.Fatal("tampered public_key was accepted — a corrupted vapid-keys.json must fail loudly, not sign with a mismatched identity")
	}
}

func TestVAPIDKey_UnmarshalRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"not json":       `{`,
		"short private":  `{"private_key":"AAAA","public_key":"AAAA"}`,
		"invalid base64": `{"private_key":"!!!","public_key":"!!!"}`,
		"empty":          `{}`,
		"zero scalar":    `{"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","public_key":"AAAA"}`,
	}
	for name, doc := range cases {
		var k webpush.VAPIDKey
		if err := k.UnmarshalJSON([]byte(doc)); err == nil {
			t.Errorf("%s: %s was accepted", name, doc)
		}
	}
}
