import ObjectiveC
import XCTest

/// Keeps `macos-swift.yml`'s image-snapshot skip list honest (#1530), and
/// measures what that list costs (#1615).
///
/// ## The policy this file enforces is written down once, in AGENTS.md
///
/// Image snapshots are graded on the reference host only, permanently and by
/// choice. The decision, the measured evidence behind it, why re-recording and
/// `perceptualPrecision` are not the fix, and what is still unknown are all in
/// `AGENTS.md`'s "macOS app" bullet under Testing (#1615, step 3). Nothing here
/// restates any of it, because a fact written down in N places drifts in N-1 of
/// them — which is not a worry but this file's own history: the paragraph
/// removed from here said CI gates "272 of 320 tests", a figure measured once
/// on one run and 115 tests stale when #1615 was closed. It lives in
/// `testTheUngatedPopulationIsExactlyTheSkippedSuites` below now, where it is
/// re-derived on every run.
///
/// ## Why this test exists
///
/// A skip list in a YAML file is invisible to everything. A new image-snapshot
/// suite would either be silently ungated in CI (and go red for a reason
/// unrelated to the PR that added it) or be added to the list by someone
/// remembering. This makes it assertable: the set of suites that render image
/// snapshots is DERIVED from the test sources, and every one of them has to be
/// classified.
///
/// Since #1615 the same classification also drives the `swift-snapshot-evidence`
/// job, which runs precisely the suites the gate skips so their pixels can be
/// looked at. That makes the workflow carry TWO `swift test` commands with
/// deliberately opposite argument lists, so the parse below is per-invocation:
/// a union over the file is satisfied by moving a suite from one command to the
/// other, which is exactly the drift worth catching.
///
/// That job now runs only on `workflow_dispatch` (the policy it was collecting
/// evidence for is decided), and the parse deliberately does not care: it reads
/// the two commands' ARGUMENTS, not the triggers, so both invocations stay
/// cross-checked whether or not one of them fires on a PR. A job whose skip
/// list has silently drifted apart from this classification produces a
/// misleading artifact on the day someone finally dispatches it, which is worse
/// than an ordinary red — nothing else would ever look.
final class ImageSnapshotCIScopeTests: XCTestCase {

    /// Every suite that takes an image snapshot, and whether `macos-swift.yml`
    /// skips it. `false` is a claim about the runner, made from a measured run
    /// (PR #1614's `swift-test` job) — not a guess.
    ///
    /// Suite-granular rather than test-granular on purpose. `HistoryViewSnapshotTests`
    /// pays the most for that — 12 of its 14 tests pass on a runner and are
    /// skipped anyway — but a 36-name list of individual tests would have to be
    /// re-derived by hand every time one is added, and a stale entry there
    /// looks exactly like coverage.
    private static let imageSnapshotSuites: [String: Bool] = [
        "BackchannelRulesViewSnapshotTests": true,      // 2/2 fail on a runner
        "GroupViewSnapshotTests": true,                 // 6/6
        "HistoryViewSnapshotTests": true,               // 2/14
        "SessionListUnappliedGrantsWiringTests": true,  // 2/2
        "SessionRowSnapshotTests": true,                // 24/24
        "PermissionWizardEffectErrorRenderTests": false, // 3/3 pass on a runner
        "UnappliedGrantsBannerRenderTests": false,       // 2/2 pass on a runner
    ]

    /// The two names that are not about snapshots at all: the harness target
    /// and its class, skipped everywhere because it drives real applications
    /// through `NSRunningApplication`.
    private static let harnessSkips: Set<String> = ["LauncherTestHarness", "LauncherHarnessTests"]

    /// Suites the evidence job runs that are not image-snapshot suites.
    ///
    /// `RasterPrimitiveEvidenceTests` renders four rasters and publishes their
    /// bytes; it never calls `assertSnapshot`, has no committed reference and
    /// therefore cannot fail for a rasterisation difference on any host —
    /// which is precisely what lets it run to completion and report on the
    /// host where the answer is "different" (#1615). So it is invisible to the
    /// `as: .pinnedImage` derivation below, and `testTheTwoClassificationsAreDisjoint`
    /// asserts that stays true rather than leaving it as a claim.
    private static let evidenceOnlySuites: Set<String> = ["RasterPrimitiveEvidenceTests"]

