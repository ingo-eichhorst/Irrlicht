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
/// **Two rules, because #1662 closed the case this file used to defer.** The
/// first bans the construct that mints a *new* persistent domain, and therefore
/// a new file, per call (#1661). The second bans **mutating**
/// `UserDefaults.standard` — which this file previously called "a different
/// problem with a different fix", correctly: under `swift test` that resolves
/// to `com.apple.dt.xctest.tool`, one already-existing plist in the developer's
/// real `~/Library/Preferences`, so it accretes no files and is not #1661. It
/// is #1662: six suites wrote keys there in `setUp` and restored them in
/// `tearDown`, which is per-suite rather than a property of the snapshot
/// strategy, pinned a smaller set than the views read, and depended on a
/// `tearDown` that the runs which abort (#1523), get their tree killed at 240s
/// or run out of `--budget` never reach. Now that `PinnedSnapshotHost` supplies
/// a store and `SessionManager` takes one, no test needs to write that domain,
/// so the rule can be stated rather than deferred.
///
/// **A third rule, because two source rules over the TEST targets could not see
/// #1672 at all.** #1662 removed every `UserDefaults.standard` mutation from the
/// test targets and two keys still appeared in the developer's real
/// `com.apple.dt.xctest.tool` domain across a suite run: `soundOnReady = funk`
/// and `soundOnContextPressure = sosumi`. The write was in the APP —
/// `NotificationEventRow` mirrored its sound key into `@State`, loaded it in
/// `.onAppear` and persisted it back from `.onChange`, so merely RENDERING the
/// row wrote the value it had just read — and `SettingsViewTests` renders
/// `SettingsView`. A rule that scans only test sources is structurally blind to
/// that: the offending line is in a file it does not read.
///
/// So the mutation rule is applied to the app target too. It is not a weaker
/// claim there, it is a stronger one: in the app that domain is the user's real
/// `io.irrlicht.app`, so a write there persists a preference the user never set
/// — the same defect #1673's `Published(initialValue:)` trap was, one layer up.
/// Every persist in this app has a seam that does not need it: `@AppStorage`,
/// which honours `.defaultAppStorage(_:)` and is therefore pinnable, or an
/// injected `UserDefaults` (`SessionManager.init(defaults:)`). Measured after
/// #1672's fix: the construct appears **zero** times in the app target, so the
/// rule needs no exemption and gets none.
///
/// READS are deliberately left legal by the three rules above, in both targets.
/// `UserDefaults.standard.object(forKey:)` is how a test says "and the process
/// domain was not touched", which is the assertion that would have caught this
/// class in the first place; banning it would ban the evidence along with the
/// defect. And app-target-wide a read ban is not tractable: measured, the app
/// contains **16** `UserDefaults.standard` references outside comments, of which
/// after #1689 **14** are ordinary reads in managers, models and the menu-bar
/// controller — none of them a view, none reachable by any pin — so the rule
/// would arrive with 14 exemptions, which is a list rather than a rule.
///
/// **A fourth rule, narrowed to where a read IS a defect: `Irrlicht/Views/`.**
/// #1689 is the read half of #1672 — `reconcileNotificationsMasterDefault()`
/// DECIDED from `UserDefaults.standard.object(forKey:)` and wrote through
/// `@AppStorage`, so under a pinned render the guard consulted the machine while
/// the write landed in the store the host supplied. No mutation rule can see
/// that, because the offending statement is a read. What makes the narrowing
/// work rather than arbitrary is the measurement: `Irrlicht/Views/` is exactly
/// the set of files declaring a SwiftUI `View` (14 files; `grep` for a `: View`
/// conformance finds none anywhere else in the app target), it is the code
/// `PinnedSnapshotHost` pins, and it held exactly **two** references to the
/// process domain — `SettingsView.swift:697` and `:710`, both reads, both
/// removed by #1689 — so this rule carries no exemption list either.
///
/// The rule bans the RECEIVER, not a set of accessors, so a read, a mutation and
/// a bare `UserDefaults.standard` handed to a function that takes a store are
/// one rule. In a view every one of them is the same defect: a value taken from
/// the machine where the equivalent value was available from the INPUT.
/// `@AppStorage` — including the optional form, which is how #1689 expresses
/// "absent" — honours `.defaultAppStorage(_:)`, and a view that needs a whole
/// store takes one (`SessionManager.init(defaults:)`).
///
/// Its declared limit is the same one a directory scan always has: a view
/// calling a helper that reads the process domain for it is invisible here
/// (`MenuBarAppearance` and `ContextPressureThreshold` both contain such reads).
/// That is a false NEGATIVE, so it cannot make the rule wrong — only incomplete
/// — and closing it would need a call-graph, which is what the behavioural half
/// (`PinnedAppStorageSnapshotTests`) covers for the sites that matter.
///
/// There is deliberately **no exemption list** for any of the four rules, for
/// the reason `core/architecture_hookbody_test.go` gives: a test that genuinely
/// needs a raw suite, or a genuine write to the process domain, or a view that
/// genuinely cannot take its store, amends this rule in a reviewable diff.
final class PersistentDefaultsLintTests: XCTestCase {

