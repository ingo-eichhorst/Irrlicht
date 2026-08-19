import Foundation

/// A `UserDefaults` that never reaches disk, and the read-only witness that
/// proves it.
///
/// # What #1661 is
///
/// `NotificationMasterGateTests` minted a fresh `UserDefaults(suiteName:)` per
/// test with a UUID name and called `removePersistentDomain(forName:)` in
/// `tearDown`. That call empties the *domain* and does not remove the *file*;
/// 1136 42-byte plists had accumulated in a developer's real
/// `~/Library/Preferences` before anyone looked.
///
/// # Why the obvious fixes were rejected, with what was measured
///
/// **Unlink the file in `tearDown`.** `cfprefsd` owns the file and rewrites it
/// on its own schedule, including *after the process has exited*, at which
/// point no code of ours is running. Measured on this machine over four
/// experiments on identical sequences: 60/60 files came back, then 0/40, then
/// 0/60, then 120/120. Both orderings produced both outcomes, so no in-process
/// sequence can be shown to prevent it. And `tearDown` does not run at all for
/// the runs that most need it — XCTest's stall detector `abort()`s the process
/// (#1523), `tools/lib/swift-suite.sh` kills the tree at 240s, and
/// `--budget` kills the gate.
///
/// **Sweep the leftovers on the way in.** That is a function whose job is to
/// delete files from a directory it did not create, in the developer's home.
/// One mutation of its filename filter removed ~1895 files from a real
/// `~/Library/Preferences`. There is no version of that idea worth its blast
/// radius, so nothing here deletes anything, ever.
///
/// **Redirect the preferences root.** `CFFIXED_USER_HOME`, `HOME`, and
/// `CFPREFERENCES_AVOID_DAEMON=1` were each measured with a standalone binary
/// that wrote one key to a fresh suite. All three redirect `NSHomeDirectory()`
/// and `FileManager.homeDirectoryForCurrentUser` — and the plist still landed
/// in the *real* `~/Library/Preferences` every time:
///
/// ```
///   HOME + CFFIXED_USER_HOME        NSHomeDirectory redirected, file in real home
///   + CFPREFERENCES_AVOID_DAEMON=1  NSHomeDirectory redirected, file in real home
/// ```
///
/// The write is not performed by the process that asks for it. `cfprefsd` runs
/// as the user, outside the test process's environment, and resolves the home
/// from the password database — so a client-side environment variable cannot
/// move it. That is why the redirect is not the mechanism.
///
/// # So the mechanism is: never ask `cfprefsd` for anything
///
/// The only thing `NotificationSettings.masterEnabled(defaults:)` needs is a
/// `UserDefaults`-shaped key-value store. `InMemoryDefaults` is one, backed by
/// a dictionary, with every mutating entry point overridden so that no write
/// path reaches the superclass. Nothing is created, so nothing has to be
/// cleaned up, so no cleanup can go wrong — the leak becomes inexpressible
/// rather than tidied up after.
///
/// `PersistentDefaultsLintTests` is the other half: it fails the build on any
/// `UserDefaults(suiteName:` in the test targets, so the next test inherits
/// this instead of having to remember it.
final class InMemoryDefaults: UserDefaults {

    /// Values written through this object. Never touched by the superclass.
    private var store: [String: Any] = [:]

    /// Values supplied via `register(defaults:)`. Foundation's registration
    /// domain is in-memory and loses to the application domain, and this
    /// reproduces both properties so a test that registers reads what it would
    /// read against a real suite.
    private var registered: [String: Any] = [:]

    /// Every key this store has been ASKED for. See `readKeys`.
    private var reads: Set<String> = []

    /// XCTest drives one test at a time here, but `SessionManager` and friends
    /// read defaults from other queues, and a double that flaked under that
    /// would be debugged as a product bug. `UserDefaults` itself is thread-safe.
    private let lock = NSLock()

