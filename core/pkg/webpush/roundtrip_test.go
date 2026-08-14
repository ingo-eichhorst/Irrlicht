package webpush_test

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"

	"irrlicht/core/pkg/webpush"
)

// TestEncrypt_RoundTripProperty drives random inputs through encrypt and the
// independent device-side decryptor. Hand-written cases encode what the
// author already thought of; the generator does not share that blind spot.
//
// Generator axes:
//   - payload size: uniform over 0..padTo-1, with 0 and padTo-1 (the empty
//     payload and the exactly-full record) forced every run;
//   - key material: fresh UA keypair, ephemeral keypair, auth secret and
//     salt per iteration;
//   - padTo: DefaultPadTo and a small size (64), so the fixed-length
//     property is checked away from the default it was tuned for.
//
// Two properties per iteration: the decryptor recovers the exact payload,
// and the body length is byte-identical across ALL payload sizes for a given
// padTo — the docs/mobile-notifications-arc42.md §8.2 guarantee that
// ciphertext length leaks nothing about content.
//
// The seed is fixed so a failure reproduces; the failure message prints the
// iteration, padTo and payload size needed to replay it by hand.
func TestEncrypt_RoundTripProperty(t *testing.T) {
	runRoundTrip(t, 20260814, 300)
}

func runRoundTrip(t *testing.T, seed int64, iterations int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	padSizes := []int{webpush.DefaultPadTo, 64}
	seenLen := map[int]int{} // padTo -> body length every iteration must reproduce

	for i := 0; i < iterations; i++ {
		padTo := padSizes[i%len(padSizes)]
		var size int
		switch i / len(padSizes) {
		case 0:
			size = 0 // empty payload: the delimiter is the whole record content
		case 1:
			size = padTo - 1 // exactly full: zero bytes of padding remain
		default:
			size = rng.Intn(padTo)
		}
		payload := make([]byte, size)
		rng.Read(payload)
		uaKey := randomECDHKey(t, rng)
		ephemeral := randomECDHKey(t, rng)
		auth := make([]byte, 16)
		rng.Read(auth)
		salt := make([]byte, 16)
		rng.Read(salt)

		body, err := webpush.Encrypt(ephemeral, salt, uaKey.PublicKey().Bytes(), auth, payload, padTo)
		if err != nil {
			t.Fatalf("seed %d iteration %d (padTo=%d, size=%d): encrypt: %v", seed, i, padTo, size, err)
		}

		// Fixed-size property: every body for this padTo has the same
		// length, and it is header(86) + record(padTo) + tag(16).
		if want, ok := seenLen[padTo]; ok && len(body) != want {
			t.Fatalf("seed %d iteration %d (padTo=%d, size=%d): body length %d differs from earlier %d — ciphertext length is leaking payload size", seed, i, padTo, size, len(body), want)
		}
		seenLen[padTo] = len(body)
		if want := 86 + padTo + 16; len(body) != want {
			t.Fatalf("seed %d iteration %d (padTo=%d, size=%d): body length %d, want %d", seed, i, padTo, size, len(body), want)
		}

		got, err := decryptBody(uaKey, auth, body)
		if err != nil {
			t.Fatalf("seed %d iteration %d (padTo=%d, size=%d): decrypt: %v", seed, i, padTo, size, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("seed %d iteration %d (padTo=%d, size=%d): decrypted payload differs from original", seed, i, padTo, size)
		}
	}

	// The exact figure the privacy table promises: with the default pad
	// size every body is 2048 + 16 + 86 = 2150 bytes.
	if got := seenLen[webpush.DefaultPadTo]; got != 2150 {
		t.Fatalf("DefaultPadTo body length = %d, want the constant 2150", got)
	}
}

// TestEncrypt_PayloadBoundary pins the ErrPayloadTooLarge edge: padTo-1
// payload bytes fit (the delimiter takes the last slot), padTo bytes do not,
// and the record must refuse to grow rather than leak length.
func TestEncrypt_PayloadBoundary(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	uaKey := randomECDHKey(t, rng)
	ephemeral := randomECDHKey(t, rng)
	auth := make([]byte, 16)
	salt := make([]byte, 16)

	const padTo = 128
	if _, err := webpush.Encrypt(ephemeral, salt, uaKey.PublicKey().Bytes(), auth, make([]byte, padTo-1), padTo); err != nil {
		t.Fatalf("payload of padTo-1 bytes must fit: %v", err)
	}
	_, err := webpush.Encrypt(ephemeral, salt, uaKey.PublicKey().Bytes(), auth, make([]byte, padTo), padTo)
	if !errors.Is(err, webpush.ErrPayloadTooLarge) {
		t.Fatalf("payload of padTo bytes: got %v, want ErrPayloadTooLarge", err)
	}
}