    // MARK: - The rule, as a pure function

    /// Assembled from two pieces so this file's own source never contains a
    /// contiguous match. Every corpus fixture below is assembled the same way.
    /// Without that, the scan would flag its own test data — and the fix for
    /// *that* is an exclusion, which is a hole in the rule rather than a
    /// property of it.
    private static let constructor = "UserDefaults" + "("

    private static let pattern = "UserDefaults" + #"\s*\(\s*suiteName\s*:"#

    /// The `UserDefaults.standard` mutators, longest-first so `setValue` is not
    /// consumed by `set`. Every entry either writes the domain or names it to
    /// `cfprefsd`; `register` is included because a registration domain is
    /// process-wide state a later test then reads.
    private static let mutators = [
        "removePersistentDomain", "setPersistentDomain", "removeObject",
        "removeSuite", "setValue", "register", "addSuite", "set"
    ]

    private static let mutationPattern =
        "UserDefaults" + #"\s*\.\s*standard\s*\.\s*(?:"#
        + mutators.joined(separator: "|") + #")\s*\("#

    /// Any reference to the process preference domain at all — read, mutation or
    /// the bare receiver passed along. Applied only to the view sources (#1689).
    private static let processDomainPattern = "UserDefaults" + #"\s*\.\s*standard"#

    private static let testTargetDirectories = ["Tests", "TestsHarness"]

    /// The app target. One directory, and the walk below fails loudly if it is
    /// not there — a renamed target must not read as a clean tree.
    private static let appTargetDirectories = ["Irrlicht"]

    /// The view sources — measured to be exactly the files declaring a SwiftUI
    /// `View`, which is what makes the fourth rule's narrowing a property rather
    /// than a preference. See the type's doc comment.
    private static let viewTargetDirectories = ["Irrlicht/Views"]

    /// 1-based line numbers of every offending construction in `source`.
    ///
    /// Line comments are stripped first, padded with spaces so that offsets —
    /// and therefore reported line numbers — survive the strip. Block comments
    /// and string literals are **not** stripped, and that is a decision rather
    /// than an omission: a line-based scan cannot tell a literal from a call,
    /// and AGENTS.md's rule is that a validator which cannot parse its input
    /// checks MORE, never less. Both limits are pinned in the corpus.
    static func offendingLines(in source: String) -> [Int] {
        lines(matching: pattern, in: source)
    }

    /// 1-based line numbers of every `UserDefaults.standard` MUTATION in
    /// `source`. Same comment handling, same declared limits.
    static func mutatingLines(in source: String) -> [Int] {
        lines(matching: mutationPattern, in: source)
    }

    /// 1-based line numbers of every reference to `UserDefaults.standard` in
    /// `source`, whatever is done with it (#1689). Same comment handling, same
    /// declared limits.
    static func processDomainLines(in source: String) -> [Int] {
        lines(matching: processDomainPattern, in: source)
    }