    private var repoRoot: URL {
        URL(fileURLWithPath: #filePath)                 // …/platforms/macos/Tests/<this file>
            .deletingLastPathComponent()                // …/platforms/macos/Tests
            .deletingLastPathComponent()                // …/platforms/macos
            .deletingLastPathComponent()                // …/platforms
            .deletingLastPathComponent()                // repo root
    }

    /// Scans the test sources for `as: .pinnedImage` and attributes each use to
    /// the class it is lexically inside.
    ///
    /// Text-based rather than a real parse, which is fine for what it has to
    /// decide — but it must not silently find nothing, so the caller checks
    /// that it read files at all.
    private func suitesTakingImageSnapshots() throws -> Set<String> {
        let testsDir = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        // This file is excluded because it carries the search string itself, in
        // the line below that does the searching — caught on the first run,
        // where the scan reported this class as an image-snapshot suite. The
        // exclusion is by path rather than by name so a rename cannot silently
        // reintroduce it, and the vacuity guards below still hold: the scan has
        // to read every other source and find real uses.
        let selfName = URL(fileURLWithPath: #filePath).lastPathComponent
        let names = try FileManager.default.contentsOfDirectory(atPath: testsDir.path)
            .filter { $0.hasSuffix(".swift") && $0 != selfName }
        XCTAssertGreaterThan(names.count, 10, "read \(names.count) test sources — the scan cannot have worked")

        var found: Set<String> = []
        var filesRead = 0
        for name in names.sorted() {
            guard let text = try? String(contentsOf: testsDir.appendingPathComponent(name), encoding: .utf8) else { continue }
            filesRead += 1
            found.formUnion(Self.snapshottingClasses(in: text))
        }
        XCTAssertEqual(filesRead, names.count, "some test sources could not be read")
        return found
    }

    /// Classes in one source that contain an `as: .pinnedImage` call, by
    /// tracking the most recent class declaration above each line.
    private static func snapshottingClasses(in source: String) -> Set<String> {
        var found: Set<String> = []
        var currentClass: String?
        for line in source.split(separator: "\n", omittingEmptySubsequences: false) {
            if let declared = className(in: String(line)) { currentClass = declared }
            guard line.contains("as: .pinnedImage"), let c = currentClass else { continue }
            found.insert(c)
        }
        return found
    }

    /// `final class Foo: XCTestCase` / `class Foo: XCTestCase` → "Foo".
    private static func className(in line: String) -> String? {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard trimmed.hasPrefix("class ") || trimmed.hasPrefix("final class ") else { return nil }
        guard let after = trimmed.range(of: "class ")?.upperBound else { return nil }
        let rest = trimmed[after...]
        let name = rest.prefix { $0.isLetter || $0.isNumber || $0 == "_" }
        return name.isEmpty ? nil : String(name)
    }

    /// A suite that renders image snapshots is either skipped in CI or claimed
    /// to pass there. Adding one and classifying it neither way fails here
    /// rather than reddening an unrelated PR's `swift-test` check.
    func testEveryImageSnapshotSuiteIsClassified() throws {
        let derived = try suitesTakingImageSnapshots()
        XCTAssertFalse(derived.isEmpty, "found no `as: .pinnedImage` uses at all — the scan is vacuous")
        XCTAssertEqual(
            derived.sorted(),
            Self.imageSnapshotSuites.keys.sorted(),
            "a suite renders image snapshots without being classified for CI (or is classified but no longer renders any)")
    }

    /// One `swift test …` command from the workflow, with the arguments it
    /// carries across shell line continuations.
    private struct Invocation {
        var skips: Set<String> = []
        var filters: Set<String> = []
    }

    /// Every identifier introduced by `flag` on one line, e.g. every
    /// `--skip <Name>`.
    private static func names(after flag: String, in line: Substring) -> Set<String> {
        var found: Set<String> = []
        var rest = line
        while let r = rest.range(of: flag) {
            rest = rest[r.upperBound...]
            let name = rest.prefix { $0.isLetter || $0.isNumber || $0 == "_" }
            if !name.isEmpty { found.insert(String(name)) }
        }
        return found
    }

    /// Every `swift test` command the workflow runs.
    ///
    /// Per-invocation rather than one union over the file, and that is the
    /// whole difference the evidence job made. A union is satisfied by ANY
    /// arrangement of the same names: moving `--skip SessionRowSnapshotTests`
    /// out of the gating command and into the evidence one changes what CI
    /// gates and leaves a union check green. Two commands that can disagree
    /// are worth exactly one of them, which is the same argument this file
    /// already made about two lists.
    private func swiftTestInvocations(in text: String) -> [Invocation] {
        var out: [Invocation] = []
        var current: Invocation?

        for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
            // COMMENTS ARE NOT THE INVOCATION. The blocks above this
            // workflow's steps discuss `--skip` at length and name suites in
            // prose; reading those would make the check agree with an
            // explanation rather than with what CI runs, and a step whose real
            // arguments were deleted would still pass.
            let code = Substring(line.trimmingCharacters(in: .whitespaces))
            guard !code.hasPrefix("#") else { continue }

            if code.contains("swift test") {
                if let open = current { out.append(open) }
                current = Invocation()
            }
            guard current != nil else { continue }

            current?.skips.formUnion(Self.names(after: "--skip ", in: code))
            current?.filters.formUnion(Self.names(after: "--filter ", in: code))

            // A shell continuation is what holds one command's arguments
            // together across lines; the first line that does not end in `\`
            // ends the command.
            if !code.hasSuffix("\\") {
                out.append(current!)
                current = nil
            }
        }
        if let open = current { out.append(open) }
        return out
    }

    /// …and the YAML actually skips the ones marked skipped, no more and no
    /// fewer — in the invocation that GATES.
    func testWorkflowSkipListMatchesTheClassification() throws {
        let workflow = repoRoot.appendingPathComponent(".github/workflows/macos-swift.yml")
        let invocations = swiftTestInvocations(in: try String(contentsOf: workflow, encoding: .utf8))

        // Fail loudly rather than comparing two empty sets: a workflow this
        // cannot parse is the case where it knows least about what CI runs.
        let gating = invocations.filter { $0.filters.isEmpty }
        XCTAssertEqual(gating.count, 1,
                       "expected exactly one unfiltered `swift test` (the gate) in \(workflow.path), parsed \(invocations.count) invocation(s)")
        guard let gate = gating.first else { return }
        XCTAssertFalse(gate.skips.isEmpty, "parsed no --skip arguments — the check is vacuous")

        let expected = Set(Self.imageSnapshotSuites.filter { $0.value }.map { $0.key })
            .union(Self.harnessSkips)
        XCTAssertEqual(gate.skips.sorted(), expected.sorted(),
                       "macos-swift.yml's --skip list and this file's classification disagree")
    }

    /// The evidence job runs exactly the suites the gate skips (#1615).
    ///
    /// Without this, "skipped in CI" and "collected as evidence" are two hand-
    /// maintained lists again, and the failure it admits is the quiet one: a
    /// suite dropped from the gate's `--skip` list but left in the evidence
    /// filter, or added to the skip list and never collected, publishes an
    /// artifact that reads as complete while missing exactly the suite someone
    /// went looking for.
    ///
    /// The harness names are asserted here too. `--filter` already makes them
    /// unreachable, so passing them is belt-and-braces — which is the reason to
    /// pin it: a future filter widened past those six names would otherwise
    /// start driving real applications through `NSRunningApplication` on a
    /// machine, and the whole of `AGENTS.md`'s prohibition rests on both names
    /// being present at every invocation.
    func testEvidenceJobRunsExactlyTheSuitesTheGateSkips() throws {
        let workflow = repoRoot.appendingPathComponent(".github/workflows/macos-swift.yml")
        let invocations = swiftTestInvocations(in: try String(contentsOf: workflow, encoding: .utf8))

        let filtered = invocations.filter { !$0.filters.isEmpty }
        XCTAssertEqual(filtered.count, 1,
                       "expected exactly one `--filter`ed `swift test` (the evidence job) in \(workflow.path)")
        guard let evidence = filtered.first else { return }

        let expected = Set(Self.imageSnapshotSuites.filter { $0.value }.map { $0.key })
            .union(Self.evidenceOnlySuites)
        XCTAssertEqual(evidence.filters.sorted(), expected.sorted(),
                       "the evidence job collects a different set of suites than swift-test skips")
        XCTAssertEqual(evidence.skips.sorted(), Self.harnessSkips.sorted(),
                       "the evidence job must skip the harness target and its class, and nothing else")
    }

    /// The two classifications name disjoint suites.
    ///
    /// `evidenceOnlySuites` claims its members take no image snapshot, and
    /// nothing else checks that claim — the derivation above simply would not
    /// see them. If one grows an `as: .pinnedImage` call it becomes a suite
    /// with a committed reference that the evidence job runs expecting a
    /// failure, which is two different contracts at once; this is where that
    /// is noticed.
    func testTheTwoClassificationsAreDisjoint() throws {
        let derived = try suitesTakingImageSnapshots()
        XCTAssertTrue(derived.isDisjoint(with: Self.evidenceOnlySuites),
                      "an evidence-only suite now takes image snapshots: \(derived.intersection(Self.evidenceOnlySuites).sorted())")
        XCTAssertTrue(Set(Self.imageSnapshotSuites.keys).isDisjoint(with: Self.evidenceOnlySuites),
                      "a suite is classified both as an image-snapshot suite and as evidence-only")
    }

    // MARK: - What the decision costs, counted rather than typed (#1615)

    /// How many tests the reference-host-only decision takes out of CI.
    ///
    /// Pinned because it is the one figure the decision record in `AGENTS.md`
    /// states, and a number that documents behaviour without being produced by
    /// it drifts silently and is then quoted with full confidence — this
    /// repo's rule for the replay census, applied to the same failure one
    /// platform over. It moves only when someone adds or removes a test in a
    /// suite CI does not run, which is precisely the event the record needs
    /// surfaced: a test written into an ungated suite is graded on one machine
    /// in the world, forever, and that should cost a deliberate line in a diff.
    ///
    /// The TOTAL is deliberately not pinned. It moves with every test added
    /// anywhere in the target — 320 when the workflow header typed it, 435 at
    /// #1615 — so pinning it would make an unrelated PR edit a literal about
    /// image snapshots, and the ratchet would be reset rather than read. It is
    /// printed instead, on every run of this test and by every run of the
    /// suite itself (`Executed N tests`).
    private static let committedUngatedTestCount = 48

    /// The module the gated test target's classes live in, read off this class
    /// rather than typed: a literal here that stopped naming the real target
    /// would select no classes at all, and "found nothing" is the failure this
    /// whole file exists to make impossible.
    private static var gatedTargetModule: String {
        String(NSStringFromClass(ImageSnapshotCIScopeTests.self).split(separator: ".").first ?? "")
    }

    /// Every `XCTestCase` subclass in this target, by simple name.
    ///
    /// Deliberately scoped to one module, which is what keeps
    /// `LauncherTestHarness` out — its classes drive real applications through
    /// `NSRunningApplication`, and while merely counting a class's test methods
    /// runs none of them, the safest way not to run that target is not to touch
    /// it. That also makes the total the RIGHT one: `swift test` skips the
    /// harness at every invocation this repo documents, so the number below is
    /// the number a run reports.
    private static func testCaseClasses() -> [String: AnyClass] {
        let module = gatedTargetModule
        var found: [String: AnyClass] = [:]
        let count = objc_getClassList(nil, 0)
        guard count > 0 else { return found }
        let buffer = UnsafeMutablePointer<AnyClass>.allocate(capacity: Int(count))
        defer { buffer.deallocate() }
        objc_getClassList(AutoreleasingUnsafeMutablePointer<AnyClass>(buffer), count)

        for index in 0..<Int(count) {
            let candidate: AnyClass = buffer[index]
            let mangled = NSStringFromClass(candidate).split(separator: ".", maxSplits: 1)
            guard mangled.count == 2, mangled[0] == module, descendsFromXCTestCase(candidate) else { continue }
            found[String(mangled[1])] = candidate
        }
        return found
    }

    /// Walks the superclass chain rather than asking `is XCTestCase.Type`,
    /// because a subclass of a subclass is still a suite XCTest runs and the
    /// count has to match what a run reports.
    private static func descendsFromXCTestCase(_ candidate: AnyClass) -> Bool {
        var parent: AnyClass? = class_getSuperclass(candidate)
        while let step = parent {
            if step == XCTestCase.self { return true }
            parent = class_getSuperclass(step)
        }
        return false
    }

    /// How many tests XCTest would run for one class — asked of XCTest itself,
    /// so it agrees with the run's own `Executed N tests` by construction
    /// rather than by a source scan agreeing with a test runner by luck.
    private static func testCount(of cls: AnyClass) -> Int {
        XCTestSuite(forTestCaseClass: cls).tests.count
    }

    /// The census: how much of the suite CI grades, how much it does not, and
    /// that the ungated part still exists.
    ///
    /// Two failures it is built to report and one it is not. It reports the
    /// ungated population CHANGING, which is the figure `AGENTS.md` quotes.
    /// And it reports a skipped suite going EMPTY — a suite that runs on one
    /// machine and contains nothing runs nowhere, and reads exactly like a
    /// suite that passes everywhere, which is the shape this repo keeps
    /// removing. It does NOT report those tests failing on the reference host;
    /// only running them does that, which is why the decision record names
    /// `tools/preflight.sh --only swift` and the pre-push hook as the whole of
    /// the gate.
    func testTheUngatedPopulationIsExactlyTheSkippedSuites() throws {
        let classes = Self.testCaseClasses()
        XCTAssertGreaterThan(classes.count, 20,
                             "found \(classes.count) test classes in module \(Self.gatedTargetModule) — the runtime scan cannot have worked, and an empty scan would report a comfortable 0 ungated tests")

        var counts: [String: Int] = [:]
        for name in Self.imageSnapshotSuites.keys.sorted() {
            guard let cls = classes[name] else {
                XCTFail("\(name) is classified for CI but is not a test class in this bundle — the classification names a suite that no longer exists")
                continue
            }
            counts[name] = Self.testCount(of: cls)
        }

        let skipped = Self.imageSnapshotSuites.filter { $0.value }.keys.sorted()
        for name in skipped {
            XCTAssertGreaterThan(counts[name] ?? 0, 0,
                                 "\(name) is skipped in CI and now holds no tests at all — it runs on no machine anywhere, which is indistinguishable from passing on all of them")
        }

        let ungated = skipped.reduce(0) { $0 + (counts[$1] ?? 0) }
        let total = classes.values.reduce(0) { $0 + Self.testCount(of: $1) }
        XCTAssertGreaterThan(total, ungated,
                             "counted \(total) tests in total and \(ungated) ungated — the total cannot be the smaller number")

        // Printed on every run, because the figures a reader wants are the two
        // this test refuses to pin, and a passing test that prints nothing is
        // an unread measurement.
        print("ci-scope census: module=\(Self.gatedTargetModule) total=\(total) "
              + "gated=\(total - ungated) ungated=\(ungated) skippedSuites=\(skipped.count)")
        for name in Self.imageSnapshotSuites.keys.sorted() {
            print("ci-scope suite \(name) tests=\(counts[name] ?? -1) "
                  + "skippedInCI=\(Self.imageSnapshotSuites[name] == true)")
        }

        XCTAssertEqual(ungated, Self.committedUngatedTestCount, """
            the number of tests CI never grades changed: \(ungated), committed \(Self.committedUngatedTestCount).
            Those tests run on the reference Mac and nowhere else (AGENTS.md, "macOS app"), so this is a
            deliberate line in a diff rather than a stale figure. If the change is intended, set
            `committedUngatedTestCount` to \(ungated) and say in the PR which suite moved.
            """)
    }
}
