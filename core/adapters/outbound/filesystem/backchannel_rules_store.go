// backchannel_rules_store.go persists the user's event→action rules (issue
// #724) to <dir>/backchannel_rules.json. A missing file means no rules. The
// in-memory slice is the source of truth on the hot Rules() path (the engine
// reads it per evaluated session) so reads never hit disk.
package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"irrlicht/core/domain/backchannel"
)

const backchannelRulesFileVersion = 1

type backchannelRulesFile struct {
	Version int                `json:"version"`
	Rules   []backchannel.Rule `json:"rules"`
}

// BackchannelRulesStore persists []backchannel.Rule atomically. Safe for
// concurrent use.
type BackchannelRulesStore struct {
	mu    sync.RWMutex
	path  string
	rules []backchannel.Rule
}

// NewBackchannelRulesStore returns a store rooted at dir, loading any
// persisted rules (none on a missing/unreadable file).
func NewBackchannelRulesStore(dir string) *BackchannelRulesStore {
	s := &BackchannelRulesStore{path: filepath.Join(dir, "backchannel_rules.json")}
	if data, err := os.ReadFile(s.path); err == nil {
		var f backchannelRulesFile
		if json.Unmarshal(data, &f) == nil {
			s.rules = f.Rules
		}
	}
	return s
}

// Rules returns a copy of the current rule set.
func (s *BackchannelRulesStore) Rules() []backchannel.Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]backchannel.Rule, len(s.rules))
	copy(out, s.rules)
	return out
}

// SetRules replaces and persists the rule set.
func (s *BackchannelRulesStore) SetRules(rules []backchannel.Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(backchannelRulesFile{Version: backchannelRulesFileVersion, Rules: rules}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(s.path, data, 0o600); err != nil {
		return err
	}
	s.rules = rules
	return nil
}
