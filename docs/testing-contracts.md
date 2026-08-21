# Testing Contracts — Permission, Hook, and Construction Guarantees

Referenced from [AGENTS.md](../AGENTS.md)'s Testing section. This is the
detailed write-up for the `core/internal/contracttesting` package and the
related package-local guards it sits beside: the seven contract families that
bind permission gating, hook delivery, hook disclosure, hook path
confinement, hook version floors, unrecognized hook events, hook entry
presence, and hook receipts to runtime behavior rather than to a static rule;
the managed-user-file declaration that backs `--print-managed-files` /
`--uninstall-hooks` / `--uninstall-task-eta` and the grant-all daemon's
shared-config refusal; the guarded-construction pair of guards in
`core/application/services`; and the probe-cost doc-comment convention in
`core/internal/costreport`. Every contract family obligation, its self-test
harness, and the incidents that motivated each rule are preserved verbatim
below.

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
    dispatch-shaped assertion stays green (measured on all seven receivers —
    claudecode's hooks and statusline, codex, copilot, geminicli, kirocli,
    vibe; reproduce the count with `git grep -rl hookjson.RequireConsent
    core/adapters/inbound/agents/` rather than trusting this list once an
    eighth adapter ships). It is not silent about
    it: reaching the backstop means a receiver skipped a check or consent was
    revoked mid-request, so it logs an error, where a receiver's own gate
    answers a quiet 200. That difference is both the surviving discriminator
    and a real user-facing property — an ordinary denied session must not
    collect an error line per tool call — so each of the seven receivers' hand-
    written consent tests now asserts **no error was logged**, and each was
    seen red again with its gate deleted. Statusline is the receiver to watch
    here: it declares ONE permission and keeps no second gate, so that
    assertion is the whole of its live per-adapter proof.
  - Beside those seven, the coverage is one shared proof plus one lock per
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
  runtime obligation for adapters that install hooks into a shared config
  file (JSON, or since #1734 TOML) —
  an install writes the resolved port not `:7837`, an entry left by a daemon
  on another port is rewritten in place rather than duplicated, and uninstall
  is not port-scoped (#1178). A new hook-installing adapter wires one call
  (see any hooks-declaring adapter's `hookport_test.go`) instead of porting a
  test file. It grades against the delivery route the adapter DECLARES
  (`HookInstaller.Delivery`, #1453), because there are two ways to satisfy it
  and the second makes the first unsatisfiable. `DeliveryURL` — the zero
  value, left unset by `claudecode`, `codex` and `copilot` — is an entry
  that CARRIES the daemon's address.
  `DeliveryAddressFree` is an entry that carries none, because it names the
  `irrlichd hook-post` beacon (`core/pkg/hookbeacon`, #1373), which reads the
  addr file at fire time — `geminicli` (#1724), `kirocli` (#1732) and `vibe`
  (#1733) all declare it (`git grep -n "Delivery:
  contracttesting.DeliveryAddressFree" core/adapters/`, the other half of
  the six adapters above); three of the four port obligations then fail by
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
  Since #1734 the same contract also grades a format the JSON matcher-group
  walk cannot read at all: `HookInstaller.ReadEntries` (raw `[][]byte` per
  event) and `EndpointOfRaw` (the delivery string out of one raw entry)
  default to bridges built from the existing JSON machinery, so every
  JSON-shaped adapter needs no changes, while `vibe`'s hooks.toml — pure
  byte-range edits, never a decoded generic structure, matched by
  `bytes.Contains` rather than a named field — supplies both directly
  (`vibe/hookport_test.go`).
  Real adapters now exercise every route and entry shape this family grades —
  `DeliveryAddressFree` (geminicli, kirocli, vibe), the flat `EntriesOf` shape
  kiro-cli's schema needs (#1716), and the raw-bytes TOML shape vibe's
  `hooktoml` needs (#1718) — but none of the three reference-wiring fixtures
  those adoptions were supposed to retire has been touched since. All three
  (`hook_endpoint_addressfree_test.go`, `hook_endpoint_flat_test.go`,
  `hook_endpoint_toml_test.go`) carry the same verbatim sunset clause, each in
  its own header: "…this file should be
  reduced to whatever it still uniquely proves, or deleted." Only one has
  a reason to stay as written: `hook_endpoint_addressfree_test.go`'s
  `correctBeaconInstaller` is what `hook_endpoint_selftest_test.go`'s whole
  `DeliveryAddressFree` mutation corpus drives against, so deleting it today
  would leave that corpus with no fixture rather than a real adapter's — a
  hermetic fixture the corpus doesn't churn against every time a real
  adapter's own wiring changes, which is a real justification but not the one
  the header states. `hook_endpoint_flat_test.go` and
  `hook_endpoint_toml_test.go` have no such tether —
  `hook_endpoint_selftest_test.go` references neither — so kiro-cli and vibe
  meeting each file's own stated sunset condition (`EntriesOf:
  FlatHookEntries`, `ReadEntries`/`EndpointOfRaw` respectively) leaves both
  ripe for the reduce-or-delete their headers call for, with no code-level
  obstacle found. Whether to actually shrink either, or formally re-justify
  keeping `hook_endpoint_addressfree_test.go` deliberately, is an open
  question this bullet does not resolve (#1730).
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
  call (see any hooks-declaring adapter's `hookdisclosure_test.go`). The
  "names no uninstalled event" arm checks against `session.AllHookEvents`, itself kept
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
  hook-receiving adapter wires one call (see any hooks-declaring adapter's
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
  unconfined in the first place. That sentence is general to every family, and
  since #1740 the *wiring itself* is enforced for hooks-declaring adapters (see
  "Contract wiring" below) — but note the two close different halves and
  neither subsumes the other: #1740's tripwire is PACKAGE-granular, so a
  claudecode that wired this contract for its hook receiver and not for its
  statusline receiver still passes it. The original bug is closed by the
  build-level rules that follow, not by that tripwire.
  `hookjson.DecodeConfined` welds the body decode
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
  `settings.json` at any version; every hook-installing adapter now declares
  the floor the same way, by declaring
  `Version: &agent.VersionGate{Min: "x.y.z", Probe: []string{"<cli>",
  "--version"}}` and nothing else. No roster is kept here, because this one
  is enforced rather than maintained: `TestEveryHookInstallDeclaresAVersionFloor`
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
- Contract wiring: not a contract family — a registry tripwire,
  `TestEveryHookInstallWiresItsContractFamilies`
  (`core/adapters/inbound/agents/hookcontracts_test.go`), riding the same
  projection as #1365's and #1372's. It closes the asymmetry #1740 named: two
  obligations over a hooks-declaring adapter are things the adapter DECLARES as
  a struct field (`Verify`, `Version`), which a registry walk can read, and the
  rest are wired as a TEST CALL in the adapter's own test package, which
  nothing could. "Harder to detect" was not "less important" — the three the
  issue was filed about are the ones whose absence is least visible in
  production (an unconfined caller-supplied path, a dead port behind a granted
  permission, an undisclosed write to a user's config), and their only
  enforcement was that whoever added the adapter remembered to copy an existing
  adapter's test files. Seven entry points are required, not the three #1740
  named: the other four (`AssertHookVersionGate`,
  `AssertUnknownHookEventObserved`, `AssertHookReceiptObserved`,
  `AssertHookReceiverPermissionGated`) have the identical shape and every
  hooks-declaring adapter already wired all seven, so they were measured in
  rather than assumed out. Note what the version row adds on top of #1365's
  floor tripwire: that one proves a floor is DECLARED and parseable, this one
  that the floor actually refuses.
  Three things are load-bearing. **It is a static walk and #1740 preferred a
  registration seam** — the seam was rejected on a fact rather than on cost, and
  the rejection is recorded in the file's header rather than here: `go test`
  compiles one binary per package, so a recorder in six adapter processes
  cannot reach an asserting test in a seventh, and carrying it across processes
  means a file that is a cache the reader cannot validate — stale entries make
  a DELETED wiring read as covered, which is #1740's own failure mode with a
  longer half-life. **It is an AST + type walk, not a grep**, which is what
  makes it worth more than the issue assumed a static walk would be: a
  reference in a comment is not a call node, a call must be reachable from a
  function `go test` actually runs (a helper nothing calls is reported
  separately, so the failure says re-attach rather than re-write), an aliased
  import counts, and a local helper that merely shares the name does not.
  **The adapter's Go package is derived, never mapped** — from `runtime.FuncForPC`
  over the permission's own `Apply` and `Uninstall` closures, which must agree —
  because a hand-written adapter-name → directory map is one more thing a new
  adapter is covered by only if someone remembers to edit it, which is this
  issue one level up (and `agent.Identity.Name` cannot answer it: "claude-code"
  is package `claudecode`, "mistral-vibe" is package `vibe`).
  Its mutation evidence is a committed corpus, `hookcontracts_shapes_test.go`:
  one synthetic adapter test package per spelling, pinned to a THREE-valued
  verdict (called / unreached / absent) so a detector that reports dead wirings
  stays distinguishable from one blind to them. The rows that must NOT be
  credited carry the value — a name in a comment, a name in a string, a
  reference that is not a call, a local helper of the same name, a dead helper
  — and so do the rows pinning declared LIMITS in both directions: a skipped
  test and a call under `if false` still count (over-crediting), while a wiring
  held in a package-level var of func type is not seen at all (under-crediting,
  fail-closed). The corpus has already earned its keep twice: the needle guard
  caught a case that had stopped containing its own construct, and the
  `skipped_test` row — whose directory NAME is the fixture — caught the walk
  recovering the package under test by trimming `_test` off `PkgPath` instead
  of reading `packages.Package.ID`'s variant tag, which silently dropped every
  test file of any package whose path ends in `_test`.
  What it does not cover is stated in the file's header rather than implied,
  and the headline limit was MEASURED rather than reasoned about. The walk is
  PACKAGE-granular: it never asks which receiver or which permission a call was
  for. Replacing the `AssertHookReceiverPermissionGated` call in claudecode's
  `hooks_test.go` with a local no-op leaves that adapter's row GREEN, because
  its statusline receiver wires the same family in the same package — the exact
  #1361 shape. `AssertPermissionGated` is excluded from the required set for the
  same reason and says so in `unenforceableHere()`, whose row's PREMISE (every
  hooks adapter's package already calls it, so a required row would discriminate
  nothing) is re-derived every run rather than trusted once. That exclusion also
  surfaced a finding worth its own ticket: five of the six hooks-declaring
  adapters wire `AssertPermissionGated` for their hooks INSTALL closures and
  claude-code does not — it wires the family only for its instructions
  permission. The other limits: a skipped or constant-false-guarded call still
  counts; a wiring reached through a package-level func var or a helper in a
  third package is not seen (fail-closed); an entry point taken as a value and
  called later is not seen (fail-closed); and — the standing one — nothing here
  says the wiring is CORRECT, which is what the families themselves are for.
  Note what the walk DOES catch, which is what #1740 is about: the new adapter
  that wires a family nowhere at all.
- Managed user files: every `modify`-kind permission with an `Apply` closure
  declares the shared, user-owned file(s) that closure writes
  (`agent.Permission.Writes`, an `agent.ManagedUserFile` carrying `Path`,
  `Uninstall`, and `Verify`/`Version` (each described in its own bullet
  above)). Since #1731 it also carries `Also []func() (string, error)`, for
  the rare permission whose ONE `Apply` writes a SECOND shared file beyond
  `Path` — two adapters use it today: mistral-vibe's hooks install writes
  `$VIBE_HOME/hooks.toml` (Path) plus the `enable_experimental_hooks` gate in
  `$VIBE_HOME/config.toml` (Also), without which the CLI never reads the
  hooks at all; kiro-cli's writes its hook entries (Path) plus
  `chat.defaultAgent` in `settings/cli.json` (Also), so the entries are read
  by the agent kiro-cli actually dispatches to. `Path` stays what every
  narrowed projection below means by "the file this permission writes" —
  `Also` changes that for nobody — but both consumers that protect an
  undeclared write (`PermissionService.sharedConfigRefusal` and
  `agents.ManagedUserFiles`, both below) resolve every `Also` entry with the
  same rigor as `Path`, because an unprotected secondary write is exactly
  #1449's incident shape one field over. Three projections read `Writes`,
  and they read deliberately different slices: `agents.ManagedUserFiles`
  returns everything — what
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
  OR any `Writes.Also` entry (#1731) resolves outside the daemon's own
  `IRRLICHT_HOME`, with an error naming the
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
  All seven contract families pass by construction against a correct adapter
  — every route and entry shape the hook-endpoint family grades now has one
  (see that bullet above) — so their whole value is that they *can* fail.
  Seven is a count of
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
- Probe costs in doc comments: `core/internal/costreport` is "Replay's measured
  figures" one layer down, for the numbers that justify a CONSTANT rather than
  describe a catalog — `shelloutTimeout`, `ancestryReads`, `clientHostBudget`,
  `bundleIDMemo`, `gitTimeout`, `gitHistoryTimeout`, `gitMaxOutput` (#1572). Two
  doc comments in one file described the same `plutil` exec at 2.2ms (#1524) and
  9.7ms (#1544); the mechanism is what settled it, and the answer is that the
  4x is LOAD — the same generator on one machine reports 5.4ms idle and 10.2ms
  under twelve busy loops, and #1544's `ps` figure moves the same way, so one
  cause explains both of its numbers. **Read `min`, never median, when carrying
  any of these to another machine.** Three things are load-bearing and none is
  the obvious one.
  **No equality gate, deliberately.** Replay's census can compare a committed
  literal against a fresh measurement because its subject is a fixed catalog on
  disk; these are a property of a machine under a load, so a threshold would
  fail for reasons unrelated to the code and be widened until it protected
  nothing. What IS enforced is that every anchored figure names the command
  that regenerates it (`AssertFiguresNameTheirGenerator`, checked in both
  directions; `git grep "regenerate:" core/` is the reverse), and that the
  generator REFUSES rather than printing zeros. Discovering figures was tried
  and rejected on the measurement — "median" appears 4x in the git adapter's
  doc comments and only 3 are figures, so a heuristic arrives with an exemption
  list nobody re-reads. That is #1518's subject, in the tree that has more of
  them; #1578's magnitude floor does NOT transfer here, because these values
  are 2-300 and collide with everything.
  **A DERIVED figure needs the same refusal as a measured one, and that is the
  hole this closes.** `Row.WithRate` renders bytes-per-population and refuses
  BY NAME instead of rounding to zero — because #1572's own first generator
  printed `0 bytes out = 0/commit` for a grep walk and `41 bytes out =
  0/commit` for `rev-parse`, in a block that PASSED. A guard keyed on
  `bytes == 0` catches the first and not the second: 41 is a real measurement
  and `41/3209` is 0 in Go. The pre-fix rendering is committed as
  `rateAsShipped` and re-measured every run, and the sweep beside it prints
  "24 of 42 pairs" so an axis that stopped containing the defect fails loudly.
  **The cannot-run case execs a path that does not exist** rather than stubbing
  the answer, because "the loop collected nothing" and "the loop collected the
  duration of the FAILURE" are different defects and only a real
  never-answering child reaches the second.
  Two costs are stated where they live rather than left to be discovered.
  Sample counts are PER PLAN — the whole-process-table `lsof` scans cost ~0.3s
  a call, so #1544's n=300 would be two minutes for one row — and `n` is
  printed in every row so a smaller one is disclosed. And the git generator
  deliberately does NOT build the `git fast-import` synthetic behind
  `gitHistoryTimeout`'s 100k/1M curve points (~200 lines, minutes per point,
  for a constant chosen against an order of magnitude): those two rows are
  present and REFUSED by name, because a block that quietly stops at what is
  easy reads as a complete measurement of a shorter curve. `tmux.list_clients`
  refuses for a different reason — asking a socket with no server STARTS one.
