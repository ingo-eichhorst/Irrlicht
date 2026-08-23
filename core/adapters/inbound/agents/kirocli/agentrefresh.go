// agentrefresh.go keeps the copied `irrlicht` agent config a MIRROR of
// kiro-cli's built-in default rather than a snapshot frozen at grant time
// (issue #1736, follow-up to #1716).
//
// hookinstaller.go's package comment states the problem in the adapter's own
// words: kiro-cli fires hooks only for an agent selected via `--agent` or
// `chat.defaultAgent`, a default install has neither, so the first grant
// materializes a full COPY of the built-in default (`kiro-cli agent create
// irrlicht --from kiro_default`) and points chat.defaultAgent at it. A copy is
// not a mirror — it does not pick up what a kiro-cli upgrade changes in the
// built-in agent (system prompt, resources list, tool set), so a user who
// grants once and then upgrades silently keeps running the old built-in agent
// forever.
//
// # The mechanism, and why it is not the re-verification loop
//
// #1736 sketched this as "exactly the shape services.HookEntryVerifier already
// re-checks granted installs with". It is not built on that loop, and the
// reason is a property of agent.HookEntryStatus rather than a cost:
//
//   - Verify's two buckets are DAMAGE. Stale is defined in declaration.go as
//     "event names whose irrlicht entry is present but not in the shape (or at
//     the endpoint) we would install today" — and after a kiro-cli upgrade our
//     entries ARE exactly the shape we would install today. Nothing about the
//     hook entries is wrong; what changed is an artifact OUTSIDE the file.
//   - Reporting it anyway would make the loop log, verbatim: "irrlicht's hook
//     entries were stale in <path> and have been re-installed (#1372).
//     Something outside irrlicht rewrote that file — an agent settings UI that
//     syncs by omission does this on any config change". That is a false
//     accusation, and hook_reverify.go's own header exists to keep the three
//     hook diagnoses (entries GONE, entries present but nothing ARRIVING,
//     install FAILED) distinguishable. A fourth cause wearing the first one's
//     clothes is the collapse that header refuses.
//
// So the refresh runs where the install already runs: inside
// EnsureHooksInstalled, i.e. the hooks permission's Apply closure. That is
// still the consent-gated path and nothing else — the adapter owns no timer and
// no write path of its own, PermissionService is the only caller of Apply, and
// it re-reads consent under effectMu immediately before running it (#570,
// gate 3 in hook_reverify.go's header). Both routes into Apply carry the
// refresh: the boot re-apply in PermissionService.Start, and
// PermissionService.RepairGrantedHookInstall, which is what the verification
// loop calls when the entries themselves are damaged.
//
// What that costs, stated rather than left to be discovered: the refresh lands
// at the next daemon start (or the next re-grant, or the next hook-entry
// repair) rather than within the verifier's five-minute cadence. The drift
// window goes from UNBOUNDED — today's behaviour, forever — to the daemon's own
// uptime.
//
// # "The user has edited it", and why our own entries cannot trip it
//
// #1736's other open question. The baseline is a FINGERPRINT of the agent
// config with the "hooks" key removed: the document is parsed, "hooks" is
// deleted, and what remains is re-marshalled canonically (encoding/json sorts
// map keys) and hashed. Recorded in the sidecar every time this adapter writes
// the non-hook part of that file, which is exactly twice — when it materializes
// the copy, and when it regenerates it.
//
// Removing "hooks" is what answers the question structurally instead of by
// policy. Everything this adapter writes into that file — the beacon entries,
// their rewrite when the daemon binary moves (#1373), a re-injection after
// something deleted one — lives entirely under "hooks", so no write of ours can
// move the fingerprint, and their mere presence is not evidence of anything.
// A whole-file hash would have needed a rule saying "re-record after our own
// writes", and that rule is wrong in the one case that matters: a user edit and
// a beacon rewrite in the same file would have re-recorded the USER's bytes as
// ours and armed the next upgrade to destroy them.
//
// It also makes a foreign hook entry a non-event for the trigger, which is only
// safe because regeneration PRESERVES the whole "hooks" object across the
// rebuild (regenerateFromBuiltinDefault) — the same "leave a foreign entry
// alone" rule uninstallFlatHooks follows.
//
// # An unreadable version fails CLOSED here, unlike the version FLOOR
//
// core/pkg/cliversion fails OPEN for #1365's floor: the daemon runs under
// launchd with a minimal PATH and routinely cannot see the user's CLI, so
// refusing an install on an unreadable version would disable hooks for real
// users. "Consistent with the floor" is not the argument here, because the
// consequences are not symmetric:
//
//   - Floor, fail open = install anyway. Worst case an install against a CLI
//     too old to read it, which then fails visibly and is retried by
//     re-granting.
//   - Refresh, fail open = regenerate anyway. Worst case is that the config is
//     removed and `kiro-cli agent create` then cannot run — because the reading
//     and the regeneration are the same binary through the same seam
//     (kiroCLIVersion's doc comment) — leaving the user with no agent config,
//     chat.defaultAgent pointing at a file that no longer exists, and no hooks.
//
// A refresh that cannot read the version is a refresh that cannot be performed,
// so it is not attempted. It is not SILENT about it either: the verdict is
// recorded in the sidecar as its own word, "unknown_version", which is
// deliberately not the word for "checked, nothing to do" ("up_to_date").
// Absence of a finding and inability to look produce different bytes on disk,
// which is what TestRefreshVerdicts_UnknownVersionIsNotUpToDate pins.
package kirocli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/atomicfile"
)

