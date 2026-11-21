import XCTest
import Security
@testable import Irrlicht

/// The relay token is read once per process.
///
/// The release app read it from the real keychain on every daemon adopt/spawn
/// push, every relay (re)connect and every Settings appearance, and a process
/// the item's ACL does not trust gets one `securityd` dialog PER READ. A
/// declined dialog read as "no token", so the next reconnect asked again.
/// `KeychainSecrets` now caches the value — including the "" of a failed
/// read — and `set`/`delete` keep that cache current.
///
/// Every arm builds a private store over `CountingKeychainBackend`, so the
/// counts belong to the arm alone and no arm touches `KeychainStore.shared`.
final class KeychainSecretsCacheTests: XCTestCase {

    private let account = "relayToken"

    func testTwoGetsReadTheBackendOnce() {
        let backend = CountingKeychainBackend(items: [account: "tok"])
        let store = KeychainSecrets(backend: backend)

        XCTAssertEqual(store.get(account: account), "tok")
        XCTAssertEqual(store.get(account: account), "tok")

        XCTAssertEqual(backend.reads, 1, "the second get must be served from the cache")
    }

    /// `get` is called from detached tasks (`DaemonManager.pushPublishConfig`)
    /// and from the main actor (`SessionManager.relayConnect`, `SettingsView`)
    /// at launch, so the first reads race. Each one that reaches the backend is
    /// a dialog of its own. The backend sleeps inside `read` to hold the race
    /// window open; without that a fast double could serialise by luck.
    func testConcurrentFirstGetsReadTheBackendOnce() {
        let backend = CountingKeychainBackend(items: [account: "tok"], readDelay: 0.05)
        let store = KeychainSecrets(backend: backend)
        let callers = 16
        let results = ResultBox()

        DispatchQueue.concurrentPerform(iterations: callers) { _ in
            results.append(store.get(account: account))
        }

        XCTAssertEqual(results.values, Array(repeating: "tok", count: callers),
                       "every concurrent caller must see the token")
        XCTAssertEqual(backend.reads, 1,
                       "\(callers) concurrent first gets must produce ONE backend read")
    }

    /// A read the user declined (or that failed any other way) is cached as
    /// "" for the process: re-reading would re-raise the dialog they just
    /// dismissed, once per relay reconnect.
    func testAFailedReadIsCachedAsEmptyAndNotRetried() {
        let backend = CountingKeychainBackend(items: [account: "tok"], readStatus: errSecUserCanceled)
        let store = KeychainSecrets(backend: backend)

        XCTAssertEqual(store.get(account: account), "")
        XCTAssertEqual(store.get(account: account), "")

        XCTAssertEqual(backend.reads, 1, "a declined read must not be retried in the same process")
    }

    func testSetIsVisibleToTheNextGetWithoutARead() {
        let backend = CountingKeychainBackend(items: [:])
        let store = KeychainSecrets(backend: backend)

        XCTAssertTrue(store.set("new", account: account))

        XCTAssertEqual(store.get(account: account), "new")
        XCTAssertEqual(backend.writes, 1, "precondition: the write reached the backend")
        XCTAssertEqual(backend.reads, 0, "a value this process just wrote must not be read back")
    }

    func testDeleteIsVisibleToTheNextGetWithoutARead() {
        let backend = CountingKeychainBackend(items: [account: "tok"])
        let store = KeychainSecrets(backend: backend)
        XCTAssertEqual(store.get(account: account), "tok")

        store.delete(account: account)

        XCTAssertEqual(store.get(account: account), "")
        XCTAssertEqual(backend.deletes, 1, "precondition: the delete reached the backend")
        XCTAssertEqual(backend.reads, 1, "a delete must update the cache, not invalidate it")
    }

    /// `set("")` is how clearing the token field forgets it.
    func testSettingEmptyDeletesAndCachesEmpty() {
        let backend = CountingKeychainBackend(items: [account: "tok"])
        let store = KeychainSecrets(backend: backend)
        XCTAssertEqual(store.get(account: account), "tok")

        XCTAssertTrue(store.set("", account: account))

        XCTAssertEqual(store.get(account: account), "")
        XCTAssertEqual(backend.deletes, 1)
        XCTAssertEqual(backend.reads, 1)
    }

    /// LOCK, not a red-first defect test: it passes without any cache too.
    /// What it pins is that a write the backend REFUSED is not cached — the
    /// next get must report what the keychain actually holds.
    func testAFailedWriteIsNotCached() {
        let backend = CountingKeychainBackend(items: [account: "old"], writeSucceeds: false)
        let store = KeychainSecrets(backend: backend)

        XCTAssertFalse(store.set("new", account: account))

        XCTAssertEqual(store.get(account: account), "old")
    }

    /// LOCK: passes without any cache too. Pins that the cache is keyed by
    /// account, so one account's value is never answered for another.
    func testTheCacheIsPerAccount() {
        let backend = CountingKeychainBackend(items: ["a": "1", "b": "2"])
        let store = KeychainSecrets(backend: backend)

        XCTAssertEqual(store.get(account: "a"), "1")
        XCTAssertEqual(store.get(account: "b"), "2")
        XCTAssertEqual(backend.reads, 2)
    }
}

/// A `KeychainBackend` double that counts every call and can be told to fail.
private final class CountingKeychainBackend: KeychainBackend, @unchecked Sendable {
    private var items: [String: String]
    private let readStatus: OSStatus
    private let writeSucceeds: Bool
    private let readDelay: TimeInterval
    private let lock = NSLock()
    private var readCount = 0
    private var writeCount = 0
    private var deleteCount = 0

    init(items: [String: String], readStatus: OSStatus = errSecSuccess,
         writeSucceeds: Bool = true, readDelay: TimeInterval = 0) {
        self.items = items
        self.readStatus = readStatus
        self.writeSucceeds = writeSucceeds
        self.readDelay = readDelay
    }

    var reads: Int { lock.lock(); defer { lock.unlock() }; return readCount }
    var writes: Int { lock.lock(); defer { lock.unlock() }; return writeCount }
    var deletes: Int { lock.lock(); defer { lock.unlock() }; return deleteCount }

    func read(account: String) -> (value: String?, status: OSStatus) {
        lock.lock()
        readCount += 1
        lock.unlock()
        if readDelay > 0 { Thread.sleep(forTimeInterval: readDelay) }
        lock.lock()
        defer { lock.unlock() }
        guard readStatus == errSecSuccess else { return (nil, readStatus) }
        guard let value = items[account] else { return (nil, errSecItemNotFound) }
        return (value, errSecSuccess)
    }

    func write(_ value: String, account: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        writeCount += 1
        guard writeSucceeds else { return false }
        items[account] = value
        return true
    }

    func delete(account: String) {
        lock.lock()
        defer { lock.unlock() }
        deleteCount += 1
        items[account] = nil
    }
}

/// Collects values from concurrent callers.
private final class ResultBox: @unchecked Sendable {
    private var stored: [String] = []
    private let lock = NSLock()

    func append(_ value: String) {
        lock.lock()
        defer { lock.unlock() }
        stored.append(value)
    }

    var values: [String] {
        lock.lock()
        defer { lock.unlock() }
        return stored
    }
}
