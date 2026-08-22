// refreshconsent_test.go proves the #1736 obligation that neither the pure
// decision function nor the adapter-local behaviour tests can reach: the
// version-triggered refresh is a WRITE to a shared, user-owned config, so it
// only ever happens behind the same #570 consent the original install passed —
// and it reaches that file through PermissionService, never through a path this
// adapter owns.
//
// Two arms, and the second is the one that discriminates. Granted: the repair
// entry point the re-verification loop calls (#1372's
// PermissionService.RepairGrantedHookInstall) does perform the refresh, which
// is what stops "consent-gated" from being achieved by the refresh simply never
// running. Revoked: the same call with consent withdrawn reports NOT ATTEMPTED
// and leaves the copy at the old built-in default.
//
// # Why an adapter test imports the application layer
//
// The mirror image of the note core/application/services/permission_version_gate_test.go
// carries for importing codex: this is the only place the real kirocli
// declaration and the real consent enforcement meet, and it is confined to a
// test file, so core/architecture_test.go's hexagonal import-direction rule is
// unaffected (that walk loads packages without Tests, i.e. non-test imports
// only — see its own comment on the pkg/ rule).
//
// It runs HERE rather than in the services package because the refresh's whole
// trigger is what `kiro-cli --version` prints, and the seam that stands a fake
// binary in for the real one (kiroCLIPath, kiroctl.go) is package-private on
// purpose. Exporting it to move this test one directory over would put a
// test-only mutation point in the adapter's production API.
package kirocli

import (
	"strings"
	"sync"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// memPermissionStore is the smallest PermissionStore that can hold a decision.
type memPermissionStore struct {
	mu  sync.Mutex
	set permission.Set
}

func (s *memPermissionStore) Load() (permission.Set, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set.Clone(), nil
}

func (s *memPermissionStore) Save(set permission.Set) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.set = set.Clone()
	return nil
}

// consentLog captures what the service reported, so a refusal can be told from
// a silent no-op.
type consentLog struct {
	mu     sync.Mutex
	errors []string
}

func (l *consentLog) LogInfo(_, _, _ string) {}
func (l *consentLog) LogError(_, _, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}
func (l *consentLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *consentLog) Close() error                                            { return nil }

func (l *consentLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.errors, "\n")
}

// serviceOverKiroCLI builds a real PermissionService over the real kirocli
// declaration, with the hooks permission in the given state.
func serviceOverKiroCLI(t *testing.T, state permission.State) (*services.PermissionService, *consentLog) {
	t.Helper()
	set := permission.Set{}
	set.Put(AdapterName, PermissionKeyHooks, state)
	log := &consentLog{}
	// The REAL declaration, and only it: the service can then resolve exactly
	// one adapter and one permission, so a repair that reached the file could
	// not have reached it through anything else.
	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents: []agent.Agent{Agent()},
		Store:  &memPermissionStore{set: set},
		Log:    log,
	})
	return svc, log
}

// TestRefreshReachesTheConfigOnlyThroughTheConsentGatedRepairPath is the
// obligation itself.
func TestRefreshReachesTheConfigOnlyThroughTheConsentGatedRepairPath(t *testing.T) {
	t.Run("granted", func(t *testing.T) {
		_, fake := steerableKiroHome(t)
		svc, log := serviceOverKiroCLI(t, permission.StateGranted)

		// Install through the same gate, so the arms differ in consent and
		// nothing else.
		if attempted, err := svc.RepairGrantedHookInstall(AdapterName, PermissionKeyHooks); !attempted || err != nil {
			t.Fatalf("initial install through the repair path: attempted=%v err=%v; service log:\n%s",
				attempted, err, log.joined())
		}
		if got := readAgentConfig(t)["prompt"]; got != "built-in as of 2.6.0" {
			t.Fatalf("prompt after install = %v, want the 2.6.0 built-in", got)
		}

		fake.setVersion("2.7.0")
		attempted, err := svc.RepairGrantedHookInstall(AdapterName, PermissionKeyHooks)
		if !attempted {
			t.Fatal("the repair path declined a granted permission, so this arm proves nothing")
		}
		if err != nil {
			if strings.Contains(err.Error(), minCLIVersion) {
				t.Fatalf("the declared version floor refused this install (%v) — the kiro-cli "+
					"on this machine is below %s, so the refresh never ran and this test "+
					"could not look", err, minCLIVersion)
			}
			t.Fatalf("repair after an upgrade: %v; service log:\n%s", err, log.joined())
		}

		if got := readAgentConfig(t)["prompt"]; got != "built-in as of 2.7.0" {
			t.Errorf("prompt after the upgrade = %v, want the 2.7.0 built-in — the refresh "+
				"does not reach the file through PermissionService.RepairGrantedHookInstall, "+
				"which is the entry point #1372's loop uses", got)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		_, fake := steerableKiroHome(t)

		// Install with consent, exactly as the granted arm does.
		granted, _ := serviceOverKiroCLI(t, permission.StateGranted)
		if attempted, err := granted.RepairGrantedHookInstall(AdapterName, PermissionKeyHooks); !attempted || err != nil {
			t.Fatalf("initial install: attempted=%v err=%v", attempted, err)
		}
		before := readAgentConfig(t)["prompt"]
		if before != "built-in as of 2.6.0" {
			t.Fatalf("prompt after install = %v, want the 2.6.0 built-in", before)
		}

		// Now the user withdraws consent, and kiro-cli is upgraded.
		revoked, _ := serviceOverKiroCLI(t, permission.StateDenied)
		fake.setVersion("2.7.0")

		attempted, err := revoked.RepairGrantedHookInstall(AdapterName, PermissionKeyHooks)
		if err != nil {
			t.Fatalf("repair with consent withdrawn returned an error: %v", err)
		}
		if attempted {
			t.Error("the repair path reported a write ATTEMPTED for a denied permission")
		}
		if got := readAgentConfig(t)["prompt"]; got != before {
			t.Errorf("the copy was rebuilt (%v -> %v) with the hooks permission denied — a "+
				"refresh is a WRITE to a shared user config and has to pass the same #570 "+
				"consent the install did", before, got)
		}
	})
}
