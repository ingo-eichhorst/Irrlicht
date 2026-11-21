import Foundation
import Security
import os

/// A tiny generic-password Keychain wrapper for credential-grade secrets.
///
/// The relay bearer token lives here rather than in UserDefaults (which is the
/// plist-on-disk, world-readable-within-the-sandbox store) — UserDefaults keeps
/// only non-secret relay metadata (URL, enabled). Per the relay protocol doc:
/// "macOS uses the Keychain; UserDefaults for metadata only."
///
/// # Why there is a backend at all
///
/// The item's ACL trusts only the signed release app. Every other process that
/// reads it — the Swift test runner (`xctest`) above all — makes `securityd`
/// put a password dialog on the developer's screen, one per read. Before this
/// type had a seam, `DaemonManagerTests` and the `SettingsView`-hosting suites
/// reached `SecItemCopyMatching` through `DaemonManager.pushPublishConfig()`
/// and `SettingsView`'s `.onAppear`; `securityd`'s "displaying keychain
/// prompt" events in the unified log (read 2026-11-21) named `xctest` as the
/// requesting process for every prompt that day. So the store's bytes live behind
/// `KeychainBackend`, and which backend the process-wide store uses is decided
/// ONCE, at first use, by `defaultBackend(underXCTest:)`:
///
///   * the app        → `SystemKeychainBackend`, the SecItem code that shipped
///                      before this seam, attribute for attribute;
///   * a test process → `InMemoryKeychainBackend`, which never calls Security.
///
/// The test-process signal is `AppHome.isRunningTests`
/// (`NSClassFromString("XCTestCase") != nil`) — the same one that keeps tests
/// off the real home directory (#1669/#1670) and off the live daemon socket
/// (#832). `KeychainStoreBackendGuardTests` asserts both that the detector
/// answers true in the test bundle and that the shared store took the
/// in-memory backend.
enum KeychainStore {

    /// Picks the backend for the process-wide store. A pure function so the
    /// rule can be asserted without constructing the thing it protects — the
    /// system arm is never READ by any test, only its type is checked.
    static func defaultBackend(underXCTest: Bool) -> KeychainBackend {
        underXCTest ? InMemoryKeychainBackend() : SystemKeychainBackend()
    }

    /// The process-wide store every production call site goes through.
    /// `static let` makes the backend decision once per process, lazily and
    /// thread-safely (Swift initializes static stored properties exactly once).
    static let shared = KeychainSecrets(backend: defaultBackend(underXCTest: AppHome.isRunningTests))

    /// Stores `value` for `account`, replacing any existing item. An empty
    /// `value` deletes the item (so clearing the token field forgets it).
    @discardableResult
    static func set(_ value: String, account: String) -> Bool {
        shared.set(value, account: account)
    }

    /// Returns the stored secret for `account`, or "" when absent.
    static func get(account: String) -> String {
        shared.get(account: account)
    }

    /// Removes the stored secret for `account` (no-op if absent).
    static func delete(account: String) {
        shared.delete(account: account)
    }
}

/// A store of secrets over one `KeychainBackend`, read at most once per
/// account per process.
///
/// An instance type rather than more statics so a test can build a private
/// store over a counting backend and assert on it without sharing state with
/// any other test (or with a detached read some other test left running).
///
/// # Why it caches
///
/// The app reads the relay token on every daemon adopt/spawn publish, every
/// relay (re)connect and every Settings appearance. A process the item's ACL
/// does not trust gets one `securityd` dialog per read, and a dismissed
/// dialog read as "no token" — so the next reconnect asked again. So:
///
///   * the first `get` for an account reads the backend and caches the result;
///     every later `get` is answered from the cache;
///   * a FAILED read (denied, cancelled, interaction not allowed — not only
///     not-found) caches "" for the rest of the process, so a declined dialog
///     is not re-raised in a loop; its `OSStatus` is logged, never a value;
///   * `set` and `delete` update the cache. A write the backend refuses drops
///     the account's entry instead, so the next `get` reports what the
///     keychain actually holds. A `set` of the value already cached writes
///     nothing at all.
///
/// The lock is held across the backend read on purpose: concurrent first
/// reads (a detached publish task and the main actor's relay connect, both at
/// launch) then produce ONE backend read, and the waiters get its result. A
/// waiter blocks no longer than it would have blocked on a dialog of its own.
/// `KeychainSecretsCacheTests` drives each of these rules.
final class KeychainSecrets: @unchecked Sendable {
    let backend: KeychainBackend

    private var cache: [String: String] = [:]
    private let lock = NSLock()
    private let logger = Logger(subsystem: "io.irrlicht.app", category: "KeychainStore")

