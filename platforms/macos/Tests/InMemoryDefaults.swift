import Foundation

/// A `UserDefaults` that never reaches disk, and the read-only witness that
/// proves it.
///
/// # What #1661 is
///
/// `NotificationMasterGateTests` minted a fresh `UserDefaults(suiteName:)` per
/// test with a UUID name and called `removePersistentDomain(forName:)` in
/// `tearDown`. That call empties the *domain* and does not remove the *file*;
/// 1160 42-byte plists had accumulated in a developer's real
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

    /// XCTest drives one test at a time here, but `SessionManager` and friends
    /// read defaults from other queues, and a double that flaked under that
    /// would be debugged as a product bug. `UserDefaults` itself is thread-safe.
    private let lock = NSLock()

    /// - Note: `super.init(suiteName: nil)` binds the process's *own*
    ///   application domain, which already exists — it mints no new domain and
    ///   creates no file (measured: a process doing exactly this, writing many
    ///   keys through the overrides below and calling `synchronize()`, left the
    ///   real preferences directory byte-for-byte unchanged; that measurement is
    ///   re-run on every suite run by
    ///   `InMemoryDefaultsTests.testTheDoubleAddsNothingToTheRealPreferencesDirectory`).
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

    override func persistentDomain(forName domainName: String) -> [String: Any]? {
        dictionaryRepresentation()
    }

    override func addSuite(named suiteName: String) {}

    override func removeSuite(named suiteName: String) {}

    /// There is nothing to flush. Answering `true` keeps callers that check the
    /// result on the success path.
    override func synchronize() -> Bool { true }
}

/// A read-only witness over a preferences directory: what files were there
/// before, what files are there now, and what appeared in between.
///
/// It reads and never writes, and it has no delete path at all — the whole
/// point of #1661's rework is that no code here is allowed to remove a file
/// from a directory it did not create, and the simplest way to hold that is to
/// own no `removeItem` call anywhere in this file.
struct PreferencesDirectoryWitness {

    /// Failing to *look* and finding nothing must not produce the same answer.
    /// An unreadable directory silently snapshotting as the empty set would make
    /// every delta zero and every #1661 assertion pass forever — AGENTS.md's
    /// "a verification mechanism must fail loudly when it cannot run", and here
    /// it is also the difference between a real guarantee and a comfortable one.
    enum Failure: Error, CustomStringConvertible {
        case unreadable(path: String, underlying: Error)

        var description: String {
            switch self {
            case let .unreadable(path, underlying):
                return "could not read \(path): \(underlying) — " +
                       "the witness cannot report a delta it was unable to measure"
            }
        }
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
}