// agentSnapshotStateFilename is irrlicht's own record of WHICH kiro-cli built
// the `irrlicht` agent config and what its non-hook content looked like when we
// last wrote it. A sibling of agents/ and settings/, never a member of agents/,
// for the same reason priorDefaultStateFilename is not: that directory's *.json
// files are kiro-cli agent configs subject to its deny_unknown_fields schema,
// and this document is not one.
//
// Deliberately a SECOND sidecar rather than two more fields on
// .irrlicht-prior-default.json. That file's whole contract is
// recordPriorDefaultAgentOnce — write once, never overwrite, because a later
// call would clobber the true original with "irrlicht". This one has the
// opposite contract: it must be rewritten whenever the copy is rebuilt. One
// document with two write policies is a trap, not an economy.
const agentSnapshotStateFilename = ".irrlicht-agent-snapshot.json"

// agentSnapshotState is that record.
type agentSnapshotState struct {
	// CLIVersion is the kiro-cli version whose built-in default the current
	// agent config was generated from. Empty means "no baseline" — either an
	// install that predates #1736, or one where every version read so far has
	// failed. Never written from a failed read: an empty baseline can therefore
	// not be mistaken for a matching one.
	CLIVersion string `json:"cli_version"`
	// ConfigFingerprint is agentConfigFingerprint of the config as this adapter
	// last wrote its non-hook part. Empty is treated exactly like a mismatch —
	// we cannot vouch for bytes we never recorded, so we do not overwrite them.
	ConfigFingerprint string `json:"config_fingerprint"`
	// LastVerdict is the word for what the most recent reconciliation decided.
	// Present so that "checked and up to date" and "could not check" are
	// different bytes on disk rather than the same silence.
	LastVerdict string `json:"last_refresh_verdict"`
}

// refreshVerdict is what one reconciliation decided. The set is closed.
type refreshVerdict string

const (
	// refreshDue: kiro-cli has been upgraded and the config is still the one
	// this adapter wrote, so it is regenerated. Never recorded — it becomes
	// refreshRefreshed or refreshFailed.
	refreshDue refreshVerdict = "due"
	// refreshCreated: the config was materialized in this same call, so it IS
	// the built-in default as of now and there is nothing to refresh.
	refreshCreated refreshVerdict = "created"
	// refreshRefreshed: regenerated from kiro_default and re-baselined.
	refreshRefreshed refreshVerdict = "refreshed"
	// refreshAdopted: nothing on record to compare against (an install from
	// before #1736, or one whose version reads have all failed). The current
	// state is adopted as the baseline, arming the NEXT upgrade, rather than
	// regenerating against a baseline we do not have.
	refreshAdopted refreshVerdict = "adopted_baseline"
	// refreshUpToDate: same kiro-cli version as the baseline. The common answer.
	refreshUpToDate refreshVerdict = "up_to_date"
	// refreshUserEdited: kiro-cli changed, but the config's non-hook content is
	// no longer what we wrote. Regenerating would destroy the user's edit.
	refreshUserEdited refreshVerdict = "user_edited"
	// refreshUnknownVersion: the installed version could not be read, so no
	// upgrade can be established AND no regeneration could be performed.
	refreshUnknownVersion refreshVerdict = "unknown_version"
	// refreshUnreadableConfig: the config exists but could not be read or
	// parsed. Not damage and not health — the same answer #1372's loop gives an
	// unparseable file: report it, write nothing.
	refreshUnreadableConfig refreshVerdict = "unreadable_config"
	// refreshFailed: a regeneration was due, was attempted, and failed. The
	// previous config was restored.
	refreshFailed refreshVerdict = "refresh_failed"
)

