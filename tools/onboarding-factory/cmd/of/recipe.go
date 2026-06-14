package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

// A recipe (details.recipe) is the per-cell driver program run-cell.sh feeds to
// the agent's interactive driver. The driver reads timeout_seconds and settings
// positionally; a recipe that carried a script but omitted timeout_seconds once
// reached a driver as the literal "null" and crashed it (`null: unbound
// variable`). These helpers make the factory — the only sanctioned writer —
// default those fields on write and reject a missing timeout_seconds on
// validate, so a malformed recipe never reaches a driver.

// recipeWouldDrive reports whether a recipe will actually be driven: it carries
// a non-empty script AND is not record_blocked (applicable:false). Headless and
// record_blocked recipes never reach a driver, so their fields aren't required.
func recipeWouldDrive(r map[string]json.RawMessage) bool {
	if raw, ok := r["applicable"]; ok {
		var b *bool
		if json.Unmarshal(raw, &b) == nil && b != nil && !*b {
			return false // applicable:false → record_blocked, never driven
		}
	}
	if raw, ok := r["script"]; ok {
		var steps []json.RawMessage
		if json.Unmarshal(raw, &steps) == nil && len(steps) > 0 {
			return true
		}
	}
	return false
}

// defaultRecipeFields fills the driver-consumed fields a script recipe omits:
// timeout_seconds (→ 120, the same floor run-cell.sh applies at runtime) and
// settings (→ {}). It only touches a recipe that would actually be driven, and
// only adds an ABSENT field — an explicit value is never overwritten. No-op when
// the cell has no recipe or the recipe isn't a JSON object (validate surfaces
// the latter).
func defaultRecipeFields(cell *shard.ShardAgent) {
	if len(cell.Details.Recipe) == 0 {
		return
	}
	var r map[string]json.RawMessage
	if json.Unmarshal(cell.Details.Recipe, &r) != nil {
		return
	}
	if !recipeWouldDrive(r) {
		return
	}
	changed := false
	if _, ok := r["timeout_seconds"]; !ok {
		r["timeout_seconds"] = json.RawMessage("120")
		changed = true
	}
	if _, ok := r["settings"]; !ok {
		r["settings"] = json.RawMessage("{}")
		changed = true
	}
	if changed {
		// Match the repo's writer style: don't HTML-escape <, >, & — recipes
		// carry literal markers like the <!-- irrlicht-eta --> task marker, and
		// the rest of of writes them unescaped (writeJSONFileAtomic does too).
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(r) == nil {
			cell.Details.Recipe = bytes.TrimRight(buf.Bytes(), "\n")
		}
	}
}

// recipeTurnCount estimates how many agent turns (≈ requests) a recipe drives:
// the count of wait_turn steps in its script. A recipe with no script (a
// headless prompt cell) or one that doesn't parse counts as a single turn. Used
// by `of record prereq-check` to size a column's request budget before a sweep.
func recipeTurnCount(recipe json.RawMessage) int {
	if len(recipe) == 0 {
		return 1
	}
	var r struct {
		Script []struct {
			Type string `json:"type"`
		} `json:"script"`
	}
	if json.Unmarshal(recipe, &r) != nil {
		return 1
	}
	turns := 0
	for _, s := range r.Script {
		if s.Type == "wait_turn" {
			turns++
		}
	}
	if turns == 0 {
		return 1
	}
	return turns
}

// recipeTimeoutFinding returns a validate finding when a recipe that would be
// driven omits a positive numeric timeout_seconds — the field whose absence
// crashed a driver. Empty string means no finding. settings is defaulted on
// write but not gated here: a missing settings writes an empty file, not a
// crash.
func recipeTimeoutFinding(recipe json.RawMessage) string {
	if len(recipe) == 0 {
		return ""
	}
	var r map[string]json.RawMessage
	if json.Unmarshal(recipe, &r) != nil {
		return ""
	}
	if !recipeWouldDrive(r) {
		return ""
	}
	raw, ok := r["timeout_seconds"]
	if !ok {
		return "recipe drives a script but omits timeout_seconds (the driver needs it; `of cell write` defaults it to 120)"
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil || n <= 0 {
		return fmt.Sprintf("recipe timeout_seconds must be a positive number, got %s", string(raw))
	}
	return ""
}