    init(backend: KeychainBackend) {
        self.backend = backend
    }

    @discardableResult
    func set(_ value: String, account: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        // The store already holds exactly this value: nothing to write. This
        // includes "" over a cached "" — and a cached "" can be a read that
        // FAILED, in which case skipping the delete leaves an item this
        // process could not read rather than destroying it.
        if cache[account] == value { return true }
        if value.isEmpty {
            deleteLocked(account: account)
            return true
        }
        let written = backend.write(value, account: account)
        if written {
            cache[account] = value
        } else {
            cache[account] = nil
        }
        return written
    }

    func get(account: String) -> String {
        lock.lock()
        defer { lock.unlock() }
        if let cached = cache[account] { return cached }
        let result = backend.read(account: account)
        if result.value == nil, result.status != errSecItemNotFound {
            logger.error("""
                Keychain read for account \(account, privacy: .public) failed with OSStatus \(result.status, privacy: .public); \
                treating it as empty for the rest of this process
                """)
        }
        let value = result.value ?? ""
        cache[account] = value
        return value
    }

    func delete(account: String) {
        lock.lock()
        defer { lock.unlock() }
        deleteLocked(account: account)
    }

    /// `delete`'s body for a caller that already holds `lock` (`set("")`):
    /// `NSLock` is not recursive.
    private func deleteLocked(account: String) {
        backend.delete(account: account)
        cache[account] = ""
    }
}

/// Where a `KeychainSecrets` store's bytes actually live.
protocol KeychainBackend: AnyObject {
    /// The secret for `account` and the Security status of the read. `value`
    /// is non-nil exactly when the read succeeded.
    func read(account: String) -> (value: String?, status: OSStatus)
    /// Replaces the secret for `account`. True on success.
    func write(_ value: String, account: String) -> Bool
    /// Removes the secret for `account` (no-op if absent).
    func delete(account: String)
}

/// The real login keychain. This is the SecItem code `KeychainStore` carried
/// before the backend seam, moved here unchanged: same service, account and
/// accessibility attributes, same delete-then-add replace. No test is meant
/// to read or write through it: under XCTest the shared store selects
/// `InMemoryKeychainBackend` instead, and the tests that need a backend build
/// their own over a double.
final class SystemKeychainBackend: KeychainBackend {
    /// Service identifier namespacing irrlicht's Keychain items.
    private static let service = "ai.irrlicht.relay"

    func write(_ value: String, account: String) -> Bool {
        guard let data = value.data(using: .utf8) else { return false }
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.service,
            kSecAttrAccount as String: account,
        ]
        // Replace: delete-then-add keeps the call idempotent without juggling
        // SecItemUpdate's attributes-vs-query split.
        SecItemDelete(query as CFDictionary)
        var attrs = query
        attrs[kSecValueData as String] = data
        // Deliberately no SecAccessControl / biometric gate (SonarQube
        // swift:S6288): irrlichd reads this token unattended to auto-reconnect
        // to the relay, including right after login before the user has
        // touched the app. AfterFirstUnlock is the standard non-interactive
        // tier — item is unavailable before the device's first unlock, but
        // doesn't block a headless read afterward. ThisDeviceOnly formalizes
        // that this never leaves the device (matches kSecAttrSynchronizable's
        // default of false, now explicit rather than incidental).
        attrs[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly // NOSONAR (swift:S6288) — see comment above
        return SecItemAdd(attrs as CFDictionary, nil) == errSecSuccess
    }

    func read(account: String) -> (value: String?, status: OSStatus) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        guard status == errSecSuccess,
              let data = item as? Data,
              let value = String(data: data, encoding: .utf8) else {
            return (nil, status == errSecSuccess ? errSecDecode : status)
        }
        return (value, status)
    }

    func delete(account: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(query as CFDictionary)
    }
}

/// A dictionary. What the process-wide store uses inside a test process, so
/// `swift test` never asks `securityd` for anything (and so never raises a
/// keychain dialog, and never reads, rewrites or deletes the user's real
/// item). Each process starts empty.
final class InMemoryKeychainBackend: KeychainBackend, @unchecked Sendable {
    private var items: [String: String] = [:]
    private let lock = NSLock()

    func read(account: String) -> (value: String?, status: OSStatus) {
        lock.lock()
        defer { lock.unlock() }
        guard let value = items[account] else { return (nil, errSecItemNotFound) }
        return (value, errSecSuccess)
    }

    func write(_ value: String, account: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        items[account] = value
        return true
    }

    func delete(account: String) {
        lock.lock()
        defer { lock.unlock() }
        items[account] = nil
    }
}
