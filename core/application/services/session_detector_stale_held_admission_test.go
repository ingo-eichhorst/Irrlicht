//go:build darwin || linux

package services_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/dsh"
	"irrlicht/core/adapters/inbound/agents/muse"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// Issue #2081: at daemon start, a live but idle codex root was skipped as an
// orphan transcript and stayed invisible until its next write. The rescue for
// stale transcripts (isLiveStaleSession, #576) assumes one live transcript per
// directory plus a cwd it can read. Codex rollouts sit in date directories,
// one managed app-server daemon holds every live root's rollout open, and an
// idle rollout's last 32KB need not name a cwd at all. These tests hold the
// rollouts open with a real helper process (startRolloutHolder) and ask the
// real codex writer probe, as pid_manager_codex_shared_pid_test.go does.

const (
	threadA = "01a12197-2519-75f2-a238-022308ff1721"
	threadB = "01a12065-fe16-7dd0-a249-4f7b5c55f7cc"
	threadC = "01a1181a-0349-7712-8ec0-c750ef098f2e"
	threadD = "01a126a0-82f9-7a13-8638-9dec848ecb6b"
)

// sharedPIDAgents are the adapters that declare a SharedPIDOwner. The detector
// wires their ownership and release probes the way startup.go does, by
// projecting each declaration.
func sharedPIDAgents() []agent.Agent {
	return []agent.Agent{codex.Agent(), dsh.Agent(), muse.Agent()}
}

// newStaleAdmissionDetector wires the shared-PID adapters' probes as
// startup.go does, plus a live-process lookup that reports a process of every
// adapter in cwd. So the cwd rescue gets every chance to admit. When a test
// still sees a skip, a heuristic made it: no process lookup was missing.
func newStaleAdmissionDetector(tw *mockAgentWatcher, repo *mockRepo, git outbound.GitResolver,
	discovers map[string]agent.PIDDiscoverFunc, cwd string, log *mockLogger,
) *services.SessionDetector {
	return services.NewSessionDetector([]inbound.Watcher{tw}, services.SessionDetectorDeps{
		PW: newMockProcessWatcher(), Repo: repo, Log: log, Git: git,
		Metrics: &mockMetrics{}, Version: "test",
		PIDDiscovers:    discovers,
		SharedPIDOwners: agents.SharedPIDOwners(sharedPIDAgents()),
		ReleasedPIDs:    agents.ReleasedPIDs(sharedPIDAgents()),
		ProcessNames:    map[string]string{codex.AdapterName: codex.ProcessName, "claude-code": "claude"},
		LiveCWDs:        liveCWDSet(cwd),
	})
}

