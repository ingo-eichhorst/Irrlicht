import Foundation

/// The menu-bar status item's persisted identity (issue #1845).
///
/// macOS has no public API to place a status item at an absolute position.
/// The user reorders it with a Cmd-drag, and AppKit persists that position in
/// the app's own defaults domain under
/// `NSStatusItem Preferred Position <autosaveName>`.
///
/// **What the app did before this type existed.** It never set
/// `NSStatusItem.autosaveName`, so AppKit generated one. Measured on a real
/// install with `defaults read io.irrlicht.app`, the app's domain carried:
///
///     "NSStatusItem Preferred Position Item-0" = 298;
///
/// `Item-0` is AppKit's generated name for the first status item an app
/// creates, and this app creates exactly one (`MenuBarController.swift` holds
/// the only `NSStatusBar.system.statusItem(...)` call in the tree). So the
/// position was already being persisted — under a name derived from creation
/// order rather than from anything this app declares.
///
/// **Why that is worth changing.** The generated name is stable only for as
/// long as this app keeps creating exactly one status item, in the same
/// order. A second status item added later would renumber it, and the user's
/// dragged position would be silently orphaned. Naming the item makes the key
/// a property of the app rather than an accident of its startup sequence.
///
/// **Why the migration below is not optional.** Setting an `autosaveName`
/// changes which key AppKit reads. Without a migration, every existing
/// install's `Item-0` position would be orphaned on the very first launch
/// after upgrading, and the icon would jump — exactly the "existing installs
/// see no change until they opt in" criterion this issue set. So the first
/// launch under the new name copies the old value across, once.
enum MenuBarStatusItemIdentity {
    /// The stable name this app declares for its one status item.
    static let autosaveName = "IrrlichtStatusItem"

    /// AppKit's defaults key for a named status item's user-dragged position.
    /// The literal prefix (including the spaces) is AppKit's, not ours.
    static func preferredPositionKey(forAutosaveName name: String) -> String {
        "NSStatusItem Preferred Position \(name)"
    }

    /// The key AppKit generated for this app's status item before it had a
    /// declared name. Observed on a real install — see the type's doc.
    static let legacyAutosaveName = "Item-0"

    /// Carry an existing install's dragged position over to the declared
    /// name, once.
    ///
    /// Must run before `autosaveName` is assigned on the status item: that
    /// assignment is what points AppKit at the new key, so the value has to
    /// be in place first. (Creating the item is not itself the deadline —
    /// an unnamed item still reads the generated key — but the two happen
    /// one line apart, and ordering against the earlier of the two is the
    /// safer thing to pin.)
    ///
    /// Runs only when the new key is absent AND the legacy key is present, so
    /// it is a no-op on a fresh install (nothing to carry), on every launch
    /// after the first (the new key now exists), and for a user who never
    /// dragged the icon at all. It never overwrites a position the user set
    /// under the new name.
    ///
    /// Takes the store rather than reaching for `UserDefaults.standard`, both
    /// so it is testable against a double and because `PersistentDefaultsLintTests`
    /// requires exactly that seam for any write in the app target.
    ///
    /// - Returns: whether a value was actually carried over, so a caller (or a
    ///   test) can tell "migrated" apart from "nothing to migrate".
    @discardableResult
    static func migrateLegacyPreferredPosition(in defaults: UserDefaults) -> Bool {
        let currentKey = preferredPositionKey(forAutosaveName: autosaveName)
        let legacyKey = preferredPositionKey(forAutosaveName: legacyAutosaveName)

        guard defaults.object(forKey: currentKey) == nil,
              let legacyValue = defaults.object(forKey: legacyKey) else {
            return false
        }

        defaults.set(legacyValue, forKey: currentKey)
        return true
    }
}
