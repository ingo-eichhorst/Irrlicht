//go:build darwin

package processlifecycle

import "testing"

// makeProcargs2Buf builds a synthetic KERN_PROCARGS2 buffer:
//
//	argc (int32 LE) | exec_path\0 | argv[0]\0 ... | envp[0]\0 ...
//
// Mirrors TestParseProcargs2's local helper (osutil_test.go) — duplicated
// rather than shared because that one is a closure private to its test
// function, and this file needs to construct the argv-present/envp-empty
// shape TestParseProcargs2 never exercises.
func makeProcargs2Buf(argc int32, execPath string, argv, envp []string) []byte {
	var b []byte
	b = append(b, byte(argc), byte(argc>>8), byte(argc>>16), byte(argc>>24))
	b = append(b, []byte(execPath)...)
	b = append(b, 0)
	for _, a := range argv {
		b = append(b, []byte(a)...)
		b = append(b, 0)
	}
	for _, e := range envp {
		b = append(b, []byte(e)...)
		b = append(b, 0)
	}
	return b
}

// TestEnvHiddenOrAbsent_HardenedRuntimeIsUnreadableNotAbsent is the case
// #2002 flags as mattering most: a hardened-runtime process (e.g.
// Anthropic's signed `claude` binary) answers KERN_PROCARGS2 with argv
// intact but its envp section entirely stripped by the kernel. Before
// errEnvHidden existed, parseProcargs2 returned an empty map exactly as it
// would for a normal process with none of the caller's keys set — the
// "absent" vs. "unreadable" collapse the fixture table forbids, landing on
// the one process this reader exists to observe.
func TestEnvHiddenOrAbsent_HardenedRuntimeIsUnreadableNotAbsent(t *testing.T) {
	// argv present, envp section empty: the shape a hardened-runtime process
	// produces (kernel strips env, not argv, from the procargs2 response).
	buf := makeProcargs2Buf(1, "/usr/bin/claude", []string{"/usr/bin/claude"}, nil)

	out, scanned := parseProcargs2(buf, endpointEnvKeys)
	if scanned != 0 {
		t.Fatalf("scanned = %d, want 0 for an envp-empty buffer", scanned)
	}

	got, err := envHiddenOrAbsent(out, scanned, endpointEnvKeys)
	if err != errEnvHidden {
		t.Errorf("err = %v, want errEnvHidden", err)
	}
	if got != nil {
		t.Errorf("got map %v, want nil on the hidden-env path", got)
	}
}

// TestEnvHiddenOrAbsent_NoMatchingKeyIsAbsentNotUnreadable is the sibling
// fixture: envp is present and readable, it simply names none of the
// caller's keys. This must NOT trip errEnvHidden — scanned counts every
// envp entry seen, matched or not.
func TestEnvHiddenOrAbsent_NoMatchingKeyIsAbsentNotUnreadable(t *testing.T) {
	buf := makeProcargs2Buf(1, "/usr/bin/claude", []string{"/usr/bin/claude"},
		[]string{"PATH=/usr/bin", "HOME=/Users/alice"})

	out, scanned := parseProcargs2(buf, endpointEnvKeys)
	if scanned == 0 {
		t.Fatalf("scanned = 0, want >0 — this buffer's envp is readable and non-empty")
	}

	got, err := envHiddenOrAbsent(out, scanned, endpointEnvKeys)
	if err != nil {
		t.Errorf("err = %v, want nil (readable, just no matching key)", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want an empty (not nil) map", got)
	}
}

// TestEnvHiddenOrAbsent_EmptyKeysNeverReportsHidden pins the port doc's
// stated behavior: a nil/empty keys set has nothing to retain, but the read
// still happened, so it must never be reported as errEnvHidden regardless of
// scanned.
func TestEnvHiddenOrAbsent_EmptyKeysNeverReportsHidden(t *testing.T) {
	got, err := envHiddenOrAbsent(map[string]string{}, 0, nil)
	if err != nil {
		t.Errorf("err = %v, want nil for an empty key set", err)
	}
	if got == nil {
		t.Error("want a non-nil empty map, not nil")
	}
}
