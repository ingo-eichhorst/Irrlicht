package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// The writer for replaydata/agents/adapters.json (#1369).
//
// It exists because the file would otherwise be the one piece of replaydata/
// with no write verb, and the factory's whole contract is that every write
// goes through `of` — "Don't write replaydata/ by hand" is the create-agent
// skill's headline rule. Worse than inconsistent, the omission was
// self-defeating: `of agent add` registers a column in scenarios.json, and
// `validateCapModel` then fails the tree with "adapter X is an onboarded
// column but has no entry". Step one of onboarding a new agent would have
// left `of validate` red with a hand-edit as the only remedy — the opposite
// of the cost this ticket is meant to remove.

// capabilityFile mirrors adapters.json field-for-field, INCLUDING the
// `_comment` block, which is carried through as raw JSON. A map[string]any
// round-trip would reorder the top-level keys (encoding/json sorts map keys)
// and churn the file on every write.
type capabilityFile struct {
	SchemaVersion int                            `json:"schema_version"`
	Comment       json.RawMessage                `json:"_comment,omitempty"`
	Adapters      map[string]matrix.AdapterModel `json:"adapters"`
}

// loadCapabilityFile reads adapters.json, tolerating absence (a repo that has
// not adopted the model yet gets a fresh one at schema_version 1).
func loadCapabilityFile(repoRoot string) (*capabilityFile, error) {
	b, err := os.ReadFile(matrix.CapabilityFile(repoRoot))
	if os.IsNotExist(err) {
		return &capabilityFile{SchemaVersion: 1, Adapters: map[string]matrix.AdapterModel{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var f capabilityFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", matrix.CapabilityFile(repoRoot), err)
	}
	if f.Adapters == nil {
		f.Adapters = map[string]matrix.AdapterModel{}
	}
	return &f, nil
}

// ensureAdapterModel gives an adapter an entry if it has none, at the maturity
// floor that claims nothing. Idempotent: an adapter that already has an entry
// is left exactly as it is, so re-running `of agent add`-adjacent flows can
// never quietly demote a declared tier.
func ensureAdapterModel(repoRoot, id string) error {
	f, err := loadCapabilityFile(repoRoot)
	if err != nil {
		return err
	}
	if _, exists := f.Adapters[id]; exists {
		return nil
	}
	f.Adapters[id] = matrix.AdapterModel{Maturity: matrix.MaturityPlanned}
	return writeJSONFileAtomic(matrix.CapabilityFile(repoRoot), f)
}

// setAdapterModel applies an explicit maturity and/or capability edits.
//
// Setting a trait to `traced` DELETES the declaration rather than storing it:
// traced is the default for anything unmentioned, so storing it would be a
// second spelling of the same fact, and the file's stated shape is "only the
// non-default values".
func setAdapterModel(repoRoot, id, maturity string, caps map[string]string) error {
	f, err := loadCapabilityFile(repoRoot)
	if err != nil {
		return err
	}
	entry, ok := f.Adapters[id]
	if !ok {
		entry = matrix.AdapterModel{Maturity: matrix.MaturityPlanned}
	}
	if maturity != "" {
		if !matrix.IsValidMaturity(maturity) {
			return fmt.Errorf("maturity %q is not one of: %s", maturity, strings.Join(matrix.Maturities, ", "))
		}
		entry.Maturity = maturity
	}
	for trait, state := range caps {
		if _, known := matrix.TraitByID(trait); !known {
			return fmt.Errorf("%q is not a known trait (see internal/matrix/capability.go)", trait)
		}
		if !matrix.IsValidCapabilityState(state) {
			return fmt.Errorf("capability state %q is not one of: %s", state, strings.Join(matrix.CapabilityStates, ", "))
		}
		if state == matrix.CapabilityTraced || state == "" {
			delete(entry.Capabilities, trait)
			continue
		}
		if entry.Capabilities == nil {
			entry.Capabilities = map[string]string{}
		}
		entry.Capabilities[trait] = state
	}
	if len(entry.Capabilities) == 0 {
		entry.Capabilities = nil // keep the file free of empty objects
	}
	f.Adapters[id] = entry
	return writeJSONFileAtomic(matrix.CapabilityFile(repoRoot), f)
}

// capabilityFlag parses repeatable --capability trait=state pairs.
type capabilityFlag map[string]string

func (c capabilityFlag) String() string {
	parts := make([]string, 0, len(c))
	for k, v := range c {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (c capabilityFlag) Set(v string) error {
	trait, state, ok := strings.Cut(v, "=")
	if !ok || trait == "" || state == "" {
		return fmt.Errorf("expected trait=state, got %q", v)
	}
	c[trait] = state
	return nil
}