// rebaselines reports whether this verdict means the recorded baseline should
// be replaced with the config's current state.
//
// Exactly the three verdicts under which the non-hook content on disk is either
// ours or the best we will ever know it to be. Notably NOT refreshUpToDate: a
// pass that changed nothing must not adopt whatever it happens to find, or a
// user edit made between two daemon starts would silently become the baseline
// and arm the next upgrade to overwrite it.
func (v refreshVerdict) rebaselines() bool {
	switch v {
	case refreshCreated, refreshRefreshed, refreshAdopted:
		return true
	}
	return false
}

// decideRefresh is the whole trigger, as a pure function over three inputs:
// the version kiro-cli reports NOW (empty when it could not be read), whatever
// is on record, and the config's current fingerprint.
//
// The order of the clauses is the contract. "Could not read the version" is
// asked FIRST so that it can never be answered as up-to-date or as due; the
// user-edit guard is asked LAST so that it only ever suppresses a refresh that
// was otherwise going to happen, rather than masking one of the other verdicts.
func decideRefresh(current string, rec agentSnapshotState, configFingerprint string) refreshVerdict {
	if current == "" {
		return refreshUnknownVersion
	}
	if rec.CLIVersion == "" {
		return refreshAdopted
	}
	if rec.CLIVersion == current {
		return refreshUpToDate
	}
	if rec.ConfigFingerprint == "" || rec.ConfigFingerprint != configFingerprint {
		return refreshUserEdited
	}
	return refreshDue
}

// reconcileAgentSnapshot decides whether the copied agent must be rebuilt from
// kiro_default, rebuilds it when it must, and reports the version it read plus
// the verdict. Called by EnsureHooksInstalled BEFORE the hook entries are
// reconciled, so the rebuilt document goes through the same injection a fresh
// one does.
//
// justCreated short-circuits every question: a copy materialized moments ago IS
// the built-in default, and probing to confirm it would spend a process to
// learn nothing.
func reconcileAgentSnapshot(ctx context.Context, configPath string, justCreated bool) (version string, verdict refreshVerdict, regenerated bool, err error) {
	// Read the version even on the just-created path: it is what gets recorded
	// as the baseline, and without it the first upgrade after a grant would find
	// nothing to compare against and merely adopt.
	version, _ = kiroCLIVersion(ctx)
	if justCreated {
		return version, refreshCreated, false, nil
	}

	fingerprint, fpErr := agentConfigFingerprint(configPath)
	if fpErr != nil {
		return version, refreshUnreadableConfig, false, nil
	}

	rec := readAgentSnapshotState()
	verdict = decideRefresh(version, rec, fingerprint)
	if verdict != refreshDue {
		return version, verdict, false, nil
	}

	if err := regenerateFromBuiltinDefault(ctx, configPath); err != nil {
		return version, refreshFailed, false, err
	}
	return version, refreshRefreshed, true, nil
}

