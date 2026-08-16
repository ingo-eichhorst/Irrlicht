import XCTest

/// The structural half of #1661.
///
/// `InMemoryDefaults` makes the safe thing available; it cannot make the unsafe
/// thing impossible, because `UserDefaults(suiteName:)` is a Foundation
/// initializer and nothing stops the next test from calling it. That is exactly
/// how 1136 plists reached a developer's real `~/Library/Preferences`: one
/// suite, one test class, no reviewer with a reason to notice.
///
/// So the rule is enforced the way AGENTS.md enforces the same shape twice
/// elsewhere — an agent-owned SQLite store is opened through `core/pkg/sqlitero`
/// and never `sql.Open`, and an inbound hook body is read only by
/// `hookjson.DecodeConfined` — by failing the build on any other call site.
/// This is what makes the fix cover the *target* rather than one suite: a test
/// written next year inherits it without anyone remembering #1661.
///
/// Scope, stated so it is not mistaken for more: this bans the construct that
/// mints a *new* persistent domain, and therefore a new file, per call. It does
/// not ban `UserDefaults.standard`, which several suites here mutate under
/// snapshot-and-restore — that writes into one already-existing domain rather
/// than accreting files, and is a different problem with a different fix.
///
/// There is deliberately **no exemption list**, for the reason
/// `core/architecture_hookbody_test.go` gives: a test that genuinely needs a raw
/// suite amends this rule in a reviewable diff.
final class PersistentDefaultsLintTests: XCTestCase {

    // MARK: - The rule, as a pure function

    /// Assembled from two pieces so this file's own source never contains a
    /// contiguous match. Every corpus fixture below is assembled the same way.
    /// Without that, the scan would flag its own test data — and the fix for
    /// *that* is an exclusion, which is a hole in the rule rather than a
    /// property of it.
    private static let constructor = "UserDefaults" + "("

    private static let pattern = "UserDefaults" + #"\s*\(\s*suiteName\s*:"#

    private static let testTargetDirectories = ["Tests", "TestsHarness"]

    /// 1-based line numbers of every offending construction in `source`.
    ///
    /// Line comments are stripped first, padded with spaces so that offsets —
    /// and therefore reported line numbers — survive the strip. Block comments
    /// and string literals are **not** stripped, and that is a decision rather
    /// than an omission: a line-based scan cannot tell a literal from a call,
    /// and AGENTS.md's rule is that a validator which cannot parse its input
    /// checks MORE, never less. Both limits are pinned in the corpus.
    static func offendingLines(in source: String) -> [Int] {
        let code = commentsBlanked(source)
        guard let regex = try? NSRegularExpression(pattern: pattern) else { return [] }
        let text = code as NSString
        var lines: [Int] = []
        regex.enumerateMatches(in: code, range: NSRange(location: 0, length: text.length)) { match, _, _ in
            guard let match else { return }
            lines.append(text.substring(to: match.range.location).components(separatedBy: "\n").count)
        }
        return lines
    }

