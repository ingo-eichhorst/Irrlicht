# Irrlicht — Development Guide

## Short Cuts

- Issue execution is handled entirely by the `ir:exec` skill:
  `/ir:exec [mode] <N>` (mode defaults to `auto`) — see its "Modes" section
  (`.claude/skills/ir:exec/SKILL.md`) for what each mode does.
- NEVER RUN: the Workflow tool (multi-agent orchestration) if not explicitly requested (too expensive)

## Process Rules

Worktrees share the parent repo's `.git` dir, so **`git stash` is not isolated per worktree** — it's a single shared stack. Concurrent agents stashing in different worktrees can pop each other's WIP. Use a local commit as a checkpoint instead (`git commit -m wip`, amend/reset later).
When encountering suboptimal processes or issues make improvment suggestions in you final answer to the user.

**Dismissals carry evidence.** Any claim whose function is *"you don't need to look
here"* — *already fixed in #X*, *self-heals*, *likely benign*, *non-issue* — gets the
same bar as the claim it supports: cite what you ran or read, or mark it assumed. This
binds wherever a finding is written down (issue bodies, PR descriptions, triage
comments, final answers), because a wrong dismissal is the one kind of error that stops
anyone from checking it. Marking works: in #1088 every claim explicitly labelled
"unverified" was investigated and came back not real, while that issue's one real bug
hid in an unmarked aside.

## Build Artifacts

Use `./.build` for build artifacts.

## Building the daemon

- **Release builds**: `tools/build-release.sh` reads the base version from
  `version.json` and produces signed universal binaries + the installer.
  The binary's `--version` output is the bare version (e.g. `0.3.13`) so
  release tags stay clean.
- **Dev builds**: `tools/build-dev.sh` produces a native binary at
  `core/bin/irrlichd` with a version string like `0.3.13+1f702e7.dirty`
  — base version plus short SHA plus `.dirty` if the worktree has
  uncommitted changes. `+` is semver build metadata so dev binaries never
  compare as "newer" than their base release.
- The string is computed by `tools/version.sh` (pass `--base` for the
  bare version.json value). `promote-recording.sh` captures it into
  archive and top-level `manifest.json` files so the viewer's metadata
  panel shows which dev build produced each recording.

## Key Conventions

- Go code follows hexagonal architecture: `domain/` → `ports/` → `adapters/` → `application/services/`
- Three session states only: `working`, `waiting`, `ready` — no cancelled state
- Errors are logged via `Logger` interface, not propagated with `fmt.Errorf`
- Child sessions (subagents and background agents) use `ParentSessionID` for parent-child linking
- Adapters declare `Permissions` on `agent.Agent` (with Apply/Remove effect closures); every read or modification an adapter performs must be consent-gated behind one of its declared permissions — nothing is exercised while pending or denied
- An adapter reading an agent-owned SQLite store (hermes, opencode, antigravity)
  opens it through `core/pkg/sqlitero`, never `sql.Open` directly. The DSN
  grammar decides whether "read-only" is real, and every wrong spelling reads
  identically to the right one: a bare path drops `mode=ro` entirely, an
  unescaped path can be truncated into a *different* file, and `_journal=WAL`
  is a write. All three shipped at some point; that package is where the
  evidence and the regression tests live.

## Testing

**A test earns its place by having been seen fail.** A green that was never red is a
claim, not evidence. How you obtain that red depends on what the test is for.

**A defect test proves nothing until it has been seen red.** Run it before the fix
exists, confirm it fails, paste the failure. A test that passes on `main` means either
the diagnosis is wrong or the test doesn't reach the defect (a stub blind to the
asserted field is the classic) — stop and report rather than shipping the green. Locks
— tests pinning behavior that must *not* change — pass by construction; say which ones
those are. `ir:exec` enforces this at Phase 4 step 11a; it binds outside `ir:exec` too.

