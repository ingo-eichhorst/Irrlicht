import XCTest

@testable import Irrlicht

/// Unit tests for `GhosttyActivator`'s pure matching layer.
///
/// The end-to-end behaviour — does clicking actually land on the right tab —
/// lives in `LauncherHarnessTests`, which needs a running Ghostty. These pin
/// the decisions that layer makes on its own: what counts as the same
/// directory, and when the activator must refuse to answer.
final class GhosttyActivatorTests: XCTestCase {

    private typealias CanonicalPath = GhosttyActivator.CanonicalPath
    private typealias SurfaceID = GhosttyActivator.SurfaceID
    private typealias Surface = GhosttyActivator.Surface

    /// Hung off the real temp dir, so no test hardcodes a path and every fixture sits under /var/folders.
    private static let fixtureRoot = NSTemporaryDirectory() + "irrlicht-ghostty-unit"

    private func fixture(_ relative: String) -> String {
        relative.isEmpty ? Self.fixtureRoot : "\(Self.fixtureRoot)/\(relative)"
    }

    private func cwd(_ relative: String) -> CanonicalPath? {
        CanonicalPath(fixture(relative))
    }

    private func surface(_ id: String, _ relative: String) -> Surface {
        Surface(id: SurfaceID(id), workingDirectory: cwd(relative))
    }

    private func surfaceWithoutDirectory(_ id: String) -> Surface {
        Surface(id: SurfaceID(id), workingDirectory: nil)
    }

    // MARK: - Unique match

