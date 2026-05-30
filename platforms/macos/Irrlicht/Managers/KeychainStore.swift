import Foundation
import Security

/// A tiny generic-password Keychain wrapper for credential-grade secrets.
///
/// The relay bearer token lives here rather than in UserDefaults (which is the
/// plist-on-disk, world-readable-within-the-sandbox store) — UserDefaults keeps
/// only non-secret relay metadata (URL, enabled). Per the relay protocol doc:
/// "macOS uses the Keychain; UserDefaults for metadata only."
enum KeychainStore {
    /// Service identifier namespacing irrlicht's Keychain items.
    private static let service = "ai.irrlicht.relay"

    /// Stores `value` for `account`, replacing any existing item. An empty
    /// `value` deletes the item (so clearing the token field forgets it).
    @discardableResult
    static func set(_ value: String, account: String) -> Bool {
        if value.isEmpty {
            delete(account: account)
            return true
        }
        guard let data = value.data(using: .utf8) else { return false }
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        // Replace: delete-then-add keeps the call idempotent without juggling
        // SecItemUpdate's attributes-vs-query split.
        SecItemDelete(query as CFDictionary)
        var attrs = query
        attrs[kSecValueData as String] = data
        attrs[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        return SecItemAdd(attrs as CFDictionary, nil) == errSecSuccess
    }

    /// Returns the stored secret for `account`, or "" when absent.
    static func get(account: String) -> String {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data,
              let value = String(data: data, encoding: .utf8) else {
            return ""
        }
        return value
    }

    /// Removes the stored secret for `account` (no-op if absent).
    static func delete(account: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(query as CFDictionary)
    }
}
