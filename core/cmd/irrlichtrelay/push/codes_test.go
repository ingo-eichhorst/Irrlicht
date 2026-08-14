package push

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMintCodeShapeAndAlphabet(t *testing.T) {
	svc, _, _ := newTestService(t)
	code := mustMint(t, svc, "acme")
	if len(code) != 9 || code[4] != '-' {
		t.Fatalf("code %q is not XXXX-XXXX shaped", code)
	}
	for _, r := range strings.ReplaceAll(code, "-", "") {
		if !strings.ContainsRune(codeAlphabet, r) {
			t.Fatalf("code %q contains %q, outside the ambiguity-free alphabet %q", code, r, codeAlphabet)
		}
	}
}

func TestRedeemRoundTripAndNormalization(t *testing.T) {
	svc, _, _ := newTestService(t)
	// Each variant consumes its own code: codes are single use.
	variants := []struct {
		name      string
		transform func(string) string
	}{
		{"as presented", func(c string) string { return c }},
		{"lowercase", strings.ToLower},
		{"no dash", func(c string) string { return strings.ReplaceAll(c, "-", "") }},
		{"extra dashes", func(c string) string {
			raw := strings.ReplaceAll(c, "-", "")
			return raw[:2] + "-" + raw[2:4] + "-" + raw[4:6] + "-" + raw[6:]
		}},
		{"spaces", func(c string) string { return " " + strings.ReplaceAll(c, "-", " ") + " " }},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			code := mustMint(t, svc, "acme")
			ws, err := svc.Redeem(v.transform(code))
			if err != nil {
				t.Fatalf("Redeem(%q) = %v", v.transform(code), err)
			}
			if ws != "acme" {
				t.Fatalf("Redeem workspace = %q, want %q", ws, "acme")
			}
		})
	}
}

func TestRedeemSingleUse(t *testing.T) {
	svc, _, _ := newTestService(t)
	code := mustMint(t, svc, "acme")
	if _, err := svc.Redeem(code); err != nil {
		t.Fatalf("first Redeem: %v", err)
	}
	if _, err := svc.Redeem(code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("second Redeem = %v, want ErrCodeInvalid", err)
	}
}

func TestCodeExpiresAtExactlyTTL(t *testing.T) {
	svc, clk, _ := newTestService(t)

	// One tick short of the TTL the code still redeems…
	fresh := mustMint(t, svc, "acme")
	clk.advance(CodeTTL - time.Second)
	if _, err := svc.Redeem(fresh); err != nil {
		t.Fatalf("Redeem just before TTL: %v", err)
	}

	// …and at exactly the TTL it is expired.
	stale := mustMint(t, svc, "acme")
	clk.advance(CodeTTL)
	if _, err := svc.Redeem(stale); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("Redeem at exactly TTL = %v, want ErrCodeInvalid", err)
	}
}

// TestRedeemFailureIsUniform pins the no-oracle property: an unknown code,
// an expired code and an already-used code produce the very same error
// value, so nothing about a failure reveals whether the code ever existed.
func TestRedeemFailureIsUniform(t *testing.T) {
	svc, clk, _ := newTestService(t)

	_, unknownErr := svc.Redeem("ABCD-EFGH") // nothing minted yet: unknown

	expired := mustMint(t, svc, "acme")
	clk.advance(CodeTTL)
	_, expiredErr := svc.Redeem(expired)

	used := mustMint(t, svc, "acme")
	if _, err := svc.Redeem(used); err != nil {
		t.Fatalf("redeeming the to-be-used code: %v", err)
	}
	_, usedErr := svc.Redeem(used)

	for name, err := range map[string]error{"unknown": unknownErr, "expired": expiredErr, "used": usedErr} {
		if !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("%s code error = %v, want ErrCodeInvalid", name, err)
		}
		// Identical error VALUE, not merely the same sentinel in a chain:
		// a wrapped variant would carry a distinguishing message.
		if err != ErrCodeInvalid {
			t.Fatalf("%s code error %q is distinguishable from the uniform failure %q", name, err, ErrCodeInvalid)
		}
	}
}

func TestMintCapAt32SweepsExpiredFirst(t *testing.T) {
	svc, clk, _ := newTestService(t)
	for i := 0; i < maxOutstandingCodes; i++ {
		mustMint(t, svc, "acme")
	}
	if _, _, err := svc.MintCode("acme"); !errors.Is(err, ErrTooManyCodes) {
		t.Fatalf("mint #33 = %v, want ErrTooManyCodes", err)
	}
	// Expired codes are swept at mint, so the cap frees itself.
	clk.advance(CodeTTL)
	if _, _, err := svc.MintCode("acme"); err != nil {
		t.Fatalf("mint after expiry sweep: %v", err)
	}
}

// TestRedeemFailureRateLimit pins the online brute-force bound: the 10th
// failure inside the rolling minute trips the limiter, a correct code is
// then refused too, and the window drains by time alone.
func TestRedeemFailureRateLimit(t *testing.T) {
	svc, clk, _ := newTestService(t)
	good := mustMint(t, svc, "acme")

	for i := 0; i < redeemFailureLimit; i++ {
		if _, err := svc.Redeem("ZZZZ-ZZZZ"); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("failure #%d = %v, want ErrCodeInvalid", i+1, err)
		}
	}
	// Tripped: even the correct code is refused, and the refusal is
	// distinguishable from an invalid code (a legitimate user must see
	// "try later", not "your code is wrong").
	if _, err := svc.Redeem(good); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Redeem(correct) while tripped = %v, want ErrRateLimited", err)
	}

	// The window drains: a minute after the failures, the same correct
	// code (still inside its own 10-minute TTL) redeems.
	clk.advance(redeemFailureWindow + time.Second)
	ws, err := svc.Redeem(good)
	if err != nil {
		t.Fatalf("Redeem after window drained: %v", err)
	}
	if ws != "acme" {
		t.Fatalf("workspace = %q, want %q", ws, "acme")
	}
}
