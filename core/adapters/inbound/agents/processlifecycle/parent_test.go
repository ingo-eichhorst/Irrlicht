//go:build darwin || linux

package processlifecycle

import (
	"context"
	"os"
	"testing"
)

func TestParentPIDOfCurrentProcess(t *testing.T) {
	got, err := ParentPIDOf(context.Background(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if want := os.Getppid(); got != want {
		t.Fatalf("parent PID = %d, want %d", got, want)
	}
	if _, err := ParentPIDOf(context.Background(), -1); err == nil {
		t.Fatal("invalid PID was treated as known ancestry")
	}
}