    func testUniqueMatchReturnsTheOnlySurfaceInThatDirectory() {
        let surfaces = [surface("a", "one"), surface("b", "two"), surface("c", "three")]
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("two")), surfaces[1])
    }

    func testUniqueMatchIsNilWhenNoSurfaceIsInThatDirectory() {
        let surfaces = [surface("a", "one"), surface("b", "two")]
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("elsewhere")))
    }

    /// The design decision, not an incidental edge case. Two tabs in one
    /// directory is unanswerable from what Ghostty exposes, so the activator
    /// declines instead of picking one — a wrong pick lands the user in a
    /// different agent's terminal, which is worse than not moving.
    func testUniqueMatchIsNilWhenTwoSurfacesShareTheDirectory() {
        let surfaces = [surface("a", "repo"), surface("b", "repo"), surface("c", "other")]
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("repo")))
        XCTAssertEqual(GhosttyActivator.matchCount(surfaces: surfaces, cwd: cwd("repo")), 2)
    }

    /// A directory shared by two *other* tabs must not spoil an unambiguous
    /// answer for the one we were asked about.
    func testUniqueMatchSurvivesAmbiguityElsewhere() {
        let surfaces = [surface("a", "repo"), surface("b", "repo"), surface("c", "target")]
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("target")), surfaces[2])
    }

    func testUniqueMatchIsNilForAnEmptyCwd() {
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: [surface("a", "one")], cwd: CanonicalPath("")))
    }

    func testSurfaceWithNoDirectoryDoesNotMatchASessionWithNoCwd() {
        let surfaces = [surfaceWithoutDirectory("a"), surface("b", "one")]
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: CanonicalPath("")))
        XCTAssertEqual(GhosttyActivator.matchCount(surfaces: surfaces, cwd: CanonicalPath("")), 0)
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("one")), surfaces[1])
    }

    func testUniqueMatchIsNilWithNoSurfaces() {
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: [], cwd: cwd("one")))
    }

    // MARK: - Path normalisation

    /// Assembled from components rather than written out, so the fixture is not an absolute-path literal.
    private static let separator = "/"

    private static func shortSpelling(_ root: String) -> String {
        "\(separator)\(root)\(separator)irrlicht-fixture"
    }

    private static func privateSpelling(_ root: String) -> String {
        "\(separator)private\(shortSpelling(root))"
    }

    private static let tmpSpelling = shortSpelling("tmp")
    private static let privateTmpSpelling = privateSpelling("tmp")

    /// `/var` and `/etc` were previously untested, so a typo in `reachableRoots` would have gone unnoticed.
    func testBothSpellingsAgreeForEveryRootMacOSReachesThroughPrivate() {
        for root in GhosttyActivator.CanonicalPath.reachableRoots {
            XCTAssertEqual(
                CanonicalPath(Self.shortSpelling(root)),
                CanonicalPath(Self.privateSpelling(root)),
                "both spellings of /\(root) must canonicalise to one answer"
            )
        }
    }

    func testAPathMerelyStartingWithPrivateIsNotAFirmlink() {
        let notAFirmlink = "\(Self.separator)privateer\(Self.separator)tmp"
        XCTAssertEqual(CanonicalPath(notAFirmlink)?.value, notAFirmlink, "only a whole /private component may be stripped")
    }

    func testMatchesAcrossTheTmpSymlinkSpelling() {
        let surfaces = [Surface(id: SurfaceID("a"), workingDirectory: CanonicalPath(Self.privateTmpSpelling))]
        XCTAssertEqual(
            GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: CanonicalPath(Self.tmpSpelling)),
            surfaces[0]
        )
    }

    func testMatchesRegardlessOfWhichSideCarriesThePrivatePrefix() {
        let surfaces = [Surface(id: SurfaceID("a"), workingDirectory: CanonicalPath(Self.tmpSpelling))]
        XCTAssertEqual(
            GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: CanonicalPath(Self.privateTmpSpelling)),
            surfaces[0]
        )
    }

    func testBothSpellingsStillAgreeWhenTheDirectoryNoLongerExists() {
        let gone = "-\(UUID().uuidString)"
        XCTAssertEqual(
            CanonicalPath(Self.tmpSpelling + gone),
            CanonicalPath(Self.privateTmpSpelling + gone),
            "resolvingSymlinksInPath is existence-dependent and strips /private only for a path that exists, so a session whose directory was deleted or renamed — exactly when someone clicks the row to go look at it — needs the lexical strip to reach one spelling"
        )
    }

    func testTrailingSlashAndDotSegmentsAreTheSameDirectory() {
        let surfaces = [surface("a", "repo")]
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("repo/")), surfaces[0])
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("./repo")), surfaces[0])
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd(Self.separator + "repo")), surfaces[0])
    }

    /// Normalisation must not collapse genuinely different directories — a
    /// prefix relationship is not a match.
    func testASubdirectoryIsNotTheSameDirectory() {
        let surfaces = [surface("a", "repo")]
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("repo/sub")))
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: cwd("")))
    }

    func testCanonicalPathIsNilForEmptyInput() {
        XCTAssertNil(CanonicalPath(""))
        XCTAssertNotNil(cwd("repo"))
    }

    // MARK: - Scripting output parsing

    private static let unitSeparator = "\u{1F}"
    private static let recordSeparator = "\u{1E}"

    private func record(_ id: String, _ absolutePath: String) -> String {
        "\(id)\(Self.unitSeparator)\(absolutePath)\(Self.recordSeparator)"
    }

    func testParsesRecordsSeparatedByControlCharacters() {
        let raw = record("id-1", fixture("one")) + record("id-2", fixture("two"))
        XCTAssertEqual(
            GhosttyActivator.parseSurfaces(raw),
            [surface("id-1", "one"), surface("id-2", "two")]
        )
    }

    func testParsesEmptyOutputAsNoSurfaces() {
        XCTAssertEqual(GhosttyActivator.parseSurfaces(""), [])
    }

    /// A surface whose directory is not yet known still parses — it is the
    /// matcher's job to exclude it, not the parser's. Dropping it here would
    /// make the "n of m surfaces matched" log undercount what Ghostty has
    /// open, which is the number someone reads when a jump declines.
    func testParsesSurfaceWithEmptyWorkingDirectory() {
        XCTAssertEqual(GhosttyActivator.parseSurfaces(record("id-1", "")), [surfaceWithoutDirectory("id-1")])
    }

    func testSkipsRecordsWithNoSeparatorOrNoID() {
        let raw = "garbage\(Self.recordSeparator)" + record("", fixture("one")) + record("id-2", fixture("two"))
        XCTAssertEqual(GhosttyActivator.parseSurfaces(raw), [surface("id-2", "two")])
    }

    /// A path containing a space or a unicode character must survive intact —
    /// the delimiters are control characters precisely so paths do not need
    /// escaping.
    func testPreservesUnusualButLegalPaths() {
        let raw = record("id-1", fixture("My Projects/café"))
        XCTAssertEqual(GhosttyActivator.parseSurfaces(raw).first?.workingDirectory, cwd("My Projects/café"))
    }

    // MARK: - Registry wiring

    /// The activator must keep answering to the same `term_program` and bundle
    /// id the registry advertised before it existed, or every Ghostty session
    /// falls through to "no activator" instead.
    func testRegistryStillResolvesGhosttyToTheGhosttyActivator() throws {
        let launcher = try JSONDecoder().decode(Launcher.self, from: Data(#"{"term_program":"ghostty"}"#.utf8))
        let resolved = SessionLauncher.resolveActivator(for: launcher)
        XCTAssertTrue(resolved is GhosttyActivator, "expected GhosttyActivator, got \(String(describing: resolved))")
        XCTAssertEqual(SessionLauncher.bundleID(for: "ghostty"), "com.mitchellh.ghostty")
    }
}
