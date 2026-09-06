import XCTest

/// The structural half of #1814.
///
/// Every notice surface in this app — the panel-width banners, the inline alert
/// strips, the row pills — sits on the same tinted ground: the notice's hue at
/// one shared alpha. That alpha was spelled independently at five sites written
/// at five different times, and it had already drifted to three values
/// (`0.12` in `SessionRowView`, `0.08` in `PermissionWizardView`, `0.10` in
/// `SessionListView.errorView`).
///
/// The drift is not cosmetic. `TokenContrastTests` proves the error text clears
/// WCAG AA *against a 12% wash*; while the row's error strip shipped 8% the
/// test was bounding a value nothing rendered rather than measuring one that
/// did (#1802, recorded in `AlertStrip`'s own doc comment). A sixth site
/// written tomorrow would reopen exactly that gap, and the only thing that
/// closes it for good is a rule: the alpha is spelled once, and every ground
/// reads it.
///
/// So this fails the build on both halves of the defect:
///
///   - **`background-alpha`** — a `.background(…)` whose argument composes its
///     own alpha. Grounds read `IrrColors.noticeWash(_:)` or a token derived
///     from it, never `hue.opacity(<literal>)`.
///   - **`wash-alpha`** — the wash alpha written out anywhere other than its
///     one definition, whether as an `.opacity(…)` call or as an argument to a
///     contrast measurement. This is the half that keeps the *test* and the
///     *view* reading the same number.
///
/// # Why there are no exemptions
///
/// Every ground in the tree reads a token after #1814, including the two that
/// are not notices at all (`IrrColors.surfaceHover`, `IrrColors.chipFill`), so
/// the noise floor is zero rather than "low" and an exemption list would be a
/// hole rather than a property. `testTheCorpusPinsBothVerdicts` is the vacuity
/// guard in its place: with no offenders left in the tree, "the rule found
/// nothing" and "the rule stopped matching anything" are otherwise the same
/// output.
final class NoticeWashLintTests: XCTestCase {

    // MARK: - The rule, as a pure function

    /// Assembled from pieces so this file's own source never contains a
    /// contiguous match — the corpus fixtures below are built the same way.
    /// Without that the live scan would flag its own test data, and the fix for
    /// *that* is an exclusion, which is a hole in the rule rather than a
    /// property of it.
    enum Needle {
        /// The one alpha every notice ground is drawn at.
        static let washAlpha = "0." + "12"
        static let opacity = ".opa" + "city("
        static let background = ".back" + "ground("
        /// `TokenContrastTests` composites the wash by hand; its alpha argument
        /// is the second place the number has to stay in step with the view.
        static let alphaArgument = "alpha" + ":"
    }

    /// Directories walked for each rule, relative to `platforms/macos/`.
    ///
    /// `background-alpha` is a rule about *product* chrome, so it reads the app
    /// target only — a test may host a fixture on any ground it likes.
    /// `wash-alpha` reads the tests too, because the whole point of the second
    /// rule is that the number the contrast test measures and the number the
    /// view renders cannot drift apart again.
    private static let backgroundRuleDirectories = ["Irrlicht"]
    private static let washRuleDirectories = ["Irrlicht", "Tests", "TestsHarness"]

    /// `"<line>:<verdict>"` for every offending occurrence in `source`, sorted
    /// so the verdict is stable.
    ///
    /// Line comments are blanked first, padded to the same width so reported
    /// line numbers survive the blanking — the files scanned here discuss these
    /// constants at length in prose, and a rule that could not tell
    /// documentation from code would be unusable in this repo. String literals
    /// are deliberately NOT stripped: a scan cannot tell a literal from a call,
    /// and AGENTS.md's rule is that a validator which cannot parse its input
    /// checks MORE, never less. The same principle is why an unbalanced
    /// `.background(` reports `background-unparsable` instead of being skipped.
    /// Both limits are pinned in the corpus.
    static func offendingOccurrences(in source: String,
                                     rules: Set<Rule> = Set(Rule.allCases)) -> [String] {
        let code = commentsBlanked(source)
        let text = code as NSString
        var found: [String] = []

        if rules.contains(.backgroundAlpha) {
            for start in occurrences(of: Needle.background, in: text) {
                let line = lineNumber(of: start, in: text)
                guard let argument = balancedArgument(in: text, from: start + Needle.background.utf16.count) else {
                    found.append("\(line):background-unparsable")
                    continue
                }
                if argument.contains(Needle.opacity) {
                    found.append("\(line):background-alpha")
                }
            }
        }

        if rules.contains(.washAlpha) {
            for needle in [Needle.opacity + Needle.washAlpha + ")",
                           Needle.alphaArgument + " " + Needle.washAlpha] {
                for start in occurrences(of: needle, in: text) {
                    found.append("\(lineNumber(of: start, in: text)):wash-alpha")
                }
            }
        }

        return found.sorted()
    }

