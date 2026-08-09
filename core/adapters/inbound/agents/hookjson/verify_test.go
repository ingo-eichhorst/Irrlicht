package hookjson

import (
	"os"
	"path/filepath"
	"testing"
)

// Verify is the read-only half of #1372: EnsureInstalled answers "make it so",
// Verify answers "is it so", and the loop that repairs a clobbered config needs
// the second before it is allowed to run the first.
//
// Every test here runs against both delivery shapes, for the reason
// hookjson_test.go's header gives: a check that only holds for `type: http`
// would leave Codex's curl install unverified and nothing would say so.

// clobberedPath seeds path with an install and then deletes it the way
// gemini-cli's sync-by-omission writer does — by dropping the whole "hooks"
// key, not by editing our entries. That is the live-verified shape of the
// defect (see #1372 / the Phase C audit on #1355), so it is the shape the
// fixture uses.
func clobberedPath(t *testing.T, mk func(string) Config) (string, Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	cfg := mk(path)
	if _, err := EnsureInstalled(cfg); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	seedSettings(t, path, map[string]interface{}{"theme": "dark"})
	return path, cfg
}

func TestVerify_IntactInstallReportsNoDamage(t *testing.T) {
	for name, mk := range configs() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			cfg := mk(path)
			if _, err := EnsureInstalled(cfg); err != nil {
				t.Fatalf("install: %v", err)
			}
			got, err := Verify(cfg)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if !got.Intact() {
				t.Errorf("freshly installed config reports damage %q; a verifier that "+
					"cannot see its own install would repair on every pass forever", got.Damage())
			}
		})
	}
}

func TestVerify_ExternallyDeletedEntriesAreReported(t *testing.T) {
	for name, mk := range configs() {
		t.Run(name, func(t *testing.T) {
			_, cfg := clobberedPath(t, mk)
			got, err := Verify(cfg)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if got.Intact() {
				t.Fatalf("Verify reports an intact install after the whole `hooks` key was " +
					"deleted externally — this is the #1372 defect: the permission still reads " +
					"granted, nothing errors, and waiting detection is silently gone")
			}
			if len(got.Missing) != len(cfg.Events) {
				t.Errorf("Missing = %v, want all %d installed events %v",
					got.Missing, len(cfg.Events), cfg.Events)
			}
			if len(got.Stale) != 0 {
				t.Errorf("Stale = %v, want empty: nothing of ours is present to BE stale", got.Stale)
			}
		})
	}
}

func TestVerify_SingleDeletedEventIsReportedAlone(t *testing.T) {
	for name, mk := range configs() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			cfg := mk(path)
			if _, err := EnsureInstalled(cfg); err != nil {
				t.Fatalf("install: %v", err)
			}
			settings, err := ReadSettings(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			hooks := settings["hooks"].(map[string]interface{})
			delete(hooks, eventNoMatcher)
			seedSettings(t, path, settings)

			got, err := Verify(cfg)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if len(got.Missing) != 1 || got.Missing[0] != eventNoMatcher {
				t.Errorf("Missing = %v, want exactly [%s] — a partial clobber must not be "+
					"rounded up to a total one, or the log line names events that are fine",
					got.Missing, eventNoMatcher)
			}
		})
	}
}

// A stale entry is the #1178 shape: our sentinel is still there, so nothing
// looks deleted, but the endpoint names a port this daemon is not listening on.
// It is a different bucket from Missing because a user reading the file sees an
// irrlicht entry and concludes the install is fine.
func TestVerify_StaleEndpointIsReportedAsStaleNotMissing(t *testing.T) {
	for name, mk := range configs() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			cfg := mk(path)
			if _, err := EnsureInstalled(cfg); err != nil {
				t.Fatalf("install: %v", err)
			}
			settings, err := ReadSettings(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			// Rewrite every inner entry into a shape that still carries the
			// sentinel but is not canonical, exactly as a daemon on another port
			// would have left it.
			hooks := settings["hooks"].(map[string]interface{})
			for _, groups := range hooks {
				for _, g := range groups.([]interface{}) {
					entries := g.(map[string]interface{})["hooks"].([]interface{})
					for _, h := range entries {
						hook := h.(map[string]interface{})
						hook["timeout"] = 5
						if _, ok := hook["url"]; ok {
							hook["url"] = "http://" + cfg.Sentinel + "?stale=1"
						} else {
							hook["command"] = "curl http://" + cfg.Sentinel + " # stale"
						}
					}
				}
			}
			seedSettings(t, path, settings)

			got, err := Verify(cfg)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if len(got.Missing) != 0 {
				t.Errorf("Missing = %v, want empty: our entries are present, just wrong", got.Missing)
			}
			if len(got.Stale) != len(cfg.Events) {
				t.Errorf("Stale = %v, want all %d events %v", got.Stale, len(cfg.Events), cfg.Events)
			}
		})
	}
}