    /// The keys WRITTEN through this store, with the registration domain
    /// excluded — and that exclusion is the entire reason this exists (#1672).
    ///
    /// `object(forKey:)` and `dictionaryRepresentation()` both merge `registered`
    /// under `store`, so neither can answer "did this render PERSIST anything".
    /// The write #1672 is about put back the exact value `register(defaults:)`
    /// had just seeded, so every merged view of the store — and, on a real
    /// domain, every value comparison and the plist's own mtime — reads
    /// identically whether it happened or not. Only the key set of the
    /// application domain tells them apart.
    /// `testBuildingASessionManagerWritesNoPreference` can assert through
    /// `object(forKey:)` only because the two keys it names are not in that
    /// seed. The three sound keys are.
    var writtenKeys: Set<String> {
        lock.lock()
        defer { lock.unlock() }
        return Set(store.keys)
    }

    /// The keys this store was READ for. The vacuity guard for any
    /// "wrote nothing" assertion over a rendered view: without it, a view that
    /// never rendered and a view that rendered and wrote nothing produce the
    /// same verdict — AGENTS.md's "absence of a finding and inability to look
    /// must never produce the same output". A view whose `@AppStorage` resolved
    /// `UserDefaults.standard` instead of this store also reads as "wrote
    /// nothing", and this is what separates that case out too.
    var readKeys: Set<String> {
        lock.lock()
        defer { lock.unlock() }
        return reads
    }

    /// - Note: `super.init(suiteName: nil)` binds the process's *own*
    ///   application domain, which already exists — it mints no new domain and
    ///   creates no file (measured: a process doing exactly this, writing many
    ///   keys through the overrides below and calling `synchronize()`, left the
    ///   real preferences directory byte-for-byte unchanged; that measurement is
    ///   re-run on every suite run by
    ///   `InMemoryDefaultsTests.testTheDoubleLeavesNothingAttributableToItInTheRealPreferencesDirectory`,
    ///   which since #1714 grades what the run OWNS rather than what the
    ///   directory did — see `PreferencesDirectoryWitness` for what that gives
    ///   up and what covers it instead).
    ///   The force-unwrap is deliberate and is the fail-loud: `init(suiteName:)`
    ///   answers nil only for the main bundle identifier, and if it ever did
    ///   answer nil here the correct outcome is a trap, not a silent fallback to
    ///   an object that writes into the developer's home.
    init() {
        super.init(suiteName: nil)!
    }

    // MARK: - Primitive methods
    //
    // Apple documents these as the primitives a `UserDefaults` subclass must
    // override; every derived accessor (`bool(forKey:)`, `string(forKey:)`,
    // `integer(forKey:)`, KVC reads) funnels through them. That funnelling was
    // verified rather than assumed — see `InMemoryDefaultsTests`'s faithfulness
    // arm, which drives each derived accessor and pins the answer.

    override func object(forKey defaultName: String) -> Any? {
        lock.lock()
        defer { lock.unlock() }
        // Recorded here rather than in each derived accessor: Foundation funnels
        // every read through this primitive, which is the same argument the
        // overrides below rest on.
        reads.insert(defaultName)
        return store[defaultName] ?? registered[defaultName]
    }

    override func set(_ value: Any?, forKey defaultName: String) {
        lock.lock()
        defer { lock.unlock() }
        if let value {
            store[defaultName] = value
        } else {
            store.removeValue(forKey: defaultName)
        }
    }

    override func removeObject(forKey defaultName: String) {
        lock.lock()
        defer { lock.unlock() }
        store.removeValue(forKey: defaultName)
    }

    override func dictionaryRepresentation() -> [String: Any] {
        lock.lock()
        defer { lock.unlock() }
        return registered.merging(store) { _, written in written }
    }

    // MARK: - Typed setters
    //
    // Foundation funnels `setBool:forKey:` and friends into `setObject:forKey:`,
    // so overriding the primitive alone happens to be enough today. They are
    // overridden anyway: the whole guarantee of this type is that no write
    // reaches the superclass, and resting that guarantee on an undocumented
    // funnel would make a future Foundation change a silent leak rather than a
    // test failure. Each boxes the value the way Foundation does, so
    // `object(forKey:) as? Bool` — what `NotificationSettings` actually reads —
    // answers identically to a real suite.

