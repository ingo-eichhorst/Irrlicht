import XCTest

/// Coverage for #1661's mechanism, in four arms.
///
/// 1. **Faithfulness** — the double answers what a real suite answers for
///    everything `NotificationSettings.masterEnabled(defaults:)` reads. A double
///    that is diskless but wrong would make `NotificationMasterGateTests` assert
///    nothing.
/// 2. **The guarantee** — a run of the double leaves nothing **of its own** in
///    the real `~/Library/Preferences`. "Of its own" is #1714: the assertion
///    used to be the raw before/after delta, which on a GitHub runner reported
///    an OS-written Siri plist as "the defaults double reached disk".
/// 3. **The witness's own mutation evidence** — arm 2 is only worth its ink if
///    its detector can fire. `PreferencesDirectoryWitness` is driven against
///    temporary directories this test creates, with the "it must stay silent"
///    cases beside the "it must report" ones, because a witness that reported
///    unconditionally would satisfy arm 2 forever.
/// 4. **The attribution's mutation evidence** (#1714) — seeing a file appear
///    and knowing whose it is are different claims, and only the second is what
///    arm 2 now asserts. A committed corpus pins both verdicts, including the
///    measured #1714 failure as a row that must stay silent and the shape's
///    declared LIMIT as another, plus one end-to-end run of the whole pipeline
///    against #1661's actual artifact.
final class InMemoryDefaultsTests: XCTestCase {

    // MARK: - Arm 1: faithfulness

    /// The exact read `NotificationSettings.masterEnabled(defaults:)` performs:
    /// `object(forKey:) as? Bool`, which must distinguish absent from
    /// explicitly-false. `bool(forKey:)` cannot, which is why production does
    /// not use it for the master key — so a double that collapsed the two would
    /// silently retire `testAbsentKeyIsDistinguishableFromExplicitFalse`.
    func testAbsentExplicitTrueAndExplicitFalseAreThreeDistinctAnswers() {
        let defaults = InMemoryDefaults()

        XCTAssertNil(defaults.object(forKey: "k"))
        XCTAssertFalse(defaults.bool(forKey: "k"))

        defaults.set(true, forKey: "k")
        XCTAssertEqual(defaults.object(forKey: "k") as? Bool, true)
        XCTAssertTrue(defaults.bool(forKey: "k"))

        defaults.set(false, forKey: "k")
        XCTAssertNotNil(defaults.object(forKey: "k"))
        XCTAssertEqual(defaults.object(forKey: "k") as? Bool, false)
        XCTAssertFalse(defaults.bool(forKey: "k"))

        defaults.removeObject(forKey: "k")
        XCTAssertNil(defaults.object(forKey: "k"))
    }

    /// Every derived accessor funnels through the overridden primitive. Pinned
    /// rather than assumed: the type's guarantee is that no write reaches the
    /// superclass, and a derived accessor that did not funnel would read a real
    /// domain while the writes sat in the dictionary.
    func testDerivedAccessorsReadThroughTheOverriddenPrimitive() {
        let defaults = InMemoryDefaults()

        defaults.set("s", forKey: "string")
        defaults.set(7, forKey: "int")
        defaults.set(1.5, forKey: "double")
        defaults.set(Float(2.5), forKey: "float")
        defaults.set([1, 2, 3], forKey: "array")
        defaults.set(["a": 1], forKey: "dictionary")
        defaults.set(Data([0x01]), forKey: "data")
        defaults.set(URL(fileURLWithPath: "/tmp/x"), forKey: "url")  // NOSONAR (swift:S1075) — test fixture value

        XCTAssertEqual(defaults.string(forKey: "string"), "s")
        XCTAssertEqual(defaults.integer(forKey: "int"), 7)
        XCTAssertEqual(defaults.double(forKey: "double"), 1.5)
        XCTAssertEqual(defaults.float(forKey: "float"), 2.5)
        XCTAssertEqual(defaults.array(forKey: "array") as? [Int], [1, 2, 3])
        XCTAssertEqual(defaults.dictionary(forKey: "dictionary") as? [String: Int], ["a": 1])
        XCTAssertEqual(defaults.data(forKey: "data"), Data([0x01]))
        XCTAssertEqual(defaults.url(forKey: "url"), URL(fileURLWithPath: "/tmp/x"))  // NOSONAR (swift:S1075)
    }