// regenerateFromBuiltinDefault rebuilds the agent config from kiro-cli's
// current built-in default, carrying the existing "hooks" object across.
//
// `kiro-cli agent create` refuses outright when the target file exists
// (kiroctl.go), so the old file has to go first — which is exactly the window
// in which a failure would leave the user with chat.defaultAgent pointing at
// nothing. The old bytes are therefore held in memory and written back if the
// rebuild fails, so the failure mode is "nothing changed and an error is
// surfaced" rather than "the agent is gone".
//
// The hooks object is preserved rather than rebuilt because it may hold entries
// that are not ours: uninstallFlatHooks already refuses to remove a foreign
// entry, and a regeneration that silently dropped one would be a bigger
// violation of the same rule. Ours are reconciled immediately afterwards by
// EnsureHooksInstalled's ordinary ensureFlatHooksInstalled step, so a beacon
// command that drifted in the meantime is still repaired.
func regenerateFromBuiltinDefault(ctx context.Context, configPath string) error {
	previous, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	hooks, err := hooksObjectOf(previous)
	if err != nil {
		return err
	}

	if err := os.Remove(configPath); err != nil {
		return err
	}
	if err := kiroAgentCreateFromDefault(ctx, irrlichtAgentName, kiroBuiltinDefaultAgent); err != nil {
		return errors.Join(err, restoreAgentConfig(configPath, previous))
	}

	rebuilt, err := hookjson.ReadSettings(configPath)
	if err != nil {
		return errors.Join(err, restoreAgentConfig(configPath, previous))
	}
	if hooks != nil {
		rebuilt["hooks"] = hooks
	}
	if err := hookjson.WriteSettings(configPath, rebuilt, atomicfile.WriteFile); err != nil {
		return errors.Join(err, restoreAgentConfig(configPath, previous))
	}
	return nil
}

// restoreAgentConfig puts the pre-regeneration bytes back. Its own error is
// joined onto the failure that triggered it rather than replacing it: a user
// reading "the rebuild failed" needs to know whether the restore also failed,
// and which one happened first.
func restoreAgentConfig(configPath string, previous []byte) error {
	return atomicfile.WriteFile(configPath, previous)
}

// hooksObjectOf extracts the "hooks" object from an agent config document, or
// nil when there is none. A document that will not parse is an error rather
// than an empty result, because "no hooks" and "unreadable" must not lead to
// the same write.
func hooksObjectOf(data []byte) (interface{}, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc["hooks"], nil
}

// agentConfigFingerprint hashes the agent config with its "hooks" key removed —
// see this file's header for why that subtraction is the answer to "has the
// user edited it".
//
// Canonicalised through encoding/json rather than hashed as raw bytes:
// json.Marshal sorts map keys, so a reformat or a key reordering by kiro-cli
// itself is not read as a user edit, and only a semantic change to the
// non-hook content moves the value.
func agentConfigFingerprint(configPath string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", err
	}
	delete(doc, "hooks")
	canonical, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// readAgentSnapshotState reads the sidecar, returning the zero value for every
// way it can be absent or unusable. The zero value carries no version and no
// fingerprint, which decideRefresh answers with refreshAdopted — the
// conservative verdict, so a corrupt sidecar can never cause a regeneration.
func readAgentSnapshotState() agentSnapshotState {
	path, err := agentSnapshotStatePath()
	if err != nil {
		return agentSnapshotState{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agentSnapshotState{}
	}
	var state agentSnapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return agentSnapshotState{}
	}
	return state
}

// writeAgentSnapshotState records the outcome of one reconciliation.
//
// The verdict is ALWAYS written; the baseline is replaced only when the verdict
// rebaselines (see that method). A verdict that changes nothing therefore still
// leaves a trace of what was decided, which is the difference between "the
// refresh looked and found nothing to do" and "the refresh could not look".
//
// A no-op write is skipped so the ordinary daemon start does not churn the
// file — this runs on every Apply, and Apply runs on every boot.
func writeAgentSnapshotState(configPath string, version string, verdict refreshVerdict) error {
	path, err := agentSnapshotStatePath()
	if err != nil {
		return err
	}

	next := readAgentSnapshotState()
	next.LastVerdict = string(verdict)
	if verdict.rebaselines() {
		// Only from a successful read. A failed read must not overwrite a good
		// baseline with an empty one, which would silently disarm the trigger.
		if version != "" {
			next.CLIVersion = version
		}
		if fingerprint, err := agentConfigFingerprint(configPath); err == nil {
			next.ConfigFingerprint = fingerprint
		}
	}

	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
		return nil
	}
	return atomicfile.WriteFile(path, data)
}

// agentSnapshotStatePath is the sidecar's absolute path. A sibling of agents/
// and settings/ — see agentSnapshotStateFilename for why it cannot live inside
// agents/. Declared in this permission's Writes.Also (agent.go), because a file
// an Apply writes and a declaration does not name is invisible to
// --print-managed-files, to the recording rig's backup and to #1449's grant-all
// refusal (#1739).
func agentSnapshotStatePath() (string, error) {
	home, err := kiroHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, agentSnapshotStateFilename), nil
}