    override func set(_ value: Bool, forKey defaultName: String) {
        set(NSNumber(value: value) as Any, forKey: defaultName)
    }

    override func set(_ value: Int, forKey defaultName: String) {
        set(NSNumber(value: value) as Any, forKey: defaultName)
    }

    override func set(_ value: Double, forKey defaultName: String) {
        set(NSNumber(value: value) as Any, forKey: defaultName)
    }

    override func set(_ value: Float, forKey defaultName: String) {
        set(NSNumber(value: value) as Any, forKey: defaultName)
    }

    override func set(_ url: URL?, forKey defaultName: String) {
        set(url as Any?, forKey: defaultName)
    }

    /// Real `UserDefaults` archives URLs; this stores the value, so the reader
    /// is overridden to match rather than inheriting a decoder for an encoding
    /// that was never applied.
    override func url(forKey defaultName: String) -> URL? {
        object(forKey: defaultName) as? URL
    }

    /// KVC writes — `@AppStorage` and `setValue(_:forKey:)` reach defaults this
    /// way. Routed to the primitive so they cannot slip past it.
    override func setValue(_ value: Any?, forKey key: String) {
        set(value, forKey: key)
    }

    override func value(forKey key: String) -> Any? {
        object(forKey: key)
    }

    // MARK: - Registration and domains

    override func register(defaults registrationDictionary: [String: Any]) {
        lock.lock()
        defer { lock.unlock() }
        registered.merge(registrationDictionary) { _, new in new }
    }

    /// No-ops rather than forwards, and this is the load-bearing one.
    ///
    /// `removePersistentDomain(forName:)` is what the pre-fix `tearDown` called,
    /// and naming a domain to `cfprefsd` is itself one of the operations
    /// observed to schedule a write-back. Forwarding any of these to the
    /// superclass would reach the daemon and could create a file — the exact
    /// outcome this type exists to make impossible.
    override func removePersistentDomain(forName domainName: String) {}

    override func setPersistentDomain(_ domain: [String: Any], forName domainName: String) {}

    /// `nil`, never the store: this object HAS no persistent domain, and
    /// answering with the dictionary would say the opposite — that the values a
    /// test wrote are sitting in one. Reading a real domain here is not the
    /// alternative, since that is the daemon call the type exists to avoid.
    override func persistentDomain(forName domainName: String) -> [String: Any]? {
        nil
    }

    override func addSuite(named suiteName: String) {}

    override func removeSuite(named suiteName: String) {}

    /// There is nothing to flush. Answering `true` keeps callers that check the
    /// result on the success path.
    override func synchronize() -> Bool { true }
}