    /// KVC is how `@AppStorage` writes. Routed to the primitive so it cannot
    /// slip past the store and land in the process's real application domain.
    func testKeyValueCodingWritesLandInTheStore() {
        let defaults = InMemoryDefaults()

        defaults.setValue("via-kvc", forKey: "kvcKey")

        XCTAssertEqual(defaults.object(forKey: "kvcKey") as? String, "via-kvc")
        XCTAssertEqual(defaults.value(forKey: "kvcKey") as? String, "via-kvc")
    }

    /// The registration domain loses to a written value and survives a removal
    /// of one, exactly as Foundation's does.
    func testRegisteredDefaultsAreVisibleButLoseToWrittenValues() {
        let defaults = InMemoryDefaults()
        defaults.register(defaults: ["reg": true])

        XCTAssertTrue(defaults.bool(forKey: "reg"))

        defaults.set(false, forKey: "reg")
        XCTAssertFalse(defaults.bool(forKey: "reg"))

        defaults.removeObject(forKey: "reg")
        XCTAssertTrue(defaults.bool(forKey: "reg"), "the registration must survive removing the written value")
    }

    /// Two doubles are two stores. A shared one would let tests leak state into
    /// each other, which is the other half of why the pre-fix code minted a
    /// fresh suite per test in the first place.
    func testInstancesDoNotShareState() {
        let a = InMemoryDefaults()
        let b = InMemoryDefaults()

        a.set(true, forKey: "shared")

        XCTAssertTrue(a.bool(forKey: "shared"))
        XCTAssertNil(b.object(forKey: "shared"))
    }

    /// The developer's own preferences are not read through and not written to.
    /// `super.init(suiteName: nil)` binds the process's real application domain,
    /// so "the overrides never reach super" has to be asserted, not assumed.
    func testTheProcessesRealStandardDefaultsAreUntouched() {
        let key = "io.irrlicht.tests.inMemoryDefaults.\(UUID().uuidString)"
        let defaults = InMemoryDefaults()

        defaults.set(true, forKey: key)

        XCTAssertNil(UserDefaults.standard.object(forKey: key))
        XCTAssertNil(defaults.persistentDomain(forName: "com.apple.finder"), "the double must not read a real domain, nor claim to be one")
    }

    // MARK: - Arm 2: the #1661 guarantee

