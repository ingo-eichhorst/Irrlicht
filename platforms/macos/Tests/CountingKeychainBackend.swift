import Foundation
import Security
@testable import Irrlicht

/// A `KeychainBackend` double that counts every call and can be told to fail.
final class CountingKeychainBackend: KeychainBackend, @unchecked Sendable {
    private var items: [KeychainAccount: String]
    private let readStatus: OSStatus
    private let writeSucceeds: Bool
    private let readDelay: TimeInterval
    private let lock = NSLock()
    private var readCount = 0
    private var writeCount = 0
    private var deleteCount = 0

    init(items: [KeychainAccount: String], readStatus: OSStatus = errSecSuccess,
         writeSucceeds: Bool = true, readDelay: TimeInterval = 0) {
        self.items = items
        self.readStatus = readStatus
        self.writeSucceeds = writeSucceeds
        self.readDelay = readDelay
    }

    var reads: Int { lock.lock(); defer { lock.unlock() }; return readCount }
    var writes: Int { lock.lock(); defer { lock.unlock() }; return writeCount }
    var deletes: Int { lock.lock(); defer { lock.unlock() }; return deleteCount }

    func read(account: KeychainAccount) -> (value: String?, status: OSStatus) {
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

    func write(_ value: String, account: KeychainAccount) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        writeCount += 1
        guard writeSucceeds else { return false }
        items[account] = value
        return true
    }

    func delete(account: KeychainAccount) {
        lock.lock()
        defer { lock.unlock() }
        deleteCount += 1
        items[account] = nil
    }
}