    enum Rule: CaseIterable {
        case backgroundAlpha
        case washAlpha
    }

    /// UTF-16 offsets of every occurrence of `needle`.
    private static func occurrences(of needle: String, in text: NSString) -> [Int] {
        var found: [Int] = []
        var searched = 0
        while searched < text.length {
            let range = text.range(of: needle,
                                   options: [],
                                   range: NSRange(location: searched, length: text.length - searched))
            guard range.location != NSNotFound else { break }
            found.append(range.location)
            searched = range.location + max(range.length, 1)
        }
        return found
    }

    /// The text between a `(` at `start - 1` and its matching `)`, or `nil`
    /// when the parentheses never balance. Quotes are tracked so a `(` inside a
    /// string literal cannot unbalance the walk.
    private static func balancedArgument(in text: NSString, from start: Int) -> String? {
        let open: unichar = 40, close: unichar = 41, quote: unichar = 34, backslash: unichar = 92
        var depth = 1
        var index = start
        var inString = false
        var escaped = false
        while index < text.length {
            let char = text.character(at: index)
            if inString {
                if escaped {
                    escaped = false
                } else if char == backslash {
                    escaped = true
                } else if char == quote {
                    inString = false
                }
            } else if char == quote {
                inString = true
            } else if char == open {
                depth += 1
            } else if char == close {
                depth -= 1
                if depth == 0 {
                    return text.substring(with: NSRange(location: start, length: index - start))
                }
            }
            index += 1
        }
        return nil
    }

    private static func lineNumber(of offset: Int, in text: NSString) -> Int {
        text.substring(to: offset).components(separatedBy: "\n").count
    }

    /// Replaces each line comment with spaces of the same UTF-16 width, so the
    /// scan sees no comment and every match offset still maps to its line.
    private static func commentsBlanked(_ source: String) -> String {
        source.split(separator: "\n", omittingEmptySubsequences: false).map { line -> String in
            guard let comment = line.range(of: "//") else { return String(line) }
            let code = line[..<comment.lowerBound]
            let blanked = String(repeating: " ", count: line[comment.lowerBound...].utf16.count)
            return String(code) + blanked
        }.joined(separator: "\n")
    }

    // MARK: - The corpus: the scanner's own red and green

    /// Every spelling the rule has an opinion about, with its pinned verdict.
    /// This is the mutation fixture for a check that has no "before the fix" to
    /// run red: each flagged row is a deliberate break of the thing the rule
    /// protects, committed rather than described.
    private static let corpus: [(name: String, source: String, want: [String])] = [
        (
            "a ground reading the shared token is the point of the rule",
            "\(Needle.background)IrrColors.noticeWash(wash))",
            []
        ),
        (
            "a ground reading a token derived from it is fine too",
            "\(Needle.background)IrrColors.workingDim)",
            []
        ),
        (
            "a ground composing its own alpha is flagged, and the alpha is flagged again",
            "\(Needle.background)wash\(Needle.opacity)\(Needle.washAlpha)))",
            ["1:background-alpha", "1:wash-alpha"]
        ),
        (
            "a DIFFERENT alpha is flagged just the same — divergence is the defect, not the number",
            "\(Needle.background)IrrColors.pressureHigh\(Needle.opacity)0.08))",
            ["1:background-alpha"]
        ),
        (
            "a ground broken across lines is flagged: the scan follows the parentheses, not the line",
            "\(Needle.background)\n    Color.orange\(Needle.opacity)0.1)\n)",
            ["1:background-alpha"]
        ),
        (
            "a glow is not a ground — only `.background(…)` arguments are the rule's business",
            "static let workingGlow = working\(Needle.opacity)0.25)",
            []
        ),
        (
            "the contrast test's hand-composited alpha is the second place the number must not drift",
            "composite(srgb(IrrColors.error), \(Needle.alphaArgument) \(Needle.washAlpha),",
            ["1:wash-alpha"]
        ),
        (
            "the alpha's own definition is not a second spelling of it",
            "static let noticeWashAlpha: Double = \(Needle.washAlpha)",
            []
        ),
        (
            "prose about the alpha is not code — line comments are blanked before the scan",
            "// the ground is drawn at \(Needle.washAlpha) via \(Needle.opacity)\(Needle.washAlpha))",
            []
        ),
        (
            "LIMIT: an unbalanced ground is REPORTED, never skipped — a scan that cannot parse "
            + "its input checks more, not less",
            "\(Needle.background)Color.red\(Needle.opacity)0.2)",
            ["1:background-unparsable"]
        ),
        (
            "LIMIT: a block comment is not blanked, so code quoted inside one is flagged",
            "/* \(Needle.background)wash\(Needle.opacity)0.5)) */",
            ["1:background-alpha"]
        ),
    ]

