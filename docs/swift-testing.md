# macOS Swift App Testing

Referenced from [AGENTS.md](../AGENTS.md)'s Testing section. Covers
`swift build && swift test` for `platforms/macos/`, the `macos-swift.yml` CI
job, the image-snapshot CI-scope decision (why six snapshot suites are
gated only on the reference host and never in CI), the pinned-scale /
pinned-locale / pinned-timezone / pinned-`@AppStorage` / pinned-now
environment seams that made snapshot tests host-independent, the
`AppHome`/real-home isolation guard, and the `swift-suite.sh` harness's own
timeout/witness design.

- macOS app (only when touching `platforms/macos/`): `cd platforms/macos &&
  swift build && swift test --skip LauncherTestHarness --skip LauncherHarnessTests`,
  run locally by `tools/preflight.sh --only swift` and by the pre-push hook.
  Since #1530 CI's `.github/workflows/macos-swift.yml` runs it too, as a second
  job beside the build and through the same `tools/lib/swift-suite.sh` harness,
  so a hang there is a named failure at 240s rather than a job cancelled at the
  cap with an empty log. `swift` is still the one gate deliberately **stronger**
  locally than in CI — a runner has a virtual display, a stock font set and no
  usable audio stack — but it is no longer the only place the suite runs.
  **Image snapshots are graded on the reference host only, permanently and by
  choice. This paragraph is the decision record for #1615; `macos-swift.yml`'s
  header, `ImageSnapshotCIScopeTests` and the issue threads point here rather
  than restating it.** Six suites — `DaemonErrorBannerRenderTests`,
  `GroupViewSnapshotTests`, `HistoryViewSnapshotTests`,
  `SessionListDaemonErrorWiringTests`, `SessionListUnappliedGrantsWiringTests`,
  `SessionRowSnapshotTests` — are `--skip`ped in CI and run under
  `tools/preflight.sh --only swift` and the pre-push hook and nowhere else. A
  seventh, `BackchannelRulesViewSnapshotTests`, was in this set until #1874
  deleted its subject with the rest of the backchannel. This roster is not the
  source of truth — `ImageSnapshotCIScopeTests`' map is, and that map is
  cross-checked against `macos-swift.yml`'s own arguments, so this prose is the
  one copy of the list that can go stale without a test noticing. Two further
  image-snapshot suites
  (`PermissionWizardEffectErrorRenderTests`, `UnappliedGrantsBannerRenderTests`)
  reproduce byte-identically on a runner, stay gated there, and their passing is
  itself the evidence that the residual is content-dependent rather than a
  blanket host difference.
  **How big the exclusion is, is DERIVED and asserted rather than typed.**
  `ImageSnapshotCIScopeTests.testTheUngatedPopulationIsExactlyTheSkippedSuites`
  counts those six suites' tests off the live bundle through XCTest's own
  `XCTestSuite(forTestCaseClass:)` — so it agrees with a run's `Executed N
  tests` by construction rather than by a source scan agreeing with a test
  runner by luck — pins the total at **55**, and prints the whole census on
  every run. It also fails if a skipped suite goes EMPTY, which is the rot this
  decision creates: a suite that runs on one machine and holds nothing runs
  nowhere, and reads exactly like one that passes everywhere. The gated
  remainder is printed and deliberately NOT pinned, because it moves with every
  test anyone adds anywhere in the target — 487 of 542 on 2026-09-03 on the
  reference Mac, read off the `ci-scope census:` line that
  `swift test --skip LauncherTestHarness --skip LauncherHarnessTests` prints
  (392 of 440 on 2026-08-19, before #1802's suites arrived and #1874's left).
  That split is "Replay's measured figures" applied one platform over, and this
  is the file that earned it: `macos-swift.yml`'s header claimed "270 of 318",
  was corrected to "272 of 320" by the PR that measured it, and was 115 tests
  stale (320 → 435) by the time #1615 was closed.
  **Re-recording the references is not the fix, and is the one move to refuse.**
  It is what #1034 and #1044 both did, both wrongly, to what turned out to be an
  appearance-mode bug (the cautionary tale below). Here it is worse: the runner
  is on a *newer* OS than the reference Mac (runner macOS 26.5.2 / 25F84,
  reference 26.5 / 25F71), so re-recording chases a moving image AND inverts
  which host is authoritative — the app ships to the developer Mac's OS, not to
  a runner image. `git status --porcelain platforms/macos/Tests/__Snapshots__`
  is the check; every PR in this line of work (#1614, #1630/#1658, #1659, #1662,
  #1628, #1615) regenerated **zero** PNGs, and the untouched reference set is
  what makes each of their claims checkable. #1874 held that line while
  *removing* two references: it deleted the pair whose subject it deleted and
  regenerated none of the other 60, so `git status --porcelain` showed exactly
  two `D` entries and nothing else.
  **A perceptual tolerance is not the fix either**: #1509 measured one wide
  enough to absorb this class of drift as also wide enough to pass a **missing
  architecture segment**. A tolerance that admits the defect it was sized
  against is a deleted assertion with a number on it.
  **What is measured, and what is still unknown.** The pixels HAVE been looked
  at (#1628, from the evidence job's artifacts) and the hypothesis #1615 opened
  with — "the failing renders are the ones drawing glyph/vector content" — is
  **dead as stated**: a fixture rendering one thing each (flat fill, stroked
  bezier, system-font text, an SF Symbol, and each of those again in a
  translucent dynamic `Color.secondary`) came back byte-identical between runner
  and reference host on all six. The 36 failures are two populations: 34 whose
  differing pixels sit inside the 28 × 28 px box `SessionState.adapterIcon`
  draws, at maxΔ 1-5/255 over 0.01-0.9% of the image — brand icons rasterised
  from an inline SVG string through `NSImage(data:)`, a path no SwiftUI
  primitive reaches — and 2 control labels in the backchannel rule editor at
  maxΔ 142/197, the same glyphs sitting ~1 device pixel lower, i.e. a sub-point
  baseline landing on a different pixel phase. That editor and its two
  references were deleted by #1874, so the second population is now empty and
  the 36 figure is historical: only the 34 icon-box failures remain reachable.
  The toolchain is measured NOT to be the variable: all 15 Xcodes on
  `macos-latest`, including 26.3.0 build 17C529 which is bit-for-bit the
  reference Mac's, produce the same 36 failures,
  while that Mac on that build produces none. **What nobody has established is
  WHY** — "the OS differs" is where the evidence stops, not a root cause, and
  accepting the containment does not make anything about `NSImage(data:)`'s SVG
  rasteriser known. One residual is predicted rather than measured: #1658 pinned
  the locale, so `testBackchannelRuleContextTokens`'s remaining difference
  *should* have been rasterisation only — but no post-#1658 runner render was
  ever diffed against that reference, and #1874 deleted it, so the prediction
  stands permanently unconfirmed rather than merely untested. Nothing turns on
  it any more; it is recorded because a prediction that quietly stops being
  checkable should not read as one that was checked.
  **What the decision costs, stated here rather than discovered later: a
  contributor without a Mac cannot run these 55 tests at all, and CI will not
  run them either.** An outside contributor's image-snapshot change is ungraded
  until the maintainer next runs `tools/preflight.sh --only swift` or pushes
  through the pre-push hook — and CI is not merely silent about it, it is
  confidently green, because the suites are skipped by name. That contribution
  shape is not hypothetical: PR #1470 was merged from a fork on 2026-08-19
  touching five files in `platforms/macos/`. It touched no image-snapshot suite,
  so it is the shape rather than an instance, and no cheap mitigation is built
  here — the honest statement is that the local gate and the pre-push hook are
  the whole of the coverage for those 55, and that a fork PR's green `swift-test`
  check says nothing about them.
  **The pixels can still be collected, on demand.** `swift-snapshot-evidence`
  ran on every PR while the policy question was open and now runs on
  `workflow_dispatch` only — narrowed rather than deleted, because it is the
  only way anyone can look at these renders again (a runner OS bump, a
  contributor asking whether the residual moved, #1615 reopened), and a third
  macOS job per PR at 10× billing buys nothing while the answer stands. `gh
  workflow run "macOS (Swift)" --ref <branch>` publishes the failure images, the
  committed references beside them and the six primitive rasters; the job still
  fails loudly when it cannot COLLECT, which is the property that made its
  output trustworthy in the first place.
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
  The backchannel rule editor's threshold field rendered through
  `format: .number`, so its reference PNG read `150.000` — a picture of the
  recording contributor's `de_DE` regional settings, where a runner rendered
  `150,000`. (That editor is gone as of #1874; the seam it forced is not — see
  the end of this paragraph.) Three things
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
  Measured while doing it: **exactly one** committed reference was
  locale-dependent — pinning `en_US` instead reddened
  `testBackchannelRuleContextTokens` and nothing else in the other 52, and
  `format: .number` in the backchannel rule editor was the only `FormatStyle`
  in the app (every other numeric render is `String(format:)`, which takes no
  locale, or an `Int` interpolation, which carries no grouping).
  **#1874 deleted that view, so the app now renders no `FormatStyle` at all and
  no surviving reference is locale-dependent.** `\.formatLocale` stays, because
  its remaining consumers read it and pass it explicitly
  (`SessionListView.swift`, `QuotaResetFormat.swift`, `HistoryView.swift`), and
  `PinnedLocaleSnapshot.referenceLocale` stays `de_DE`, because that is still
  what the surviving 60 references were recorded under. What did NOT survive is
  the **lock**: `PinnedLocaleSnapshotTests` drove the deleted editor, so it
  retired with its subject rather than being re-pointed at a view invented to
  keep it alive — #1390's lesson, that a guard over an API the same change
  invents proves nothing, applies exactly as hard to a guard over a *view* the
  same change invents. The consequence is the thing to carry: **a wrong
  `referenceLocale`, or a `FormatStyle` that reads the process locale, would
  now redden nothing.** Whoever next renders a number through a `FormatStyle`
  owes this family a fresh both-locales lock over that real view, on the
  `PinnedScaleSnapshotTests` pattern — one view, two locales, at most one of
  which can agree with the host.
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
  test merely DRIVES, and **#1672 is that gap, closed**. The write was
  `NotificationEventRow`: it mirrored its sound key into `@State`, loaded it in
  `.onAppear` and persisted it back from `.onChange(of:)`, so merely RENDERING
  the row wrote the value it had just read — and `SettingsViewTests` renders
  `SettingsView`. Four things there are worth carrying.
  **The loop is #1673's `didSet` trap in SwiftUI clothing**, and the fix is the
  same move: remove the second copy rather than guard the write-back more
  carefully. The row is `@AppStorage` over its own key now, derived on every
  render, with a `Binding` whose `set` runs only for a pick — so it needed no new
  product seam and inherits `.defaultAppStorage` the way every other
  `@AppStorage` does.
  **It was invisible for two compounding reasons, and both of them qualify any
  "the domain did not change" measurement in this area.** It wrote back the value
  `register(defaults:)` had just seeded, so no value changed, the plist's mtime
  never moved, and `object(forKey:)`/`dictionaryRepresentation()` — which merge
  the registration domain under the application one — read identically either
  way. Only the application domain's KEY SET distinguishes a persisted default
  from a registered one, which is what `InMemoryDefaults.writtenKeys` is for, and
  even that saturates: once the key is there, the write is undetectable. And it
  fired only for the events whose `defaultSound` differs from
  `SoundChoice.default` (`.ping`) — `.ready` (funk) and `.contextPressure`
  (sosumi) wrote, `.waiting` did not, because for it the loaded value equalled the
  `@State` initial one and `.onChange` never fired.
  **Two of three keys is what made the right hypothesis look falsified.** #1672's
  own body rules out "every rendered row writes its default back" on the grounds
  that `soundOnWaiting` was absent and that a SettingsView-only run left the
  plist mtime unchanged — both measurements correct, the conclusion wrong,
  because the real rule was one step narrower and the write was idempotent. A
  dismissal can be right about what it ran and wrong about what that excludes;
  the way out was an instrumented run naming the call site (a `print` in
  `handle`, reverted), not a better inference.
  **The structural half is a THIRD rule in `PersistentDefaultsLintTests`: the
  same mutation scan over the APP target**, which is where a test-source scan is
  blind by construction. Measured, it reports exactly `SettingsView.swift:987`
  and `:1004` against the pre-fix tree and zero after, so it carries no
  exemption list. Its behavioural counterpart is
  `testRenderingTheNotificationRowsPersistsNoSoundChoice`, whose vacuity guard is
  `InMemoryDefaults.readKeys`: a row that never rendered (the master gate is
  collapsed by default) and a row still resolving `UserDefaults.standard` both
  write nothing too, so "wrote nothing" is worthless without "and it asked THIS
  store". The **runtime** witness #1672 also proposed — `tools/lib/swift-suite.sh`
  comparing named domains rather than directory entries — landed as #1688, and
  what #1690 measured about it is why it compares VALUES and not only key sets:
  the domain is quiet enough to adopt (0 changes across two suite-length
  windows, against ~1 plist/218s for the directory witness) but a GROWTH-only
  check is green on any domain that already holds the key, i.e. on the machine
  that found this. Measured again in #1688, as the mutation that reddens the
  #1672-shaped case and leaves the added/removed cases green. What no
  before-to-after comparison of a domain can reach is the last step of the same
  argument — an IDEMPOTENT write, which is #1672 exactly as it happened — so the
  domain witness prints that limit on every clean run rather than a bare clean
  line, and the checks that DO see it are the two in this paragraph.
  **#1689 is the READ half of the same member, and its premise turned out to be
  false in the useful direction.** `SettingsView.reconcileNotificationsMasterDefault()`
  DECIDED from `UserDefaults.standard.object(forKey:)` and wrote through
  `@AppStorage`, so under a pinned render the guard consulted the machine while
  the write landed in the store the host supplied — and
  `NotificationSettings.masterEnabled()` defaulted to `.standard` too, so the
  VALUE came off the machine as well. Four things are worth carrying.
  **`@AppStorage` CAN express "absent"**, which is what the issue (and the
  #940 comment) assumed it could not, and it is why this needed no product seam
  either: SwiftUI declares `AppStorage.init(_:store:)` for `Bool?` — and `Int?`,
  `Double?`, `String?`, `URL?`, `Data?` — and an optional wrapper answers `nil`
  for a key that is not in the store. So the fix is a second `@AppStorage` over
  the same key, and the decision and the write are ONE store by construction
  rather than two that a host keeps in sync; the alternative the issue proposed
  (`SettingsView(defaults: UserDefaults = .standard)`, threaded to three call
  sites) is a convention with a default argument, where this is a property of
  SwiftUI's own resolution. Two wrappers over one key is not #1672's `@State`
  mirror: neither caches, so they cannot disagree and there is no write-back
  loop. The `anyEventEnabled` half comes from the view's three per-event
  toggles — verbatim the expression it already used for its
  blocked-notifications hint — with the RULE still
  `NotificationSettings.masterEnabled(master:anyEventEnabled:)`.
  **No real user was affected, and the proof is one grep**: nothing in the app
  target applies `.defaultAppStorage(_:)`, so there `@AppStorage` and
  `UserDefaults.standard` are the same domain and the guard cannot take the
  wrong branch. It is a test-isolation defect wearing a user-facing shape, which
  is the reverse of #1672 — and the equivalence is measured rather than argued,
  over eight value shapes at one key (absent, `Bool`, `Int`, three `String`s):
  the optional `@AppStorage` agrees with a bare `object(forKey:) == nil` and the
  `Bool` one with `bool(forKey:)`'s coercion on all eight, including the five
  only a hand-run `defaults write` can produce. The plausible alternative
  (`object(forKey:) as? Bool == nil`) disagrees on five of them, which is what
  makes that arm discriminating.
  **The structural half is a FOURTH rule in `PersistentDefaultsLintTests`,
  scoped to `Irrlicht/Views/` and banning the RECEIVER rather than a set of
  accessors** — so a read, a mutation and a bare `UserDefaults.standard` handed
  to something that takes a store are one rule. Reads stay legal everywhere else
  for #1662's reason, and an app-wide read ban is not tractable: measured, 16
  non-comment references exist in the app target and 14 are ordinary reads in
  managers, models and the menu-bar controller, so the rule would arrive with 14
  exemptions. The narrowing is a property, not a preference — `Irrlicht/Views/`
  is exactly the set of files declaring a SwiftUI `View` (14 files; a `: View`
  conformance appears nowhere else in the target) and it is the code
  `PinnedSnapshotHost` pins. It held exactly two references, both reads, both
  removed, so it carries no exemption list. Its declared limit is a false
  NEGATIVE, not a false positive: a view calling a helper that reads the domain
  for it is invisible (`MenuBarStyle` and `ContextPressureThreshold` both hold
  such reads).
  **And the mutation nobody asked for is again where the coverage was.** The
  first lock written for the guard — "an explicit `notificationsEnabled = false`
  is not overridden" — stays GREEN when the guard is deleted, because the fix
  passes `master:` into the pure rule and `false ?? anyEventEnabled` is still
  `false`. The value is held by the RULE; what the guard buys is that a render
  whose key is already present persists NOTHING, i.e. #1672's write-back loop in
  a second place, idempotent and therefore invisible to every value comparison.
  Asserting it needs the key seeded through `register(defaults:)` rather than
  `set`, so it is PRESENT for the read (`object(forKey:)` merges the domains)
  while `writtenKeys` stays a clean observation of what the render itself
  persisted.
  **#1693 is that same read one call site on, and it is where this member's fix
  stopped being a seam and became a REMOVAL.** `SessionManager.sendNotification`
  guarded on `NotificationSettings.masterEnabled()` — default argument
  `UserDefaults.standard` — while the next line read the sound choice from
  `self.defaults`, so one method decided WHETHER to notify from the machine and
  WHICH sound from its input, two frames below a
  `checkStateTransitionNotification` that had just read `notifyOnReady` off that
  same injected store. Four things are worth carrying.
  **The DEFAULT ARGUMENT was the defect rather than the call, so it went.** Four
  `UserDefaults = .standard` parameter defaults existed in the app target and
  three had no caller relying on them (`masterEnabled`, `choice`,
  `resolveNotificationSound`), so removing all three cost no call site anything —
  the compile is that measurement — and the pre-fix spelling is now `error:
  missing argument for parameter 'defaults' in call`. Where removal is possible
  the compiler is a strictly stronger guard than a lint rule (#1659's shape),
  which is the whole reason no fifth `PersistentDefaultsLintTests` rule was
  added.
  **That fifth rule was measured and rejected on its own terms, not by
  inheriting #1689's count.** Re-measured here: the app target holds **14**
  non-comment `UserDefaults.standard` receivers and `Irrlicht/Views/` holds
  **0**, so widening the fourth rule app-wide is still intractable. The narrower
  candidate — ban a default argument that resolves `.standard` — would today
  flag exactly **one** declaration, `SessionManager.init(defaults:)`, which is
  the seam all four existing rules' failure messages RECOMMEND; and removing
  that default would push a `.standard` into `Irrlicht/Views/` (the
  `SessionListView` preview), which rule 4 exists to keep out and which would
  slip past it only because the leading-dot spelling evades its receiver regex.
  A line scan also cannot tell an `init` default from a `func` default across a
  multi-line signature without a parser, and per this section's parse rule it
  would then have to flag both. One exemption or a parser: the count is recorded
  instead.
  **Under `swift test` the READ is the only observable, and that is not a
  weakness of the test — it is why nothing graded this call site for its whole
  life.** `canUseUserNotifications` is false outside an app bundle, so
  `sendNotification` returns before scheduling a `UNNotificationRequest` or
  reaching `SoundPlayer.speak` and the gate's DECISION has no downstream value
  to assert on. `InMemoryDefaults.readKeys` is what remains, and "which store
  was asked" IS the defect rather than a proxy for it. The discriminating arm
  asserts the read SET equals what `NotificationSettings.masterEnabled(defaults:)`
  performs over an identically arranged second store — obtained by calling the
  rule, never written down — because a decorative `_ = defaults.object(forKey:)`
  above a guard still resolving `.standard` satisfies "the store was asked" and
  is invisible to every other assertion in the suite (measured: exactly that one
  arm reddens).
  **And the mutation that reddens NOTHING is recorded rather than omitted**:
  re-adding the removed default alone, call site untouched, leaves all 414 tests
  green. Nothing locks the removal — the guarantee is the compiler's, and only
  for call sites that already exist.
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
  further wall-clock reads on the same chip were left unconverted
  (`quotaPacePercent`, `mergeIntoBuckets`' staleness test, `formatTimeUntil`),
  two of them pixel-visible, so the `rate_limit` fixture #1663 anticipates was
  still not safe to seed: that became #1675, the member below.
  **Those three are #1675, and they are where the family's assertion shape
  needed a twin.** Same axis as the fourth member rather than a fifth one — the
  clock again, three further reads of it. All three now take `now` as an input —
  `quotaPacePercent(_:now:)` and `snapshotIsStale(_:now:)` as required
  arguments, `QuotaResetFormat.timeUntil(_:now:)` likewise, and
  `mergeIntoBuckets` / `quotaChipData` are `static` and pure so the fold cannot
  read a clock at all. Six things are worth carrying.
  **An extraction was again unavoidable, and the second one is not a leaf
  view.** #1663's `QuotaResetLabel` works because the pixels and the decision
  are the same `Text`; the staleness flip decides an `.opacity` applied to the
  WHOLE chip while the decision was made in the data fold, two hundred lines
  away, and `SessionListView.quotaChipView` cannot be hosted without dragging in
  `SessionManager`. So the vehicle is a generic wrapper — `QuotaStaleDimmed<Content>`
  (`Irrlicht/Views/QuotaChipParts.swift`) — which moves the decision to where the
  pixels are and is hostable over any content. `QuotaWindowRow` beside it is the
  ordinary leaf case, for the pace marker.
  **The stored verdict was REMOVED rather than kept beside the derived one.**
  `QuotaWidgetData.isStale` and `snapshotIsStale(snapshot, now:)` would have been
  two spellings of one fact that could only drift, and the equivalence that makes
  the removal safe is locked
  (`QuotaChipClockTests.testTheMergeNeverKeepsASnapshotWhoseStalenessDisagreesWithIt`,
  over every branch of the fold) rather than argued in a comment. `ChipBucket`
  keeps its own copy because the merge genuinely branches on it. The residual
  cost is stated where it lives: under the running default clock the fold's read
  and the wrapper's read are microseconds apart, so at a `resetsAt` boundary a
  chip can dim one frame before its tooltip says "stale".
  **A both-sides pixel arm needs a must-not-differ twin.** "Two clocks, two
  rasterisations" is satisfied by a view that varies with the clock for ANY
  reason, including one unrelated to the property under test — so each arm is
  paired with a fixture whose verdict is identical under both clocks (a window
  the clock cannot pace; a snapshot stale under both). Both twins earned their
  place: one mutation reddens only the dimming twin and another only the row's,
  while the arms they guard stay green.
  **A mutation whose two pinned instants are congruent is INERT and reads as a
  passing test.** The row's twin was first mutated with a marker at
  `epoch % 100` — and `referenceNow` and `contrastingNow` are both ≡ 0 (mod
  100), being exactly 48h apart — so a genuinely clock-varying marker rendered
  identically and the arm came back green. That is #1390's "assert the mutation
  actually changed the thing" arriving in a Swift suite, and the check is one
  line of arithmetic before trusting a green mutation run.
  **This is the one member of the family CI grades.** `QuotaMenuBarRendererTests`
  takes no `as: .pinnedImage`, so `ImageSnapshotCIScopeTests` does not skip it —
  and every fixture in it was built from `Date()` while the renderer read
  `Date()` again a moment later, two reads of the machine that merely agreed. So
  nothing in it could tell a renderer honouring its `now:` from one discarding
  it, and its own comment recorded that it could not pin an exact ramp boundary
  for the same reason. Both are now closed, and `now` threads from one visible
  read in `MenuBarImageBuilder.combinedImage` — the icon is rasterised into an
  `NSStatusItem`, so no environment can reach it and #1659's required-argument
  form is the only option.
  **And the `rate_limit` fixture #1663 anticipated is seeded, but NOT as a
  committed PNG.** The clock obstacle is gone; what remains is unrelated to it
  and is why the PNG form is still the wrong one — a new committed-reference
  image suite is a coin flip on whether it is gradeable in CI at all (5 of the 7
  existing ones are not; see the reference-host-only decision record in the
  macOS bullet above), and one that lands on the wrong side buys a check that
  runs on one Mac. The two-render-in-memory form runs everywhere, so that is
  what the fixture is.
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
  quietened the incident.
  **The IN-PROCESS half of that guard made the opposite trade in #1714, and the
  two are not in conflict — the difference is what each can say when it fires.**
  `InMemoryDefaultsTests`' arm 2 ran the same wide net inside the suite and, on
  a runner, reported `com.apple.siri.ODDI.MetricsWorker.plist` as *"The defaults
  double reached disk. #1661 is back"*: a false ACCUSATION rather than a false
  alarm, which is a different cost from the shell witness's, whose wording
  already says "background daemons also write here … re-run to tell that apart"
  and which CAN be re-run. A single in-process window cannot re-run itself, so
  that assertion is now scoped to what the run OWNS — a UUID token woven through
  every domain name and key the double is handed, plus this process's own
  application domain, both derived at run time and neither a list of names the
  run did not choose. What it gives up is exactly one shape, and it is committed
  as a corpus row rather than described: a NEW file bearing a UUID this run did
  not mint is no longer reported there. That population is still watched by the
  two halves above, which is why the narrowing is affordable — the shell's
  directory half brackets the whole run with the unattributed net, and its domain
  half sees the write into `com.apple.dt.xctest.tool` that an added-entry witness
  never could. Measured reachability before the fix: **1 failure in the 27
  `macos-swift` runs on `main`** between the suite being gated there
  (`a86ae50a`, 2026-08-16) and 2026-08-19.
  **That witness has a SECOND half since #1688, and the split between the two is
  the point rather than the sum**: the directory half watches for new FILES,
  while `SWIFT_SUITE_WITNESSED_DOMAINS` compares
  `com.apple.dt.xctest.tool` and `io.irrlicht.app` key set AND values through
  `defaults export`, because #1672 was a write into an existing key of an
  existing domain and moved no directory entry at all. Neither half is a
  superset. It COMPARES — `defaults write`/`delete` on a real domain is as
  forbidden as `rm`, so nothing there has a write path and the test corpus
  answers through a committed stub reader
  (`tools/lib/testdata/swift-suite/defaults-stub.sh`) rather than writing a
  domain to make itself deterministic. Three things are load-bearing. The
  vacuity guard is a **control probe** (`NSGlobalDomain`, read and never
  compared) rather than the watched domains' contents, because "both domains
  hold no keys" is a legitimate fresh-machine state where "no watched directory
  present" is not — a contents-based guard would refuse on a new machine
  forever. An EMPTY answer is `UNREADABLE`, not a clean empty domain, and the
  one row where that distinction is a silent pass rather than a mislabelled
  failure is a silent reader over an already-empty domain. And the limit is
  printed on every clean run: an idempotent write is invisible to any
  before/after comparison, so a saturated domain and a clean one would otherwise
  read identically. `Tests/RealHomePathLintTests.swift` is the structural
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