func TestVerify_StaleMatcherIsReported(t *testing.T) {
	for name, mk := range configs() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			cfg := mk(path)
			if _, err := EnsureInstalled(cfg); err != nil {
				t.Fatalf("install: %v", err)
			}
			settings, err := ReadSettings(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			hooks := settings["hooks"].(map[string]interface{})
			for _, g := range hooks[eventWithMatcher].([]interface{}) {
				g.(map[string]interface{})["matcher"] = "SomethingElse"
			}
			seedSettings(t, path, settings)

			got, err := Verify(cfg)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if len(got.Stale) != 1 || got.Stale[0] != eventWithMatcher {
				t.Errorf("Stale = %v, want [%s]: a rewritten matcher is a real divergence "+
					"— EnsureInstalled reconciles it, so Verify must see it", got.Stale, eventWithMatcher)
			}
		})
	}
}

// The single most important property: Verify must be a pure read. A verifier
// that writes is a way to modify a user's config outside the consent gate.
func TestVerify_NeverWritesAnything(t *testing.T) {
	for name, mk := range configs() {
		t.Run(name, func(t *testing.T) {
			path, cfg := clobberedPath(t, mk)
			writes := 0
			cfg.WriteFile = func(string, []byte) error {
				writes++
				return nil
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read before: %v", err)
			}
			if _, err := Verify(cfg); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read after: %v", err)
			}
			if writes != 0 {
				t.Errorf("Verify called WriteFile %d times; it must be a pure read — the "+
					"repair is a separate step that runs behind the consent gate (#1372/#570)", writes)
			}
			if string(before) != string(after) {
				t.Errorf("Verify changed the file on disk:\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

// Malformed JSON must error rather than report an intact install. Reporting
// intact would leave a genuinely broken config unrepaired and unmentioned;
// reporting damage would send the repair loop at a file EnsureInstalled also
// refuses to touch, spinning forever. Erroring is the only answer that lets the
// caller count it as its own outcome.
func TestVerify_MalformedJSONErrors(t *testing.T) {
	for name, mk := range configs() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			got, err := Verify(mk(path))
			if err == nil {
				t.Fatalf("Verify returned no error for malformed JSON (status %+v); "+
					"EnsureInstalled errors on it too and the two must agree", got)
			}
		})
	}
}

// A config file that does not exist yet is not an error and not "intact": the
// entries are genuinely absent, which is precisely what a repair fixes. This is
// also the shape a user gets by deleting the whole settings file.
func TestVerify_MissingFileReportsEveryEventMissing(t *testing.T) {
	for name, mk := range configs() {
		t.Run(name, func(t *testing.T) {
			cfg := mk(filepath.Join(t.TempDir(), "nope", "settings.json"))
			got, err := Verify(cfg)
			if err != nil {
				t.Fatalf("Verify on absent file: %v", err)
			}
			if len(got.Missing) != len(cfg.Events) {
				t.Errorf("Missing = %v, want all %d events", got.Missing, len(cfg.Events))
			}
		})
	}
}

// TestVerifyAgreesWithEnsureInstalled is the equivalence that makes a second
// reader safe. Verify().Intact() must be true exactly when EnsureInstalled
// would report "nothing to do" — no more, no less.
//
// Disagreement in either direction is a live bug, not a cosmetic one. Verify
// stricter than EnsureInstalled is a repair loop that reports damage, repairs
// nothing, and writes forever; Verify laxer is a clobbered install that reads
// healthy in the diagnostics bundle. The corpus is deliberately the awkward
// cases: partial clobbers, hand-edited matchers, foreign entries alongside ours.
func TestVerifyAgreesWithEnsureInstalled(t *testing.T) {
	mutations := map[string]func(t *testing.T, path string, cfg Config){
		"untouched": func(*testing.T, string, Config) {},
		"hooks key deleted": func(t *testing.T, path string, _ Config) {
			seedSettings(t, path, map[string]interface{}{"theme": "dark"})
		},
		"one event deleted": func(t *testing.T, path string, _ Config) {
			s := mustRead(t, path)
			delete(s["hooks"].(map[string]interface{}), eventNoMatcher)
			seedSettings(t, path, s)
		},
		"matcher rewritten": func(t *testing.T, path string, _ Config) {
			s := mustRead(t, path)
			for _, g := range s["hooks"].(map[string]interface{})[eventWithMatcher].([]interface{}) {
				g.(map[string]interface{})["matcher"] = "Other"
			}
			seedSettings(t, path, s)
		},
		"matcher added to turn-end event": func(t *testing.T, path string, _ Config) {
			s := mustRead(t, path)
			for _, g := range s["hooks"].(map[string]interface{})[eventNoMatcher].([]interface{}) {
				g.(map[string]interface{})["matcher"] = "Bash"
			}
			seedSettings(t, path, s)
		},
		"entry made non-canonical": func(t *testing.T, path string, cfg Config) {
			s := mustRead(t, path)
			for _, groups := range s["hooks"].(map[string]interface{}) {
				for _, g := range groups.([]interface{}) {
					for _, h := range g.(map[string]interface{})["hooks"].([]interface{}) {
						hook := h.(map[string]interface{})
						if _, ok := hook["url"]; ok {
							hook["url"] = "http://" + cfg.Sentinel + "/other"
						} else {
							hook["command"] = "curl http://" + cfg.Sentinel + "/other"
						}
					}
				}
			}
			seedSettings(t, path, s)
		},
		"foreign group alongside ours": func(t *testing.T, path string, _ Config) {
			s := mustRead(t, path)
			hooks := s["hooks"].(map[string]interface{})
			hooks[eventWithMatcher] = append(hooks[eventWithMatcher].([]interface{}),
				map[string]interface{}{
					"matcher": "Bash",
					"hooks":   []interface{}{map[string]interface{}{"type": "command", "command": "echo mine"}},
				})
			seedSettings(t, path, s)
		},
		"only a foreign group": func(t *testing.T, path string, _ Config) {
			seedSettings(t, path, map[string]interface{}{"hooks": map[string]interface{}{
				eventWithMatcher: []interface{}{map[string]interface{}{
					"matcher": "Bash",
					"hooks":   []interface{}{map[string]interface{}{"type": "command", "command": "echo mine"}},
				}},
			}})
		},
	}

	for shape, mk := range configs() {
		for name, mutate := range mutations {
			t.Run(shape+"/"+name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "settings.json")
				cfg := mk(path)
				if _, err := EnsureInstalled(cfg); err != nil {
					t.Fatalf("seed install: %v", err)
				}
				mutate(t, path, cfg)

				status, verr := Verify(cfg)
				if verr != nil {
					t.Fatalf("Verify: %v", verr)
				}
				modified, ierr := EnsureInstalled(cfg)
				if ierr != nil {
					t.Fatalf("EnsureInstalled: %v", ierr)
				}
				if status.Intact() == modified {
					t.Errorf("Verify says intact=%v but EnsureInstalled %s the file.\n"+
						"They must agree: a Verify stricter than the repair writes forever, "+
						"a Verify laxer leaves a clobbered install reading healthy.\ndamage: %q",
						status.Intact(), map[bool]string{true: "MODIFIED", false: "did NOT modify"}[modified],
						status.Damage())
				}
				// And after the repair, Verify must be satisfied — otherwise the
				// loop never converges no matter how the two disagree.
				after, err := Verify(cfg)
				if err != nil {
					t.Fatalf("Verify after repair: %v", err)
				}
				if !after.Intact() {
					t.Errorf("Verify still reports %q immediately after EnsureInstalled repaired "+
						"the file — the repair loop would never converge", after.Damage())
				}
			})
		}
	}
}

func mustRead(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	s, err := ReadSettings(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return s
}
