// activation_migration.go folds the pre-wizard task-eta consent record
// (activation.json, issue #558) into the permission store (issue #577).
// The task-eta managed block in ~/.claude/CLAUDE.md originally carried its
// own consent mechanism because it predates the #570 permission wizard;
// it is now a regular modify-kind permission (claude-code/instructions),
// and this one-time migration preserves the user's legacy answer.
package services

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"irrlicht/core/domain/permission"
	"irrlicht/core/ports/outbound"
)

// legacyActivationFilename is the pre-#577 consent record, written beside
// permissions.json under the daemon data dir.
const legacyActivationFilename = "activation.json"

// MigrateLegacyTaskEtaConsent seeds the agentName/permKey permission from a
// legacy activation.json, then retires the file. Call before the
// PermissionService loads the store. The legacy answer only applies while
// the permission is still pending — an answer already given through the
// wizard always wins, and a lingering legacy file can never overwrite it.
func MigrateLegacyTaskEtaConsent(dir string, store outbound.PermissionStore, agentName, permKey string, log outbound.Logger) {
	path := filepath.Join(dir, legacyActivationFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return // no legacy record (or unreadable — nothing to migrate)
	}

	var legacy struct {
		TaskEtaEnabled bool `json:"task_eta_enabled"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		log.LogError("permissions", "", fmt.Sprintf("legacy %s unparseable, discarding: %v", legacyActivationFilename, err))
		_ = os.Remove(path)
		return
	}

	set, err := store.Load()
	if err != nil {
		// Can't see the wizard state — keep the legacy file so a later
		// start can retry the migration.
		log.LogError("permissions", "", fmt.Sprintf("task-eta consent migration skipped (store unreadable): %v", err))
		return
	}
	if set.Get(agentName, permKey) == permission.StatePending {
		target := permission.StateDenied
		if legacy.TaskEtaEnabled {
			target = permission.StateGranted
		}
		set.Put(agentName, permKey, target)
		if err := store.Save(set); err != nil {
			log.LogError("permissions", "", fmt.Sprintf("task-eta consent migration failed to persist: %v", err))
			return
		}
		log.LogInfo("permissions", "", fmt.Sprintf("migrated legacy task-eta consent to %s/%s=%s", agentName, permKey, target))
	}
	_ = os.Remove(path)
}
