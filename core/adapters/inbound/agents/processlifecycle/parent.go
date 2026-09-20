package processlifecycle

import "context"

// ParentPIDOf reads one process parent link through the platform observer.
// A caller must treat any error as unknown ancestry.
func ParentPIDOf(ctx context.Context, pid int) (int, error) {
	return osProc.ParentPIDOf(ctx, pid)
}
