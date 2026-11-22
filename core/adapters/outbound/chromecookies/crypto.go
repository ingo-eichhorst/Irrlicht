package chromecookies

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1" // #nosec G505 -- Chrome's own KDF is PBKDF2-HMAC-SHA1 (keychain_key_provider.mm); this reads its format, it chooses nothing.
	"crypto/sha256"
	"fmt"
)

// Chrome's macOS cookie encryption, from keychain_key_provider.mm and
// encryptor.cc at the revision the package doc names.
const (
	encryptionPrefix = "v10"
	kdfSalt          = "saltysalt"
	kdfIterations    = 1003
	keyLength        = 16
	// hostHashVersion is the first cookie database version whose plaintext
	// starts with SHA-256(host_key) (sqlite_persistent_cookie_store.cc,
	// "Version 24 adds a SHA256 hash of the domain value").
	hostHashVersion = 24
)

// cbcIV is Chrome's fixed IV for AES-128-CBC: sixteen spaces.
var cbcIV = bytes.Repeat([]byte{' '}, aes.BlockSize)

// Key is the AES-128 key derived from the "Chrome Safe Storage" password.
// String and GoString are redacted, so an accidental %v never prints it.
type Key struct {
	b []byte
}

func (Key) String() string   { return "[redacted chrome key]" }
func (Key) GoString() string { return "[redacted chrome key]" }

// IsZero reports whether k holds no key.
func (k Key) IsZero() bool { return len(k.b) == 0 }

// DeriveKey turns the Keychain password into Chrome's cookie key.
func DeriveKey(password string) (Key, error) {
	if password == "" {
		return Key{}, fmt.Errorf("%w: empty Chrome Safe Storage password", ErrKeychain)
	}
	b, err := pbkdf2.Key(sha1.New, password, []byte(kdfSalt), kdfIterations, keyLength)
	if err != nil {
		return Key{}, fmt.Errorf("%w: deriving the cookie key", ErrKeychain)
	}
	return Key{b: b}, nil
}

// decryptValue returns one cookie's plaintext value. r's host_key and
// dbVersion decide whether a SHA-256(host_key) prefix must be present. Every
// check is a refusal, never a best guess: a value this cannot prove it
// decrypted correctly (bad padding, a host hash that does not match) is
// ErrUnknownEncryption, not a string of garbage that would later be sent.
//
// A cookie stored in the plaintext `value` column with an empty
// encrypted_value is returned as is; Chromium loads such a row the same way
// (sqlite_persistent_cookie_store.cc decrypts only a non-empty
// encrypted_value) and refuses a row that has both, as this does.
func decryptValue(key Key, r row, dbVersion int) (string, error) {
	switch {
	case len(r.encrypted) == 0:
		return r.plaintext, nil
	case r.plaintext != "":
		return "", fmt.Errorf("%w: value stored both encrypted and in plaintext", ErrUnknownEncryption)
	case !bytes.HasPrefix(r.encrypted, []byte(encryptionPrefix)):
		return "", fmt.Errorf("%w: no %q prefix", ErrUnknownEncryption, encryptionPrefix)
	}
	out, err := decryptV10(key, r.encrypted[len(encryptionPrefix):])
	if err != nil {
		return "", err
	}
	return stripHostHash(out, r.hostKey, dbVersion)
}

// decryptV10 decrypts the ciphertext after a "v10" prefix with key and
// strips its padding.
func decryptV10(key Key, ct []byte) ([]byte, error) {
	if key.IsZero() {
		return nil, fmt.Errorf("%w: no key", ErrKeychain)
	}
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("%w: ciphertext is not whole AES blocks", ErrUnknownEncryption)
	}
	block, err := aes.NewCipher(key.b)
	if err != nil {
		return nil, fmt.Errorf("%w: bad key", ErrUnknownEncryption)
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, cbcIV).CryptBlocks(out, ct) // #nosec G407 -- Chrome's fixed IV; decrypting its format, never encrypting.
	out, ok := unpad(out)
	if !ok {
		return nil, fmt.Errorf("%w: bad padding (wrong key?)", ErrUnknownEncryption)
	}
	return out, nil
}

// stripHostHash removes the SHA-256(hostKey) prefix a dbVersion 24 or later
// plaintext starts with, refusing a plaintext whose prefix does not match.
func stripHostHash(out []byte, hostKey string, dbVersion int) (string, error) {
	if dbVersion >= hostHashVersion {
		sum := sha256.Sum256([]byte(hostKey))
		if !bytes.HasPrefix(out, sum[:]) {
			return "", fmt.Errorf("%w: host hash does not match %q", ErrUnknownEncryption, hostKey)
		}
		out = out[len(sum):]
	}
	return string(out), nil
}

// unpad strips PKCS#7 padding, reporting false for anything that is not
// valid padding.
func unpad(b []byte) ([]byte, bool) {
	if len(b) == 0 {
		return nil, false
	}
	n := int(b[len(b)-1])
	if n == 0 || n > min(aes.BlockSize, len(b)) {
		return nil, false
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, false
		}
	}
	return b[:len(b)-n], true
}
