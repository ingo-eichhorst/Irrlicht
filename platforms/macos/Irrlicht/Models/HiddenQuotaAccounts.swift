import Foundation

/// The quota accounts the user has hidden (#2163): which per-account chips the
/// popover leaves out, and which accounts a provider's menu-bar slot may not
/// pick. Only the display is filtered. A hidden account's sessions keep
/// their state and cost, and the session list ignores this preference.
///
/// **It stores what is HIDDEN, not what is shown**, so "nothing stored" means
/// "everything shown". A user who never opens the setting sees no change,
/// and an account seen for the first time starts out shown.
///
/// The keys are `RateLimitInfo.accountBucketKey` values, `<provider>|<ref>`
/// or `<provider>|unconfirmed`. That is the key the popover chips and the
/// menu-bar pick already bucket by, so a key names exactly one chip. Each key
/// maps to the tier label the account showed when it was hidden ("" when it
/// had none). A hidden account that has stopped reporting has no live
/// snapshot to read a label from, and its Settings row still needs one.
///
/// **Stored as a JSON object in a `String`, not as `MenuBarQuotaProviders`'
/// comma-joined list.** A ref is provider-issued, so a delimiter this type
/// picks cannot be ruled out of it, and JSON escapes any character. It is a
/// `String` because `[String: String]` is not an `@AppStorage` type, and
/// `PersistentDefaultsLintTests` bans a `UserDefaults.standard` receiver under
/// `Irrlicht/Views/`, so the Settings rows can only reach an `@AppStorage`.
///
/// The web dashboard stores the same JSON shape under the same key name in
/// `localStorage` (`platforms/web/quotaChips.js`). The two stores are not
/// shared: hiding an account on one client does not hide it on the other.
enum HiddenQuotaAccounts {
    static let storageKey = "hiddenQuotaAccounts"

    /// Bucket key → last-seen tier label. A missing, empty or malformed value,
    /// or one that is not a JSON object, decodes as "nothing hidden", which
    /// is the default. A non-string label decodes as "".
    static func decode(_ raw: String) -> [String: String] {
        guard !raw.isEmpty,
              let data = raw.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return [:]
        }
        var hidden: [String: String] = [:]
        for (key, value) in object where !key.isEmpty {
            hidden[key] = value as? String ?? ""
        }
        return hidden
    }

    /// An empty map encodes as "", the same value as "never set". Keys are
    /// sorted so one map always encodes to one string.
    static func encode(_ hidden: [String: String]) -> String {
        guard !hidden.isEmpty,
              let data = try? JSONSerialization.data(withJSONObject: hidden, options: [.sortedKeys]),
              let raw = String(data: data, encoding: .utf8) else {
            return ""
        }
        return raw
    }

    static func current(in defaults: UserDefaults) -> [String: String] {
        decode(defaults.string(forKey: storageKey) ?? "")
    }

    /// The one filter every display consumer applies AFTER bucketing (#2163):
    /// the popover chips (`SessionListView.quotaChipData`) and the menu-bar
    /// pick (`QuotaMenuBarRenderer.selectedSnapshot`). `key` returns an item's
    /// account bucket key.
    ///
    /// The Settings rows deliberately do not call it. A row is how a hidden
    /// account gets shown again, so the row list has to include hidden
    /// accounts.
    static func visible<Item>(_ items: [Item], hidden: Set<String>, key: (Item) -> String) -> [Item] {
        guard !hidden.isEmpty else { return items }
        return items.filter { !hidden.contains(key($0)) }
    }

    /// The provider key and account ref an account bucket key was built from,
    /// for a hidden account with no live snapshot to read them off. The inverse
    /// of `RateLimitInfo.accountBucketKey`: split at the FIRST `|`, because the
    /// provider half is a provider name or `unknown:<adapter>`, while the ref
    /// half is provider-issued. `unconfirmed` reads back as the empty ref.
    static func parts(ofBucketKey key: String) -> (providerKey: String, accountRef: String) {
        guard let bar = key.firstIndex(of: "|") else { return (key, "") }
        let ref = String(key[key.index(after: bar)...])
        return (String(key[..<bar]), ref == "unconfirmed" ? "" : ref)
    }
}
