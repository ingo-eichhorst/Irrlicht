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

    /// Assembled from pieces so this file's own source never produces a match —
    /// the corpus fixtures below are built the same way. Without that the live
    /// scan would flag its own test data, and the fix for *that* is an
    /// exclusion, which is a hole in the rule rather than a property of it.
    /// `testThisFileIsCleanUnderItsOwnRule` asserts the claim rather than
    /// leaving it as prose.
    enum Needle {
        /// The one alpha every notice ground is drawn at.
        static let washAlpha = "0." + "12"
        static let opacity = ".opa" + "city("
        /// Matched WITHOUT its opening paren: SwiftUI also spells a ground as
        /// `.background { … }` and `.background(alignment:) { … }`, and a rule
        /// that only knew the parenthesised form would let the whole defect
        /// class back in through a trailing closure.
        static let background = ".back" + "ground"
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
        let text = commentsBlanked(source) as NSString
        var found: [String] = []
        if rules.contains(.backgroundAlpha) { found += groundsComposingTheirOwnAlpha(in: text) }
        if rules.contains(.washAlpha) { found += theAlphaSpelledOutAgain(in: text) }
        return found.sorted()
    }

    enum Rule: CaseIterable {
        case backgroundAlpha
        case washAlpha
    }

    /// Every `.background` whose ground builds its own alpha — in either of the
    /// spellings SwiftUI offers, `(…)` and a trailing `{ … }`, since the two
    /// render the same thing and a rule that saw only one would be evadable by
    /// typing the other. A `.background` whose delimiters never close is
    /// REPORTED (`background-unparsable`), never skipped.
    private static func groundsComposingTheirOwnAlpha(in text: NSString) -> [String] {
        occurrences(of: Needle.background, in: text).compactMap { start in
            let line = lineNumber(of: start, in: text)
            guard let ground = groundExpression(in: text, after: start + Needle.background.utf16.count) else {
                return "\(line):background-unparsable"
            }
            return ground.contains(Needle.opacity) ? "\(line):background-alpha" : nil
        }
    }

    /// The wash alpha written out anywhere other than its one definition —
    /// either as an `.opacity(…)` call or as a hand-composited measurement's
    /// `alpha:` argument, with or without the space.
    private static func theAlphaSpelledOutAgain(in text: NSString) -> [String] {
        [Needle.opacity + Needle.washAlpha + ")",
         Needle.alphaArgument + " " + Needle.washAlpha,
         Needle.alphaArgument + Needle.washAlpha]
            .flatMap { occurrences(of: $0, in: text) }
            .map { "\(lineNumber(of: $0, in: text)):wash-alpha" }
    }

    /// Everything a `.background` is handed: its parenthesised argument, its
    /// trailing closure body, or both. `nil` when a delimiter it opened never
    /// closes, and `""` when it is a bare mention with neither — prose that
    /// survived comment-blanking inside a string literal, which is not a call
    /// and cannot carry an alpha.
    private static func groundExpression(in text: NSString, after start: Int) -> String? {
        let openParen: unichar = 40, openBrace: unichar = 123
        var index = start
        var parts: [String] = []
        while index < text.length, text.character(at: index) == 32 || text.character(at: index) == 10 {
            index += 1
        }
        if index < text.length, text.character(at: index) == openParen {
            guard let argument = balancedRun(in: text, from: index + 1, opener: openParen) else { return nil }
            parts.append(argument)
            index += 1 + argument.utf16.count + 1
            while index < text.length, text.character(at: index) == 32 || text.character(at: index) == 10 {
                index += 1
            }
        }
        if index < text.length, text.character(at: index) == openBrace {
            guard let body = balancedRun(in: text, from: index + 1, opener: openBrace) else { return nil }
            parts.append(body)
        }
        return parts.joined(separator: "\n")
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

    /// The text from `start` up to the `)` or `}` closing the delimiter that
    /// opened at `start - 1`, or `nil` when it never closes. String literals are
    /// tracked so a delimiter inside one cannot unbalance the walk.
    private static func balancedRun(in text: NSString, from start: Int, opener: unichar) -> String? {
        let closer: unichar = opener == 40 ? 41 : 125
        var depth = 1
        var literal = StringLiteral()
        for index in start..<text.length {
            let char = text.character(at: index)
            guard literal.isCode(char) else { continue }
            if char == opener {
                depth += 1
            } else if char == closer {
                depth -= 1
                if depth == 0 {
                    return text.substring(with: NSRange(location: start, length: index - start))
                }
            }
        }
        return nil
    }

    /// Whether the walk is inside a `"…"` literal. Factored out so both the
    /// delimiter walk and the comment blanking read the same rule for what
    /// counts as code — they disagreed once, and the disagreement was a silent
    /// false negative (a `://` in a literal blanked the code after it).
    private struct StringLiteral {
        private var open = false
        private var escaped = false

        /// Consumes `char` and reports whether it is structural code rather
        /// than literal text.
        mutating func isCode(_ char: unichar) -> Bool {
            let quote: unichar = 34, backslash: unichar = 92
            guard open else {
                if char == quote { open = true; return false }
                return true
            }
            if escaped { escaped = false } else if char == backslash { escaped = true } else if char == quote { open = false }
            return false
        }
    }

    private static func lineNumber(of offset: Int, in text: NSString) -> Int {
        text.substring(to: offset).components(separatedBy: "\n").count
    }

    /// Replaces each line comment with spaces of the same UTF-16 width, so the
    /// scan sees no comment and every match offset still maps to its line.
    ///
    /// A `//` inside a string literal is not a comment — this tree is full of
    /// `"ws://…"` and `"http://…"` — so the blanking tracks literals rather
    /// than taking the first `//` on the line. Getting that wrong blanks real
    /// code and fails SILENTLY, which is the one direction this rule must not
    /// fail in.
    private static func commentsBlanked(_ source: String) -> String {
        source.split(separator: "\n", omittingEmptySubsequences: false).map { line -> String in
            let text = String(line) as NSString
            var literal = StringLiteral()
            var previousWasSlash = false
            for index in 0..<text.length {
                let char = text.character(at: index)
                guard literal.isCode(char) else { previousWasSlash = false; continue }
                let slash: unichar = 47
                if char == slash && previousWasSlash {
                    let head = text.substring(to: index - 1)
                    return head + String(repeating: " ", count: text.length - (index - 1))
                }
                previousWasSlash = char == slash
            }
            return String(line)
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
            "\(Fixture.call)IrrColors.noticeWash(hue))",
            []
        ),
        (
            "a ground reading a token derived from it is fine too",
            "\(Fixture.call)IrrColors.workingDim)",
            []
        ),
        (
            "a ground composing its own alpha is flagged, and the alpha is flagged again",
            "\(Fixture.call)hue\(Fixture.opacity)\(Fixture.washAlpha)))",
            ["1:background-alpha", "1:wash-alpha"]
        ),
        (
            "a DIFFERENT alpha is flagged just the same — divergence is the defect, not the number",
            "\(Fixture.call)IrrColors.pressureHigh\(Fixture.opacity)0.08))",
            ["1:background-alpha"]
        ),
        (
            "a ground broken across lines is flagged: the scan follows the delimiters, not the line",
            "\(Fixture.call)\n    Color.orange\(Fixture.opacity)0.1)\n)",
            ["1:background-alpha"]
        ),
        (
            "the trailing-closure spelling is the same ground and is flagged the same",
            "\(Fixture.background) { hue\(Fixture.opacity)0.08) }",
            ["1:background-alpha"]
        ),
        (
            "…and so is the spelling that takes an argument AND a trailing closure",
            "\(Fixture.call)alignment: .topLeading) { hue\(Fixture.opacity)0.08) }",
            ["1:background-alpha"]
        ),
        (
            "a URL in a string literal does not blank the code after it — `//` there is not a comment",
            "Text(\"ws://host\")\(Fixture.call)hue\(Fixture.opacity)0.08))",
            ["1:background-alpha"]
        ),
        (
            "even reading the alpha from its token is flagged: the canonical spelling is noticeWash",
            "\(Fixture.call)hue\(Fixture.opacity)IrrColors.noticeWashAlpha))",
            ["1:background-alpha"]
        ),
        (
            "a bare mention with no call carries no ground and is not flagged",
            "Text(\"the \(Fixture.background) modifier\")",
            []
        ),
        (
            "a glow is not a ground — only what a background is handed is the rule's business",
            "static let workingGlow = working\(Fixture.opacity)0.25)",
            []
        ),
        (
            "the contrast test's hand-composited alpha is the second place the number must not drift",
            "composite(srgb(IrrColors.error), \(Fixture.alphaArgument) \(Fixture.washAlpha),",
            ["1:wash-alpha"]
        ),
        (
            "…with or without the space after the label",
            "composite(srgb(IrrColors.error), \(Fixture.alphaArgument)\(Fixture.washAlpha),",
            ["1:wash-alpha"]
        ),
        (
            "the alpha's own definition is not a second spelling of it",
            "static let noticeWashAlpha: Double = \(Fixture.washAlpha)",
            []
        ),
        (
            "prose about the alpha is not code — line comments are blanked before the scan",
            "// the ground is drawn at \(Fixture.washAlpha) via \(Fixture.opacity)\(Fixture.washAlpha))",
            []
        ),
        (
            "LIMIT: an unbalanced ground is REPORTED, never skipped — a scan that cannot parse "
            + "its input checks more, not less",
            "\(Fixture.call)Color.red\(Fixture.opacity)0.2)",
            ["1:background-unparsable"]
        ),
        (
            "LIMIT: a block comment is not blanked, so code quoted inside one is flagged",
            "/* \(Fixture.call)hue\(Fixture.opacity)0.5)) */",
            ["1:background-alpha"]
        ),
    ]

    /// The literal Swift spellings the corpus is written in.
    ///
    /// Assembled independently of `Needle` ON PURPOSE, even though the pieces
    /// look the same. Derived from it, every fixture would move whenever the
    /// rule moved, and a corpus that follows the scanner cannot catch the
    /// scanner losing a spelling — which is exactly what it is for. These
    /// constants are the language; `Needle` is one reading of it.
    private enum Fixture {
        static let background = ".back" + "ground"
        static let call = background + "("
        static let opacity = ".opa" + "city("
        static let washAlpha = "0." + "12"
        static let alphaArgument = "alpha" + ":"
    }

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
        // Per directory, not summed over all of them: a directory that still
        // exists but has stopped holding the sources it is meant to hold is
        // invisible in an aggregate that the other three keep in the hundreds.
        XCTAssertGreaterThan(filesScanned, 0, "no Swift files under \(directory) — that walk checked nothing")
        return (offenders, filesScanned)
    }

    /// The scan reads this file too, and the `Needle` doc claims it can never
    /// match its own corpus. Asserted rather than left as prose: if it ever did
    /// match, the tempting fix is to exclude this file, and an exclusion is the
    /// hole the rule spends a paragraph refusing.
    func testThisFileIsCleanUnderItsOwnRule() throws {
        let source = try String(contentsOfFile: #filePath, encoding: .utf8)
        XCTAssertEqual(Self.offendingOccurrences(in: source), [],
                       "the lint's own source matches the lint — assemble the new spelling from `Needle`")
    }

    func testEveryNoticeGroundReadsTheOneWashToken() throws {
        var offenders: [String] = []
        for (directories, rules) in [(Self.backgroundRuleDirectories, Set<Rule>([.backgroundAlpha])),
                                     (Self.washRuleDirectories, Set<Rule>([.washAlpha]))] {
            for directory in directories {
                offenders.append(contentsOf: try walk(directory, rules: rules).offenders)
            }
        }

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