/// A read-only witness over a preferences directory: what files were there
/// before, what files are there now, what appeared in between — and which of
/// those appearances can be ATTRIBUTED to the run that asked.
///
/// It reads and never writes, and it has no delete path at all — the whole
/// point of #1661's rework is that no code here is allowed to remove a file
/// from a directory it did not create, and the simplest way to hold that is to
/// own no `removeItem` call anywhere in this file.
///
/// # Why attribution exists, and why the raw delta was not enough (#1714)
///
/// The raw delta answers *"did this directory change"*. The question the suite
/// asks is *"did the SUBJECT change it"*, and on a GitHub runner those two
/// produced different answers: `com.apple.siri.ODDI.MetricsWorker.plist` landed
/// inside the measured window and the assertion reported it as **"The defaults
/// double reached disk. #1661 is back"** — the most alarming sentence this
/// suite can say, about a file the double had nothing to do with. That is not
/// an ordinary flake: a false ACCUSATION is read, and acted on, as a finding.
///
/// A NAME FILTER is not the fix, and that is worth saying loudly because it is
/// the obvious one. #1661's leaked files were named `<uuid>.plist`, so an
/// allowlist wide enough to hide a `com.apple.*` metrics plist is wide enough
/// to hide the incident — #1509's `perceptualPrecision` mistake in a different
/// costume. The guard is therefore scoped to what the run OWNS, by two clauses,
/// neither of which is a list of names the run did not choose:
///
///   the run's own token   an identifier minted per run and woven into every
///                         domain name and key the subject is handed, so an
///                         entry carrying it can only have come from the
///                         subject. This is exactly #1661's own artifact shape
///                         — a plist named for a suite the test minted.
///   this process's own    what `super.init(suiteName: nil)` binds, derived
///   application domain    from `Bundle`/`ProcessInfo` at run time rather than
///                         written down. An entry named for it appearing is a
///                         write that reached `super`.
///
/// # What that gives up, stated rather than left to be discovered
///
/// A NEW file whose name carries neither — a leak into a domain named nowhere
/// in this process — is no longer reported here. `InMemoryDefaultsTests`
/// commits that limit as a case (a `<uuid>.plist` bearing a UUID this run did
/// not mint stays silent) so it is learned from a test rather than from an
/// incident. Two things already cover it, and neither is inside this process:
/// `tools/lib/swift-suite.sh`'s directory witness brackets the whole run with
/// the wide, unattributed net — its wording says "background daemons also write
/// here … re-run to tell that apart", which is the honest form of the same
/// finding and the one this test could not use, since a single in-process
/// window cannot re-run itself — and that file's domain half compares
/// `com.apple.dt.xctest.tool`'s keys and VALUES across the run, which sees a
/// write into an already-existing domain that no added-entry witness can.
///
/// The clauses are deterministic rather than statistical, which is why they are
/// preferred to the other shape #1714 named (a difference taken over a control
/// window with the subject not exercised): the
/// measured failure was a metrics worker writing ONCE, and a one-shot write is
/// exactly what a two-window subtraction cannot cancel.
struct PreferencesDirectoryWitness {

    /// Failing to *look* and finding nothing must not produce the same answer.
    /// An unreadable directory silently snapshotting as the empty set would make
    /// every delta zero and every #1661 assertion pass forever — AGENTS.md's
    /// "a verification mechanism must fail loudly when it cannot run", and here
    /// it is also the difference between a real guarantee and a comfortable one.
    enum Failure: Error, CustomStringConvertible {
        case unreadable(path: String, underlying: Error)
        /// The attribution could not be performed with the inputs it was given.
        /// Same rule as `unreadable`, one layer up: a needle that matches
        /// everything and a needle that matches nothing are both a guard that
        /// has stopped asking the question, and both read as a pass.
        case cannotAttribute(reason: String)

        var description: String {
            switch self {
            case let .unreadable(path, underlying):
                return "could not read \(path): \(underlying) — " +
                       "the witness cannot report a delta it was unable to measure"
            case let .cannotAttribute(reason):
                return "could not attribute what appeared: \(reason) — " +
                       "the witness cannot name a subject it was unable to identify"
            }
        }
    }

    /// One run's claim on the entries that appeared during its window.
    struct Attribution {
        /// Appeared, and carries the identifier this run minted.
        let namedForThisRun: [String]
        /// Appeared, and names the application domain THIS PROCESS binds.
        let namedForThisProcess: [String]
        /// Appeared, and is attributable to neither. Reported for context and
        /// never asserted on — this is the population #1714 is about.
        let unattributed: [String]

        /// Everything this run is answerable for, sorted for a stable message.
        var names: [String] { Set(namedForThisRun + namedForThisProcess).sorted() }
    }

    let directory: URL

    /// The directory `cfprefsd` actually writes to for this user.
    ///
    /// Resolved from the password database, not from `NSHomeDirectory()` or
    /// `$HOME`. That is not defensive noise: `HOME` and `CFFIXED_USER_HOME` both
    /// move `NSHomeDirectory()` and neither moves the daemon (measured — see the
    /// note on `InMemoryDefaults`), so a witness reading the environment would
    /// watch an empty temp directory while the leak landed in the real home, and
    /// report a clean delta.
    static var realUserPreferences: PreferencesDirectoryWitness {
        PreferencesDirectoryWitness(directory: passwordDatabaseHome
            .appendingPathComponent("Library", isDirectory: true)
            .appendingPathComponent("Preferences", isDirectory: true))
    }