    /// The property the whole issue is about: a run of the double leaves nothing
    /// **of its own** in the real `~/Library/Preferences`.
    ///
    /// Driven hard on purpose — many keys, every typed setter, `synchronize()`,
    /// the domain calls the pre-fix `tearDown` used, and a settle after each —
    /// because the write-back this replaces was asynchronous and arrived late.
    /// This cannot catch a write that lands after *this process* exits; nothing
    /// running inside the process can. What makes that acceptable is that there
    /// is no daemon-facing call left to schedule one: `InMemoryDefaults` names
    /// no suite, and `PersistentDefaultsLintTests` fails the build if any test
    /// does.
    ///
    /// **"Of its own" is #1714 and is the whole difference.** The assertion used
    /// to be the raw before/after delta, and on a runner the OS wrote
    /// `com.apple.siri.ODDI.MetricsWorker.plist` inside the window — reported
    /// here as "the double reached disk", which is a false accusation rather
    /// than a false alarm. Every identifier below now carries `runToken`, so an
    /// entry the double is answerable for is one the OS cannot produce; what
    /// that gives up, and what still covers it, is on
    /// `PreferencesDirectoryWitness`.
    func testTheDoubleLeavesNothingAttributableToItInTheRealPreferencesDirectory() throws {
        let witness = PreferencesDirectoryWitness.realUserPreferences
        let before = try witness.snapshot()

        XCTAssertFalse(
            before.isEmpty,
            "the witness saw an empty \(witness.directory.path) — a delta measured against " +
            "nothing is not evidence; check that this is the real preferences directory"
        )

        let runToken = UUID().uuidString
        var driven: InMemoryDefaults?
        for round in 0..<3 {
            let defaults = InMemoryDefaults()
            for index in 0..<50 {
                defaults.set(true, forKey: "io.irrlicht.tests.leakProbe.\(runToken).\(round).\(index)")
                defaults.set("value", forKey: "io.irrlicht.tests.leakProbeString.\(runToken).\(round).\(index)")
                defaults.set(index, forKey: "io.irrlicht.tests.leakProbeInt.\(runToken).\(round).\(index)")
            }
            defaults.register(defaults: ["io.irrlicht.tests.leakProbeRegistered.\(runToken)": true])
            _ = defaults.synchronize()
            // The two calls the pre-fix teardown made, which are also the two
            // observed to schedule a cfprefsd write-back. The domain NAME is
            // what `cfprefsd` would name a file after, so it carries the token.
            defaults.removePersistentDomain(forName: "io.irrlicht.tests.leakProbe.\(runToken).\(round)")
            _ = defaults.synchronize()
            driven = defaults
        }

        // The three vacuity guards, before the measurement rather than after:
        // a subject that was never driven, a token that never reached it, and a
        // process domain that could not be derived each produce "nothing
        // attributable appeared" while proving nothing at all.
        let exercised = try XCTUnwrap(driven, "the exercise loop built no double")
        XCTAssertEqual(exercised.writtenKeys.count, 150,
                       "the double was not driven — 150 keys were written through it, and it holds "
                       + "\(exercised.writtenKeys.count)")
        XCTAssertTrue(
            exercised.writtenKeys.allSatisfy { $0.contains(runToken) },
            "the exercise stopped weaving this run's token into the identifiers it hands the double, "
            + "so the attribution below is scoped to nothing this test names"
        )
        XCTAssertFalse(
            PreferencesDirectoryWitness.processApplicationDomains.isEmpty,
            "this process's application domain could not be derived, so a write reaching `super` "
            + "would be attributable to nothing"
        )

        // Give an asynchronous write-back a chance to land before measuring.
        Thread.sleep(forTimeInterval: 0.5)

        let appeared = PreferencesDirectoryWitness.added(from: before, to: try witness.snapshot())
        let attribution = try PreferencesDirectoryWitness.attribute(appeared, toRun: runToken)

        // Two lists, not one, because the two clauses support different claims
        // and a message that merged them would say more than the evidence does.
        XCTAssertEqual(
            attribution.names, [],
            """
            This test wrote into the real \(witness.directory.path), and nothing in this \
            repository is allowed to delete it again.

            Carrying this run's token (\(runToken)) — these can only be the DOUBLE's, \
            because nothing else in the world has that string. #1661 is back:
            \(attribution.namedForThisRun.isEmpty ? "  (none)" : attribution.namedForThisRun.map { "  \($0)" }.joined(separator: "\n"))

            Naming this process's own application domain \
            (\(PreferencesDirectoryWitness.processApplicationDomains.sorted().joined(separator: ", "))) \
            — a write that reached `super`. That is the double if one of its overrides stopped \
            overriding, and a #1672-class persist by something else this process rendered \
            otherwise; `PersistentDefaultsLintTests` and `InMemoryDefaults.writtenKeys` tell \
            those apart:
            \(attribution.namedForThisProcess.isEmpty ? "  (none)" : attribution.namedForThisProcess.map { "  \($0)" }.joined(separator: "\n"))

            \(attribution.unattributed.count) further entr(y/ies) appeared in the same window \
            and are attributable to neither. Background daemons write here too — measured at \
            roughly one plist per 218s of active use — so they are NOT part of this failure \
            (#1714). tools/lib/swift-suite.sh is what still watches that population.
            """
        )
    }

    // MARK: - Arm 3: the witness's committed mutation evidence
    //
    // Arm 2's assertion is only as good as its detector, and the faithful
    // mutation — making `InMemoryDefaults` persist — writes into the
    // developer's real `~/Library/Preferences`, which is the incident this
    // change exists to prevent. So the detector is driven here against
    // directories this test creates, with both verdicts pinned: a witness that
    // reported everything and one that reported correctly are
    // indistinguishable without the cases that must stay silent.

    func testWitnessReportsAFileThatAppeared() throws {
        let directory = try makeTemporaryDirectory()
        let witness = PreferencesDirectoryWitness(directory: directory)
        let before = try witness.snapshot()

        FileManager.default.createFile(atPath: directory.appendingPathComponent("leaked.plist").path, contents: Data())

        XCTAssertEqual(PreferencesDirectoryWitness.added(from: before, to: try witness.snapshot()), ["leaked.plist"])
    }

    func testWitnessReportsEveryFileThatAppeared() throws {
        let directory = try makeTemporaryDirectory()
        let witness = PreferencesDirectoryWitness(directory: directory)
        let before = try witness.snapshot()

        for name in ["b.plist", "a.plist"] {
            FileManager.default.createFile(atPath: directory.appendingPathComponent(name).path, contents: Data())
        }

        XCTAssertEqual(
            PreferencesDirectoryWitness.added(from: before, to: try witness.snapshot()),
            ["a.plist", "b.plist"]
        )
    }

