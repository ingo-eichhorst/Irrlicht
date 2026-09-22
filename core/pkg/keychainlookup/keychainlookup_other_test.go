//go:build !darwin

package keychainlookup

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRaw_UnsupportedOnNonDarwin(t *testing.T) {
	_, err := Raw(context.Background(), "svc", "acct", time.Second)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
