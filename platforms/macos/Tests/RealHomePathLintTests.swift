import XCTest

/// The structural half of #1669 / #1670.
///
/// `AppHome` makes the safe resolution available and `RealHomeIsolationTests`
/// proves the paths land outside the developer's home — but neither stops the
/// next call site from asking Foundation for the real home directly. That is
/// exactly how both instances arose: five call sites, written at five different
/// times, each reasonably resolving `~` the obvious way, three of them reached
/// from the test suite.
///
/// So the rule is enforced the way this repo enforces the same shape three times
/// elsewhere — an agent-owned SQLite store is opened through `core/pkg/sqlitero`
/// and never `sql.Open`; an inbound hook body is read only by
/// `hookjson.DecodeConfined`; a test never constructs `UserDefaults(suiteName:)`
/// (#1661) — by failing the build on any other call site.
///
/// # Scope, and why it is wider than the two suites that were filed
///
/// Both the **app** target and the **test** targets are scanned. The app target
/// is where the resolution actually happens: every one of the four writes in
/// #1669/#1670 was performed by product code that a test merely called. Scanning
/// only `Tests/` would leave the class fully re-enterable from the side it
/// entered from. Measured before writing this: after the rewrite, the whole tree
/// contains exactly two occurrences outside the exemptions below, both of which
/// are the exemptions — so the noise floor is zero, not "low".
///
/// # Exemptions
///
/// Unlike `PersistentDefaultsLintTests`, this rule HAS an exemption list, for a
/// reason that does not apply there: the safe construct is itself built out of
/// the banned one, so there must be at least one file allowed to spell it. Each
/// entry carries a reason and is existence-checked, so an entry that stops
/// naming a real file is a failure rather than a silent widening.
final class RealHomePathLintTests: XCTestCase {

    // MARK: - The rule, as a pure function

    /// Assembled from pieces so this file's own source never contains a
    /// contiguous match — the corpus fixtures below are built the same way.
    /// Without that the scan would flag its own test data, and the fix for
    /// *that* is an exclusion, which is a hole in the rule rather than a
    /// property of it.
    private enum Needle {
        static let home = "homeDirectoryFor" + "CurrentUser"
        static let nsHome = "NSHome" + "Directory"
        static let userDomain = "userDomain" + "Mask"
        static let tilde = "expandingTilde" + "InPath"
    }

