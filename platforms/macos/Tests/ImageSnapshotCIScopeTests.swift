import XCTest

/// Keeps `macos-swift.yml`'s image-snapshot skip list honest (#1530).
///
/// ## Why there is a skip list at all
///
/// #1530's blocker 1 was two problems stacked, and the first was masking the
/// second. The committed references are 2× and a runner's display is 1×, so
/// every comparison failed on PIXEL COUNT — SnapshotTesting returns from that
/// guard before comparing a byte. `PinnedScaleSnapshot` fixes that half, which
/// is what made the second half visible for the first time: on the runner the
/// failure message changed from
///
///     Newly-taken snapshot@(350.0, 48.0) does not match reference@(350.0, 48.0).   (35×, pixel counts)
///
/// to
///
///     Newly-taken snapshot does not match reference.                               (36×, bytes)
///
/// The remaining difference is rasterisation, and it is #1615. It is not the
/// Xcode gap this comment used to name: measured by that issue's evidence job,
/// all 15 Xcodes on `macos-latest` — including 26.3.0 build 17C529, the
/// reference Mac's own build — produce the same 36 failures, and the residual
/// is confined to the brand icons `SessionState.adapterIcon` rasterises from
/// inline SVG through `NSImage(data:)`, at 1-5/255 on 0.3-0.9% of a row's
/// pixels. It is deliberately NOT papered over here:
/// re-recording a snapshot to make it pass is the exact move #1034 and #1044
/// both made and both got wrong, and #1509 measured a perceptual tolerance
/// wide enough to absorb this drift as also wide enough to pass a missing
/// architecture segment.
///
/// So CI runs everything else — 272 of 320 tests, against 0 before — and these
/// suites stay gated on a developer Mac by `tools/preflight.sh --only swift`,
/// where they demonstrably pass. Both figures are read off runs rather than
/// added up from the table below; the first draft added them up and was two
/// low, having forgotten the tests in this very file.
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

            var rest = code
            while let r = rest.range(of: "--skip ") {
                rest = rest[r.upperBound...]
                let name = rest.prefix { $0.isLetter || $0.isNumber || $0 == "_" }
                if !name.isEmpty { current?.skips.insert(String(name)) }
            }
            rest = code
            while let r = rest.range(of: "--filter ") {
                rest = rest[r.upperBound...]
                let name = rest.prefix { $0.isLetter || $0.isNumber || $0 == "_" }
                if !name.isEmpty { current?.filters.insert(String(name)) }
            }

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
}