    /// Must stay silent: nothing changed.
    func testWitnessIsSilentWhenNothingAppeared() throws {
        let directory = try makeTemporaryDirectory()
        FileManager.default.createFile(atPath: directory.appendingPathComponent("pre-existing.plist").path, contents: Data())
        let witness = PreferencesDirectoryWitness(directory: directory)
        let before = try witness.snapshot()

        XCTAssertEqual(PreferencesDirectoryWitness.added(from: before, to: try witness.snapshot()), [])
    }

    /// Must stay silent: a file that *went away* is not a leak, and reporting it
    /// would turn every unrelated `cfprefsd` tidy-up into a red suite.
    func testWitnessIsSilentWhenAFileDisappeared() throws {
        let directory = try makeTemporaryDirectory()
        let doomed = directory.appendingPathComponent("removed-by-someone-else.plist")
        FileManager.default.createFile(atPath: doomed.path, contents: Data())
        let witness = PreferencesDirectoryWitness(directory: directory)
        let before = try witness.snapshot()

        // Removing a file this test created, in a directory this test created.
        try FileManager.default.removeItem(at: doomed)

        XCTAssertEqual(PreferencesDirectoryWitness.added(from: before, to: try witness.snapshot()), [])
    }

    /// The fail-loud. A witness that cannot look must not answer "nothing
    /// changed": absence of a finding and inability to look would otherwise
    /// produce the same output, and arm 2 would pass forever on a typo in a
    /// path.
    func testWitnessThrowsRatherThanReportingNothingWhenItCannotRead() throws {
        let missing = try makeTemporaryDirectory().appendingPathComponent("does-not-exist", isDirectory: true)
        let witness = PreferencesDirectoryWitness(directory: missing)

        XCTAssertThrowsError(try witness.snapshot()) { error in
            guard case PreferencesDirectoryWitness.Failure.unreadable(let path, _) = error else {
                XCTFail("expected .unreadable, got \(error)")
                return
            }
            XCTAssertEqual(path, missing.path)
        }
    }

    // MARK: - Arm 4: the attribution's committed mutation evidence (#1714)
    //
    // Arm 3 proves the witness can SEE a file appear. That is a different claim
    // from "and it can tell whose it is", which is the claim #1714 turns on —
    // and the one that, got wrong in the permissive direction, waves through the
    // incident this file exists for. So the attribution gets its own corpus,
    // with both verdicts carried: an attribution that reported everything and
    // one that reported correctly are indistinguishable without the rows that
    // must stay silent, and here those rows are ALSO the declared limit of the
    // shape #1714 chose. A limit learned from a test is one nobody has to
    // rediscover from an incident.

    /// One window's worth of appearances, and the verdict the attribution owes
    /// for it.
    private struct AttributionCase {
        let what: String
        let appeared: [String]
        let mustReport: [String]
    }

    /// The measured #1714 failure, and the background churn
    /// `tools/lib/swift-suite.sh` measured over an 870s window of ordinary
    /// interactive use. Committed as fixtures rather than described, so the
    /// evidence outlives the PR that produced it.
    private static let churnFixtures = [
        "com.apple.siri.ODDI.MetricsWorker.plist",
        "com.apple.bluetooth.plist",
        "com.apple.DuetExpertCenter.MagicalMoments.plist",
        "com.apple.voicetrigger.plist",
        "com.googlecode.iterm2.plist",
    ]

