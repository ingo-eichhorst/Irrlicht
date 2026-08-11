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
Evidence living only in a merged PR body is re-run by nothing, which for the
`contracttesting` families is #1479. And a guard that *rewrites* an existing one owes
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
  combination in one-off tests and were never key-blind. A receiver gated on
  more than one permission wires `AssertPermissionGatedOnEachKey` rather than a
  hand-written table pairing each key with "the other one" — it names its key
  set once and the pairing is derived, because such a table can silently list
  only one direction and the direction that already works is
  indistinguishable from covering both. Install-type wirings use
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
  over the package currently returns eight, because
  `AssertPermissionGatedOnEachKey` is a driver that runs the permission-gate
  family once per key, not a family of its own. Check what a function asserts
  before moving this number. A new or reworked
  contract assertion is therefore the mutation rule at the top of this section
  in its most literal form: it lands with the deliberate mutation seen red for
  each obligation. That evidence currently lives in the PR body, where nothing
  re-runs it — #1479.
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
- POSIX shell scripts: `tools/posix-lint.sh` checks every tracked file whose
  **first line** is a `#!/bin/sh` shebang — today `site/install.sh` and
  `tools/linux-replay-entrypoint.sh`. Line 1 only, because
  `tools/lib/install-uninstall_test.sh` is a bash file that writes `#!/bin/sh`
  stubs inside a heredoc, and a content grep would try to lint it as POSIX sh.
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
- Factory: `go test ./tools/onboarding-factory/... -race -count=1`.
- Replay: `tools/replay-fixtures.sh` — gated in CI by linux.yml, and run
  natively as `tools/preflight.sh`'s `replay fixtures` gate, so golden drift
  surfaces without Docker. Takes ~3 minutes unscoped; under `--changed` it
  runs only when the diff touches `replaydata/`, the factory, or the
  `core/` parsers and tailer a golden is derived from.
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
build+test gate via Docker under `--linux`) locally, in CI's order, and prints
a pass/fail summary
instead of stopping at the first failure — so before opening a PR, run it
once instead of round-tripping through GitHub Actions per fix:

```
tools/preflight.sh                # everything except the Linux Docker gate
tools/preflight.sh --linux        # + full Linux parity (slow: needs Docker)
tools/preflight.sh --only go      # just the test.yml-equivalent gates
tools/preflight.sh --only arch    # just the ARS architecture gate
tools/preflight.sh --only skills  # just the .claude/skills/**/*.md linter
```

**For an automated caller (an agent), `--only` chunking is the recipe, not a
debugging convenience — the unscoped run does not reliably fit a foreground
`Bash` call's 600s budget** (it reliably exceeds it on this machine; the long
pole is the `go` group's core suite + replay fixtures). Run each group as its
own **foreground** invocation instead of the single unscoped command:
`tools/preflight.sh --only go|web|arch|tools|skills|posix|security` (see
`tools/preflight.sh --help` for the current group list; `linux` stays opt-in
and needs Docker). Every gate still runs — chunking only changes how many
invocations it takes. **Do not background the unscoped run to make it fit**:
a subagent is not woken by its own background job, so the run stalls silently
with the work committed but never pushed
(`.claude/skills/ir:exec/SKILL.md` Phase 4 step 11 has the incident and the
same recipe).

`tools/install-git-hooks.sh` (run once per clone; worktrees share the parent
repo's hooks automatically) wires `tools/preflight.sh`'s fast gates as a
pre-push hook, so a push that would fail CI is rejected locally instead. The
hook runs `tools/preflight.sh --changed`, which scopes every gate to the
packages and web trees the push's diff actually touches (vs `origin/main`), so
a typical push finishes in seconds rather than re-running the whole suite —
important for automated callers, whose bounded command timeout the full run
routinely blew past, killing the push after the commit had already landed. A
large or cross-cutting diff (or a `go.mod`/`go.sum` change, which falls back to
the full core suite) can still take a few minutes. Skip once with `git push
--no-verify`; run `tools/preflight.sh` manually (no `--changed`) for the
unscoped full gate.

The security gate is scoped twice over: its trigger regex decides whether the
scan runs at all, and `tools/security-scan.sh --changed` then picks which Go
modules and web trees to scan, matching each scanner against the files it
actually reads. Without that second layer a pure-Go push paid for an `npm
audit` of both web trees and was rejected by a pre-existing advisory it could
not have caused (#1213) — forcing `--no-verify`, which disables every other
gate too. Both layers read the same changed set, from
`tools/lib/changed-files.sh`; its unit tests run in the `tools` gate.

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