    /// Replaces each line comment with spaces of the same UTF-16 width, so the
    /// regex sees no comment and every match offset still maps to its original
    /// line.
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
    /// without them, and two of them (`super.init`, the similarly-named helper)
    /// are the false positives a bare substring rule produces. The two rows that
    /// are pinned as flagged *despite* being harmless are declared limits of a
    /// line-based scan, learned here rather than from an incident.
    private static let corpus: [(name: String, source: String, want: [Int])] = [
        (
            "a plain construction is the whole point of the rule",
            "let defaults = \(constructor)suiteName: \"x\")",
            [1]
        ),
        (
            "whitespace around the paren and the colon does not evade it",
            "let defaults = " + "UserDefaults" + " ( suiteName : \"x\")",
            [1]
        ),
        (
            "a call split across lines is reported at the line it starts on",
            "let defaults = \(constructor)\n    suiteName: \"x\"\n)",
            [1]
        ),
        (
            "every occurrence is reported, not only the first",
            "let a = \(constructor)suiteName: \"x\")\nlet unrelated = 1\nlet b = \(constructor)suiteName: \"y\")",
            [1, 3]
        ),
        (
            "a line comment naming the construct is documentation, not a call",
            "// never write \(constructor)suiteName:) in a test",
            []
        ),
        (
            "a doc comment naming the construct is documentation too",
            "/// see \(constructor)suiteName:) and why it is banned",
            []
        ),
        (
            "a trailing comment does not taint the real code on its line",
            "let d = InMemoryDefaults() // not \(constructor)suiteName: \"x\")",
            []
        ),
        (
            "super.init(suiteName:) inside the double is not this construct",
            "super.init(suiteName: nil)!",
            []
        ),
        (
            "a similarly named helper is not a UserDefaults suite",
            "let d = makeUserDefaults(suiteNameHint: \"x\")",
            []
        ),
        (
            "UserDefaults.standard is deliberately out of scope",
            "UserDefaults.standard.set(true, forKey: \"k\")",
            []
        ),
        (
            "a suiteless construction mints no domain",
            "let d = " + "UserDefaults" + "()",
            []
        ),
        (
            "LIMIT: a string literal holding the construct is flagged, because a line scan cannot tell it from a call",
            "let needle = \"\(constructor)suiteName:\"",
            [1]
        ),
        (
            "LIMIT: a block comment holding the construct is flagged, for the same reason",
            "/* \(constructor)suiteName: \"x\") */",
            [1]
        )
    ]

    func testScanReturnsThePinnedVerdictForEverySpelling() {
        for row in Self.corpus {
            XCTAssertEqual(
                Self.offendingLines(in: row.source), row.want,
                "\(row.name)\n---\n\(row.source)\n---"
            )
        }
    }

    /// The corpus is also the vacuity guard for the live scan below: after this
    /// change the construct appears nowhere in the test targets, so "no
    /// offenders" and "the rule stopped matching anything" are otherwise the
    /// same output.
    func testTheCorpusContainsBothVerdicts() {
        XCTAssertFalse(Self.corpus.filter { $0.want.isEmpty }.isEmpty, "no must-not-flag rows")
        XCTAssertFalse(Self.corpus.filter { !$0.want.isEmpty }.isEmpty, "no must-flag rows")
    }

    // MARK: - The live scan

    func testNoTestConstructsAPersistentUserDefaultsSuite() throws {
        var offenders: [String] = []
        var filesScanned = 0

        let macosRoot = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()        // Tests/
            .deletingLastPathComponent()        // platforms/macos/

        for directory in Self.testTargetDirectories {
            let root = macosRoot.appendingPathComponent(directory)
            var isDirectory: ObjCBool = false
            guard FileManager.default.fileExists(atPath: root.path, isDirectory: &isDirectory),
                  isDirectory.boolValue else {
                // A renamed or removed test target must be loud, not silent: a
                // walk over a directory that is not there finds nothing and is
                // indistinguishable from a clean tree.
                XCTFail("test target directory \(directory) is missing at \(root.path)")
                continue
            }
            guard let walk = FileManager.default.enumerator(at: root, includingPropertiesForKeys: nil) else {
                XCTFail("could not enumerate \(root.path)")
                continue
            }
            for case let url as URL in walk where url.pathExtension == "swift" {
                filesScanned += 1
                let source = try String(contentsOf: url, encoding: .utf8)
                let relative = url.path.replacingOccurrences(of: macosRoot.path + "/", with: "")
                offenders.append(contentsOf: Self.offendingLines(in: source).map { "\(relative):\($0)" })
            }
        }

        XCTAssertGreaterThan(filesScanned, 0, "the scan read no Swift files — it checked nothing")
        XCTAssertEqual(
            offenders, [],
            """
            A test constructs a persistent preference suite. Use \
            `InMemoryDefaults` instead: a named suite leaves a file in the \
            developer's real ~/Library/Preferences that \
            `removePersistentDomain(forName:)` does not remove and that nothing \
            in this repository is allowed to delete (#1661 — 1136 of them).

            \(offenders.joined(separator: "\n"))
            """
        )
    }
}