    static var passwordDatabaseHome: URL {
        if let entry = getpwuid(getuid()), let dir = entry.pointee.pw_dir {
            return URL(fileURLWithPath: String(cString: dir))
        }
        // Nothing sensible to fall back to that would not be a lie; the
        // environment is the one source already known to be wrong here.
        return URL(fileURLWithPath: NSHomeDirectory())
    }

    /// Every entry name directly in the directory. Throws rather than answering
    /// the empty set when it cannot look.
    func snapshot() throws -> Set<String> {
        do {
            return Set(try FileManager.default.contentsOfDirectory(atPath: directory.path))
        } catch {
            throw Failure.unreadable(path: directory.path, underlying: error)
        }
    }

    /// Names present in `after` and not in `before`, sorted for a stable message.
    static func added(from before: Set<String>, to after: Set<String>) -> [String] {
        after.subtracting(before).sorted()
    }

    /// The application domain(s) this process's own `UserDefaults` binds.
    ///
    /// Derived at run time rather than written down, for the reason the whole
    /// file exists: a committed literal is a name the run does not own, and a
    /// name the run does not own is a filter. Measured under `swift test` on
    /// 2026-08-19 this answers `["com.apple.dt.xctest.tool", "xctest"]` — the
    /// first being the domain `PersistentDefaultsLintTests` and
    /// `tools/lib/swift-suite.sh` each name independently, and the one
    /// `super.init(suiteName: nil)` binds.
    ///
    /// Both spellings are collected because Foundation falls back to the
    /// process name when a bundle identifier is absent, and which of the two is
    /// live is a property of how the suite was launched (`swift test`, Xcode, a
    /// bare `xctest`) rather than of anything here.
    /// `InMemoryDefaultsTests.testThisProcessesOwnApplicationDomainIsDerivableAndAttributes`
    /// is the vacuity guard, and it also refuses an answer short enough that
    /// containment would stop being an attribution.
    static var processApplicationDomains: Set<String> {
        var domains: Set<String> = []
        if let identifier = Bundle.main.bundleIdentifier, !identifier.isEmpty {
            domains.insert(identifier)
        }
        let process = ProcessInfo.processInfo.processName
        if !process.isEmpty {
            domains.insert(process)
        }
        return domains
    }

    /// Split what appeared into what this run is answerable for and what it is
    /// not.
    ///
    /// Matching is CONTAINMENT rather than equality on purpose: `cfprefsd`
    /// names a file for a domain but is not obliged to name it *exactly* that
    /// (a suffix, a temporary spelling during an atomic replace), and a guard
    /// that only recognised the exact form would be a guard a different
    /// spelling walks past. It cannot make the guard wider than the run owns —
    /// both needles are values this process minted or derived from itself.
    ///
    /// Refuses rather than answering for an empty needle, and that refusal is
    /// the load-bearing line: `"anything".contains("")` is **true** in Swift, so
    /// an empty token does not narrow the guard, it silently restores the
    /// unattributed net #1714 is about — with every OS write reported as the
    /// subject's, and reading as a pass until the day it fires.
    static func attribute(_ appeared: [String],
                          toRun runToken: String,
                          orProcessDomains domains: Set<String> = processApplicationDomains) throws -> Attribution {
        guard !runToken.isEmpty else {
            throw Failure.cannotAttribute(
                reason: "the run token is empty, and every entry contains the empty string")
        }
        guard !domains.isEmpty else {
            throw Failure.cannotAttribute(
                reason: "this process's application domain could not be derived, so a write that "
                      + "reached `super` would be attributable to nothing")
        }
        guard !domains.contains(where: \.isEmpty) else {
            throw Failure.cannotAttribute(
                reason: "an application domain is the empty string, and every entry contains it")
        }

        var run: [String] = []
        var process: [String] = []
        var other: [String] = []
        for name in appeared {
            if name.contains(runToken) {
                // Checked first, so an entry matching both is reported once and
                // under the more specific of the two.
                run.append(name)
            } else if domains.contains(where: { name.contains($0) }) {
                process.append(name)
            } else {
                other.append(name)
            }
        }
        return Attribution(namedForThisRun: run.sorted(),
                           namedForThisProcess: process.sorted(),
                           unattributed: other.sorted())
    }
}