// stampAge sets path's mtime age in the past. It runs after the holder has
// opened the file, so "stale" is what the detector sees, whatever the open did.
func stampAge(t *testing.T, path string, age time.Duration) {
	t.Helper()
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// awaitAdmitted polls the repo until sessionID exists, and fails with the
// elapsed time otherwise. Each admission of a stale codex rollout runs one
// real writer probe, hence the probe-sized deadline.
func awaitAdmitted(t *testing.T, repo *mockRepo, sessionID string) {
	t.Helper()
	start := time.Now()
	if !pollUntil(codexWriterDeadline, func() bool {
		_, err := repo.Load(sessionID)
		return err == nil
	}) {
		t.Fatalf("after %v session %s was not admitted",
			time.Since(start).Round(time.Millisecond), sessionID)
	}
}

// requireSkipped asserts sessionID was not admitted. Call it only after a
// later event on the same watcher has been seen admitted: the detector handles
// one watcher's events in order, so the earlier event has been decided.
func requireSkipped(t *testing.T, repo *mockRepo, sessionID, why string) {
	t.Helper()
	if _, err := repo.Load(sessionID); err == nil {
		t.Fatalf("session %s was admitted: %s", sessionID, why)
	}
}

func rolloutPath(dir, stamp, thread string) string {
	return filepath.Join(dir, "rollout-"+stamp+"-"+thread+".jsonl")
}

func codexEvent(sessionID, path string) agent.Event {
	return agent.Event{Type: agent.EventNewSession, SessionID: sessionID,
		ProjectDir: filepath.Base(filepath.Dir(path)), TranscriptPath: path}
}

func codexWatcher() *mockAgentWatcher {
	return newMockAgentWatcher().withIdentity(agent.Identity{Name: codex.AdapterName})
}

// Red-first (#2081): two stale roots in one date directory, both held open by
// one helper process, the managed daemon's shape. The cwd rescue can read the
// cwd and finds a live codex there, so on the unfixed code it admits the newest
// rollout and skips the other as a ghost sibling.
func TestSessionDetector_StaleHeldCodexRootsInOneDateDirAreAdmitted(t *testing.T) {
	cwd := rescueCWD(t)
	day := filepath.Join(t.TempDir(), "sessions", "2026", "10", "09")
	older := rolloutPath(day, "2026-10-09T18-55-16", threadA)
	newer := rolloutPath(day, "2026-10-09T13-21-57", threadB)
	now := time.Now()
	writeRollout(t, older, now)
	writeRollout(t, newer, now)
	startRolloutHolder(t, older, newer)
	stampAge(t, older, 10*time.Minute)
	stampAge(t, newer, 5*time.Minute)

	repo := newMockRepo()
	tw := codexWatcher()
	runDetector(t, newStaleAdmissionDetector(tw, repo, &cwdGit{cwd: cwd}, codexPIDDiscoverers(), cwd, &mockLogger{}))

	tw.ch <- codexEvent(threadA, older)
	tw.ch <- codexEvent(threadB, newer)
	awaitAdmitted(t, repo, threadB)
	awaitAdmitted(t, repo, threadA)
}

// Red-first (#2081): the 01a1181a shape. A held stale root is the only file in
// its date directory and a live codex runs in its cwd, but the transcript
// yields no cwd: GetCWDFromTranscript reads only the last 32KB, and an idle
// codex rollout's tail can carry none (see the PR for the reconstruction of
// 01a1181a's rollout as it stood when the daemon skipped it).
func TestSessionDetector_StaleHeldCodexRootWithoutReadableCWDIsAdmitted(t *testing.T) {
	cwd := rescueCWD(t)
	day := filepath.Join(t.TempDir(), "sessions", "2026", "10", "07")
	lone := rolloutPath(day, "2026-10-07T22-42-00", threadC)
	writeRollout(t, lone, time.Now())
	startRolloutHolder(t, lone)
	stampAge(t, lone, 3*time.Minute)

	repo := newMockRepo()
	tw := codexWatcher()
	runDetector(t, newStaleAdmissionDetector(tw, repo, &mockGit{}, codexPIDDiscoverers(), cwd, &mockLogger{}))

	tw.ch <- codexEvent(threadC, lone)
	awaitAdmitted(t, repo, threadC)
}

// Red-first (#2081): a paginated thread as 01a1181a stood on 2026-10-11. Its
// first rollout is no longer held, and the daemon holds only the newest
// segment, which shares its date directory with a newer root. The watcher
// walks 2026/10/07 before 2026/10/10, so the old segment arrives first. On the
// unfixed code the old segment is skipped for want of a cwd and the held
// segment for not being newest, so the thread is invisible. Lock: once
// admitted on the held segment, a later event from the old one does not move
// the session back.
func TestSessionDetector_StaleHeldCodexSegmentIsAdmittedOnTheSegment(t *testing.T) {
	cwd := rescueCWD(t)
	month := filepath.Join(t.TempDir(), "sessions", "2026", "10")
	oldSeg := rolloutPath(filepath.Join(month, "07"), "2026-10-07T22-42-00", threadC)
	heldSeg := filepath.Join(month, "10", "rollout-2026-10-10T17-19-15-"+threadC+"_01a12665-9cc8-7d70-9842-833231c6622e.jsonl")
	newerRoot := rolloutPath(filepath.Join(month, "10"), "2026-10-10T18-23-35", threadD)
	now := time.Now()
	for _, p := range []string{oldSeg, heldSeg, newerRoot} {
		writeRollout(t, p, now)
	}
	startRolloutHolder(t, heldSeg, newerRoot)
	stampAge(t, oldSeg, 40*time.Minute)
	stampAge(t, heldSeg, 20*time.Minute)
	stampAge(t, newerRoot, 10*time.Minute)

	repo := newMockRepo()
	tw := codexWatcher()
	runDetector(t, newStaleAdmissionDetector(tw, repo, &mockGit{}, codexPIDDiscoverers(), cwd, &mockLogger{}))

	tw.ch <- codexEvent(threadC, oldSeg)
	tw.ch <- codexEvent(threadC, heldSeg)
	awaitTranscriptPath(t, repo, threadC, heldSeg)

	tw.ch <- codexEvent(threadC, oldSeg)
	tw.ch <- codexEvent(threadD, newerRoot)
	awaitAdmitted(t, repo, threadD)
	if got := repo.transcriptPathOf(threadC); got != heldSeg {
		t.Fatalf("a late event from the released segment moved the session to %q, want %q", got, heldSeg)
	}
}

// Lock (#2081): a stale root that no process holds is still skipped. It is not
// the newest file in its directory, so the cwd rescue declines it too. That
// rescue is the behaviour #576 put in place, and this change leaves it as it
// was. A newer held root is the sentinel showing the skip was decided.
func TestSessionDetector_StaleUnheldCodexRootIsStillSkipped(t *testing.T) {
	cwd := rescueCWD(t)
	day := filepath.Join(t.TempDir(), "sessions", "2026", "10", "09")
	unheld := rolloutPath(day, "2026-10-09T18-55-16", threadA)
	held := rolloutPath(day, "2026-10-09T13-21-57", threadB)
	now := time.Now()
	writeRollout(t, unheld, now)
	writeRollout(t, held, now)
	startRolloutHolder(t, held)
	stampAge(t, unheld, 10*time.Minute)
	stampAge(t, held, 5*time.Minute)

	repo := newMockRepo()
	tw := codexWatcher()
	runDetector(t, newStaleAdmissionDetector(tw, repo, &cwdGit{cwd: cwd}, codexPIDDiscoverers(), cwd, &mockLogger{}))

	tw.ch <- codexEvent(threadA, unheld)
	tw.ch <- codexEvent(threadB, held)
	awaitAdmitted(t, repo, threadB)
	requireSkipped(t, repo, threadA, "no process holds its rollout open and it is not the newest in its directory")
}

// Lock (#2081): the writer probe never admits a held stale subagent rollout.
// The managed daemon was seen holding a subagent rollout long after its
// parent's rollout was released (see staleTranscriptHolder), so a held
// subagent is no evidence of a live parent. The #576 cwd rescue it falls
// through to is unchanged and can still admit a codex subagent, as a child,
// when it is the newest rollout in its directory. Here it is not, so the rescue
// declines it. The cwd is readable so that the sentinel root is admitted by
// that rescue as well, which keeps this a lock on the unfixed code too.
func TestSessionDetector_StaleHeldCodexSubagentIsNotAdmitted(t *testing.T) {
	cwd := rescueCWD(t)
	day := filepath.Join(t.TempDir(), "sessions", "2026", "10", "10")
	sub := rolloutPath(day, "2026-10-10T00-18-16", "01a122be-de9f-77d3-9203-48c45ff83bbc")
	root := rolloutPath(day, "2026-10-10T18-23-35", threadD)
	now := time.Now()
	writeRollout(t, sub, now)
	writeRollout(t, root, now)
	startRolloutHolder(t, sub, root)
	stampAge(t, sub, 10*time.Minute)
	stampAge(t, root, 5*time.Minute)

	repo := newMockRepo()
	tw := codexWatcher()
	runDetector(t, newStaleAdmissionDetector(tw, repo, &cwdGit{cwd: cwd}, codexPIDDiscoverers(), cwd, &mockLogger{}))

	subEv := codexEvent("01a122be-de9f-77d3-9203-48c45ff83bbc", sub)
	subEv.ParentSessionID = threadA
	tw.ch <- subEv
	tw.ch <- codexEvent(threadD, root)
	awaitAdmitted(t, repo, threadD)
	requireSkipped(t, repo, subEv.SessionID, "a held subagent rollout says nothing about its parent")
}

// Lock (#2081): an adapter that declares no SharedPIDOwner is never asked who
// holds its transcript, and its stale transcript still meets the cwd rescue
// alone. Claude Code writes and closes. Here its discovery names a pid for the
// older sibling, so asking at admission would admit the sibling the rescue
// declines.
func TestSessionDetector_StaleClaudeTranscriptIsNotProbedForAWriter(t *testing.T) {
	cwd := rescueCWD(t)
	project := t.TempDir()
	older := filepath.Join(project, "older.jsonl")
	newer := filepath.Join(project, "newer.jsonl")
	writeOldTranscript(t, older, 10*time.Minute)
	writeOldTranscript(t, newer, 5*time.Minute)

	stub := newPathDiscovery(older)
	discovers := map[string]agent.PIDDiscoverFunc{"claude-code": stub.discover}

	repo := newMockRepo()
	tw := newMockAgentWatcher()
	runDetector(t, newStaleAdmissionDetector(tw, repo, &cwdGit{cwd: cwd}, discovers, cwd, &mockLogger{}))

	tw.ch <- agent.Event{Type: agent.EventNewSession, SessionID: "older", ProjectDir: "p", TranscriptPath: older}
	tw.ch <- agent.Event{Type: agent.EventNewSession, SessionID: "newer", ProjectDir: "p", TranscriptPath: newer}
	awaitAdmitted(t, repo, "newer")
	requireSkipped(t, repo, "older", "the cwd rescue declines a stale sibling that is not the newest")
	if stub.wasAsked(older) {
		t.Fatalf("claude-code's PID discovery was asked about the stale transcript it does not hold open")
	}
}

// Lock (#2081): a writer probe that could not run admits nothing by itself,
// and says so in the log, so events.log tells "could not ask" apart from "no
// writer". The rollout is not the newest in its directory, so the cwd rescue
// declines it as before.
func TestSessionDetector_StaleCodexRootWhoseProbeFailedIsSkippedAndLogged(t *testing.T) {
	cwd := rescueCWD(t)
	day := filepath.Join(t.TempDir(), "sessions", "2026", "10", "09")
	older := rolloutPath(day, "2026-10-09T18-55-16", threadA)
	newer := rolloutPath(day, "2026-10-09T13-21-57", threadB)
	writeRollout(t, older, time.Now().Add(-10*time.Minute))
	writeRollout(t, newer, time.Now().Add(-5*time.Minute))

	probeErr := errors.New("lsof could not run")
	discovers := map[string]agent.PIDDiscoverFunc{
		codex.AdapterName: func(string, string, func([]int) int) (int, error) { return 0, probeErr },
	}
	log := &mockLogger{}
	repo := newMockRepo()
	tw := codexWatcher()
	runDetector(t, newStaleAdmissionDetector(tw, repo, &cwdGit{cwd: cwd}, discovers, cwd, log))

	tw.ch <- codexEvent(threadA, older)
	tw.ch <- codexEvent(threadB, newer)
	awaitAdmitted(t, repo, threadB)
	requireSkipped(t, repo, threadA, "a probe that could not run is no evidence of a writer")
	if !slices.ContainsFunc(log.infoSnapshot(), func(line string) bool {
		return strings.Contains(line, "could not ask which process holds it open") && strings.Contains(line, probeErr.Error())
	}) {
		t.Fatalf("no log line says the writer probe could not run; info lines: %q", log.infoSnapshot())
	}
}

// Lock (#2081): without the adapter's observe consent no writer probe runs at
// admission, as with the shared-PID probes. The discovery stub names a pid for
// the stale rollout, so a probe would admit what the cwd rescue declines. A
// fresh rollout is the sentinel, admitted without a probe.
func TestSessionDetector_StaleCodexRootIsNotProbedWithoutConsent(t *testing.T) {
	cwd := rescueCWD(t)
	day := filepath.Join(t.TempDir(), "sessions", "2026", "10", "09")
	stale := rolloutPath(day, "2026-10-09T18-55-16", threadA)
	fresh := rolloutPath(day, "2026-10-09T13-21-57", threadB)
	writeRollout(t, stale, time.Now().Add(-10*time.Minute))
	writeRollout(t, fresh, time.Now())

	stub := newPathDiscovery(stale)
	discovers := map[string]agent.PIDDiscoverFunc{codex.AdapterName: stub.discover}
	repo := newMockRepo()
	tw := codexWatcher()
	det := newStaleAdmissionDetector(tw, repo, &cwdGit{cwd: cwd}, discovers, cwd, &mockLogger{})
	det.SetConsentGate(func(adapter string) bool { return adapter != codex.AdapterName })
	runDetector(t, det)

	tw.ch <- codexEvent(threadA, stale)
	tw.ch <- codexEvent(threadB, fresh)
	awaitAdmitted(t, repo, threadB)
	requireSkipped(t, repo, threadA, "codex's observe consent is not granted, so no probe may admit it")
	if stub.wasAsked(stale) {
		t.Fatalf("the writer probe ran for an adapter without observe consent")
	}
}

// pathDiscovery is a PID discovery stub that names this test process for one
// path and nobody for any other, and records every path it is asked about.
// Naming a pid for one path only matters: an admitted stale session is then
// the only holder of its pid, so no same-PID sweep removes it before the test
// looks. With one pid for every path, the stale session was admitted and then
// retired as a same-PID duplicate before requireSkipped ran, which hid the
// admission (seen in the consent mutation of
// tools/lib/codex-held-rollout-admission-mutations_test.sh).
type pathDiscovery struct {
	held  string
	mu    sync.Mutex
	asked []string
}

func newPathDiscovery(held string) *pathDiscovery { return &pathDiscovery{held: held} }

func (p *pathDiscovery) discover(_, transcriptPath string, _ func([]int) int) (int, error) {
	p.mu.Lock()
	p.asked = append(p.asked, transcriptPath)
	p.mu.Unlock()
	if transcriptPath == p.held {
		return os.Getpid(), nil
	}
	return 0, nil
}

func (p *pathDiscovery) wasAsked(path string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.asked, path)
}