    private static func lines(matching pattern: String, in source: String) -> [Int] {
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
            "a process-domain write is out of scope for THIS rule — it mints no "
                + "new domain and no new file; the sibling rule below is what covers it",
            "\(processDomain).set(true, forKey: \"k\")",
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

    // MARK: - Committed mutation evidence for the mutation rule (#1662)

    /// Assembled the same way as `constructor`, for the same reason: a
    /// contiguous match in this file's own source would make the rule flag its
    /// own test data.
    private static let processDomain = "UserDefaults" + ".standard"

    /// One row per spelling, pinned to the verdict `mutatingLines` must return.
    ///
    /// The `want: []` rows are where the value is. `object(forKey:)` and
    /// `string(forKey:)` are the reads a test uses to PROVE the domain was not
    /// touched, so a rule that flagged them would ban the evidence along with
    /// the defect; `standardDefaults.set` and a same-named method on another
    /// receiver are the false positives a bare `.set(` rule produces; and
    /// `InMemoryDefaults().set` is the construct every converted suite now uses.
    private static let mutationCorpus: [(name: String, source: String, want: [Int])] = [
        (
            "a write to the process domain is the whole point of the rule",
            "\(processDomain).set(false, forKey: \"showCostDisplay\")",
            [1]
        ),
        (
            "so is the removal half of a snapshot-and-restore tearDown",
            "\(processDomain).removeObject(forKey: \"debugMode\")",
            [1]
        ),
        (
            "the KVC spelling does not evade it",
            "\(processDomain).setValue(1, forKey: \"k\")",
            [1]
        ),
        (
            "setValue is not swallowed by the shorter `set` alternative",
            "\(processDomain).setValue(1, forKey: \"k\")\n\(processDomain).set(1, forKey: \"k\")",
            [1, 2]
        ),
        (
            "naming a domain to cfprefsd counts, since that call schedules a write-back",
            "\(processDomain).removePersistentDomain(forName: \"x\")",
            [1]
        ),
        (
            "register seeds process-wide state a later test reads",
            "\(processDomain).register(defaults: [:])",
            [1]
        ),
        (
            "whitespace around the dots and the paren does not evade it",
            "UserDefaults" + " . standard . set (true, forKey: \"k\")",
            [1]
        ),
        (
            "every occurrence is reported, not only the first",
            "\(processDomain).set(1, forKey: \"a\")\nlet unrelated = 1\n\(processDomain).set(2, forKey: \"b\")",
            [1, 3]
        ),
        (
            "a READ is the assertion that proves the domain was untouched, and stays legal",
            "let before = \(processDomain).object(forKey: \"summaryDisplayMode\")",
            []
        ),
        (
            "so does every other read accessor",
            "if \(processDomain).string(forKey: \"k\") == nil { }",
            []
        ),
        (
            "the store every converted suite now writes is not the process domain",
            "InMemoryDefaults().set(true, forKey: \"k\")",
            []
        ),
        (
            "an injected store held in a property is not the process domain either",
            "defaults.set(80, forKey: ContextPressureThreshold.valueKey)",
            []
        ),
        (
            "a similarly named receiver is not UserDefaults.standard",
            "standardDefaults.set(true, forKey: \"k\")",
            []
        ),
        (
            "a line comment naming the construct is documentation, not a call",
            "// never write \(processDomain).set(...) in a test",
            []
        ),
        (
            "LIMIT: a string literal holding the construct is flagged, because a line scan cannot tell it from a call",
            "let needle = \"\(processDomain).set(\"",
            [1]
        )
    ]

    func testMutationScanReturnsThePinnedVerdictForEverySpelling() {
        for row in Self.mutationCorpus {
            XCTAssertEqual(
                Self.mutatingLines(in: row.source), row.want,
                "\(row.name)\n---\n\(row.source)\n---"
            )
        }
    }

    /// The vacuity guard for the live mutation scan: after #1662 the construct
    /// appears nowhere in the test targets, so "no offenders" and "the rule
    /// stopped matching anything" are otherwise the same output.
    func testTheMutationCorpusContainsBothVerdicts() {
        XCTAssertFalse(Self.mutationCorpus.filter { $0.want.isEmpty }.isEmpty, "no must-not-flag rows")
        XCTAssertFalse(Self.mutationCorpus.filter { !$0.want.isEmpty }.isEmpty, "no must-flag rows")
    }

    // MARK: - Committed mutation evidence for the view rule (#1689)

    /// One row per spelling, pinned to the verdict `processDomainLines` must
    /// return.
    ///
    /// The first three rows are where this rule differs from the mutation rule
    /// above and are the whole reason it exists: in a view a READ is a defect,
    /// so it is flagged here and must stay legal there — asserted in both
    /// directions below, because a "narrower rule" that turned out to be the
    /// same rule would be indistinguishable from coverage. The `want: []` rows
    /// carry the rest of the value: the `@AppStorage` declaration and the
    /// injected store are the seams that REPLACE the construct, so a rule that
    /// flagged either would ban its own fix.
    private static let viewCorpus: [(name: String, source: String, want: [Int])] = [
        (
            "a READ is the whole point of THIS rule — #1689's guard was one",
            "guard \(processDomain).object(forKey: k) == nil else { return }",
            [1]
        ),
        (
            "so is the coercing read the same view used for anyEventEnabled",
            "let any = allCases.contains { \(processDomain).bool(forKey: $0.enabledKey) }",
            [1]
        ),
        (
            "handing the bare receiver to something that takes a store counts too — "
                + "that is the shape a half-done store-threading refactor produces",
            "notificationsEnabled = NotificationSettings.masterEnabled(defaults: \(processDomain))",
            [1]
        ),
        (
            "a mutation is flagged here as well: the two rules overlap in a view on purpose",
            "\(processDomain).set(true, forKey: \"k\")",
            [1]
        ),
        (
            "whitespace around the dot does not evade it",
            "UserDefaults" + " . standard .object(forKey: \"k\")",
            [1]
        ),
        (
            "every occurrence is reported, not only the first",
            "let a = \(processDomain).bool(forKey: \"a\")\nlet unrelated = 1\nlet b = \(processDomain).bool(forKey: \"b\")",
            [1, 3]
        ),
        (
            "the seam that replaces it — an @AppStorage declaration names no store",
            "@AppStorage(NotificationSettings.masterEnabledKey) private var master: Bool?",
            []
        ),
        (
            "nor does the optional form #1689 uses to express ABSENT",
            "guard storedNotificationsMaster == nil else { return }",
            []
        ),
        (
            "a view that takes a whole store is reading its INPUT, not the machine",
            "SessionManager(defaults: defaults).summaryDisplayMode",
            []
        ),
        (
            "a similarly named receiver is not UserDefaults.standard",
            "standardDefaults.object(forKey: \"k\")",
            []
        ),
        (
            "a line comment naming the construct is documentation, not a call",
            "// #1689: this used to read \(processDomain).object(forKey:)",
            []
        ),
        (
            "a doc comment naming it is documentation too",
            "/// The read this replaced was \(processDomain).object(forKey:).",
            []
        ),
        (
            "LIMIT: a string literal holding the construct is flagged, because a line scan cannot tell it from a call",
            "let needle = \"\(processDomain)\"",
            [1]
        ),
        (
            "LIMIT: a block comment holding the construct is flagged, for the same reason",
            "/* \(processDomain).object(forKey: \"k\") */",
            [1]
        )
    ]

    func testViewScanReturnsThePinnedVerdictForEverySpelling() {
        for row in Self.viewCorpus {
            XCTAssertEqual(
                Self.processDomainLines(in: row.source), row.want,
                "\(row.name)\n---\n\(row.source)\n---"
            )
        }
    }

    /// The vacuity guard for the live view scan: after #1689 the construct
    /// appears nowhere under `Irrlicht/Views/`, so "no offenders" and "the rule
    /// stopped matching anything" are otherwise the same output.
    func testTheViewCorpusContainsBothVerdicts() {
        XCTAssertFalse(Self.viewCorpus.filter { $0.want.isEmpty }.isEmpty, "no must-not-flag rows")
        XCTAssertFalse(Self.viewCorpus.filter { !$0.want.isEmpty }.isEmpty, "no must-flag rows")
    }

    /// The two rules are DIFFERENT rules, asserted rather than assumed.
    ///
    /// Without this, a view rule that had silently been written as the mutation
    /// rule — or a `mutators` list that grew `object` — would pass every row
    /// above and every live scan, while the reads #1689 is about went unflagged
    /// in views and the reads #1662 deliberately protects went flagged
    /// everywhere. Both directions are named because each is one edit away.
    func testTheViewRuleAndTheMutationRuleDisagreeAboutReads() {
        let read = "let before = \(Self.processDomain).object(forKey: \"summaryDisplayMode\")"
        XCTAssertEqual(Self.processDomainLines(in: read), [1],
                       "the view rule stopped flagging a read — it has collapsed into the "
                       + "mutation rule and #1689's defect is invisible to it")
        XCTAssertEqual(Self.mutatingLines(in: read), [],
                       "the mutation rule started flagging a read — that bans the assertion "
                       + "a test uses to prove the process domain was untouched (#1662)")
    }

    // MARK: - The live scan

    /// Walks every Swift source in the test targets, applying `rule` to each,
    /// and returns `file:line` for every hit plus how many files were read.
    private func scanTestTargets(_ rule: (String) -> [Int]) throws -> (offenders: [String], filesScanned: Int) {
        try scan(Self.testTargetDirectories, rule)
    }

    /// Walks every Swift source under `directories`, applying `rule` to each,
    /// and returns `file:line` for every hit plus how many files were read.
    ///
    /// Parameterised by directory rather than duplicated per target, so the app
    /// scan #1672 adds cannot drift from the test scan it shares a rule with.
    private func scan(_ directories: [String],
                      _ rule: (String) -> [Int]) throws -> (offenders: [String], filesScanned: Int) {
        var offenders: [String] = []
        var filesScanned = 0

        let macosRoot = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()        // Tests/
            .deletingLastPathComponent()        // platforms/macos/

        for directory in directories {
            let root = macosRoot.appendingPathComponent(directory)
            var isDirectory: ObjCBool = false
            guard FileManager.default.fileExists(atPath: root.path, isDirectory: &isDirectory),
                  isDirectory.boolValue else {
                // A renamed or removed target must be loud, not silent: a walk
                // over a directory that is not there finds nothing and is
                // indistinguishable from a clean tree.
                XCTFail("target directory \(directory) is missing at \(root.path)")
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
                offenders.append(contentsOf: rule(source).map { "\(relative):\($0)" })
            }
        }
        return (offenders, filesScanned)
    }

    func testNoTestConstructsAPersistentUserDefaultsSuite() throws {
        let (offenders, filesScanned) = try scanTestTargets(Self.offendingLines(in:))

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

    func testNoTestMutatesTheProcessPreferenceDomain() throws {
        let (offenders, filesScanned) = try scanTestTargets(Self.mutatingLines(in:))

        XCTAssertGreaterThan(filesScanned, 0, "the scan read no Swift files — it checked nothing")
        XCTAssertEqual(
            offenders, [],
            """
            A test mutates UserDefaults.standard, which under `swift test` is \
            the developer's real com.apple.dt.xctest.tool domain. Take an \
            `InMemoryDefaults` instead — `PinnedSnapshotHost(…, defaults:)` for \
            an `@AppStorage` read, `SessionManager(defaults:)` for the \
            preferences it owns — so there is nothing to restore in a `tearDown` \
            that an aborted run (#1523), a 240s tree kill or an exhausted \
            `--budget` never reaches (#1662). Reads are fine.

            \(offenders.joined(separator: "\n"))
            """
        )
    }

    /// #1672: the same rule over the APP target — the half a test-source scan
    /// is structurally blind to, because the write it missed was a production
    /// line a test merely *drove*.
    func testNoAppSourceMutatesTheProcessPreferenceDomain() throws {
        let (offenders, filesScanned) = try scan(Self.appTargetDirectories, Self.mutatingLines(in:))

        XCTAssertGreaterThan(filesScanned, 0, "the scan read no Swift files — it checked nothing")
        XCTAssertEqual(
            offenders, [],
            """
            App code mutates UserDefaults.standard. In the app that is the \
            user's real io.irrlicht.app domain, so a write reachable from a \
            render persists a preference they never set; under `swift test` the \
            same line writes the developer's com.apple.dt.xctest.tool domain, \
            which is where #1672 was found and which no scan of the TEST \
            sources can see. Persist through a seam that a test can pin \
            instead: `@AppStorage`, which honours `.defaultAppStorage(_:)`, or \
            an injected `UserDefaults` (`SessionManager.init(defaults:)`). \
            Reads are fine.

            \(offenders.joined(separator: "\n"))
            """
        )
    }

    /// #1689: no VIEW touches the process preference domain at all — the half
    /// the three rules above are blind to, because the offending statement is a
    /// read and reads are legal everywhere else.
    func testNoViewSourceResolvesTheProcessPreferenceDomain() throws {
        let (offenders, filesScanned) = try scan(Self.viewTargetDirectories, Self.processDomainLines(in:))

        XCTAssertGreaterThan(filesScanned, 0, "the scan read no Swift files — it checked nothing")
        XCTAssertEqual(
            offenders, [],
            """
            A view resolves UserDefaults.standard. In a view that is a value read \
            from the MACHINE where the same value is available from the INPUT: \
            `PinnedSnapshotHost` pins `.defaultAppStorage(_:)`, so a rendered view \
            reads the preferences it is GIVEN — but only through a seam that \
            honours it. A READ is enough to break that, which is why this rule is \
            not the mutation rule above: #1689's \
            `reconcileNotificationsMasterDefault()` DECIDED from this domain and \
            wrote through `@AppStorage`, so under a pinned render the guard \
            consulted the developer's com.apple.dt.xctest.tool while the write \
            landed in the store the host supplied. Use `@AppStorage` — the \
            optional form (`Bool?`) expresses an ABSENT key, which is what that \
            guard needed — or take a `UserDefaults` the way \
            `SessionManager.init(defaults:)` does.

            \(offenders.joined(separator: "\n"))
            """
        )
    }
}