**Anything a change *adds* has no "before the fix" to run against, and owes a
deliberate mutation instead.** A new guard, a static `architecture_test.go` rule, a
linter or registry tripwire, a derived count or score, a schema constraint, a data
migration, a config rewriter, a contract assertion — every one of them passes the
moment it is written, which is exactly the condition the red-first rule exists to
prevent. So break the thing being protected — violate the invariant, perturb the
derived number, corrupt a migrated value — and confirm the new check goes red. If
nothing does, it does not reach what it claims to cover, and that is the same
stop-and-report as a defect test that passes on `main`. This includes a **lock the
change itself adds**: "passes by construction" is the reason the mutation is needed,
not an exemption from it — the Locks sentence above is about locks over behavior that
predates the change, where there is nothing new to prove reaches anything. Four agents
in one fleet run arrived at this gap from four directions — a new guard (#1366),
derived numbers (#1363), a data migration (#1367), a layering rule (#1391) — each
because a reviewer, or the agent itself, ran a mutation nobody had asked for.

**Prefer committing that mutation to describing it.** `tools/lib/testdata/posix-lint/`'s
deliberately-broken fixtures are the shape ("committed rather than improvised so the
mutation evidence outlives the PR"), and `TestSourceScanCatchesEveryKnownShape`
(`core/application/services/construction_test.go`) is the same idea as a corpus.
Evidence living only in a merged PR body is re-run by nothing; for the
`contracttesting` families #1479 committed it beside each assertion, in
`core/internal/contracttesting/<family>_selftest_test.go` — the paragraph
closing the contract-family bullets below carries the shape and its limits. And a guard that *rewrites* an existing one owes
its predecessor's cases as locks on top of its own — "Guarded construction" below
carries that rule and the incident that earned it.

**A verification mechanism must fail loudly when it cannot run.** Absence of a finding
and inability to look must never produce the same output: "the thing under test never
executed" is the most expensive way to fail, because it is indistinguishable from
success. Wherever a check greps, matches, mutates, shells out or waits on a readiness
signal, assert that the operation actually happened — not merely that it reported
nothing. The guard is one line each time. Three of them caught something real the
moment they were added: `posix-lint.sh` refusing rather than skipping when it finds no
POSIX shell, no static linter, or no files, after its first draft printed `ALL PASS`
over an installer carrying a deliberate `[[ ]]` (below); a mutation harness asserting
its mutation changed the file, which then caught two more stale mutations (#1390); and
an e2e test waiting on a signal narrower than "the daemon published its addr file",
which fires *before* the consent effects under test run, so a deliberately-broken
binary came back green (#1449; `ir:exec` Phase 4 step 11 carries the recipe). Two more
were added before they could catch anything and carry the weaker evidence that they
*can* fire: the architecture corpus asserting every case still contains the construct
it plants (below), and the harness built on #1390's lesson from the start, carrying a
deliberate no-match row that must report `STALE` (#1450).

**A fixture that waits by SLEEPING has not observed what it waits for, and the
assertion after the sleep is not evidence that it has.** Poll the condition to a
generous deadline and fail with the elapsed time: that turns "the machine was busy"
into a slower pass while a thing that genuinely never happens still fails loudly —
the property a longer sleep weakens. #1586's tmux fixture is the shape, and the
reason to MEASURE rather than reason about which condition is pending: it slept a
fixed 120ms and then hard-failed unless the helper had been reparented to init, and
those were **not the same condition**. Measured over 40 runs, the reparenting was
already complete on the first `ps` every time, while the env the test actually reads
was readable on the first sysctl *none* of the time (~1.2ms p50) — it only lands with
the exec, since until then the pid is still the Apple-signed, env-stripped `/bin/sh`.
So the sleep was covering a condition nothing checked, the hard-fail was checking one
that never failed for its stated reason, and polling only the checked one would have
replaced a 120ms margin with the duration of one `ps`. The poll is
`awaitFixtureCondition` (`processlifecycle/osutil_darwin_test.go`); each caller carries
a vacuity guard, because a fixture handed nothing to wait for reports ready having read
nothing. This is a rule about the shape, not a known-flake register: after the fix that
test is not expected to flake, and recording it as one would be a dismissal that stops
the next agent looking.

**And a fixture must observe the SUBJECT, never a side effect the subject produces on
its way out.** Polling is the second half of the rule above and not the whole of it:
#1616 fell through the gap. `gate-budget_test.sh` asked whether a killed process tree
had survived by looking for a *marker file* its innermost `sleep 30; echo … >marker`
would write — so "it was interrupted" and "it survived" could write the same evidence,
and an ordinary preemption between the two `kill`s of a depth-first walk let the shell
reap its dead `sleep` and run the `echo`. Someone following the paragraph above would
have polled the marker and kept an ambiguous fixture. Two measurements make the point
sharper than the flake did. The marker assertion could only fire at t+30 while the case
ended at t+3, so it never carried the property at all — the survivor count did. And
sparing the deepest process left the old case passing all three of its assertions while
a `sleep 30` genuinely outlived its bound. The fix is structural rather than a longer
wait: the fixture's leaf `exec`s its sleep, so there is no next command and no
mid-transition state, and survival is read as "does this pid exist" and polled to a
deadline that fails with the surviving pids. Reproduced 1-in-600 naturally under load,
and deterministically at 100% by injecting ~400µs at the identified point.

**A validator that cannot parse its input checks MORE, never less.** An input it
cannot read with confidence is neither a quiet pass nor a skip: it is the case where
the validator has the least idea what it is looking at, so it is the last place to
drop checks. `skill-lint.sh`'s fence and frontmatter checks exist for exactly that
reason (below) — skipping is how it tells "documents a marker" from "has one", and an
unbalanced delimiter would otherwise silence every check after it.

**Code that emits bytes from a structural diff gets a property test.** Anything that
computes an edit and writes the result — config rewriters, formatting-preserving
serializers, patchers, migrators — is tested by generating random inputs and random
mutations and asserting the output round-trips, not only by hand-written cases, which
encode what the author already thought of and are therefore the same set they got
right. `hookjson`'s splicer shipped with seven green round-trip tests and a defect
writing `,,` into ~11% of randomly shaped documents, because all seven removed the
*tail* of a container — the one position where the arithmetic was correct.
`TestSplice_PropertyRandomMutations`
(`core/adapters/inbound/agents/hookjson/jsonc_test.go`) is the shape to copy: a fixed
seed so a failure reproduces, the document *and* the mutation printed in the failure
message, and a committed iteration count small enough to stay in the suite (2000,
0.07s) with a much larger sweep across several seeds run locally before landing. Such
a PR says two things out loud. **Which structural axes the generator varies** — one
that varies only the axis you thought of is the same vacuous green wearing a different
hat: that test's first draft mutated only object members, so it never produced a
removal run longer than one item and never touched an array, while multi-item removals
inside an array are exactly what the production uninstall path performs
(`hooks[event]`, seven events removed in one pass). A fourth defect survived until the
generator was widened. And **which properties survive which mutation** — "every
comment is preserved" is false for a deletion, since the deleted subtree's comments
go with it, and asserting it anyway produces false failures that erode the test.

Before marking a ticket done, run the full suite — every layer must pass:

- Unit + e2e: `go test ./core/... -race -count=1` (includes the headless
  daemon startup smoke test — boots a real `irrlichd` on an ephemeral port
  under `t.TempDir()`, so it never touches the production daemon).
- Architecture: `core/architecture_test.go` (runs automatically as part of
  `go test ./core/...`) statically enforces the hexagonal import direction
  from Key Conventions — `domain/` and `ports/` packages may not import
  outward into `adapters/` or `application/`, `application/services/`
  may only reach `adapters/inbound/` through `ports/`, and `pkg/` — the
  shared leaf layer depended on from domain, adapters, application and
  cmd alike — may not import `adapters/` or `application/` at all. It checks
  **direct** imports only, so a rule constrains the edges a package declares,
  not what those edges drag in transitively. `pkg/` was unbound until #1391,
  where the natural fix for a decode shared between `pkg/tailer` and the
  `hookjson` adapter was an import that no rule in the table forbade; the
  shared code went to a new leaf (`core/pkg/jsonc`) and the missing rule was
  added with it.
  A second architecture rule lives beside it in
  `core/architecture_hookbody_test.go` (#1389) and is deliberately a separate
  file: the layering table asks a question about the IMPORT GRAPH, while that
  one asks which EXPRESSIONS a function may contain, and needs
  `NeedSyntax|NeedTypes|NeedTypesInfo` over a narrow pattern rather than
  `NeedName|NeedImports` over the module. It enforces that inside
  `core/adapters/inbound/agents/...` an inbound `*http.Request`'s body may be
  read only by `hookjson.DecodeConfined` — see "Hook path confinement" below
  for why. Its corpus is `core/architecture_hookbody_shapes_test.go`: one file
  per spelling (decoder in a variable, `io.ReadAll`, an aliased body, a helper
  in another file, `r.FormValue`, a request stashed in a struct field) pinned
  to the verdict the detector must return, plus two `want:false` cases —
  `*http.Response.Body` and `r.Method` — that pin the false positives a
  name-based rule would produce. Every case asserts the construct it plants is
  actually present in its own source before any verdict is checked, because a
  corpus that quietly stops containing its own test cases reads as a pass.
- Architecture score: `tools/ars-gate.sh` flags it when the Agent Readiness
  Score (composite or any category) regresses vs `origin/main` — advisory,
  not a merge gate: it runs as a PR check (`.github/workflows/ars-gate.yml`,
  not required by branch protection) and is mirrored locally by
  `tools/preflight.sh`'s `arch` gate (see "Local CI parity" below). A
  red result is a prompt to look closer, not a block — use judgment on
  whether the regression is worth addressing before merging. Deterministic
  and workflow-agnostic: it fires on any push, not tied to a specific agent
  skill.
- Code health: CodeScene posts a "CodeScene Code Health Review" check on every
  PR automatically (via the CodeScene GitHub App, configured on codescene.io
  project 82148 — not a workflow in this repo). Like the ARS score, it's
  advisory, not a merge gate: neither branch protection nor the "Protect
  Main" ruleset requires it to pass. A red result is a prompt to look
  closer, not something to chase to green before merging or releasing. The
  README's CodeScene badge shows the live score, auto-refreshed on every
  push to `main` by `.github/workflows/codescene-badge.yml`. For concrete,
  file:line-level findings (rule, message, fix effort) rather than a
  hotspot/trend view, run `/ir:sonarqube-report`, which reads SonarQube
  Cloud's issue list via `tools/sonarqube-report.sh` (needs `SONAR_TOKEN`
  in a local `.env` — see `.env.example`).
- Permission gating: `contracttesting.AssertPermissionGated` (`core/internal/contracttesting/permission_gate.go`)
  is the behavioral counterpart to the architecture test — it can't be checked
  statically because gating happens at runtime, by an adapter (or the shared
  services layer) choosing to call `PermissionService.Granted`/`ObserveGranted`
  before a read/write, or by wiring a permission's `Apply`/`Remove` closures.
  New adapters should wire it into their test suite for every `modify`-kind
  permission they declare — see `claudecode`'s hooks/statusline (a live
  per-request `ConsentGate`), `claudecode`'s instructions and `processlifecycle`'s
  kitty remote-control (install-type `Apply`/`Remove`), and `InputService`'s
  backchannel forwarding (the shared "control" gate) for the three call-site
  shapes it covers.
  For a **hook receiver** the key set is no longer the wiring's to name. Since
  #1488 a receiver declares its permissions to `hookjson.NewHandler` (via
  `hookjson.RequireConsent`, whose first key is positional — a keyless receiver
  does not compile), the handler publishes them as `HookHandler.Consent`, and
  `contracttesting.AssertHookReceiverPermissionGated` derives one arm per
  declared key. A receiver that later honours a third permission is graded on it
  by existing rather than by someone remembering — which is what #1475 could
  assert but not enforce, copilot's receiver having reached #1475 with no wiring
  at all. Three things to know before touching it:
  - `DecodeConfined` re-checks the whole declared set and fails closed, so a
    receiver that declares a permission and never checks it drops the payload
    instead of dispatching. That is #1466's shape, removed rather than detected.
    It refuses **before** reading the body and before the confine, so a denied
    request costs no decode, no path resolution and no confiner counter —
    asserted by `TestDecodeConfined_RefusesBeforeReadingAnything`, because
    deleting that guard and merely *moving* it are different mutations and only
    the first reddens the others.
  - **The backstop quietens every per-adapter consent mutation that graded
    DISPATCH, so what those tests assert had to move to the one thing it does
    NOT reproduce: silence.** Deleting a receiver's own `transcripts` gate used
    to fail `…/transcripts/gated_on_the_named_key` plus that adapter's #1466
    defect test; after #1488 the chokepoint drops the same payload, so every
    dispatch-shaped assertion stays green (measured on all four receivers —
    claudecode's hooks and statusline, codex, copilot). It is not silent about
    it: reaching the backstop means a receiver skipped a check or consent was
    revoked mid-request, so it logs an error, where a receiver's own gate
    answers a quiet 200. That difference is both the surviving discriminator
    and a real user-facing property — an ordinary denied session must not
    collect an error line per tool call — so each of the four receivers' hand-
    written consent tests now asserts **no error was logged**, and each was
    seen red again with its gate deleted. Statusline is the receiver to watch
    here: it declares ONE permission and keeps no second gate, so that
    assertion is the whole of its live per-adapter proof.
  - Beside those four, the coverage is one shared proof plus one lock per
    adapter: `hookjson/consent_test.go`'s committed `forgetfulReceiver`
    (declares two keys, checks one) grades the backstop for every receiver at
    once, and `AssertDeclaredPermissions` / each adapter's
    `Test…DeclaresItsPermissions` replays the key list its wiring used to spell
    out, so the predecessor's cases are not deleted along with it (#1450).
    Declaring the WRONG set reddens both.
  - What did not move is the ORDER — but note exactly how much of it is held.
    The channel key must stay ahead of `hookjson.ObserveHookReceipt`, and that
    half IS enforced: deleting the `hooks` check, or hoisting the receipt above
    it, reddens `AssertHookReceiptObserved`'s
    `consent_denied_request_is_not_counted` (measured both ways). The other
    half — the transcript key staying BEHIND the receipt, so a hooks-granted /
    transcripts-denied install is not reported dead by #1368's watchdog — is
    held by nothing: hoisting that check above the receipt is green here and
    was green before #1488 too, so it is a standing gap rather than a
    regression, and it is stated here rather than left implied by the sentence
    above it.
  `HookReceiverGate.Foreign` exists for the one receiver declaring a SINGLE
  permission — claudecode's statusline — where the derived set leaves nothing to
  hold open and the driver refuses rather than running a #1475 isolation arm
  that proves nothing.
  A wiring names the permission under test (`Key`) and at least one the call
  site must NOT be gated on (`OtherKeys`), and `SetState` takes the key it is
  driving — because until #1475 it did not, and every live-gate wiring supplied
  a `Granted(_, _ string) bool` fake that discarded the key. One permission
  moved through three states says only that a call site is gated on
  *something*: a receiver gated on the WRONG permission answers identically,
  and that is not hypothetical — claudecode's hook receiver read transcripts
  with `transcripts` denied for the whole of its life (#1466) while this
  contract was wired at that receiver and green. The fourth arm holds `Key`
  denied while `OtherKeys` are granted, the one state that tells those apart,
  and it denies `Key` **first**: a wiring that still ignored its key would end
  up holding everything granted and fail, where the reverse order would let it
  settle at "denied" and pass. Use `contracttesting.ConsentGate` for a live
  per-request gate rather than a local fake — it replaces the three key-blind
  mutable fakes those wirings had each grown (claudecode's and codex's
  `mutableGate`, the services layer's `mutableConsent`), because a contract
  whose wiring supplies the fake is a contract whose wiring can supply a
  key-blind one. The static `keyedGate` map literals in the adapter test
  packages are a different thing and stay: they pin a fixed two-permission
  combination in one-off tests and were never key-blind. A call site gated on
  more than one permission wires `AssertPermissionGatedOnEachKey` rather than a
  hand-written table pairing each key with "the other one" — it names its key
  set once and the pairing is derived, because such a table can silently list
  only one direction and the direction that already works is
  indistinguishable from covering both. A hook receiver is the exception and
  names no set at all: since #1488 it wires
  `AssertHookReceiverPermissionGated`, which derives the set from the
  receiver's own declaration (the bullet above). Install-type wirings use
  `contracttesting.OnlyKey` instead of re-deciding per adapter that a foreign
  key is a no-op. The arm is
  load-bearing only for live per-request gates; for an install-type permission
  the wiring holds that permission's own closures, so a wrong key is not
  representable and the arm is weak by construction — and where the other key
  has no closure at all (an observe-kind sibling, a single-permission
  declaration) it is inert and repeats the revoked arm, which those three call
  sites say out loud. It is kept uniform with no opt-out anyway, because a flag
  an install-type wiring could take is one a live-gate wiring could take too.
  All of which makes the obligation *assertable*, not unforgettable: a receiver
  still has to be wired once per permission it must honour (#1488 is the
  chokepoint move that would remove that remaining act of memory).
- Hook endpoints: `contracttesting.AssertHookEndpointFollowsBindAddr`
  (`core/internal/contracttesting/hook_endpoint.go`) is the same kind of
  runtime obligation for adapters that install hooks into a JSON config —
  an install writes the resolved port not `:7837`, an entry left by a daemon
  on another port is rewritten in place rather than duplicated, and uninstall
  is not port-scoped (#1178). A new hook-installing adapter wires one call
  (see `claudecode`/`codex`/`copilot` `hookport_test.go`) instead of porting a
  test file. It grades against the delivery route the adapter DECLARES
  (`HookInstaller.Delivery`, #1453), because there are two ways to satisfy it
  and the second makes the first unsatisfiable. `DeliveryURL` — the zero value,
  and all three adapters above — is an entry that CARRIES the daemon's address.
  `DeliveryAddressFree` is an entry that carries none, because it names the
  `irrlichd hook-post` beacon (`core/pkg/hookbeacon`, #1373), which reads the
  addr file at fire time; three of the four port obligations then fail by
  construction, measured against a working beacon. That is the good outcome
  rather than an exemption: the beacon makes the whole stale-port class
  INEXPRESSIBLE instead of fixing it once more — the dev daemon that left a
  user's real `~/.claude/settings.json` and `~/.codex/hooks.json` pointing at
  three dead ports (#1449) could not have — so the address-free route asserts
  that the property was actually obtained (the line varies with nothing and
  carries nothing address-shaped) plus the failure the beacon NEWLY admits: an
  entry naming a binary path that is no longer the running one, which must be
  rewritten in place exactly as a stale port must. Declaring the wrong route
  cannot pass quietly, which is why this is a declaration and not an exemption:
  the two routes' first obligations are contradictory assertions about the same
  two strings (URL requires them to DIFFER across bind addresses, address-free
  requires them IDENTICAL), so a beacon adapter that forgets the field and a URL
  adapter that sets it both go red. The reasoning, and which obligations each
  route runs, is on `deliveryRules` in that file rather than restated here.
  Whichever adapter adopts beacon delivery first replaces
  `hook_endpoint_addressfree_test.go`, the reference wiring the route is
  currently exercised by — and that file is also the shape to copy, down to
  resolving the binary path once through `hookbeacon.InstalledCommand` so the
  config builder stays infallible.
- Hook disclosure: `contracttesting.AssertHookDisclosureMatchesInstalled`
  (`core/internal/contracttesting/hook_disclosure.go`) binds a hooks
  permission's consent copy to what the installer actually writes — the
  `Touches`/`Detail` text names every event in `installedHookEvents`, states
  the right entry count, and names no event the adapter doesn't install
  (#1356). It exists because that text *is* the #570 consent contract and it
  was hand-maintained: Claude Code's copy declared six entries for the whole
  of #1173's seven-event install, so the Notification hook was written to the
  user's `settings.json` undisclosed. Adapters now derive both the count and
  the list from `installedHookEvents` via `hookjson.EventList`, and wire one
  call (see `claudecode`/`codex` `hookdisclosure_test.go`). The "names no
  uninstalled event" arm checks against `session.AllHookEvents`, itself kept
  honest by `TestAllHookEvents_CoversEveryConstant`, which scans
  `hook_signal.go`'s source rather than trusting a second hand-kept list.
- Hook path confinement: `contracttesting.AssertHookPathConfined`
  (`core/internal/contracttesting/hook_path_confinement.go`) is the
  receiving-side counterpart to the two above — the `transcript_path` in an
  inbound hook body is caller-supplied on a local, unauthenticated endpoint, so
  a receiver confines it to the adapter's own declared transcript roots
  (`agent.Source`'s `FilesUnderRoot.AllRootsFor`, never a second list that can
  drift) before anything downstream opens it. Six obligations: an in-tree path
  is still accepted (the vacuity guard); an out-of-tree path, a `..`
  traversal, a symlink planted inside the root and a *dangling* symlink inside
  the root are each refused, logged and counted; and for a path that IS
  accepted, the string dispatched downstream is the confined spelling rather
  than the caller's (#1389 — see the end of this bullet).
  A DIFFERENT sixth — "the adapter's production constructor confines, not merely
  the handler the test assembled" — was retired in #1390, and how it went is the
  point rather than that it went. It existed because reaching the rejection
  counter meant calling a second, test-shaped constructor
  (`NewHookHandlerWithConfiner` and friends), so the handler the other five
  obligations ran against was not provably the one the daemon builds. Each
  receiver now has ONE exported constructor returning a `hookjson.HookHandler`
  — the handler together with the confiner it actually uses — so the counter
  the contract reads is taken off the handler under test, and "this handler
  confines" and "the counter proving it" stop being two objects that could
  disagree. `HookReceiverUnderTest.Handler` is typed as that concrete struct
  rather than `http.Handler`, and the count is derived from it rather than
  declared beside it, so the wiring cannot name two different confiners even
  by accident. A contract that grows a sub-test to police an API the same PR
  invented is a signal to remove the API, not to test it. What obligation 6
  never covered, and still does not, is an adapter wiring `New` to a hand-rolled
  handler instead of its constructor — `NewProduction` was adapter-supplied too.
  Two are load-bearing and neither is obvious. **Symlinks are resolved BEFORE
  the containment check** — a guard with that order reversed passes every
  lexical traversal test and confines nothing (#1361, where claudecode
  forwarded the raw path while codex confined). And **"unresolvable" is not
  "not written yet"**: resolution reports the same error for a broken link as
  for an absent file, so a receiver that waves through an unresolvable leaf
  (a reasonable allowance — the hook fires around the write) lets an attacker
  plant a broken link, have it accepted, then create the target. A new
  hook-receiving adapter wires one call (see `claudecode`/`codex`
  `hookpath_test.go`; claudecode wires it twice, because its statusline
  endpoint is a receiver too and was the one the original fix forgot).
  **A confinement refusal answers 2xx** — it is reported by the log and the
  counter, never by a status code on the user's critical path. The path is
  already contained by not being forwarded, so the status buys no security,
  while a non-2xx is an untested interaction with the agent CLI: Claude Code
  documents that a non-2xx from a `type: http` hook "can't block actions" but
  not whether it surfaces an error, and gemini-cli's pre-tool hooks and
  Copilot's `preToolUse` are known to fail CLOSED on an error result. The
  contract asserts the 2xx so a new receiver inherits the rule instead of
  re-deciding it (#1361, #1364). `hookjson.RejectPath` is the single place it
  is implemented, and its doc comment records what was and was not observed.
  Since #1389 the contract is only half the enforcement, and the weaker half: a
  contract can fail only for an adapter that WIRED it, so a receiver nobody
  wired it for is invisible to it — which is how the statusline endpoint shipped
  unconfined in the first place. `hookjson.DecodeConfined` welds the body decode
  to the confinement (the confiner is an argument to the decode, so a receiver
  cannot reach its payload without supplying one), and
  `core/architecture_hookbody_test.go` fails the build if anything else under
  `core/adapters/inbound/agents/...` reads an inbound request body. Two arms:
  none outside `hookjson`, and **exactly one** inside it, in `DecodeConfined` —
  the second is what stops the exemption being a hole, and doubles as the
  vacuity guard. There is deliberately **no exemption list** (neither has
  `architecture_test.go`): an endpoint that genuinely carries no path amends the
  rule in a reviewable diff. The archaeology — why the rule #1389 proposed
  (keying on files referencing a `HookEndpointPath`) selects zero receiver files
  and would have passed against the very bug it was written for, and the four
  things the implemented rule does not cover — is in that file's header, next to
  the code it constrains, rather than restated here.
  Obligation 6 is #1389's too, and note it is NOT the #1390 one described above:
  that one policed WIRING and was replaced by a type guarantee, this one polices
  the VALUE that travels and cannot be. Obligations 1-5 are all about the
  accept/refuse DECISION, and every one of them is satisfied by a receiver that
  decides correctly and then forwards the caller's own string anyway — measured,
  by replacing a receiver's write-back with a no-op and watching the whole suite
  stay green. Two independent layers now catch it, and each was seen red with
  the other disabled: `DecodeConfined` verifies its own postcondition (the
  caller's `get`/`set` pair must address the same field, else fail closed), and
  the contract posts an in-tree path spelled with a redundant `/./` and asserts
  the dispatched string is not the caller's. The contract checks the *spelling*
  rather than string-equality with the fixture, because the confiner rebuilds an
  accepted path on the adapter's DECLARED root — which on macOS is the `/var`
  spelling of a `/private/var` temp dir, and a naive comparison fails there for
  a reason unrelated to the obligation.
  `DecodeConfined` also bounds the body (`http.MaxBytesReader`, 1 MiB): these
  endpoints are unauthenticated and local, and sharing one decode is what lets
  every receiver, present and future, inherit the bound.
- Hook version floors: `contracttesting.AssertHookVersionGate`
  (`core/internal/contracttesting/hook_version.go`) is the static half of
  #1365 — a hooks permission declares the minimum upstream CLI version its
  install requires (`agent.ManagedUserFile.Version`), that floor is at or above
  every installed event's own `Since` entry, and it actually refuses one patch
  below itself with a reason naming both versions. The runtime half — that
  `PermissionService.runClosureEffect` consults the declaration before running
  `Apply` — is `core/application/services/permission_version_gate_test.go`, the
  same split `AssertPermissionGated` draws. It exists because Codex carried a
  private `codexSupportsHooks` with its own parser and floor constants while
  Claude Code carried nothing and wrote seven entries into the user's
  `settings.json` at any version; a third adapter joins by declaring
  `Version: &agent.VersionGate{Min: "x.y.z", Probe: []string{"<cli>",
  "--version"}}` and nothing else. `TestEveryHookInstallDeclaresAVersionFloor`
  (`core/adapters/inbound/agents/hookversion_test.go`) walks the registry
  projection so a new hook adapter is covered by existing rather than by
  remembering to wire the contract; it narrows on
  `agent.HooksPermissionKey`, because since #1383 the declaration it walks
  covers every managed user file, and a floor is an obligation only of writing
  into an agent's OWN config format. A refusal is an ordinary effect error, not
  a separate "skipped" concept, so #1362's surfacing carries it for free — the
  wizard shows "granted but NOT applied, because <the CLI is too old>", and
  because a re-answer re-runs an effect that previously failed, the refusal's
  own advice (upgrade the CLI and grant again) is the gesture that retries it.
  Note the direction: an unknown or
  unparseable version fails **open** (`core/pkg/cliversion`) — the daemon runs
  under launchd with a minimal PATH and routinely cannot see the user's CLI, so
  only a version successfully read AND parsed AND found below the floor blocks
  an install.
- Unrecognized hook events: `contracttesting.AssertUnknownHookEventObserved`
  (`core/internal/contracttesting/unknown_hook_event.go`) covers the other end of
  the same receiver — the event name it does *not* know. Before #1364 both
  receivers ended their switch with an Info log and a 200, which is
  indistinguishable from health: an upstream event rename would leave the
  permission reading `granted`, the config still holding our entries, and state
  detection quietly degrading, with nothing anywhere to look at. Four
  obligations: a recognized event still dispatches and is counted by nobody (the
  vacuity guard — a receiver that counts *everything* otherwise looks identical
  to one that counts correctly); an unrecognized event is answered 2xx and
  dispatched nowhere; it is counted per **(adapter, event name)**, because a
  rename fires on every tool call forever while a stray POST fires once and a
  single scalar cannot tell those apart; and it is reported **exactly once per
  distinct name**, since a line per tool call buries the evidence it exists to
  surface. `hookjson.IgnoreUnknownEvent` is the single place it is implemented —
  the sibling of `RejectPath`, and it deliberately takes no `http.ResponseWriter`
  so it cannot grow an opinion about the status code. Its counters are read into
  the diagnostics bundle's `hooks.json`; note that `--diagnose` runs in a process
  that never served a hook, so that form omits the counts and says so rather than
  publishing zeros (the live counts come from `GET /debug/bundle`).
- Hook entry presence: not a contract family — a registry tripwire,
  `TestEveryHookInstallDeclaresAVerifier`
  (`core/adapters/inbound/agents/hookverify_test.go`), riding the same
  projection as #1365's version-floor tripwire. It exists because the install
  is not a one-shot fact: an agent whose own settings UI rewrites its config
  deletes our entries silently, with the permission still reading `granted`
  and no error anywhere. gemini-cli's writer is sync-by-omission and #1355's
  Phase C audit verified the deletion live. So a hooks permission declares
  `Verify` beside `Uninstall` (one line, `hookjson.Verify` does the matching
  via the adapter's own `hookConfig`), and `services.HookEntryVerifier`
  re-reads every granted install on a timer, repairing through
  `PermissionService.RepairGrantedHookInstall` — never through the adapter's
  `Apply` directly, because the repair is a WRITE and has to pass the same
  #570 consent the install did. An adapter that omits `Verify` is skipped in
  silence, which is why the tripwire is the coverage rather than a per-adapter
  test. Keep the three hook diagnoses distinguishable: entries GONE is this
  one, entries present but nothing ARRIVING is #1368, and an install that
  FAILED is #1362's `effect_error` — `hooks.json` prints all three side by
  side and each note names the other two.
- Hook receipts: `contracttesting.AssertHookReceiptObserved`
  (`core/internal/contracttesting/hook_receipt.go`) is the #1368 counterpart —
  the event that never *arrives*, rather than the one that arrives unrecognized.
  It is the only receiving-side contract whose failure is a false **accusation**
  instead of a silence: the liveness watchdog demotes a channel that produces no
  receipts across N turns and releases the holds it placed, so a receiver that
  forgets `hookjson.ObserveHookReceipt` is reported dead and stops being trusted
  while working perfectly — and the watchdog cannot tell that apart from a real
  outage. Four obligations: a recognized event counts exactly one receipt; an
  *unrecognized* one counts too (alive-but-misunderstood is #1364's diagnosis);
  a path-confinement rejection counts too (alive-but-misrouted is #1361's); and a
  consent-denied request counts nothing, because noting that a POST arrived is
  itself an observation.
- Managed user files: every `modify`-kind permission with an `Apply` closure
  declares the shared, user-owned file that closure writes
  (`agent.Permission.Writes`, an `agent.ManagedUserFile` carrying `Path` +
  `Uninstall`). Three projections read it, and they read deliberately different
  slices: `agents.ManagedUserFiles` returns everything — what
  `irrlichd --print-managed-files` prints and the onboarding recorder backs up
  before spawning a `grant-all` daemon against the user's real `$HOME` — while
  `agents.HookConfigs` narrows to `agent.HooksPermissionKey`, so
  `--uninstall-hooks` keeps meaning what its name says instead of revoking the
  CLAUDE.md instruction blocks or the kitty patch nobody asked it to touch.
  Since #1437 there is a third, `agents.InstructionConfigs`, narrowed to
  `agent.InstructionsPermissionKey` for `--uninstall-task-eta`; both narrowings
  share one `configsForKey`, which narrows **before** resolving. That order is
  the rule, not an optimization: a narrowed projection validates exactly the set
  it collects, so a command cannot die on a declaration outside its own blast
  radius. Resolving first made `--uninstall-task-eta` abort and remove *nothing*
  from the user's CLAUDE.md because kitty's path did not resolve. The full
  projection keeps the opposite semantics on purpose — for the recorder a
  short list reads as "nothing to protect". Every uninstall command runs over a narrowed
  projection because it does not only remove content — it records the matching
  consent as **denied**, and without that the Apply closure rewrites the file at
  the next daemon start (#570). `--uninstall-task-eta` did exactly that for its
  whole life: it called one adapter function by hand instead of projecting, so
  it had no `(Adapter, Key)` pair to deny with, and the blocks came back in the
  user's own `CLAUDE.md`. The two slices are asserted **disjoint**
  (`TestUninstallTaskEtaReadsOnlyTheInstructionsSlice`), because a
  single-sided narrowing check cannot see the one failure that matters most —
  two commands revoking each other's capability.
  All three project the **full consent catalog** (`consentCatalog` in
  `core/cmd/irrlichd`), not `agents.All()`: three daemon-wide declarations —
  gastown, launcher, kitty — are appended outside the adapter registry, and
  projecting only the registry is exactly how the kitty config patch was
  offered by the wizard while being invisible to every one of them (#1383). The
  catalog-wide tripwire is
  `TestEveryModifyPermissionDeclaresTheFileItWrites`
  (`core/cmd/irrlichd/managedfiles_test.go`); a new modify permission is
  covered by existing rather than by remembering. It keys on a non-nil `Apply`
  — what `grant-all` actually runs — never on `Kind`, which is the wizard label
  the adapter author picked and which a file-writing permission could be given
  wrongly; a permission whose `Apply` writes nothing is named in
  `applyWritesNoUserFile` with its reason rather than falling out silently.
  `agent.ControlPermission` needs no entry: its `Apply` is nil, which is the
  shape to prefer.
  Since #1449 the declaration is also what a **grant-all daemon refuses to
  write**. `PermissionService.sharedConfigRefusal` — the same call site as
  #1365's version gate, so #1362's "granted but NOT applied" surfacing and the
  re-answer retry carry it for free — refuses any `Apply` whose `Writes.Path`
  resolves outside the daemon's own `IRRLICHT_HOME`, with an error naming the
  file. Note what "isolated" does NOT mean: "`IRRLICHT_HOME` is set and differs
  from `$HOME`" is vacuous here, because the daemon that caused the incident had
  it set and so does the recording rig. A `ManagedUserFile` follows `$HOME` by
  definition, so isolation is asked of the FILE — it is inside an isolated home
  only when its resolved path is inside `IRRLICHT_HOME`, which happens only when
  `$HOME` or a per-agent override (`CODEX_HOME`, `COPILOT_HOME`,
  `XDG_CONFIG_HOME`) points in there too. Containment is deliberately lexical,
  unlike `hookjson`'s confiner: the input is our own resolver's output, not a
  caller's, and the asymmetry runs the other way — a false "outside" refuses,
  which is fail-closed. `IRRLICHT_ALLOW_SHARED_CONFIG_WRITES=1` lifts it, and
  the recording rig is the one caller that sets it, in
  `spawn-record-daemon.sh`, immediately beside the `snapshot_managed_files`
  call that earns the entitlement. **`ir:test-mac`'s separate mode therefore no
  longer installs hooks** — that was the damage, not a regression.
  All seven contract families pass by construction against a correct adapter —
  or, for a delivery route no adapter has adopted yet, against its reference
  wiring — so their whole value is that they *can* fail. Seven is a count of
  *obligations*, not of exported `Assert…` functions: `grep -c "^func Assert"`
  over the package currently returns ten, because three of those are not
  families. `AssertPermissionGatedOnEachKey` and
  `AssertHookReceiverPermissionGated` are **drivers** running the
  permission-gate family once per key — the first over a set the wiring names,
  the second over one derived from the receiver itself (#1488) — and
  `AssertDeclaredPermissions` is a one-line **lock** on that derivation rather
  than an obligation of its own. Check what a function asserts before moving
  this number (and before re-counting with `git grep`, whose pathspec `*`
  crosses directory boundaries and returns one more). A new or reworked
  contract assertion is therefore the mutation rule at the top of this section
  in its most literal form: it lands with the deliberate mutation seen red for
  each obligation — and since #1479 that mutation is **committed beside the
  assertion** rather than described in the PR that added it, because a
  paragraph in a merged PR body is re-run by nothing and an assertion that
  silently stops discriminating looks exactly like health.
  `<family>_selftest_test.go` drives one obligation against a fixture that is
  wrong in exactly ONE way and asserts that obligation reports it;
  `selftest_test.go` holds the harness — a recording reporter behind the
  `reporter` seam introduced by #1475/PR #1489, plus `verdictReported` and
  `verdictSilent` — and the harness's OWN committed mutation, `deafRecorder`
  and `criesWolfRecorder`, since a recorder that stopped observing would be
  this failure reproduced inside its own fix. Two rules are load-bearing and
  neither is obvious. A negative self-test names a fragment of the
  obligation's own failure message and a bare failure is refused: "the arm
  failed" and "THIS obligation failed" are different claims, and #1453's
  second instance is the one where only the first was true (`--port 7837` left
  `delivery_carries_no_address` green while the run failed incidentally
  elsewhere). And every family carries a vacuity guard running each arm
  against a CORRECT fixture, because an arm that reported unconditionally
  would satisfy every mutation and read as excellent coverage. Note what this
  does NOT close: #1475's key-blindness would have survived it, because those
  arms all passed correctly for what they asserted and the gap was that the
  FIXTURE could not express the distinguishing state — a negative self-test
  grades an obligation against the wrong receiver it can build, never against
  the one nobody thought to build. **A new arm takes `reporter`, or `armT` when
  it also builds fixtures — never `*testing.T`**, or it cannot be driven at all
  and its obligation silently leaves the covered set. `armT.fixtures` is typed
  `fixtureT`, which carries `Setenv` and nothing that can decide a verdict, so
  reporting through it does not compile; that is a type guarantee rather than a
  guard, the same trade #1390 made for path confinement. Since #1497 **every**
  family carries self-tests. The three receiver-shaped ones share one fixture,
  `receiver_fixture_test.go`: a receiver built through `hookjson.NewHandler`
  over a real `PathConfiner` and a real `RequireConsent`, decoding through
  `DecodeConfined`, whose `receiverBreak` struct has one field per committed
  mutation and whose ZERO VALUE is a correct receiver — so a case names what it
  broke and nothing else. Three things there are worth knowing before writing a
  fourth family's fixture. **A verdict a self-test cannot reach is stated, not
  faked**: obligations 2-5 of the confinement family grade a decision
  `hookjson.PathConfiner` makes, and #1380/#1446 recorded their mutations as
  edits to `confine.go`, which a test in this package cannot make — so the
  fixture consults the real confiner and second-guesses its answer in the
  direction each recorded mutation produced, and the file says that is what the
  evidence supports. **The counter is only reachable through `Confine`** —
  `RejectPath` logs and answers 2xx but counts nothing — so a fixture that
  skipped it would fail every arm with "counted 0 rejection(s)" for a reason
  unrelated to the mutation under test. And a mutated receiver that cannot use
  `DecodeConfined` differs from a correct one in TWO ways, the verdict and the
  decode, so a hand-rolled-but-faithful baseline is run through every arm first
  to prove the decode is not what was reported.
  Each case also names the obligations it must leave **silent**, which is half
  the evidence: without it, six mutations against six obligations are equally
  satisfied by six arms that all report on everything. Those silences reproduce
  the recorded #1446 matrix — M1 red on obligation 1 alone, M4 (containment
  before symlink resolution) red on 4 and 5 while 1-3 stay green, M6 red on 5
  alone. Two rows are reproduced with a deliberately NARROWER blast radius than
  the recorded one, because the recorded mutation edited `confine.go` and a
  reproduction in this package cannot; both say so where they live, and the M3
  row's narrowing is the point rather than a compromise — its reproduction
  leaves obligation 2 green, which is exactly what separates the traversal
  obligation from the out-of-tree one.
  Two mutation sets could not simply be transcribed, and both gaps are worth
  knowing about. #1403 recorded obligations 1 and 2 of the unrecognized-event
  family as **locks** with no mutation at all; deriving them found that
  obligation 1 carries two independent claims (a recognized event still
  DISPATCHES, and it is counted as unrecognized by NOBODY), which a receiver
  can fail one at a time. And #1413's receipt mutation covers obligations 1-3
  **jointly** — removing `ObserveHookReceipt` entirely — which proves neither
  PLACEMENT those obligations exist to pin; the two placements that isolate
  them (`receiptOnlyWhenRecognized`, `receiptAfterConfinement`) are #1497's and
  are recorded nowhere else.
  What makes the seam structural rather than remembered is
  `seam_walk_test.go`, an AST walk over the package's own sources with two
  rules. **Rule 1**: a function in a non-test file whose FIRST parameter is
  `*testing.T` must be an exported `Assert…` entry point, `realT`, or named in
  `deferredToTheSeam` with its reason — keyed on the parameter TYPE and
  POSITION, never on a name convention, since a naming rule is satisfied by
  renaming — and `testing.TB`, `*testing.B` and `*testing.M` count as the same
  type for it, because TB carries `Errorf`/`Fatalf` AND an unexported method, so
  no recorder can implement it and a TB-first arm is exactly as unswappable as a
  `*testing.T`-first one. The exemption map admits two KINDS of function, stated
  as two rather than generalized because the generalization is false and a false
  one there reads as "you don't need to look": fixture machinery (the line
  `fixtureT` draws — a fixture that cannot be BUILT must fail loudly rather than
  be recorded as an obligation firing), and one helper that does decide a
  verdict but has no family to self-test (`CompareGolden`).
  **Rule 2**: every arm (first parameter `reporter` or `armT`) must be
  reachable, along a chain that does **not** pass through an exported entry
  point, from a reference in a `_test.go` **that also calls `mustReport`**.
  All three clauses matter — reference-based rather than filename-based, because
  the permission-gate family's self-tests live in `permission_gate_test.go`;
  not-through-an-entry-point, because a family's vacuity guard CALLS its entry
  point and would otherwise mark every arm of a self-test-less family as driven;
  and the `mustReport` clause, which came from review, because seeded from every
  test file an arm counted as driven merely by being NAMED — deleting a family's
  four negative self-tests and leaving the now-uncalled table that listed them
  passed the rule in full. `mustReport` is the one call every negative self-test
  makes and no positive test does. Its corpus is `seam_walk_corpus_test.go`: one
  row per parameter spelling pinned to the verdict the detector must return,
  plus one per call graph pinning the propagation (no count is stated here,
  because nothing would keep one honest). The `want:false` rows carry the value,
  per #1450 — a `*testing.T` in second position, one inside a struct FIELD type,
  one inside a parameter's own func type, a function literal assigned to a
  package var, and an aliased `testing` import. The first three are false
  positives a text-based rule produces; the last two are declared LIMITS of an
  `ast.FuncDecl` walk over syntactic types, pinned so they are learned from a
  test rather than from an incident. A `want:false` row is also how this
  corpus's own worst defect shipped and was caught in review: `testing.TB` sat
  in the must-NOT-flag block, so the rule's biggest hole came with an approved
  spelling.
  Beside the seam walk sits `fixture_drift_test.go` (#1520), which closes the
  direction the vacuity guards cannot see. Each obligation of a receiver-shaped
  family states its fixture TWICE — once in the entry point's `t.Run` body,
  which is what an adapter runs, and once in the self-test's `armBuilder`
  table — and only the SELF-TEST side's drift was caught: a self-test that stops
  posting the input its obligation grades goes silent against a
  deliberately-wrong fixture and fails, while an ENTRY POINT that changes an
  input leaves every self-test green, still certifying an input production no
  longer posts. Measured, not argued: renaming the entry point's `in-tree`
  subdirectory left the whole package green. Folding the two statements into one
  source was rejected in #1512 and again in #1520 — those `t.Run` bodies are
  what an adapter author reads to learn what their adapter owes — so the
  duplication stays and an assertion removes the silence. Two rules over one
  parse, both in the seam walk's shape. The **first** compares, per obligation,
  the multiset of BASIC LITERALS the two bodies contain, which is where "a
  different path spelling" actually lives and which an identifier-only rule
  cannot see (`32` becoming `2` shortens the traversal below the point where it
  bottoms out at `/`); the set of package-level identifiers declared in
  NON-test files that each side references — the arm, the `what` constant its
  failure prints, the fixture helper — where a self-test's own
  `fakePathReceiver` and `receiverBreak` are test-declared and drop out by
  construction rather than by an exemption list; and the obligation NAMES in
  ORDER, which catches the one drift no input comparison can, a seventh
  obligation reusing an existing arm, invisible to the seam walk's rule 2
  because it introduces no new arm. The family list is derived from the
  package-level `receiverFamily` vars, so a fourth receiver-shaped family is
  graded by existing. Names the body BINDS are subtracted before the comparison
  (the two sides legitimately call one path `inTree` and `spelled`), and the
  declared limit is EXPRESSION STRUCTURE — two bodies holding the same pieces in
  a different arrangement agree — pinned by a `want:false` corpus row. AST
  equality was tried first and abandoned: the two sides already differ in ways
  nobody should have to mirror, so it would have been red on arrival. The
  **second** rule is #1520's smaller sibling: every `receiverBreak` knob must
  still be SPENT by a negative self-test and HONOURED by the fixture, over a
  knob set derived from the struct's fields plus the constants of every enum a
  field is declared with — `confine` and `receipt` are single fields carrying
  three and four distinct mutations, so a field-level rule would report
  `confine` as spent while `confineAcceptUnresolvable` rotted. All 22 knobs were
  spent when it was written; one, `receiptNever`, is honoured by matching NO
  placement guard and is named in `knobsHonouredByAbsence` rather than left to
  be inferred from the absence of a branch. Both rules' mutation evidence is
  committed in `fixture_drift_corpus_test.go`, and it does NOT reach the four
  non-receiver families: their entry points construct no per-obligation input
  (`assertFloorParses(t, gate.Min)` grades a value the WIRING carries), so there
  is no second statement to drift from.
  One further cost is worth knowing before writing a fifth family:
  `hookjson`'s distinct-name table retains `MaxUnknownEventNames` `(adapter,
  name)` pairs for the life of the process and never resets, so an obligation
  needing a globally FRESH name (only #1364's fourth) spends a slot per run.
  Keeping the delta-measuring obligations on an adapter-stable name, and driving
  the fresh-name one only where it is the subject, is what buys the headroom —
  measured at `go test -count=12` when this was written, against roughly eight
  before. Trust neither number: nothing produces either, which is precisely the
  drift "Replay's measured figures" below is about. What IS load-bearing is that
  `assertNameTableHadRoom` reports a saturated table as a limit of the TEST
  BINARY — quoting the bound it reads from `hookjson` rather than a copy of it —
  instead of letting it become the false accusation ("counted 0") it otherwise
  is.
- Guarded construction: not a contract family — a package-local pair of guards,
  `core/application/services/construction_test.go`. A service whose fields
  include maps, channels, or anything else whose zero value is unusable is
  built by exactly ONE unexported allocator (`newPermissionService`,
  `newSessionDetector`), and the exported constructor assigns dependencies onto
  its result. Two guards of deliberately different kinds enforce it, both
  table-driven over `guardedConstructions()` — a third type joins by adding a
  row. The **source scan** walks this package's own AST and fires on a literal
  no test ever executes, reporting its `file:line`; the panic never does, since
  it names whichever method wrote first (#1400's stack bottomed out in
  `recordEffectResult`, #1450's in `removeFromProjectSessions`, neither
  anywhere near the literal). The **reflection walk** catches the other half —
  a FUTURE map/chan field added to the struct and forgotten in the allocator,
  which the source scan cannot see because every construction site stays legal
  — and it walks both the allocator and the exported constructor, because the
  latter assigns afterwards and can re-nil what the former allocated. Two
  exemption maps carry the knowledge neither guard can derive, and their
  polarities differ on purpose: `nilTolerant` is opt-out (a new map is covered
  by default), `mustBeNonZero` is opt-in and names the fields that are neither
  map nor channel yet unusable at zero — `detectInterval`, where
  `time.NewTicker` panics, and SessionDetector's five that fail *silently*
  instead, which is worse. Both maps' keys are existence-checked, so an entry
  that stopped naming a real field is a failure rather than a silent no-op.
  `TestSourceScanCatchesEveryKnownShape` is the corpus: 31 construction
  spellings × both types, parsed from source strings, pinning what the scan
  must report AND what it must leave alone. It exists because #1450's rewrite
  of #1444's scanner silently dropped a case the original caught (an
  index-keyed slice element) while eighteen hand-planted probes all passed —
  every one of them an addition to coverage, none a lock on the predecessor's.
  A rewritten guard replays its predecessor's cases or it is not known to be a
  superset.
- Skill files: `tools/skill-lint.sh` reads every `.md` under
  `.claude/skills/` plus any other tracked `SKILL.md` (there is one under
  `tools/irrlicht-design-system/`) — the files that tell agents how to triage,
  plan, implement and review, and which had no mechanical coverage at all
  until #1209 (PR #1204 changed two of them, `preflight.sh --changed` skipped
  all ten gates, and thirteen PR checks went green having read nothing that
  changed). Unresolved conflict markers, leftover `{{TOKEN}}` / `REPEAT:` /
  `OPTIONAL:` template scaffolding and an unbalanced code fence are hard
  failures; the internal-reference, list-count and frontmatter checks are
  heuristics and only warn until their noise floor is known (`--strict`
  promotes them, which is how one gets hardened). The fence and frontmatter
  checks exist because skipping is how the linter tells "documents a marker"
  from "has one" — so an unbalanced delimiter would otherwise silence the rest
  of the file. That is the parse-failure rule at the top of this section in its
  local form. Runs as
  its own `skill-file lint` gate in `tools/preflight.sh` (scoped to skill
  markdown plus the linter itself) and unscoped as test.yml's "Lint skill
  files" step — first in the job, before `setup-go`. Its own tests are
  `tools/lib/skill-lint_test.sh`, over the fixture corpus under
  `tools/lib/testdata/skill-lint/` — so the assertions never move when a real
  skill file is edited, and `testdata/` is excluded from the gate's own walk
  because those fixtures are deliberately corrupt.
- POSIX shell scripts: `tools/posix-lint.sh` checks every file git knows
  about — tracked, plus **untracked and not gitignored** (#1611) — whose
  **first line** is a `#!/bin/sh` shebang; today `site/install.sh`,
  `tools/linux-replay-entrypoint.sh` and `tools/git-hooks/shim` (#1591 brought
  the third into scope, which is the whole reason that file was written in
  POSIX sh rather than bash). Line 1 only, because
  `tools/lib/install-uninstall_test.sh` is a bash file that writes `#!/bin/sh`
  stubs inside a heredoc, and a content grep would try to lint it as POSIX sh.
  The untracked half is #1611 and it is #1591's own consequence one layer
  down: once `changed_files_vs_origin_main` counted untracked files, a brand
  new `#!/bin/sh` script DID put this gate in scope, and a gate walking
  `git ls-files` then could not see the file that summoned it — `ALL PASS`
  over a file it never read, this gate's founding incident arriving through
  file selection. Untracked paths join the SAME stream and the SAME loop as
  tracked ones rather than getting a second walk, so the `testdata/` exclusion
  and the line-1 rule cannot apply to only half the set; the mis-implementation
  is not hypothetical — measured, a second walk lints the deliberately-corrupt
  fixture corpus and the linter's own bash source.
  It runs two different kinds of check on each file: a real POSIX shell's
  parser (`dash -n`) and **every** static bashism linter installed —
  `shellcheck --shell=sh` filtered to its POSIX-compatibility codes
  (`SC3xxx`, plus `SC2039` and `SC2112`/`SC2113`, so general style debt stays
  out of scope) and `checkbashisms`. Both kinds, because the parser alone is
  far weaker than it looks: measured one bashism per file, `dash -n` catches
  **3 of 8** — it flags arrays, process substitution and the `function`
  keyword, and accepts `[[ ]]`, `${v,,}`, `+=`, `echo -e` and `source`, where
  either static linter catches all eight of those. *Every* installed linter
  rather than the first one found, because the two disagree beyond that
  sample: `checkbashisms` accepts `local`, `set -o pipefail` and `echo -n`,
  which `shellcheck` rejects (SC3043/SC3040/SC3037) and which an installer
  accretes. CI has shellcheck and not checkbashisms, so preferring the other
  one locally would let a developer's preflight pass a diff CI rejects —
  running both is monotone, and a run without shellcheck says out loud that
  it is weaker than CI. That gap is #1423: `site/install.sh` reaches users as
  `curl … | sh`, which on Debian and Ubuntu is dash, so a bashism lands on a
  new user's first command before anything is installed that could report it.
  The gate lives in **`linux.yml`**, not test.yml, and the placement is the
  decision rather than an accident — ubuntu-latest is the only runner where
  `/bin/sh` is genuinely dash *and* the image ships shellcheck (0.9.0); the
  macos image ships none, and test.yml's `go-test` job is pinned to macOS for
  the runtime paths in `go test ./core/...`. Mirrored locally by
  `tools/preflight.sh --only posix`. **Three ways out are hard failures, not
  skips** — no POSIX shell, no static linter, and an empty file set — because
  a gate whose absence reads as a pass is the exact defect it was built to
  remove. Its tests are `tools/lib/posix-lint_test.sh` over the corpus under
  `tools/lib/testdata/posix-lint/`: one deliberately-broken fixture per
  bashism class, committed rather than improvised so the mutation evidence
  outlives the PR, plus a clean `good-clean.sh` as the vacuity guard,
  `noisy-but-posix.sh` (POSIX-clean but SC2086-noisy) pinning the severity
  filter in the one direction the `bad-*` files cannot reach, and two cases
  pinning the refusals. That suite runs in `linux.yml`, **not** in test.yml's
  `tools/lib/*_test.sh` loop — it needs a linter the macOS image lacks, and
  the loop skips it by name for that reason. One of those cases exists because the first
  draft of the linter reproduced #1423 inside itself — it piped into `grep`
  and tested the capture for emptiness, so a linter that failed to run came
  back empty, empty read as clean, and it printed `ALL PASS` over an installer
  carrying a deliberate `[[ ]]`. `testdata/` is excluded from the gate's own
  walk, the same split `skill-lint.sh` draws. Separately,
  `install-uninstall_test.sh` now *executes* the installer under `dash` rather
  than `sh` (macOS ships `/bin/dash`), which is the runtime half — it reaches
  only the lines a case runs, where the linter reads every line.
- Sourced shell libraries: not a contract family — a tripwire,
  `tools/lib/shell-lib-errexit_test.sh`, over every `tools/lib/*.sh` that is
  not a `*_test.sh`. Each function it can drive must, under a caller's
  `set -e`, return what its library DOCUMENTS rather than aborting the caller,
  and must leave the caller's shell options byte-identical. It exists because
  three issues in a row were the same defect in a different file — #1629 (a
  workflow step reading `$?` on a line GitHub's implicit `-e` never reached),
  #1633 (`swift_suite_run`'s post-timeout kill sequence aborting before
  `return 124`), #1635 (`budget_run`'s backgrounded child inheriting errexit
  and dying before writing the status file that is its "it finished" signal,
  so a gate that failed instantly was reported as a TIMEOUT that burned the
  whole budget) — and each sibling was found only because someone happened to
  look. These files are SOURCED, so they run with whatever options their caller
  has, and none of them said anything about that. Four things are load-bearing:
  - **Bare statement position is the whole point.** Every other calling shape —
    `if f`, `f || x`, `x=$(f)` — makes bash ignore errexit for the whole
    function body, and (measured on 3.2.57) for a subshell that body
    backgrounds as well. The very call that came back 1 instead of 3 against
    the unfixed `gate-budget.sh` returns 3 written as `budget_run … || rc=$?`,
    so a lock written that way is green against a broken library.
  - **`$(set +o)` cannot capture the options.** bash 3.2 reports errexit and
    nounset as OFF inside a command substitution regardless of the parent —
    measured, same shell, same instant: `$( )` gives `set +o errexit` where a
    redirect to a file gives `set -o errexit`. A probe built the obvious way is
    byte-identical before and after any leak and can never fail. The redirect
    spelling is used, and `tw_fixture_leaks` is its committed guard.
  - **No function is blind-called.** Each has a named setup+call recipe;
    expected statuses are chosen DISTINCTIVE (0, 2, 3, 124) because a documented
    1 is indistinguishable from an errexit abort, which also exits 1. Anything
    undrivable is named in `TW_EXEMPT_KEYS` with its reason — today one entry,
    `swift_suite_run` on a non-Darwin host, and it is inactive on macOS where
    the suite actually runs. Recipe keys and exemption keys are existence-
    checked against the walk in BOTH directions, so a library that stopped
    being walked surfaces as its recipes naming nothing rather than as silence.
  - Its own mutation evidence is committed (`tw_fixture_correct` /
    `tw_fixture_aborts` / `tw_fixture_leaks`, four synthetic bijection cases,
    and a real walk over a directory holding three of the libraries,
    swift-suite.sh deliberately not among them — phrased that way rather than
    as "three of the four" because #1639 added a fifth and made that count
    stale). The
    fixtures matter in both directions: a leaked `set +e` is a *working* fix for
    the return value, so obligation (a) passes it and only (b) objects.
  What it buys over a hand-written lock was measured on this repo: deleting
  `_budget_kill_tree`'s own guard reddens the tripwire and leaves
  `gate-budget_test.sh`'s whole `-e` block green, because that helper is only
  ever reached through a region `budget_run` guards separately.
- The shell-lib suite runner: `tools/lib/shell-lib-suite.sh` is the ONE
  implementation behind test.yml's "Test the shared shell libs" step and
  `tools/preflight.sh`'s `tools` gate. Before #1639 those were two copies of
  the same loop that disagreed about the only thing that matters: preflight's
  collected every file's status, CI's had no `|| rc=1` and — test.yml declaring
  no `shell:` and no `defaults:`, so GitHub's `bash -e {0}` applies (errexit
  only; see "Which bash a workflow step gets" below) — aborted on the FIRST
  failing file with every later file
  in glob order never run and nothing in the log saying so. One round trip per
  red file, on suites that take seconds each. The sharing follows
  `macos-swift.yml`'s own reason for sharing `swift-suite.sh`: CI and the
  pre-push hook judge a run by the same rules rather than by two
  implementations that can disagree. Three things about it are load-bearing.
  It prints a **census** — found / skipped / ran / failed — because "they all
  passed" and "the loop stopped early" had no distinguishing line in either
  predecessor. An **empty corpus is a named refusal** (status 2, distinct from
  1) rather than either predecessor's answer, both measured: CI's loop iterated
  once with the literal unexpanded pattern (nullglob is off by default and
  neither invocation changes it) and died with `No such file or directory`,
  while preflight's `[[ -e "$t" ]] || continue` filtered that out and returned
  0, passing silently. And the two callers' one remaining scope difference —
  CI skips `posix-lint_test.sh`, which needs a linter the macos image lacks —
  is an **argument** that is validated against the corpus before anything runs,
  so a skip that stops matching a real file is a refusal instead of a `case`
  pattern quietly matching nothing. Its tests are
  `tools/lib/shell-lib-suite_test.sh`, which grades the runner in BARE
  STATEMENT position as well as under `|| rc=$?`: bash suppresses errexit for a
  function's whole body when the call sits in a `||` position, so a runner that
  ran its files as bare `bash "$f"` statements is invisible from the `||` shape
  and from the CI step itself. Both predecessors are emitted verbatim there and
  re-measured on every run, which doubles as the vacuity guard — if bash ever
  stopped aborting, the fix would be protecting nothing and would pass for the
  wrong reason.
- The ARS badge job: `tools/lib/ars-badge-push_test.sh` EXTRACTS each of
  `.github/workflows/ars.yml`'s three `run:` steps out of the workflow
  file and EXECUTES it against a stub, under the invocation that step
  actually gets — DERIVED from the workflow, today `bash -e` (see "Which bash a
  workflow step gets" below; this bullet claimed
  `bash --noprofile --norc -e -o pipefail` until #1650). Behavioural rather
  than a text scan, because a
  scan pins one spelling of a guard where running the block pins the property.
  Before #1641 the push-retry loop's last statement was `sleep`, which
  succeeds, so five failed attempts ended the loop — and the step, the job and
  this workflow's check — at status 0 with the badge unpushed and nothing
  saying so; the observable symptom is a README badge that quietly stops
  tracking `main` behind a green history. `-e` does not save it: errexit is
  suppressed for every command in an `&&` list except the one after the final
  `&&`, which is exactly what MAKES the loop a retry. Three things are
  load-bearing. The pre-fix loop is emitted verbatim there and re-measured on
  every run, so "five failures exit 0" is a fact rather than a sentence in a
  merged PR body — and it is the vacuity guard, since a bash that stopped
  behaving that way would leave every other arm passing for the wrong reason.
  The **third-attempt** arm is what keeps the fix honest: a "fix" dropping the
  retry, or one that stops suppressing errexit inside the `&&` list, satisfies
  the exhausted-case arm just as well and simply gives up after one attempt.
  And the git stub answers an **unmodelled** subcommand with a loud, distinctive
  99 naming the call, never a quiet 0 — a stub that said "fine" to a call it
  does not model would make every arm pass for a reason unrelated to its
  obligation (seen red: adding one `git status` to the step reports
  `STUB: unmodelled call`, not a pass).
  **No linter was built, and the measurement is the reason.** Over the 19
  multi-line `run:` blocks in `.github/workflows/`, a rule keyed on "the
  block's last statement is `echo`/`sleep`/`cat`/`printf`" flags 6 and **misses
  this very defect** — the loop is nested inside an `if`/`else`, so the block's
  last line is `fi` — while 4 of the 6 it does flag are correct code; a rule
  keyed on `|| true` flags 2 and also misses it; "the block contains a loop"
  flags 3, of which 1 is the defect. No candidate both catches the subject and
  stays quiet on the correct blocks, so a green would claim coverage it does
  not have — the same conclusion #1629 reached by the same measurement about a
  `$?`-keyed rule, and #1639 about the sibling family. `preflight.sh`'s `tools`
  trigger gains `ars.yml`, its fourth widening for this reason (#1591, #1629,
  #1639), because that assertion lives entirely inside that gate.
  **The two steps ABOVE it produced the same symptom for the same reason**
  (#1644): `ars scan … || true` followed by a `cat` that succeeds, then an
  extract step whose `if [ -n "$ARS_BADGE" ]` skipped in silence — three green
  steps, `No badge changes to commit`, and a frozen badge. The `|| true` was
  **not** protecting a legitimate low-score exit, and that had to be checked
  rather than assumed because the issue proposed leaving it in place: pinned
  v0.0.9 returns `ExitError{Code: 2}` only under `if p.threshold > 0`, this
  invocation passes no `--threshold`, and there is no `.arsrc.yml` to supply
  one — measured, the pinned binary scoring `./core` at 7.9/10 exits 0. So the
  scan's status is now read (`|| scan_status=$?`, never a bare `; rc=$?`, the
  line #1629's implicit `-e` never reaches) and three outcomes are
  distinguished where there were two: the scan FAILED / it succeeded with no
  badge line in its output / the badge was extracted. Three things about the
  shape are worth carrying. Each refusal asserts the other two's WORDING is
  ABSENT, because a shared non-zero is satisfied by three refusals that all
  fire together — measured: deleting the missing-badge refusal leaves the step
  exiting 1 via the URL refusal, so every status arm stays green and only the
  wording arms go red. The last guard is the same defect one level down —
  `sed` exits 0 having matched nothing, so the file is re-read for the URL just
  written, which is also what tells the legitimate no-op of an unchanged score
  (the URL is present, because it was written back identically) from a rewrite
  that landed nowhere; deleting THAT refusal is not merely silent, since the
  empty `$ARS_URL` then deletes README's badge URL outright (measured). And a
  failed scan **fails the job**, because this workflow gates nothing: it runs
  post-merge on `main`, no PR and no merge depends on it, and its own "Install
  ARS CLI" step already fails the job for the likeliest transient cause (a
  `go install` outage) — a scan that could not run was the one failure here
  that was silent.
  Audited while there, per "dismissals carry evidence": the only statement left
  in this job whose status is read more narrowly than it is produced is
  `git diff --staged --quiet`, whose three-valued status (0 / 1 / error) an
  `if` reads as two-valued — and the misread routes to the `else` branch, where
  `git commit` with nothing staged exits 1 and errexit aborts the step.
  Measured under `bash -e` rather than argued: it degrades to a LOUD failure,
  never a silent pass. `git config` / `git add` / `git commit` are simple
  commands in bare statement position, so errexit already decides on them (also
  measured), and no `run:` block in this workflow reads a `${{ }}` expansion, so
  #1645's second hazard has no instance here.
- The replaydata deletion guard:
  `tools/lib/replaydata-deletion-guard_test.sh` does to
  `.github/workflows/replaydata-deletion-guard.yml`'s "Detect deletions of
  load-bearing replaydata" step what the bullet above does to ars.yml —
  extracts it and EXECUTES it against a git stub, under the same derived
  invocation (`bash -e` for that step, which is why the step supplies its own
  `set -euo pipefail` on line 1). It is the same family's most
  expensive member, because that workflow is a **merge gate**: before #1645 its
  diff was captured as `deletions=$(git diff … || true)`, and an empty
  `$deletions` is the gate's SUCCESS condition — so a git exiting 128 produced
  no violations, printed `OK: no disallowed deletions` and exited 0, permitting
  exactly the #268 deletion the gate exists to refuse. **Where** the defect sat
  is the part worth carrying: this block was spot-checked and cleared TWICE
  during #1639 and #1641, both times on the strength of the classification
  LOOP, which is genuinely fine. The statement that decides the step's status
  is the loop's INPUT, one line above it — a `case` walk over `$deletions` can
  only ever be as good as `$deletions`, and nothing in the loop can see that it
  was handed an empty string by a failure rather than by a clean PR.
  Three outcomes now, never two: deletions found and disallowed (fail), no
  deletions (pass), and **could not determine** (fail, naming why). Three
  refusals implement the third, and the second of them is a SECOND measured
  hazard rather than defensive padding — `on: workflow_dispatch` carries no
  `github.event.pull_request`, so both `${{ }}` expressions expand to the empty
  string and `git diff "..."` is `HEAD...HEAD`: exit 0, no output, PASS. That is
  measured against real git in the lock, not stubbed, since a claim about how
  git parses `...` has to be made by git. Note honestly what each refusal
  independently buys: deleting the empty-context one still leaves the run
  failing, because an empty sha does not rev-parse either — what it uniquely
  buys is the DIAGNOSIS ("no pull_request context" rather than "the base commit
  is not present in this checkout", which points at the checkout instead of the
  trigger). The commit-presence refusal is the one that is defensive: with
  `fetch-depth: 0` no ordinary condition was found that makes `base.sha`
  unreachable, so unlike the other two it is not backed by a reproduction, only
  by the observation that it is one edit to the checkout step away.
  Two more things about the lock. `cell_is_live` reads the WORKING TREE, so
  "live cell" and "orphan cell" are properties of the directory a body runs
  in — every arm executes against a throwaway tree under `$TMP`, and the real
  deletion-guarded catalog is never touched. And what the stub CANNOT grade is
  said out loud: `git mv` is permitted because git's own rename detection
  reports an R that `--diff-filter=D` drops, not because of anything the step
  does, and detection is ON by default in modern git — so `--find-renames=50%`
  pins the threshold, not the behaviour, and that arm grades the invocation the
  step makes rather than claiming the step implements renaming.
  `preflight.sh`'s `tools` trigger gains this workflow, its fifth widening for
  this reason (#1591, #1629, #1639, #1641).
- The snapshot-evidence copy:
  `tools/lib/swift-snapshot-evidence_test.sh` is the same treatment for
  `macos-swift.yml`'s "Collect the skipped suites' pixels" step (#1646), and
  it is the family's cheapest member — a `cp -R` of the reference snapshots
  into the artifact whose status was read by NOTHING. That step opens with
  `set +e` for a good reason (its whole purpose is to run assertions that
  fail), so the failed copy neither aborted nor reached `bad`: green job,
  uploaded artifact missing the `__References__` tree, and therefore failure
  images with nothing to compare against. Three outcomes now — copied /
  nothing to copy / could not copy — plus a fourth no exit status can see, an
  empty tree, which `cp -R` copies with a cheerful 0. **#1646 is where
  extract-and-execute stopped being blocked by a real `swift test`**: the
  fixture checkout's `tools/lib/swift-suite.sh` SOURCES the repo's own library
  by absolute path and overrides only `swift_suite_run`, so the two predicates
  the body consults are production code reading a committed log fixture, and
  every outcome of a 20-minute macOS job is reachable in a second. That is the
  shape to copy for any step whose blocker is one expensive command rather than
  the body. Two arms carry more than they look: the references must be copied
  even when the RUN is judged bad (this job exists to publish a failed run, so
  a copy on the happy path only would ship nothing on exactly the runs the
  artifact is for), and they must not be counted as failure images — 53
  references would otherwise satisfy the "not one of the suites produced a
  failure image" guard forever. The re-audit is in that file's header rather
  than here: `exit "$bad"` decides, six guards write `bad`, and of the four
  statements it cannot see, two degrade LOUDLY (a library that will not load
  reports TRUNCATED — wrong headline, right verdict; a failed `mkdir -p`
  reaches two guards, not the one the issue claimed) and one is silent and
  cosmetic (`>> "$GITHUB_STEP_SUMMARY"`, whose text is already on stdout).
  This workflow was in `preflight.sh`'s `tools` trigger since #1629, so the
  trigger needed no sixth widening — the assertion simply joins the gate that
  already covers it.
- Which bash a workflow step gets: **there are two invocations and this repo
  conflated them** for the whole of the two bullets above (#1650). A step
  DECLARING `shell: bash` runs as `bash --noprofile --norc -e -o pipefail {0}`;
  a step declaring no `shell:` and no `defaults:` runs as **`bash -e {0}`** —
  errexit only, no `--noprofile`, no `--norc` and **no pipefail**. That is
  measured off a runner rather than read off the docs: run 31960152598's own
  group header for `replaydata-deletion-guard.yml`'s step reads
  `shell: /usr/bin/bash -e {0}`. Of this repo's workflows only
  `macos-swift.yml` declares `shell: bash` (on two steps; its job-level
  `defaults:` sets `working-directory` and no shell), so every other step is on
  the `bash -e` side.
  **The direction of the error is what made it worth a library rather than a
  comment fix.** Four harnesses extracted a step body and ran it under the
  pipefail spelling, each saying in its own header that running it under
  anything else "would grade a different program" — which was true, and was
  what they were doing. A body whose correctness depends on pipefail
  (`x=$(thing | grep -v skip)`) is graded SAFE by a harness that supplies it and
  swallows the failure in production: a false green, the same "absence of a
  finding and inability to look produce the same output" shape as the rest of
  this section. (Under `shell: bash` the error reverses and is loud.)
  So the invocation is **derived, not typed**: `tools/lib/workflow-step.sh`
  answers `workflow_step_shell <workflow> <step>` and `workflow_step_body`
  off the same one-pass scan, so a harness cannot grade one step's body under
  another step's shell, and a step that later gains `shell: bash` — or whose
  job or workflow gains a `defaults: { run: { shell: … } }` — moves its harness
  with it. It REFUSES (status 2, naming what it could not do) for an unreadable
  file, an absent step, a DUPLICATE step name, an unmodelled `shell:` value and
  a step with no `run: |` block, and never falls back to a default: a harness
  handed a plausible `bash -e` for a step that no longer exists would grade an
  empty body, which exits 0 and reads as a clean run. Its tests are
  `tools/lib/workflow-step_test.sh` over the committed corpus
  `tools/lib/testdata/workflow-step/` (one fixture per declaration shape, plus
  the refusals), and three of its obligations are the ones to keep: the two
  invocations are shown to grade the SAME body differently (without that,
  deriving is ceremony and a derivation stuck on one answer looks correct); a
  copy of a real workflow is mutated BOTH ways, since a derivation hard-wired
  to either answer passes one direction; and the five real harnessed steps are
  resolved through the same code, which is what stops
  `swift-suite_test.sh`'s hand-written `shell: bash` spelling — correct, and
  left alone — from silently stopping to match.
  Verified while fixing it, per "dismissals carry evidence": **no live pipefail
  dependency existed** anywhere in `.github/workflows/`. All 16 pipeline-
  carrying lines were read — ars.yml's two are `$(… | head -1 || echo "")` and
  its `sed "s|…|…|"` is a delimiter not a pipe; the deletion guard sets its own
  `set -euo pipefail`; macos-swift.yml is on the `shell: bash` side already;
  test.yml's is inside an `echo` message; and codescene-badge.yml's
  `SCORE=$(… | jq -r …)` and coverage.yml's `pct=$(…)` are each followed
  immediately by an explicit emptiness guard that does the work pipefail would.
- Factory: `go test ./tools/onboarding-factory/... -race -count=1`.
- Replay: `tools/replay-fixtures.sh` — gated in CI by linux.yml, and run
  natively as `tools/preflight.sh`'s `replay fixtures` gate, so golden drift
  surfaces without Docker. Takes ~3 minutes unscoped; under `--changed` it
  runs only when the diff touches `replaydata/`, the factory, or the
  `core/` parsers and tailer a golden is derived from.
- Replay transition timing: a golden records a `virtual_time` on every
  transition, and until #1480 **nothing compared it to anything** — the
  goldens pin it only against their own previous value, `compareOrdered` walks
  `prev_state`/`new_state` index-by-index and never reads the time, and the
  "N of M recordings diverge" headline every replay PR quotes is counts and
  kinds only (that headline is `extendedCheck.Diverges` counted over the
  catalog — see the next bullet for why it has exactly one definition). So a transition reproduced at the right position in the ORDER but
  31 seconds from when the daemon made it was a full pass, and the golden then
  pinned it as correct. `compareOrdered`
  (`tools/onboarding-factory/cmd/replay/extended_check.go`) now returns the
  MATCHED pairs rather than a count of them, each carrying how far apart in time
  the two sides fired — so the ordered-divergence figure and the timing figure
  are one traversal and cannot describe different transitions. The first draft
  had a second, identical loop plus three comments and a runtime assertion
  saying the two must agree; one loop makes that structural instead.
  Only KIND-MATCHED pairs carry a delta — where `compareOrdered` reports
  `state_differs` the two sides are not the same transition, so their timestamp
  difference means nothing and counting it would report one sequence divergence
  twice. The reporting side (`timing_drift.go`) buckets and ranks those deltas;
  it reuses `core/domain/stats.Percentile` rather than carrying its own.
  It is a **ratchet, not a tolerance gate**, and that is the deliberate
  shape: roughly a quarter of the catalog's kind-matched pairs are still more
  than 1s from their daemon, so a gate failing on all of them would protect
  nothing. `TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog` walks every
  sidecar-driven recording, prints the distribution, and fails when a
  recording NEWLY drifts, when a pinned entry stops drifting and is left to rot,
  or when the aggregate counts grow — the same idiom `knownZeroTransition`
  uses. The 1s threshold is read off the measured distribution rather than
  picked: |delta| is sharply bimodal (74.4% under 100ms, 24.3% over 1s) with a
  near-empty decade between, so the cut lands where ~1% of the population
  lives; `driftThreshold`'s doc comment carries the histogram. That trough is
  the same 10 pairs before and after #1478 reshaped both modes around it, which
  is stronger evidence for the cut than the original measurement was. The
  goldens #1476 documented as its accepted cost are enumerated by name in
  `knownFirstTransitionDrift` rather than living in a paragraph of a doc
  comment, which is what #1480 was filed about. Its mutation evidence is
  committed, not described: `cmd/replay/testdata/timing/` holds one file per
  timing shape, carrying both verdicts on purpose — a detector that flags
  everything and one that flags correctly are indistinguishable without the
  cases that must stay silent. `tools/replay-fixtures.sh` reports the same
  figure beside the divergence figure, because a `go test` that passes prints
  nothing without `-v` and an unread measurement is the failure mode this
  closes.
- Replay's measured figures: the counterpart rule to "a verification mechanism
  must fail loudly when it cannot run" is that **a number which documents
  behaviour but is not produced by it drifts silently, and is then quoted with
  full confidence**. The replay tree carried one example of each outcome:
  `knownFirstTransitionDrift` is machine-generated and stayed right across a
  change that reshaped the distribution it describes, while the
  `zero`/`fabricated`/`divergent` counts were typed by hand and went wrong twice
  in two PRs — #1478 had to correct copies in five places, and one it missed
  left `tools/replay-fixtures.sh` claiming 198 divergent recordings while the Go
  side measured 140. Two mechanisms close it (#1503). **One named predicate**
  decides each population — `extendedCheck.Diverges`, `.ReproducesNothing`,
  `.Fabricates` — and every counter derives from it, including `main.go`'s exit
  code, which had its own wider spelling (`ordered || missing kinds || extra
  kinds`); those disjuncts are structurally unreachable and the census asserts
  the two spellings agree on every recording it walks rather than trusting that
  argument. `Diverges` is `len(OrderedMismatches) > 0`, and the near-misses are
  the point: "the counts or the kind sets differ" reads as a restatement and is
  one low, because one committed recording replays the same two kinds and the
  same four transitions in the wrong ORDER — that is the recording #1478's table
  lost, and it is pinned by name in
  `TestDivergesCatchesTheOrderSwapThatCountsAndKindsMiss`. **The counts are
  machine-generated**: `censusOfTheCommittedCatalog` is the pasteable literal
  `TestCatalogCensusMatchesTheCommittedFigures` prints on every run, the same
  idiom `knownFirstTransitionDrift` uses, and no doc comment restates a figure
  it carries. Two guards run before the equality check, because "paste the
  measured literal" is the wrong advice when the measurement itself broke: a
  walk that reached fewer recordings than the committed census, and a catalog
  where nothing diverges at all. Its mutation evidence is a committed corpus
  (`TestCensusDiffNamesEveryStaleShape`) — one deliberately-stale literal per
  shape, including #1478's exact one-low, plus the identical-census row that
  stops a diff which reports everything from looking correct.
  Three of the census's figures are not defect counts, and each exists because
  a prose sentence was carrying the number. `DivergentByCountsAndKinds` is the
  near-miss spelling itself, carried beside `Divergent` so that "they differ by
  exactly one recording" is re-derived every run instead of being true once.
  `UnpairedSidecars` and `PairedButUngraded` are the denominator's honesty:
  every sidecar on disk is either graded, unpairable, or paired-but-ungradeable,
  and the three figures sum to the catalog. Nothing is unpairable today — the
  Go walk and `tools/replay-fixtures.sh` select recordings by the same two
  transcript names, from the one declaration `replay.TranscriptNames`, with
  `TestSweepAndGatesWalkTheSameTranscriptNames` pinning the shell's own `find`
  list to it. They do not walk one identical *set* — the sweep enumerates
  transcripts, the gates enumerate sidecars — and the NAMES are what had
  drifted. The figure stays in the census at zero because it
  is what would report the blindness returning: before #1517 it counted every
  aider recording — graded by the sweep, by no Go gate, so
  `knownZeroTransition`, `knownFabricated` and #1480's ratchets described the
  catalog *that walk could see* rather than the catalog, and #1342's stated
  goal of catalog-wide coverage was silently false for a whole adapter. The
  remaining `PairedButUngraded` recordings produce no extended check at all —
  the process-owned-store adapters, plus the aider recordings whose sidecar
  names no real session and is not drivable.
- Replay read boundary: the sidecar records `file_size` from the fswatcher's
  stat at fire time but stamps `ts` at the daemon's **dequeue** time, and the
  watcher's stat time is not a field of `lifecycle.Event` at all — so "where
  had the daemon's read reached" is reconstructed, never read off. `#1342`'s
  `readBoundaryFor` widens a provably-manufactured pass by one recorded stat;
  `#1478` adds `readBoundaryClusterWindow` (10ms), which additionally takes
  every later stat dequeued within that window, on the inference that a serial
  detector loop dequeuing an event microseconds later had it queued while the
  read ran. **Additive, never narrowing** — that is what keeps it off the 35
  gemini-cli recordings a *replacement* time bound broke during #1476's review.
  The constant is CALIBRATED, not derived, and both walls are measured facts
  about the committed catalog: below 3ms the rescues are incomplete, at 28ms
  replay fabricates in `codex/2-1_basic-turn` and at 52ms in
  `codex/1-1_session-start`. Those two are exactly the goldens #1342's rejected
  guard-narrowing broke — two unrelated mechanisms hitting the same wall, which
  is why it is treated as a property of the catalog. `TestReadBoundaryClusterWindow_BothWallsAreMeasured`
  drives the window past each wall and is the calibration's committed mutation
  evidence; a window justified only from below could be raised to 1s with
  nothing objecting. One recording remains in `knownZeroTransition` because
  reaching it needs 69ms, i.e. it can only be bought by making two goldens
  assert something false — the trade this whole line of work exists to refuse.
- Replay goldens (when a recording or replay-output change is in play):
  regenerate with `UPDATE_REPLAY_GOLDENS=1 go test
  ./tools/onboarding-factory/cmd/replay/... -count=1` (the `-count=1`
  matters — without it the cached test result skips the write). Commit only
  the goldens of the adapter you touched.
- replaydata integrity (the onboarding recording/fixture catalog): `go run ./tools/onboarding-factory/cmd/of validate`
  (schema + referential integrity over the catalog + cells — a CI gate). When a
  `web/` or recording-rig change is in play, also `bash
  tools/onboarding-factory/scripts/smoke-test.sh` (the rig's `bash -n` + lib
  unit tests).
- Onboarding maturity + capability model (#1369): `replaydata/agents/adapters.json`
  is the COLUMN file of the matrix, symmetric to `scenarios.json`'s rows. It
  holds each adapter's claimed maturity (`planned`/`alpha`/`beta`/`stable`) and
  the traits it lacks. `of validate` gates three things over it, and all three
  are scoped to a catalog that actually carries the core set, so a partial
  fixture tree is unaffected:
  - **The core twelve.** Only 12 of the 46 scenarios gate a promotion; the
    other 34 are optional and block nothing. The set, and one line of
    justification per scenario, is in `internal/matrix/vocabulary.go` — in code
    rather than in data, so weakening it is a reviewable diff. `alpha` requires
    four state scenarios reachable from hooks alone, `beta` all nine
    state-core, `stable` all twelve including the three metrics ones. That
    split is what `alpha` MEANS: state only, no metrics.
  - **The capability model.** Traits are a closed set in
    `internal/matrix/capability.go`; each adapter's value for one is
    three-valued — `absent` (derives `n/a`), `untraced` (has the feature, it
    reaches no Source the adapter reads → derives `unobservable`), or the
    default `traced`. A boolean cannot express this and the two-valued version
    was pruned in #529 after producing false positives. A declared dead pair
    **needs no cell directory** — the matrix synthesizes it — which is the
    part that shortens onboarding. Agreement is checked both ways: a
    declaration must match its cell, and a structurally dead cell must be
    explained by a declaration or by a documented `record_blocked` reason.
  - **Maturity claims.** Declaring more than the core standing earns is a
    failure; declaring less never is (the 30-day / adoption criteria live in
    `site/docs/adapters.html` and no gate can see them). `of status --summary`
    shows claimed vs earned side by side.
- macOS app (only when touching `platforms/macos/`): `cd platforms/macos &&
  swift build && swift test --skip LauncherTestHarness --skip LauncherHarnessTests`,
  run locally by `tools/preflight.sh --only swift` and by the pre-push hook.
  Since #1530 CI's `.github/workflows/macos-swift.yml` runs it too, as a second
  job beside the build and through the same `tools/lib/swift-suite.sh` harness,
  so a hang there is a named failure at 240s rather than a job cancelled at the
  cap with an empty log. `swift` is still the one gate deliberately **stronger**
  locally than in CI — a runner has a virtual display, a stock font set and no
  usable audio stack — but it is no longer the only place the suite runs.
  Four host dependencies had to go first, and the general lesson is worth more
  than the four fixes: each was a value read from the MACHINE where the
  equivalent value was available from the INPUT. Image snapshots rasterised at
  `NSScreen.main`'s backing scale rather than at a pinned one
  (`Tests/PinnedScaleSnapshot.swift`); `UpdateManager` started Sparkle in a test
  host, where `startUpdater` fails outside an app bundle and answers by running
  a modal `NSAlert` on the main queue a second later, hanging whichever
  unrelated test next spun the run loop — which is why #1530 and #1523 each
  attributed the hang to a different test; and `AXTitleMatchActivator` filtered
  path segments by the running process's own `$HOME` instead of by the cwd it
  was scoring, in two places. A test that reads the host and a test that reads
  its fixture look identical on the machine where they agree. **Never run the
  suite unskipped**: the
  `LauncherTestHarness` target drives real terminal applications through
  `NSRunningApplication`, so on a developer machine it manipulates live
  windows. Both names are passed because a test ID is `<target>.<class>`, and
  either alone stops covering the harness the moment a class is added to that
  target or the target is renamed.
  Nothing built or tested Swift in CI until #1509, which is why two snapshot
  tests sat red on `main` for weeks with no failing check anywhere — the macOS
  platform had no automated floor at all, and `preflight.sh --changed` reported
  every gate as SKIP on a `platforms/macos/`-only diff. That issue is also the
  cautionary tale for reading a snapshot diff: a failure confined to a small
  region was twice diagnosed as toolchain antialiasing and treated by
  regenerating the reference (#1034, #1044), when it was an appearance-mode bug
  that made the suite green by day and red by night. `AdapterIconAppearanceTests`
  is the lock, and it sets both appearances itself so it cannot pass by daylight.
  `PinnedScaleSnapshotTests` is #1530's version of the same shape, and the shape
  is what to copy: asserting "renders at 2×" would have been green on every Mac
  anyone runs it on whether the scale was pinned or inherited, so it drives 1×,
  2× and 3× through one view instead — whatever the host's scale is, at most one
  arm can agree with it. A host-independence lock that can only be checked on
  one host is not a lock.
  **Scale was not the only such value: the LOCALE was one too** (#1630).
  `BackchannelRulesView`'s threshold field renders through `format: .number`, so
  its reference PNG read `150.000` — a picture of the recording contributor's
  `de_DE` regional settings, where a runner renders `150,000`. Three things
  there are worth carrying and none is the obvious one. **The fix the issue
  proposed does not work**: `TextField(_:value:format:)` ignores `\.locale`, so
  `.environment(\.locale, …)` on the hosted view leaves the field reading
  `150.000` while `@Environment(\.locale)` inside that same subtree reports
  `en_US` — a pin that is set and reaches nothing, which is the vacuous green
  this section is about, arriving inside its own fix. A product seam was
  therefore unavoidable; it is `\.formatLocale`
  (`Irrlicht/Views/FormatLocaleEnvironment.swift`), defaulting to
  `Locale.autoupdatingCurrent`, which is *by construction* the locale a bare
  `.number` already resolved through — deliberately NOT `\.locale`, which SwiftUI
  derives from bundle localizations and which would have been a user-visible
  change of unknown sign. **The pinned locale is `de_DE`** for the same reason
  `referenceScale` is 2: it is what the committed references were recorded
  under, so #1630 regenerated NO reference and the untouched 53-PNG set is
  itself the evidence that neither the seam nor the pin changed any rendering.
  Any other choice buys a cosmetic preference with a re-record — the move
  #1034/#1044 got wrong. And the pin is a **type, not a modifier plus a
  guard**: `Snapshotting.pinnedImage` is declared over `PinnedSnapshotHost`,
  whose only initializer applies the pin, so a suite that forgets is a compile
  error. The first draft applied the modifier by hand in seven host helpers and
  added a source scan to police it — a guard over an API the same change
  invented (#1390's lesson), and it flagged `ImageSnapshotCIScopeTests` and
  `RasterPrimitiveEvidenceTests` on its first run for merely saying the words.
  What the type cannot close is a suite that stops using `.pinnedImage`
  altogether; that string is also what `ImageSnapshotCIScopeTests` derives its
  CI classification from, so such a suite loses both pins and its CI
  classification together, as one visible failure there.
  Measured while doing it: **exactly one** committed reference is
  locale-dependent — pinning `en_US` instead reddens
  `testBackchannelRuleContextTokens` and nothing else in the other 52, and
  `format: .number` at `BackchannelRulesView.swift:149` is the only
  `FormatStyle` in the app (every other numeric render is `String(format:)`,
  which takes no locale, or an `Int` interpolation, which carries no grouping).
  **The sibling family is TIMEZONE, and closing it found a second machine read
  nobody had named** (#1659). `HistoryFormat`'s formatters pinned `en_US_POSIX`
  and never set `timeZone`, so 8 of the 14 `HistoryViewSnapshotTests` references
  were held only by that suite's own `setUp` assigning `NSTimeZone.default`.
  The seam is `\.formatTimeZone` (`Irrlicht/Views/FormatTimeZoneEnvironment.swift`),
  and unlike `\.formatLocale` it could not be read by the formatters as they
  stood — a file-scope `private static let DateFormatter` is reachable from no
  view — so `HistoryFormat.axisLabel`/`clock`/`fullDateTime` take a `TimeZone`
  as a REQUIRED argument and the views pass what they read. A zone-less call
  site is `error: missing argument for parameter 'timeZone' in call`. The
  default is `NSTimeZone.default` and **not** `TimeZone.autoupdatingCurrent`,
  which reads like the obvious mirror of `\.formatLocale`'s
  `Locale.autoupdatingCurrent` and is wrong: measured, after
  `NSTimeZone.default = UTC` an unset `DateFormatter` renders UTC while
  `TimeZone.autoupdatingCurrent` **and** `TimeZone.current` both still report
  the host zone, so either would have been a real change for exactly the
  processes that assign it. The pinned zone is `UTC` because that is what the
  references were recorded under, so nothing was regenerated.
  Two things there are worth carrying beyond the fix. **The pin is three
  environments, not one**: pinning only `\.formatTimeZone` still reddened 6 of
  the 14, because **Swift Charts resolves `AxisMarks(values: .automatic(…))`
  through `\.calendar`** — whose default `Calendar.current` *does* follow
  `NSTimeZone.default` — so the deleted `setUp` had been holding tick and
  gridline POSITIONS as well as label text, which is why the `compact` quota
  chart moved despite drawing no axis labels at all. `PinnedSnapshotHost` now
  pins `\.formatTimeZone`, `\.calendar` (rebuilt `.gregorian` + the pinned
  locale, so the host's week rules go too) and `\.timeZone`. And **the two
  halves are separately invisible**: reverting the product seam while keeping
  the pins reddens 4 references, dropping the `\.calendar` pin reddens the other
  6, and the union is the 8 — reverting the seam leaves every `M/d` label green
  on a `Europe/Berlin` host, because a one-hour shift does not change a date.
  That is #1630's mutation B one family later: only the rendered-string
  assertions in `PinnedTimeZoneSnapshotTests` catch it. That suite deletes
  `HistoryViewSnapshotTests`' `setUp` rather than keeping it, deliberately —
  with no process-wide assignment left, those 8 references are themselves the
  live evidence that the seam reaches every date the panel draws.
  **The third member is `@AppStorage`, and it is the one where the seam already
  existed** (#1662). `GroupViewSnapshotTests` and `SessionRowSnapshotTests` each
  held eight real preference keys open in `setUp` and put them back in
  `tearDown`, `AdapterIconAppearanceTests` six — and unlike the locale and the
  zone, **no product seam was needed for the views**: SwiftUI's own
  `.defaultAppStorage(_:)` is honoured by `@AppStorage`, where `format: .number`
  ignores `\.locale` and a file-scope `DateFormatter` is reachable from no view
  at all. So the whole view half is one modifier on `PinnedSnapshotHost`. Three
  things there are worth carrying. The parameter is typed **`InMemoryDefaults`,
  not `UserDefaults`**, so `.standard` is not expressible at a snapshot host —
  the type guarantee #1390 prefers over a guard, one family further on — and its
  default is an EMPTY store, which is what made adoption free: an unset key
  renders at the `@AppStorage` declaration's own default, which is exactly what
  the deleted `setUp`s were assigning, so all 53 references still match and none
  was regenerated. **A suite that forgets to pass its store gets isolation and a
  visible failure** ("my pinned value did not reach the view"), never a
  reference that silently photographs someone's Settings — the polarity is what
  makes the default safe. And the abort path (#1523, the 240s tree kill,
  `--budget`) is answered by **removing the state, not unwinding it more
  carefully**: with nothing written there is nothing a skipped `tearDown` can
  fail to restore.
  Two keys did need a product seam, because they are not `@AppStorage` and no
  environment reaches them: `SessionManager` reads `summaryDisplayMode` and
  `projectGroupOrder` itself, so it takes the store it reads them from
  (`.standard` by default). One measured trap there, found by the change's own
  new test rather than in review: **`didSet` DOES fire for a write in the second
  phase of a base class's own `init`**, so seeding a value read from the store
  wrote it straight back — in the app, a preference persisted into the user's
  real `io.irrlicht.app` domain that they never set. The seed goes through the
  `@Published` STORAGE (`self._summaryDisplayMode = Published(initialValue:)`),
  which the property observer does not see.
  Measured while doing it, and it settles the "is this hypothetical" question the
  issue left open: with the pin deleted, the probe in
  `PinnedAppStorageSnapshotTests` reads `menuBarStyle = combined`,
  `notificationsEnabled = true` and `advancedSettingsExpanded = true` off this
  machine — the nine keys nothing pinned were live inputs, not a worry. The
  structural half is `PersistentDefaultsLintTests`' second rule, which fails the
  build on any `UserDefaults.standard` **mutation** in the test targets; READS
  are deliberately left legal, because `object(forKey:)` is how a test says "and
  the process domain was not touched", and banning it would ban the evidence
  along with the defect. What that rule cannot see is a production call site a
  test drives — measured, two sound keys still reach that domain across a full
  gate run and no test source names the write (#1672).
  **The fourth member is the WALL CLOCK, and it is the one where pinning the
  other three would have looked like coverage** (#1663).
  `SessionListView.formatResetTime` stacked four machine reads —
  `Calendar.current`, `Date()`, and a `DateFormatter` with neither `locale` nor
  `timeZone` — and #1659 left it whole rather than half-covering it, because
  `Date()` there does not shift a rendered time, it **selects the format
  string**: the same input renders `"09:00"` before midnight and `"Fr. 9:00"`
  after. The seam is `\.formatNow` (`Irrlicht/Views/FormatNowEnvironment.swift`),
  the formatting moved to `QuotaResetFormat` where all four values are REQUIRED
  arguments (#1659's shape), and `PinnedSnapshotHost` gained a `now:` whose
  default is a fixed instant, on #1662's polarity. Five things are worth
  carrying.
  **The distinction between the two halves is measured, not argued**: putting
  `Date()` back at the call site leaves EVERY string assertion in
  `PinnedNowSnapshotTests` green and reddens exactly one arm, the two-clock byte
  comparison — the same "only the rendered assertion catches it" shape #1630's
  mutation B and #1659's had, one family on.
  **The key carries a closure, not a `Date`**, which is not the obvious mirror
  of `\.formatTimeZone`'s computed `NSTimeZone.default` default: a computed
  `Date()` default is a value never equal to itself, where that one is stable
  between assignments, and a fixture wants ONE instant per subtree rather than a
  fresh one per read (the microsecond-disagreement hazard `quotaWindowRow`
  already documents for `quotaPacePercent`). `FormatNow.wallClock` is a single
  stored value and `FormatNow(fixed:)` is the stopped form.
  **`Calendar.current` was closed by taking `\.calendar`, not by forcing
  `.gregorian`** — that key already existed, already defaults to
  `Calendar.current`, and has been pinned by the host since #1659 — so a user on
  a Japanese or Buddhist calendar keeps theirs. What IS forced is the calendar's
  ZONE, to the zone the formatter renders in, so the day the branch is decided
  in and the day the string is rendered in cannot disagree; that line is
  load-bearing (removing it reddens four arms) while the identity provably is
  not (measured across all 16 Foundation identifiers at a fixed zone, zero
  disagreements, asserted rather than assumed — which is what justifies there
  being no both-sides pixel arm for the calendar).
  **A pin alone would have been untested by construction**, since #1663 verified
  the site is reached by no committed reference. The row's `Text` is therefore
  extracted into `QuotaResetLabel`, a view a test can host: an `@Environment`
  read off a `SessionListView` value a test constructed itself, outside a view
  update, answers the DEFAULT — a pin reaching nothing wearing the shape of a
  passing test. `PinnedNowSnapshot.referenceNow` is the one constant in this
  family NOT read off the committed set, because no reference contains a
  wall-clock-dependent render; #1663 regenerated no PNG and the untouched 53 are
  the evidence.
  **The blast radius is ONE call site, deliberately** — `QuotaResetLabel`, not
  the app-wide clock injection #1663 defers. (The other function that issue names,
  `formatClockTime`, never read a clock: its two machine reads were the
  formatter's unset locale and zone, closed by the existing two seams.) Three
  further wall-clock reads on the same chip stay unconverted (`quotaPacePercent`,
  `mergeIntoBuckets`' staleness test, `formatTimeUntil`), and the first two are
  pixel-visible, so the `rate_limit` fixture #1663 anticipates is still not safe
  to seed: that is #1675, filed rather than folded in.
  And no, a test
  run does not modify the user's real app preferences:
  `UserDefaults.standard` under `swift test` resolves to
  `com.apple.dt.xctest.tool`, measured again across two full gate runs against a
  byte- and mtime-identical `io.irrlicht.app.plist`.
  **A sibling family reads the machine for its WRITES rather than its
  formatting**, and is closed the same way: `AppHome`
  (`Irrlicht/Managers/AppHome.swift`) is now the one place the app resolves the
  user's home, and under XCTest it answers a per-process directory beneath
  `NSTemporaryDirectory()`. Five call sites resolved it directly and three of
  them wrote — a fixture into the live daemon's
  `~/Library/Application Support/Irrlicht/instances/`, the developer's own
  `session-order.json` **overwritten** with test data (`{"order":["b","a"]}`,
  measured), and `~/Library/Sounds/IrrlichtCustom-<event>.<ext>` behind a
  `defer` (#1669, #1670; #1661 was the same class in `~/Library/Preferences`).
  Four things there are worth carrying. **`HOME` is inert** — measured,
  `HOME=<tmp>` alone leaves `NSHomeDirectory()` at the real home and only
  `CFFIXED_USER_HOME` moves it — so a redirect built on `HOME` looks like a fix
  and changes nothing; the redirect is therefore in-process and keyed on
  `NSClassFromString("XCTestCase")`, the signal #832 already uses to keep tests
  off the live daemon socket, so it holds under a bare `swift test` and under
  Xcode rather than only under a script that sets an env var. **Nothing sweeps**:
  a function deleting files from a directory it did not create is what removed
  ~1895 files from a real `~/Library/Preferences` during #1661, and no code in
  this family has a removal path. **The standing guard is split by what each
  half can see** — `tools/lib/swift-suite.sh`'s witness brackets the `swift
  test` invocation and fails on any new entry in `~/Library/Preferences` or
  `~/Library/Sounds`, which is the only check that still runs when the suite
  ABORTS (#1523) and a `defer` does not, while
  `Tests/RealHomeIsolationTests.swift` locks the resolved PATHS, because the
  worst of #1669 is an overwrite that adds no directory entry and the level that
  would see the rest is written continuously by the live daemon. And the
  witness's noise is a function of its WINDOW, stated honestly because the
  flattering version is what erodes a guard: 0 additions across each of two
  suite-length windows, 4 unrelated background plists across one 870s window of
  interactive use. It is not filtered by name — #1661's leaked files were
  `<uuid>.plist`, so any name filter that quietened the churn would have
  quietened the incident. `Tests/RealHomePathLintTests.swift` is the structural
  half, over the app AND test targets, with an existence-checked exemption list
  (two entries) because the safe construct is built out of the banned one.
  The suite also aborts intermittently (#1523) in a way that **truncates the
  run** while every suite that did report says "0 failures" — so the exit code
  is the only reliable signal, never the last totals line you can see. That is
  what `tools/lib/swift-suite.sh` judges a run by, and its `SWIFT_SUITE_TIMEOUT`
  default is 240s rather than 600s for a reason worth generalizing: at 600 it
  equalled an automated caller's entire command budget, so the caller killed the
  gate at the instant the gate would have started explaining itself and the
  `HUNG` diagnosis could never print for the caller that most needed it. A bound
  nobody can outlive reports nothing. The relation
  (`timeout + cold build < pre-push budget`) is asserted in
  `tools/lib/swift-suite_test.sh` rather than left true the day it was typed.
- Web (only when touching a `web/` tree): `npm test` in that tree. There are
  two independent suites, each with its own `node_modules`:
  - `platforms/web/` — the dashboard.
  - `tools/onboarding-factory/internal/viewer/web/` — the onboarding viewer.

  `npm test` runs `vitest run` (single CI-shaped pass, no watch).

  `node_modules/` is gitignored, so a fresh clone — or any new
  `git worktree add` — starts without dependencies. No manual install step is
  needed: each tree's `pretest` script runs `npm ci --ignore-scripts` when
  they're missing, so `npm test` self-heals on its first run (slow once,
  instant afterwards). To get it out of the way up front, run `npm ci` in the
  tree yourself. **Never `npm install`** — it re-resolves the dependency graph
  and rewrites `package-lock.json`; on an npm older than the one that wrote the
  lockfile it silently strips the `libc` fields from the
  `@rolldown/binding-linux-*` entries, and that churn then rides along in an
  unrelated PR. `npm ci` installs *from* the lockfile and never writes it.

  Because the two trees resolve independently, they can drift onto different
  versions of the same transitive package — which is how one ended up carrying
  a vulnerable `postcss` while the other was patched (#1225). Both are kept
  current by weekly dependabot updates configured in `.github/dependabot.yml`,
  which covers the Go modules and GitHub Actions on the same schedule; a bump
  landing in only one tree is a signal something is wrong with that config, not
  normal.

### Local CI parity — catch failures before pushing

`tools/preflight.sh` runs every PR-gating check (test.yml + web-test.yml +
ars-gate.yml + linux.yml's replay-fixtures step natively, plus the full Linux
build+test gate via Docker under `--linux`) locally and prints a pass/fail
summary instead of stopping at the first failure — so before opening a PR, run
it once instead of round-tripping through GitHub Actions per fix. Gates run
**cheapest first**, in two phases, not in "CI's order": there is no single CI
order to mirror, since those are separate workflows GitHub runs concurrently,
and the order is load-bearing under `--budget` (below) because it decides which
gates survive a squeeze. `skill-file lint` and `POSIX sh lint` are the only
coverage their file families have and cost a fraction of a second each, so they
run before four minutes of `go test` — which is the argument test.yml already
makes in-file for running the skill lint before `setup-go`.

```
tools/preflight.sh                # everything except the Linux Docker gate
tools/preflight.sh --linux        # + full Linux parity (slow: needs Docker)
tools/preflight.sh --only go      # just the test.yml-equivalent gates
tools/preflight.sh --only arch    # just the ARS architecture gate
tools/preflight.sh --only skills  # just the .claude/skills/**/*.md linter
tools/preflight.sh --only swift   # just the macOS Swift build + test suite
tools/preflight.sh --budget 540   # bound the whole run; see "The budget" below
```

**For an automated caller (an agent), `--only` chunking is the recipe, not a
debugging convenience — the unscoped run does not reliably fit a foreground
`Bash` call's 600s budget** (it reliably exceeds it on this machine; the long
pole is the `go` group's core suite + replay fixtures). Run each group as its
own **foreground** invocation instead of the single unscoped command:
`tools/preflight.sh --only go|web|arch|tools|skills|posix|security|swift` (see
`tools/preflight.sh --help` for the current group list; `linux` stays opt-in
and needs Docker). Every gate still runs — chunking only changes how many
invocations it takes. **Do not background the unscoped run to make it fit**:
a subagent is not woken by its own background job, so the run stalls silently
with the work committed but never pushed
(`.claude/skills/ir:exec/SKILL.md` Phase 4 step 11 has the incident and the
same recipe). Chunking is still the recipe for a *manual* unscoped run;
`--budget` is what covers the `--changed` run the hook performs, and the two
compose — `--only <group> --budget <n>` bounds one group.

Also read a push's exit status directly, never through a pipe: `git push … |
tail` reports `tail`'s status, so a push the hook refused looks like a success
to the caller. Assert afterwards that `git status -sb` shows a tracking branch —
this is a plausible cause of the "committed but never pushed" incident recorded
in `ir:exec` Phase 4 (#1570).

**Not `PIPESTATUS`**, which is what this paragraph advised until #1559's agent
tried it: this repo's shell is zsh, where the array is spelled `$pipestatus` and
indexed from **1**, so the bash spelling `${PIPESTATUS[0]}` expands to the empty
string and the check reports nothing at all. Advice for reading a status that
silently yields no status is this section's own subject arriving in its own
prose, which is why the fix is to name the portable check rather than to correct
the spelling — `git status -sb` works in either shell and asserts the thing
actually wanted (the branch is tracking), where a pipe status only asserts that
one command in a pipeline exited zero.

`tools/install-git-hooks.sh` (run once per clone; worktrees share the parent
repo's hooks automatically) wires `tools/preflight.sh`'s fast gates as a
pre-push hook, so a push that would fail CI is rejected locally instead. What
it installs into the shared `.git/hooks/<name>` is neither the hook script nor
a symlink to it, but a copy of `tools/git-hooks/shim`, which resolves the
**pushing** working tree at run time and execs *that* tree's
`tools/git-hooks/<name>` (#1591). Before that, the installed hook was a symlink
into the MAIN checkout, so every worktree's push ran the main checkout's
script — meaning a hook change in a worktree did not govern that worktree's own
push, and anything under `tools/git-hooks/` was untestable from the branch that
changed it. PR #1590 rewrote the hook to bound its own runtime and its own push
ran the old unbounded one, hitting the exact defect it was fixing. Three
consequences worth knowing:

- **The shim is now the one link a `git pull` cannot update.** Changing
  `tools/git-hooks/shim` means re-running the installer; changing
  `tools/git-hooks/pre-push` does not. That is the right way round — the shim
  resolves a path and execs, and has no reason to change. The installer
  overwrites whatever it finds (an older symlink install, a hand-edited copy,
  a stale shim), so re-running it is always safe and a second run in a row
  installs nothing.
- **A revision that genuinely carries no hook passes, loudly**, on stderr —
  a bisect, or a branch predating the file, has no gate there to skip, and
  refusing would only make `git bisect` hostile.
- **A hook missing from the tree while `HEAD` still carries it refuses**, as
  does one present but not executable. That is a broken working tree, not a
  revision without the hook, and a gate skipped because a file was invisible is
  this repo's most-repeated failure shape.

`tools/lib/git-hooks_test.sh` covers both halves in throwaway repos (bare
origin + main checkout + linked worktree, pushing over a filesystem path), and
carries the mutation beside the assertion: one case installs the pre-#1591
symlink and pins the OPPOSITE outcome from the identical rig, so an assertion
that the worktree's refusing hook ran cannot be satisfied by a rig where
nothing ran at all.

The hook runs `tools/preflight.sh --changed --budget 540`, which scopes every gate
to the packages and web trees the push's diff actually touches (vs
`origin/main`), so a typical push finishes in seconds rather than re-running
the whole suite. A large or cross-cutting diff (or a `go.mod`/`go.sum` change,
which falls back to the full core suite) can still take a few minutes. Skip
once with `git push --no-verify`; run `tools/preflight.sh` manually (no
`--changed`) for the unscoped full gate.

**The budget is the part not to remove** (#1570). Scoping alone did not make
the hook fit: on a one-file diff under `core/adapters/inbound/agents/` the run
measured **621s** — go 250s, arch 16s, security 355s — against an automated
caller's 600s command budget, so the *caller* killed the tool call. That is the
worst available failure: no summary, no gate name, no exit code, the commit
already made and the push not sent, and the documented recovery (`--no-verify`)
then skips the sub-second gates nobody ran. Six of thirteen PRs in one day went
out that way. `--budget <seconds>` makes the run bound itself: each gate is
given whatever is left, a gate that outlives it is **killed and reported
`TIMEOUT` by name**, every gate behind it is reported **`NOT RUN`**, and both
exit non-zero. Neither is a `SKIP` — `SKIP` means "this diff cannot break it",
which is a finished answer; these two are the absence of one, and the closing
block lists them again after the summary. `PREPUSH_BUDGET` overrides the hook's
540s (`0` = unbounded, exactly the old behaviour); an unflagged
`tools/preflight.sh` is unbounded and unchanged. The bounded runner is
`tools/lib/gate-budget.sh` — pure bash 3.2, because `timeout(1)` is not on a
stock macOS and a gate that stops being bounded on the machines missing an
optional dependency is the same defect wearing a different hat. Its unit tests
plus the end-to-end mutation (a copy of `preflight.sh` with one gate replaced
by a `sleep`) are `tools/lib/gate-budget_test.sh`, in the `tools` gate.

Two things the budget makes visible that were previously invisible, both
measured while #1570 was being fixed:
- **`gosec` was running twice per module.** `-severity`/`-confidence` filter
  the *report*, not the analysis, so `security-scan.sh`'s informational pass
  and its gate pass were the same 172s scan of the same 263 files, twice. One
  `-fmt=json` run now answers both (`tools/lib/gosec-report.sh`), which took
  the security gate from **355s to 186s** with identical coverage and verdict.
  Nothing was narrowed — deduplicating a scan is not scanning less, which is
  why gosec was *not* scoped to changed packages instead. A report that will
  not parse, or whose own `.Stats.files` is 0, is refused rather than read as
  clean: a scan that read nothing produces "no High/High findings" too.
- **The `swift` gate can consume the whole budget on its own.** Its trigger
  includes `tools/preflight.sh` itself, and `SWIFT_SUITE_TIMEOUT` defaults to
  600s — equal to an automated caller's entire command budget, so the gate's
  own careful HUNG diagnosis could never print before the caller killed
  everything. Measured on this machine at `origin/main`, the suite reaches
  `SessionRowSnapshotTests.testRelayCloudOnline` and stops there (twice, at the
  identical point; that test passes in 0.157s in isolation) — #1523/#1530
  territory. Under `--budget` the outer bound fires first and names the gate,
  and the tree kill reaches through `script -q`'s separate session, leaving no
  orphaned `xctest` (measured at `--budget 45`).

The security gate is scoped twice over: its trigger regex decides whether the
scan runs at all, and `tools/security-scan.sh --changed` then picks which Go
modules and web trees to scan, matching each scanner against the files it
actually reads. Without that second layer a pure-Go push paid for an `npm
audit` of both web trees and was rejected by a pre-existing advisory it could
not have caused (#1213) — forcing `--no-verify`, which disables every other
gate too. Both layers read the same changed set, from
`tools/lib/changed-files.sh`; its unit tests run in the `tools` gate. That set
counts **untracked, non-ignored** files as well as committed, staged and
unstaged ones (#1591). It did not until then: `git diff` cannot see a file
that was never added and `--cached` only catches it once staged, so a
newly written script selected no gates at all while the function's own doc
said uncommitted work counted. Invisible to the pre-push hook — a file has to
be committed to be pushed — and wrong for every manual `--changed` run.

Two of the failure modes it won't catch: environment-specific timing flakes
that only manifest on loaded Linux CI runners (not this machine), and true
Linux-only bugs unless you pass `--linux`.

## Task Management
- Use github issues to track tickets
- Break down larger tasks into tasks using a task tool (e.g. todowrite in opencode or TaskCreate in claude code)
- An agent picking up an issue should self-assign before starting work
  (`gh issue edit <N> --add-assignee @me`), so others can see it's actively
  being worked — `ir:exec` does this automatically at the start of its
  implement phase
