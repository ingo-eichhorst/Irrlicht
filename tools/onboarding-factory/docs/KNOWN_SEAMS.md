# Known architectural seams

Cross-cutting daemon bug *shapes* that have already been independently
rediscovered more than once during onboarding-factory runs. Check here before
sinking a full research-plus-live-recording cycle into a new cell's `bug`
verdict — if the symptom matches one of these, it may be one more instance of
an already-understood pattern rather than a fresh investigation.

This is a pointer, not a mandate: recognizing a seam saves the *investigation*
cost, but each cell still gets its own citation-anchored assessment (per
`assess/SKILL.md`'s evidence rule) and, if genuinely new territory, its own
write-up here.

## Presession-identity-rekey

**Shape:** per-session state keyed to the transient presession id
(`proc-<PID>`, before the daemon reconciles it into the real `session_<id>`)
gets dropped instead of carried forward on reconciliation, instead of being
re-keyed to the new id.

Confirmed instances (#1018 retrospective on the mistral-vibe onboarding run):

1. **Offline spec matcher** — #988 → #995
   (`tools/onboarding-factory/internal/validate/expected.go`,
   `session_id_prefix`).
2. **`TerminalObserver` live state** — #997 → #1001
   (`core/application/services/terminal_observer.go`) — a `lastUI` map keyed
   by session id that was never re-keyed. The textbook case of this shape.
3. **`BackchannelEngine` live state** — #1002 → #1007
   (`backchannel_service.go`) — same shape as #997, per that PR's own
   description.

Each was discovered fresh, with no cross-reference until after the fact —
the single biggest cost driver of that onboarding run (three full
research-plus-live-recording cycles for the same underlying bug shape). If a
new adapter's onboarding turns up a symptom that looks like this — a value
that's correct right after a presession is created but goes stale/missing the
moment the daemon reconciles it into the real session — check whether it's a
fourth instance of the same rekey gap before treating it as new.

**NOT this shape**, despite being informally grouped with it during the
mistral-vibe run — mechanistically distinct bugs, each needing its own
write-up if they recur:

- **`fswatcher` cold-start latency** (#996/#998 → #1000/#1011) — pure
  scan-ordering/latency work in `fswatcher/watcher.go` (arm the fsnotify
  watch before the historical backlog scan) plus a session-detector
  catch-up mechanism for a swallowed `ready→working→ready` transition when
  discovery is late. Neither fix touches any rekey path.
- **`processlifecycle` PID-reap** (#906 → #994) — a false-positive-reap
  confirmation gap (a single missed process-list snapshot treated as proof
  of exit), whose symptom fires *before* any real session exists to carry
  state into. #994 itself does not claim to fully resolve #906 — treat #906
  as still open, not a confirmed instance of anything.

## Adding a new seam

A seam earns an entry here once the *same* bug shape has shown up in two or
more independent subsystems (not just two symptoms that merely feel similar —
see the "NOT this shape" list above for what that looks like in practice).
Cite the PRs that fixed each instance and name the shape precisely enough
that a future assessor can pattern-match a new symptom against it in one
read.
