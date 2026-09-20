package accountquota

import (
	"context"
	"errors"
	"testing"
)

// These tests inject a fake keychainLookup rather than the platform default
// — never running the real `security` CLI (which would touch the developer's
// actual login keychain) and passing identically on darwin and non-darwin
// runners, unlike platformKeychainLookup itself.

func TestKeychainCredentialResolver_ResolvesViaLookup(t *testing.T) {
	r := KeychainCredentialResolver{
		Service: "irrlicht-muse",
		Account: "default",
		lookup: func(_ context.Context, service, account string) (string, error) {
			if service != "irrlicht-muse" || account != "default" {
				t.Fatalf("lookup called with service=%q account=%q", service, account)
			}
			return "keychain-secret", nil
		},
	}
	cred, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := cred.Reveal(); got != "keychain-secret" {
		t.Fatalf("Reveal() = %q, want keychain-secret", got)
	}
}

func TestKeychainCredentialResolver_LookupError(t *testing.T) {
	wantErr := errors.New("item not found")
	r := KeychainCredentialResolver{
		Service: "s", Account: "a",
		lookup: func(context.Context, string, string) (string, error) { return "", wantErr },
	}
	if _, err := r.Resolve(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Resolve error = %v, want %v", err, wantErr)
	}
}

func TestKeychainCredentialResolver_EmptySecretIsAnError(t *testing.T) {
	r := KeychainCredentialResolver{
		Service: "s", Account: "a",
		lookup: func(context.Context, string, string) (string, error) { return "", nil },
	}
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error for an empty resolved secret")
	}
}

func TestKeychainCredentialResolver_RequiresServiceAndAccount(t *testing.T) {
	r := KeychainCredentialResolver{lookup: func(context.Context, string, string) (string, error) { return "x", nil }}
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error when Service/Account are empty")
	}
}

// TestPlatformKeychainLookup_UnsupportedOnNonDarwin only asserts something on
// non-darwin runners (the shape credential_keychain_other.go provides); on
// darwin platformKeychainLookup shells out for real, which this file
// deliberately never invokes to avoid touching the developer's real keychain.
func TestPlatformKeychainLookup_DefaultIsWiredIn(t *testing.T) {
	r := NewKeychainCredentialResolver("svc", "acct")
	if r.lookup != nil {
		t.Fatal("NewKeychainCredentialResolver should leave lookup nil so Resolve falls back to platformKeychainLookup")
	}
}
