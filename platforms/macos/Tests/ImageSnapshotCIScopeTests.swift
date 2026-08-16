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
/// The remaining difference is rasterisation, between a runner on Xcode 26.6
/// and a developer Mac on 26.3, and it is #1615. It is deliberately NOT
/// papered over here:
/// re-recording a snapshot to make it pass is the exact move #1034 and #1044
/// both made and both got wrong, and #1509 measured a perceptual tolerance
/// wide enough to absorb this drift as also wide enough to pass a missing
/// architecture segment.
///
/// So CI runs everything else — 270 of 318 tests, against 0 before — and these
/// suites stay gated on a developer Mac by `tools/preflight.sh --only swift`,
/// where they demonstrably pass.
///
/// ## Why this test exists
///
/// A skip list in a YAML file is invisible to everything. A new image-snapshot
/// suite would either be silently ungated in CI (and go red for a reason
/// unrelated to the PR that added it) or be added to the list by someone
/// remembering. This makes it assertable: the set of suites that render image
/// snapshots is DERIVED from the test sources, and every one of them has to be
/// classified.
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
            var currentClass: String?
            for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
                if let declared = Self.className(in: String(line)) { currentClass = declared }
                if line.contains("as: .pinnedImage"), let c = currentClass { found.insert(c) }
            }
        }
        XCTAssertEqual(filesRead, names.count, "some test sources could not be read")
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

    /// …and the YAML actually skips the ones marked skipped, no more and no
    /// fewer. Two lists that can disagree are worth exactly one of them.
    func testWorkflowSkipListMatchesTheClassification() throws {
        let workflow = repoRoot.appendingPathComponent(".github/workflows/macos-swift.yml")
        let text = try String(contentsOf: workflow, encoding: .utf8)

        var skipped: Set<String> = []
        var sawSwiftTest = false
        for line in text.split(separator: "\n") {
            // COMMENTS ARE NOT THE INVOCATION. The block above this workflow's
            // test step discusses `--skip` at length and names suites in prose;
            // reading those would make the check agree with an explanation
            // rather than with what CI runs, and a step whose real arguments
            // were deleted would still pass.
            let code = line.drop { $0 == " " }
            guard !code.hasPrefix("#") else { continue }
            guard code.contains("swift test") || code.contains("--skip") else { continue }
            if code.contains("swift test") { sawSwiftTest = true }
            var rest = code
            while let r = rest.range(of: "--skip ") {
                rest = rest[r.upperBound...]
                let name = rest.prefix { $0.isLetter || $0.isNumber || $0 == "_" }
                if !name.isEmpty { skipped.insert(String(name)) }
            }
        }
        // Fail loudly rather than comparing two empty sets: a workflow this
        // cannot parse is the case where it knows least about what CI runs.
        XCTAssertTrue(sawSwiftTest, "found no `swift test` invocation in \(workflow.path)")
        XCTAssertFalse(skipped.isEmpty, "parsed no --skip arguments — the check is vacuous")

        let expected = Set(Self.imageSnapshotSuites.filter { $0.value }.map { $0.key })
            .union(Self.harnessSkips)
        XCTAssertEqual(skipped.sorted(), expected.sorted(),
                       "macos-swift.yml's --skip list and this file's classification disagree")
    }
}
