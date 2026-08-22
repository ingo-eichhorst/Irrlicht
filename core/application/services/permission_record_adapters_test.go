package services_test

import (
	"context"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
)

// This file covers issue #1769: lib/spawn-record-daemon.sh:87 sets
// IRRLICHT_PERMISSION_MODE=grant-all unconditionally for every recording,
// whatever adapter the run is actually about. Before this fix,
// PermissionService.Start auto-granted EVERY agent's permissions under
// grant-all, which ran EVERY adapter's Apply closure — including hook
// installers for adapters the run had nothing to do with. claudecode is the
// sharpest case: it is one of righome.Unisolatable's structurally
// unrelocatable adapters (claudeSettingsPath joins os.UserHomeDir()
// unconditionally), so recording e.g. a mistral-vibe cell repointed the
// operator's REAL ~/.claude/settings.json at the recording daemon for the
// run's whole duration — and, since Claude Code reloads its settings file
// live rather than snapshotting it at session start (per upstream docs,
// quoted in the issue thread — not measured on this machine, since measuring
// it means rewriting the operator's real settings.json), every
// already-running Claude Code session on the machine picked up the rig's
// endpoint too, for as long as the recording ran.
//
// IRRLICHT_RECORD_ADAPTERS (config.Config.RecordAdapters) narrows Start's
// grant-all auto-grant to the adapter(s) a run actually names, for
// modify-kind permissions that declare a managed user file. Left unset,
// behaviour is byte-for-byte what it was before this issue (see
// TestRecordAdaptersUnset_PreservesExistingGrantAllBehavior, a LOCK).

// writingAgent builds a minimal agent declaring one modify-kind permission
// with a managed user file (the shape every hook installer has) plus one
// observe-kind permission (the shape a transcript-reading permission has).
// applied records whether the modify permission's Apply closure ran.
func writingAgent(name string, applied *bool) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: name, DisplayName: name},
		Process:  agent.Process{Match: agent.ExactName{Name: name}},
		Permissions: []agent.Permission{
			{
				Key:    "hooks",
				Kind:   permission.KindModify,
				Title:  "Install status hooks",
				Apply:  func() error { *applied = true; return nil },
				Remove: func() error { *applied = false; return nil },
				Writes: &agent.ManagedUserFile{
					Path:      func() (string, error) { return "/tmp/irr-1769-" + name + "-settings.json", nil },
					Uninstall: func() (bool, error) { return false, nil },
				},
			},
			{
				Key:   "transcripts",
				Kind:  permission.KindObserve,
				Title: "Read session transcripts",
			},
		},
	}
}

// TestRecordAdapters_ScopesGrantAllApplyAndGrantedToTheNamedAdapter is the
// #1769 defect test. Seen red before PermissionService.Start's
// scopedOutByRecordAdapters check existed: both assertions on the foreign
// adapter failed (its Apply ran, and Granted read true) because grant-all
// auto-granted every agent unconditionally.
func TestRecordAdapters_ScopesGrantAllApplyAndGrantedToTheNamedAdapter(t *testing.T) {
	recordedApplied, foreignApplied := false, false
	recorded := writingAgent("recorded-adapter", &recordedApplied)
	foreign := writingAgent("foreign-adapter", &foreignApplied)

	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents: []agent.Agent{recorded, foreign},
		Store:  &mockPermStore{},
		Push:   &mockPush{},
		Log:    &mockLogger{},
		Mode:   config.PermissionModeGrantAll,
		// AllowSharedConfigWrites mirrors the rig setting
		// IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1 unconditionally (#1449): this
		// test is about #1769's adapter-scoping gate specifically, not the
		// separate isolated-home guard, so lift the latter the way the rig
		// does rather than let it refuse both agents for an unrelated reason.
		AllowSharedConfigWrites: true,
		Registrar:               &mockRegistrar{},
		RecordAdapters:          []string{"recorded-adapter"},
	})
	svc.Start(context.Background())

	if !recordedApplied {
		t.Error("the recorded adapter's hooks Apply never ran — grant-all should still auto-grant the adapter this run is actually recording")
	}
	if !svc.Granted("recorded-adapter", "hooks") {
		t.Error("the recorded adapter's hooks permission reads as not granted")
	}
	if foreignApplied {
		t.Error("the foreign adapter's hooks Apply ran anyway — grant-all wrote a real shared config for an adapter this run was never recording (issue #1769)")
	}
	if svc.Granted("foreign-adapter", "hooks") {
		t.Error("the foreign adapter's hooks permission reads as granted — a hook POST for this adapter would still be accepted by the consent gate and could land in the recording")
	}
}

// TestRecordAdapters_ObserveKindStaysGrantedForEveryAdapter proves the
// restriction does NOT silence a co-resident agent's own transcript/process
// visibility — only the install-a-real-file side effect (and the consent
// state gating its hook endpoint) is narrowed. This is what keeps a
// "multiple-agents-same-workspace" style scenario, where a second agent is
// deliberately present, from losing its OWN file-based signal.
func TestRecordAdapters_ObserveKindStaysGrantedForEveryAdapter(t *testing.T) {
	recordedApplied, foreignApplied := false, false
	recorded := writingAgent("recorded-adapter", &recordedApplied)
	foreign := writingAgent("foreign-adapter", &foreignApplied)

	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:                  []agent.Agent{recorded, foreign},
		Store:                   &mockPermStore{},
		Push:                    &mockPush{},
		Log:                     &mockLogger{},
		Mode:                    config.PermissionModeGrantAll,
		AllowSharedConfigWrites: true,
		Registrar:               &mockRegistrar{},
		RecordAdapters:          []string{"recorded-adapter"},
	})
	svc.Start(context.Background())

	if !svc.Granted("foreign-adapter", "transcripts") {
		t.Error("the foreign adapter's OBSERVE-kind transcripts permission reads as not granted — " +
			"the restriction must not silence a co-resident agent's own file-based visibility")
	}
	if !svc.Granted("recorded-adapter", "transcripts") {
		t.Error("the recorded adapter's transcripts permission reads as not granted")
	}
}

// TestRecordAdapters_UnsetPreservesExistingGrantAllBehavior is a LOCK: with
// RecordAdapters unset (nil), grant-all behaves exactly as it did before
// #1769 — every declared permission is granted and applied, matching a dev
// daemon or any rig caller that has not been updated to pass the new env var.
func TestRecordAdapters_UnsetPreservesExistingGrantAllBehavior(t *testing.T) {
	oneApplied, twoApplied := false, false
	one := writingAgent("agent-one", &oneApplied)
	two := writingAgent("agent-two", &twoApplied)

	svc := services.NewPermissionService(services.PermissionServiceDeps{
		Agents:                  []agent.Agent{one, two},
		Store:                   &mockPermStore{},
		Push:                    &mockPush{},
		Log:                     &mockLogger{},
		Mode:                    config.PermissionModeGrantAll,
		AllowSharedConfigWrites: true,
		Registrar:               &mockRegistrar{},
		// RecordAdapters intentionally omitted (nil).
	})
	svc.Start(context.Background())

	if !oneApplied || !twoApplied {
		t.Fatalf("expected both agents' Apply to run with RecordAdapters unset; applied=%v/%v", oneApplied, twoApplied)
	}
	if !svc.Granted("agent-one", "hooks") || !svc.Granted("agent-two", "hooks") {
		t.Fatal("expected both agents' hooks permission granted with RecordAdapters unset")
	}
}