// Lock (#2081): dsh and muse declare a SharedPIDOwner as well, and their writer
// probe asks about the session's sibling lock file rather than the transcript
// (dsh/pid.go, muse/pid.go). A stale session whose lock a live process holds is
// admitted. The transcript yields no cwd, so the #576 rescue would decline it.
// These cases pass by construction; mutation 5 of
// tools/lib/codex-held-rollout-admission-mutations_test.sh, which stops asking
// any adapter but codex, is their red evidence.
func TestSessionDetector_StaleHeldLockSessionsAreAdmitted(t *testing.T) {
	for _, tc := range []struct {
		adapter          string
		transcript, lock string
	}{
		{dsh.AdapterName, "session.v3.jsonl.zstd", "session.lock"},
		{muse.AdapterName, "session.jsonl", ".session.lock"},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			cwd := rescueCWD(t)
			const id = "01a1181a-0000-7000-8000-000000000001"
			dir := filepath.Join(t.TempDir(), "sessions", id)
			transcript := filepath.Join(dir, tc.transcript)
			lock := filepath.Join(dir, tc.lock)
			writeRollout(t, transcript, time.Now())
			writeRollout(t, lock, time.Now())
			startRolloutHolder(t, lock)
			stampAge(t, transcript, 10*time.Minute)

			repo := newMockRepo()
			tw := newMockAgentWatcher().withIdentity(agent.Identity{Name: tc.adapter})
			runDetector(t, newStaleAdmissionDetector(tw, repo, &mockGit{},
				agents.PIDDiscoverers(sharedPIDAgents()), cwd, &mockLogger{}))

			tw.ch <- agent.Event{Type: agent.EventNewSession, SessionID: id,
				ProjectDir: filepath.Base(dir), TranscriptPath: transcript}
			awaitAdmitted(t, repo, id)
		})
	}
}