    /// One row per shape, both verdicts carried. Lifted into its own table for
    /// the reason `guardedConstructions()` is one in
    /// `core/application/services/construction_test.go`: a corpus grows a row
    /// at a time, and a row is easier to add to a table than to a method.
    private func attributionCorpus(runToken: String,
                                   processDomain: String,
                                   foreignUUID: String) -> [AttributionCase] {
        let leak1661 = "io.irrlicht.tests.NotificationMasterGate.\(runToken).plist"
        let probeDomain = "io.irrlicht.tests.leakProbe.\(runToken).round0.plist"
        return [
            .init(what: "#1661's own artifact — the empty plist cfprefsd left behind for a UUID "
                      + "suite the test minted",
                  appeared: [leak1661],
                  mustReport: [leak1661]),
            .init(what: "#1661's leaked files as the issue names them — a bare <uuid>.plist",
                  appeared: ["\(runToken).plist"],
                  mustReport: ["\(runToken).plist"]),
            .init(what: "the domain this test itself names when it drives removePersistentDomain",
                  appeared: [probeDomain],
                  mustReport: [probeDomain]),
            .init(what: "a spelling that merely CONTAINS the minted identifier — a suffixed or "
                      + "temporary form is not a way past the guard, which is why the match is "
                      + "containment and not equality",
                  appeared: ["\(probeDomain).tmp"],
                  mustReport: ["\(probeDomain).tmp"]),
            .init(what: "a write that reached `super` — this process's own application domain, "
                      + "which is what init(suiteName: nil) binds",
                  appeared: ["\(processDomain).plist"],
                  mustReport: ["\(processDomain).plist"]),
            .init(what: "the leak and #1714's churn in the SAME window — the discriminator itself, "
                      + "which is the row every other row exists to support",
                  appeared: [leak1661, "com.apple.siri.ODDI.MetricsWorker.plist"],
                  mustReport: [leak1661]),

            .init(what: "#1714 itself: the Siri metrics plist a GitHub runner wrote inside the "
                      + "window, reported as 'the defaults double reached disk'",
                  appeared: ["com.apple.siri.ODDI.MetricsWorker.plist"],
                  mustReport: []),
            .init(what: "the background churn swift-suite.sh measured over an 870s window",
                  appeared: Self.churnFixtures,
                  mustReport: []),
            .init(what: "THE DECLARED LIMIT: a <uuid>.plist — #1661's exact shape — bearing a UUID "
                      + "this run did not mint is NOT reported. That is what attribution gives up, "
                      + "and what swift-suite.sh's unattributed net still watches for",
                  appeared: ["\(foreignUUID).plist"],
                  mustReport: []),
            .init(what: "the vacuity guard: nothing appeared, so nothing is reported and nothing "
                      + "is ignored",
                  appeared: [],
                  mustReport: []),
        ]
    }

    func testAttributionReportsEveryLeakShapeAndStaysSilentOnEveryChurnShape() throws {
        let runToken = UUID().uuidString
        let domains = PreferencesDirectoryWitness.processApplicationDomains
        let processDomain = try XCTUnwrap(
            domains.sorted().first,
            "this process's application domain could not be derived, so the clause that catches a "
            + "write reaching `super` is graded by nothing below"
        )
        // A UUID this run minted for NOTHING — it names no domain, no key, and
        // is handed to no subject. It stands in for #1661's leaked filenames as
        // produced by somebody else's process.
        let foreignUUID = UUID().uuidString
        XCTAssertNotEqual(foreignUUID, runToken)

        // A premise, not decoration: if a derived process domain ever appeared
        // inside one of the churn fixtures the silent rows would go red for a
        // reason unrelated to what they grade — and that condition is exactly
        // "the derivation became too generic to be an attribution", which is
        // worth failing on in its own right.
        for name in Self.churnFixtures {
            for domain in domains {
                XCTAssertFalse(
                    name.contains(domain),
                    "the derived process domain '\(domain)' occurs inside the churn fixture "
                    + "'\(name)' — that derivation cannot attribute anything"
                )
            }
        }

        for probe in attributionCorpus(runToken: runToken,
                                       processDomain: processDomain,
                                       foreignUUID: foreignUUID) {
            let attribution = try PreferencesDirectoryWitness.attribute(probe.appeared, toRun: runToken)
            XCTAssertEqual(attribution.names, probe.mustReport.sorted(), probe.what)
            XCTAssertEqual(
                attribution.unattributed,
                probe.appeared.filter { !probe.mustReport.contains($0) }.sorted(),
                "\(probe.what) — the entries it must NOT report have to be accounted for, not "
                + "silently dropped: a count is what tells the reader the window was noisy"
            )
        }
    }