    func testTheScannerReturnsThePinnedVerdictForEverySpelling() {
        for row in Self.corpus {
            XCTAssertEqual(
                Self.offendingOccurrences(in: row.source), row.want,
                "\(row.name)\n---\n\(row.source)\n---"
            )
        }
    }

    /// The corpus is the vacuity guard for the live scan below: once the tree
    /// holds no offenders, a scanner that had silently stopped matching would
    /// produce the same empty verdict as a clean tree.
    func testTheCorpusPinsBothVerdicts() {
        XCTAssertFalse(Self.corpus.filter { $0.want.isEmpty }.isEmpty,
                       "no must-not-flag rows — the rule could be flagging everything")
        for verdict in ["background-alpha", "wash-alpha", "background-unparsable"] {
            XCTAssertTrue(
                Self.corpus.contains { row in row.want.contains { $0.hasSuffix(":\(verdict)") } },
                "no corpus row pins \(verdict) — that verdict is untested"
            )
        }
    }

    // MARK: - The live scan

    /// What one directory's walk found. A walk over a directory that is not
    /// there finds nothing and is indistinguishable from a clean tree, so both
    /// unreachable cases `XCTFail` rather than returning an empty result.
    private func walk(_ directory: String, rules: Set<Rule>) throws -> (offenders: [String], filesScanned: Int) {
        let root = Self.macosRoot.appendingPathComponent(directory)
        var isDirectory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: root.path, isDirectory: &isDirectory),
              isDirectory.boolValue else {
            XCTFail("scanned directory \(directory) is missing at \(root.path)")
            return ([], 0)
        }
        guard let enumerator = FileManager.default.enumerator(at: root, includingPropertiesForKeys: nil) else {
            XCTFail("could not enumerate \(root.path)")
            return ([], 0)
        }
        var offenders: [String] = []
        var filesScanned = 0
        for case let url as URL in enumerator where url.pathExtension == "swift" {
            let relative = url.path.replacingOccurrences(of: Self.macosRoot.path + "/", with: "")
            filesScanned += 1
            let source = try String(contentsOf: url, encoding: .utf8)
            offenders.append(contentsOf: Self.offendingOccurrences(in: source, rules: rules)
                .map { "\(relative):\($0)" })
        }
        return (offenders, filesScanned)
    }

    func testEveryNoticeGroundReadsTheOneWashToken() throws {
        var offenders: [String] = []
        var filesScanned = 0
        for (directories, rules) in [(Self.backgroundRuleDirectories, Set<Rule>([.backgroundAlpha])),
                                     (Self.washRuleDirectories, Set<Rule>([.washAlpha]))] {
            for directory in directories {
                let found = try walk(directory, rules: rules)
                offenders.append(contentsOf: found.offenders)
                filesScanned += found.filesScanned
            }
        }

        XCTAssertGreaterThan(filesScanned, 0, "the scan read no Swift files — it checked nothing")
        XCTAssertEqual(
            offenders.sorted(), [],
            """
            A notice ground composes its own alpha instead of reading the one token. \
            Draw grounds with `IrrColors.noticeWash(hue)` (or a token derived from it) and \
            spell the alpha only at `IrrColors.noticeWashAlpha`.

            Five sites spelled it independently and it had drifted to three values by #1814; \
            while one of them shipped 8%, `TokenContrastTests` was proving WCAG AA against a \
            12% wash that nothing rendered (#1802).

            \(offenders.sorted().joined(separator: "\n"))
            """
        )
    }

    private static let macosRoot = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent()        // Tests/
        .deletingLastPathComponent()        // platforms/macos/
}