    /// Each banned construct with the pattern that finds it.
    ///
    /// `NSHomeDirectory` requires a following `(` and the others do not, and the
    /// asymmetry is deliberate: the first is a function whose NAME appears in
    /// prose all over this repo, while `userDomainMask` and
    /// `expandingTildeInPath` only ever appear as code. The corpus pins both
    /// behaviours.
    private static let constructs: [(name: String, pattern: String)] = [
        (Needle.home, Needle.home),
        (Needle.nsHome, Needle.nsHome + #"\s*\("#),
        (Needle.userDomain, Needle.userDomain),
        (Needle.tilde, Needle.tilde),
    ]

    private static let scannedDirectories = ["Irrlicht", "Tests", "TestsHarness"]

    /// Files allowed one occurrence of a banned construct, with the reason.
    ///
    /// Paths are relative to `platforms/macos/` and are existence-checked by
    /// `testEveryExemptionNamesAFileThatExists`.
    private static let exemptions: [String: String] = [
        "Irrlicht/Managers/AppHome.swift":
            "the one place the user's home is resolved; every other call site goes through it",
        "Tests/InMemoryDefaults.swift":
            "#1661's witness resolves the home from the password database and needs a last-resort "
            + "fallback when getpwuid answers nothing",
    ]

    /// `"<line>:<construct>"` for every offending occurrence in `source`,
    /// sorted so the verdict is stable.
    ///
    /// Line comments are stripped first, padded with spaces so reported line
    /// numbers survive the strip — several of the files scanned here discuss
    /// these constructs at length in prose, and a rule that could not tell
    /// documentation from a call would be unusable in this repo. Block comments
    /// and string literals are deliberately NOT stripped: a line-based scan
    /// cannot tell a literal from a call, and AGENTS.md's rule is that a
    /// validator which cannot parse its input checks MORE, never less. Both
    /// limits are pinned in the corpus.
    static func offendingOccurrences(in source: String) -> [String] {
        let code = commentsBlanked(source)
        let text = code as NSString
        var found: [String] = []
        for construct in constructs {
            guard let regex = try? NSRegularExpression(pattern: construct.pattern) else { continue }
            regex.enumerateMatches(in: code, range: NSRange(location: 0, length: text.length)) { match, _, _ in
                guard let match else { return }
                let line = text.substring(to: match.range.location).components(separatedBy: "\n").count
                found.append("\(line):\(construct.name)")
            }
        }
        return found.sorted()
    }

    /// Replaces each line comment with spaces of the same UTF-16 width, so the
    /// regex sees no comment and every match offset still maps to its line.
    private static func commentsBlanked(_ source: String) -> String {
        source.split(separator: "\n", omittingEmptySubsequences: false).map { line -> String in
            guard let comment = line.range(of: "//") else { return String(line) }
            let code = line[..<comment.lowerBound]
            return code + String(repeating: " ", count: line.utf16.count - code.utf16.count)
        }
        .joined(separator: "\n")
    }

    // MARK: - Committed mutation evidence for the rule

    /// One row per spelling, pinned to the verdict the scan must return.
    ///
    /// The `want: []` rows carry most of the value: a rule that flagged
    /// everything and a rule that flagged correctly are indistinguishable
    /// without them. `NSTemporaryDirectory()` and `AppHome.url` are the two the
    /// fix itself introduces, so a rule that flagged them would make its own
    /// remedy unusable.
    private static let corpus: [(name: String, source: String, want: [String])] = [
        (
            "the construct #1669 was written with",
            "let home = FileManager.default.\(Needle.home)",
            ["1:\(Needle.home)"]
        ),
        (
            "the C-style spelling of the same thing",
            "let home = URL(fileURLWithPath: \(Needle.nsHome)())",
            ["1:\(Needle.nsHome)"]
        ),
        (
            "the search-path spelling #1670 was written with",
            "FileManager.default.urls(for: .libraryDirectory, in: .\(Needle.userDomain)).first",
            ["1:\(Needle.userDomain)"]
        ),
        (
            "the string spelling",
            "let p = (\"~/Library/Sounds\" as NSString).\(Needle.tilde)",
            ["1:\(Needle.tilde)"]
        ),
        (
            "one line can carry two different constructs",
            "FileManager.default.url(for: .libraryDirectory, in: .\(Needle.userDomain), "
                + "appropriateFor: URL(fileURLWithPath: \(Needle.nsHome)()), create: false)",
            ["1:\(Needle.nsHome)", "1:\(Needle.userDomain)"]
        ),
        (
            "every occurrence is reported, not only the first",
            "let a = FileManager.default.\(Needle.home)\nlet unrelated = 1"
                + "\nlet b = FileManager.default.\(Needle.home)",
            ["1:\(Needle.home)", "3:\(Needle.home)"]
        ),
        (
            "a line comment naming a construct is documentation, not a call",
            "// never resolve \(Needle.home) outside AppHome",
            []
        ),
        (
            "a doc comment naming a construct is documentation too",
            "/// `\(Needle.nsHome)()` and `\(Needle.home)` are both banned here",
            []
        ),
        (
            "a trailing comment does not taint the real code on its line",
            "let home = AppHome.url // not FileManager.default.\(Needle.home)",
            []
        ),
        (
            "AppHome is the remedy and must never be flagged",
            "let sounds = AppHome.library.appendingPathComponent(\"Sounds\")",
            []
        ),
        (
            "the temporary directory is not the home directory",
            "let tmp = NSTemporary" + "Directory()",
            []
        ),
        (
            "FileManager.temporaryDirectory is not the home directory either",
            "let tmp = FileManager.default.temporaryDirectory",
            []
        ),
        (
            "a bare mention of the function name without a call is not a call",
            "let name = \"\(Needle.nsHome)\" + \"-marker\"",
            []
        ),
        (
            "the system-wide domain is a different domain",
            "FileManager.default.urls(for: .libraryDirectory, in: .systemDomainMask).first",
            []
        ),
        (
            "LIMIT: a string literal holding a construct is flagged, because a line scan cannot tell it from a call",
            "let needle = \"FileManager.default.\(Needle.home)\"",
            ["1:\(Needle.home)"]
        ),
        (
            "LIMIT: a block comment holding a construct is flagged, for the same reason",
            "/* \(Needle.userDomain) */",
            ["1:\(Needle.userDomain)"]
        ),
    ]

    func testScanReturnsThePinnedVerdictForEverySpelling() {
        for row in Self.corpus {
            XCTAssertEqual(
                Self.offendingOccurrences(in: row.source), row.want,
                "\(row.name)\n---\n\(row.source)\n---"
            )
        }
    }

    /// The corpus is also the vacuity guard for the live scan: after this change
    /// the tree contains no offenders, so "no offenders" and "the rule stopped
    /// matching anything" are otherwise the same output.
    func testTheCorpusContainsBothVerdictsForEveryConstruct() {
        XCTAssertFalse(Self.corpus.filter { $0.want.isEmpty }.isEmpty, "no must-not-flag rows")
        for construct in Self.constructs {
            XCTAssertTrue(
                Self.corpus.contains { row in row.want.contains { $0.hasSuffix(":\(construct.name)") } },
                "no corpus row pins \(construct.name) as flagged — that construct is untested"
            )
        }
    }

    // MARK: - The live scan

    func testEveryExemptionNamesAFileThatExists() {
        for (relative, reason) in Self.exemptions {
            XCTAssertTrue(
                FileManager.default.fileExists(atPath: Self.macosRoot.appendingPathComponent(relative).path),
                "exemption '\(relative)' (\(reason)) no longer names a file — a stale exemption "
                + "silently widens the rule"
            )
        }
    }

    func testNoSourceOutsideAppHomeResolvesTheRealHome() throws {
        var offenders: [String] = []
        var filesScanned = 0
        var exemptionsHit = Set<String>()

        for directory in Self.scannedDirectories {
            let root = Self.macosRoot.appendingPathComponent(directory)
            var isDirectory: ObjCBool = false
            guard FileManager.default.fileExists(atPath: root.path, isDirectory: &isDirectory),
                  isDirectory.boolValue else {
                // Loud, not silent: a walk over a directory that is not there
                // finds nothing and is indistinguishable from a clean tree.
                XCTFail("scanned directory \(directory) is missing at \(root.path)")
                continue
            }
            guard let walk = FileManager.default.enumerator(at: root, includingPropertiesForKeys: nil) else {
                XCTFail("could not enumerate \(root.path)")
                continue
            }
            for case let url as URL in walk where url.pathExtension == "swift" {
                let relative = url.path.replacingOccurrences(of: Self.macosRoot.path + "/", with: "")
                filesScanned += 1
                let occurrences = Self.offendingOccurrences(in: try String(contentsOf: url, encoding: .utf8))
                if Self.exemptions[relative] != nil {
                    if !occurrences.isEmpty { exemptionsHit.insert(relative) }
                    continue
                }
                offenders.append(contentsOf: occurrences.map { "\(relative):\($0)" })
            }
        }

        XCTAssertGreaterThan(filesScanned, 0, "the scan read no Swift files — it checked nothing")
        // An exemption that no longer covers anything is a rule that has quietly
        // grown wider than it needs to be, and — more to the point here — it is
        // the shape a scan takes when it has stopped reading the files it thinks
        // it is reading.
        XCTAssertEqual(
            exemptionsHit, Set(Self.exemptions.keys),
            "an exemption covered nothing this run: either the file no longer needs it, or the "
            + "scan did not actually read it"
        )
        XCTAssertEqual(
            offenders, [],
            """
            Source outside `AppHome` resolves the user's real home directory. Use `AppHome.url` \
            or `AppHome.library`, which redirect to a per-process temporary home under XCTest.

            Resolving the real home here is what put a fixture into the live daemon's \
            instances/ directory, replaced the developer's own session-order.json with test data \
            and installed audio into ~/Library/Sounds behind a `defer` that aborts skip \
            (#1669, #1670).

            \(offenders.joined(separator: "\n"))
            """
        )
    }

    private static let macosRoot = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent()        // Tests/
        .deletingLastPathComponent()        // platforms/macos/
}