    /// The whole pipeline, driven through the same three calls arm 2 makes,
    /// against a directory this test created.
    ///
    /// Never the real home: the faithful mutation — making `InMemoryDefaults`
    /// persist — writes into the developer's own `~/Library/Preferences`, which
    /// is the incident this file exists to prevent, and there is no version of
    /// that experiment worth its blast radius.
    func testThePipelineReportsAn1661LeakWhileTheOSWritesInTheSameWindow() throws {
        let directory = try makeTemporaryDirectory()
        let witness = PreferencesDirectoryWitness(directory: directory)
        FileManager.default.createFile(
            atPath: directory.appendingPathComponent("pre-existing.plist").path, contents: Data())
        let before = try witness.snapshot()
        XCTAssertFalse(before.isEmpty, "a delta measured against nothing is not evidence")

        let runToken = UUID().uuidString
        // #1661's artifact as it actually was: an EMPTY plist named for the
        // UUID suite the test minted. Empty is load-bearing — the file carries
        // none of the values that were written, so only its name can attribute
        // it, which is why the name clause exists at all.
        let leak = "io.irrlicht.tests.NotificationMasterGate.\(runToken).plist"
        FileManager.default.createFile(
            atPath: directory.appendingPathComponent(leak).path, contents: Data())
        // …and #1714, in the same window.
        let churn = "com.apple.siri.ODDI.MetricsWorker.plist"
        FileManager.default.createFile(
            atPath: directory.appendingPathComponent(churn).path, contents: Data())

        let appeared = PreferencesDirectoryWitness.added(from: before, to: try witness.snapshot())
        XCTAssertEqual(
            appeared, [churn, leak].sorted(),
            "the witness did not see both files appear, so neither verdict below is evidence"
        )

        let attribution = try PreferencesDirectoryWitness.attribute(appeared, toRun: runToken)
        XCTAssertEqual(attribution.namedForThisRun, [leak],
                       "the guard no longer goes red against #1661's actual failure")
        XCTAssertEqual(attribution.unattributed, [churn],
                       "#1714's churn must be counted, and must not be part of the accusation")
    }

    /// The vacuity guard for the second attribution clause. A derivation that
    /// answered nothing would retire it in silence, and a write that reached
    /// `super` would then be attributable to nobody.
    func testThisProcessesOwnApplicationDomainIsDerivableAndAttributes() throws {
        let domains = PreferencesDirectoryWitness.processApplicationDomains
        XCTAssertFalse(domains.isEmpty, "no application domain could be derived for this process")
        for domain in domains {
            XCTAssertFalse(domain.isEmpty)
            XCTAssertFalse(domain.contains("/"), "'\(domain)' is a path, not a preference domain")
            XCTAssertGreaterThan(
                domain.count, 3,
                "'\(domain)' is short enough that containment would match unrelated names"
            )
        }

        let domain = try XCTUnwrap(domains.sorted().first)
        let attribution = try PreferencesDirectoryWitness.attribute(
            ["\(domain).plist"], toRun: UUID().uuidString)
        XCTAssertEqual(
            attribution.namedForThisProcess, ["\(domain).plist"],
            "the derived domain does not match the file cfprefsd would name for it"
        )
    }

    /// The fail-loud for the attribution itself, and it is not defensive noise:
    /// `"anything".contains("")` is **true** in Swift, so an empty needle does
    /// not narrow the guard — it silently restores the unattributed net #1714
    /// removed, reporting every OS write as the subject's, and reads as a pass
    /// until the day it fires.
    /// Each arm names a fragment of its OWN refusal, because "it refused" and
    /// "it refused for this reason" are different claims and all three guards
    /// throw the same case — without the fragment, one guard could cover for
    /// another's removal and the arms could not tell them apart.
    func testAttributionRefusesAnInputThatWouldMakeItMatchEverythingOrNothing() {
        let appeared = ["com.apple.siri.ODDI.MetricsWorker.plist"]

        for (what, naming, call) in [
            ("an empty run token", "the run token is empty",
             { try PreferencesDirectoryWitness.attribute(appeared, toRun: "") }),
            ("an empty process domain", "an application domain is the empty string", {
                try PreferencesDirectoryWitness.attribute(appeared, toRun: "t", orProcessDomains: [""])
            }),
            ("no process domain at all", "could not be derived", {
                try PreferencesDirectoryWitness.attribute(appeared, toRun: "t", orProcessDomains: [])
            }),
        ] as [(String, String, () throws -> PreferencesDirectoryWitness.Attribution)] {
            XCTAssertThrowsError(try call(), what) { error in
                guard case PreferencesDirectoryWitness.Failure.cannotAttribute = error else {
                    XCTFail("\(what): expected .cannotAttribute, got \(error)")
                    return
                }
                XCTAssertTrue(
                    "\(error)".contains(naming),
                    "\(what): refused, but not for its own reason — '\(naming)' is absent from "
                    + "\"\(error)\", so this arm cannot tell its guard from another one's"
                )
            }
        }
    }

    // MARK: - Helpers

    /// A directory this test creates, so that the one `removeItem` above is
    /// provably scoped to something it made itself. Cleanup is XCTest's, via
    /// the temporary directory it hands out; nothing here sweeps a shared one.
    private func makeTemporaryDirectory() throws -> URL {
        let url = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("io.irrlicht.tests.witness.\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.removeItem(at: url) }
        return url
    }
}
