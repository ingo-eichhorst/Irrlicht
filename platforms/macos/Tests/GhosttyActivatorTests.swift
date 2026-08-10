import XCTest

@testable import Irrlicht

/// Unit tests for `GhosttyActivator`'s pure matching layer.
///
/// The end-to-end behaviour — does clicking actually land on the right tab —
/// lives in `LauncherHarnessTests`, which needs a running Ghostty. These pin
/// the decisions that layer makes on its own: what counts as the same
/// directory, and when the activator must refuse to answer.
final class GhosttyActivatorTests: XCTestCase {

    private func surface(_ id: String, _ cwd: String) -> GhosttyActivator.Surface {
        GhosttyActivator.Surface(id: id, workingDirectory: cwd)
    }

    // MARK: - Unique match

    func testUniqueMatchReturnsTheOnlySurfaceInThatDirectory() {
        let surfaces = [
            surface("a", "/Users/dev/one"),
            surface("b", "/Users/dev/two"),
            surface("c", "/Users/dev/three"),
        ]
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev/two"), surfaces[1])
    }

    func testUniqueMatchIsNilWhenNoSurfaceIsInThatDirectory() {
        let surfaces = [surface("a", "/Users/dev/one"), surface("b", "/Users/dev/two")]
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev/elsewhere"))
    }

    /// The design decision, not an incidental edge case. Two tabs in one
    /// directory is unanswerable from what Ghostty exposes, so the activator
    /// declines instead of picking one — a wrong pick lands the user in a
    /// different agent's terminal, which is worse than not moving.
    func testUniqueMatchIsNilWhenTwoSurfacesShareTheDirectory() {
        let surfaces = [
            surface("a", "/Users/dev/repo"),
            surface("b", "/Users/dev/repo"),
            surface("c", "/Users/dev/other"),
        ]
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev/repo"))
        XCTAssertEqual(GhosttyActivator.matchCount(surfaces: surfaces, cwd: "/Users/dev/repo"), 2)
    }

    /// A directory shared by two *other* tabs must not spoil an unambiguous
    /// answer for the one we were asked about.
    func testUniqueMatchSurvivesAmbiguityElsewhere() {
        let surfaces = [
            surface("a", "/Users/dev/repo"),
            surface("b", "/Users/dev/repo"),
            surface("c", "/Users/dev/target"),
        ]
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev/target"), surfaces[2])
    }

    func testUniqueMatchIsNilForAnEmptyCwd() {
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: [surface("a", "/Users/dev/one")], cwd: ""))
    }

    /// A surface with no reported directory (shell integration off, or the
    /// shell has not reported yet) must never match, and in particular must
    /// not match an empty session cwd by both sides being empty.
    func testSurfaceWithNoWorkingDirectoryNeverMatches() {
        let surfaces = [surface("a", ""), surface("b", "/Users/dev/one")]
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: ""))
        XCTAssertEqual(GhosttyActivator.matchCount(surfaces: surfaces, cwd: ""), 0)
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev/one"), surfaces[1])
    }

    func testUniqueMatchIsNilWithNoSurfaces() {
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: [], cwd: "/Users/dev/one"))
    }

    // MARK: - Path normalisation

    /// `/tmp` is a symlink to `/private/tmp` on macOS and the two sides of
    /// this comparison do not agree on which spelling to use. Comparing raw
    /// strings fails here, and fails *silently* — it is indistinguishable
    /// from "no tab is in that directory".
    func testMatchesAcrossTheTmpSymlinkSpelling() {
        let surfaces = [surface("a", "/private/tmp/irrlicht-fixture")]
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/tmp/irrlicht-fixture"), surfaces[0])
    }

    func testMatchesRegardlessOfWhichSideCarriesThePrivatePrefix() {
        let surfaces = [surface("a", "/tmp/irrlicht-fixture")]
        XCTAssertEqual(
            GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/private/tmp/irrlicht-fixture"),
            surfaces[0]
        )
    }

    func testTrailingSlashAndDotSegmentsAreTheSameDirectory() {
        let surfaces = [surface("a", "/Users/dev/repo")]
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev/repo/"), surfaces[0])
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev/./repo"), surfaces[0])
        XCTAssertEqual(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev//repo"), surfaces[0])
    }

    /// Normalisation must not collapse genuinely different directories — a
    /// prefix relationship is not a match.
    func testASubdirectoryIsNotTheSameDirectory() {
        let surfaces = [surface("a", "/Users/dev/repo")]
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev/repo/sub"))
        XCTAssertNil(GhosttyActivator.uniqueMatch(surfaces: surfaces, cwd: "/Users/dev"))
    }

    func testNormalizedPathIsNilForEmptyInput() {
        XCTAssertNil(GhosttyActivator.normalizedPath(""))
        XCTAssertNotNil(GhosttyActivator.normalizedPath("/Users/dev/repo"))
    }

    // MARK: - Scripting output parsing

    func testParsesRecordsSeparatedByControlCharacters() {
        let raw = "id-1\u{1F}/Users/dev/one\u{1E}id-2\u{1F}/Users/dev/two\u{1E}"
        XCTAssertEqual(
            GhosttyActivator.parseSurfaces(raw),
            [surface("id-1", "/Users/dev/one"), surface("id-2", "/Users/dev/two")]
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
        XCTAssertEqual(GhosttyActivator.parseSurfaces("id-1\u{1F}\u{1E}"), [surface("id-1", "")])
    }

    func testSkipsMalformedRecords() {
        // No unit separator at all, and an empty id — neither addresses a surface.
        let raw = "garbage\u{1E}\u{1F}/Users/dev/one\u{1E}id-2\u{1F}/Users/dev/two\u{1E}"
        XCTAssertEqual(GhosttyActivator.parseSurfaces(raw), [surface("id-2", "/Users/dev/two")])
    }

    /// A path containing a space or a unicode character must survive intact —
    /// the delimiters are control characters precisely so paths do not need
    /// escaping.
    func testPreservesUnusualButLegalPaths() {
        let raw = "id-1\u{1F}/Users/dev/My Projects/café\u{1E}"
        XCTAssertEqual(GhosttyActivator.parseSurfaces(raw), [surface("id-1", "/Users/dev/My Projects/café")])
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
