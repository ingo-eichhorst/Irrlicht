import XCTest

/// Coverage for #1661's mechanism, in three arms.
///
/// 1. **Faithfulness** — the double answers what a real suite answers for
///    everything `NotificationSettings.masterEnabled(defaults:)` reads. A double
///    that is diskless but wrong would make `NotificationMasterGateTests` assert
///    nothing.
/// 2. **The guarantee** — a run of the double adds no file to the real
///    `~/Library/Preferences`.
/// 3. **The witness's own mutation evidence** — arm 2 is only worth its ink if
///    its detector can fire. `PreferencesDirectoryWitness` is driven against
///    temporary directories this test creates, with the "it must stay silent"
///    cases beside the "it must report" ones, because a witness that reported
///    unconditionally would satisfy arm 2 forever.
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

    /// The property the whole issue is about: a run of the double leaves the
    /// real `~/Library/Preferences` with exactly the files it started with.
    ///
    /// Driven hard on purpose — many keys, every typed setter, `synchronize()`,
    /// the domain calls the pre-fix `tearDown` used, and a settle after each —
    /// because the write-back this replaces was asynchronous and arrived late.
    /// This cannot catch a write that lands after *this process* exits; nothing
    /// running inside the process can. What makes that acceptable is that there
    /// is no daemon-facing call left to schedule one: `InMemoryDefaults` names
    /// no suite, and `PersistentDefaultsLintTests` fails the build if any test
    /// does.
    func testTheDoubleAddsNothingToTheRealPreferencesDirectory() throws {
        let witness = PreferencesDirectoryWitness.realUserPreferences
        let before = try witness.snapshot()

        XCTAssertFalse(
            before.isEmpty,
            "the witness saw an empty \(witness.directory.path) — a delta measured against " +
            "nothing is not evidence; check that this is the real preferences directory"
        )

        for round in 0..<3 {
            let defaults = InMemoryDefaults()
            for index in 0..<50 {
                defaults.set(true, forKey: "io.irrlicht.tests.leakProbe.\(round).\(index)")
                defaults.set("value", forKey: "io.irrlicht.tests.leakProbeString.\(round).\(index)")
                defaults.set(index, forKey: "io.irrlicht.tests.leakProbeInt.\(round).\(index)")
            }
            defaults.register(defaults: ["io.irrlicht.tests.leakProbeRegistered": true])
            _ = defaults.synchronize()
            // The two calls the pre-fix teardown made, which are also the two
            // observed to schedule a cfprefsd write-back.
            defaults.removePersistentDomain(forName: "io.irrlicht.tests.leakProbe.\(round)")
            _ = defaults.synchronize()
        }

        // Give an asynchronous write-back a chance to land before measuring.
        Thread.sleep(forTimeInterval: 0.5)

        let added = PreferencesDirectoryWitness.added(from: before, to: try witness.snapshot())
        XCTAssertEqual(
            added, [],
            """
            The defaults double reached disk. #1661 is back: files appeared in \
            \(witness.directory.path) during this test, and nothing in this \
            repository is allowed to delete them again.

            \(added.joined(separator: "\n"))
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
