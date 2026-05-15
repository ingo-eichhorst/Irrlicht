import Foundation
import Security

/// Provides the current macOS Focus / Do Not Disturb state so notification
/// emitters can suppress sound (and TTS) alongside the system-suppressed
/// banner. Conforms to `Sendable` because the `UNUserNotificationCenter`
/// delegate's `willPresent` runs nonisolated.
protocol FocusStateProviding: AnyObject, Sendable {
    var isFocusActive: Bool { get }
}

/// Reads Focus state via `INFocusStatusCenter` (Intents framework, macOS 12+).
///
/// **Why the dynamic-dispatch dance.** Statically referencing `INFocusStatusCenter`
/// (i.e. `import Intents` + direct API calls) makes the linker pull in
/// `Intents.framework`. macOS then preflights TCC for `kTCCServiceListenEvent`
/// at process startup — *before any of our code runs*. On an ad-hoc-signed
/// binary that preflight aborts the process with
/// `__TCC_CRASHING_DUE_TO_PRIVACY_VIOLATION__`, reporting that
/// `NSFocusStatusUsageDescription` is missing (regardless of whether the key
/// is actually in `Info.plist`). This is what killed v0.4.3 on every end
/// user's first launch.
///
/// The fix: never reference Intents APIs statically. We resolve `INFocusStatusCenter`
/// through `NSClassFromString` *and only if the binary is Developer-ID-signed*,
/// gated by `isDeveloperIDSigned` below. On ad-hoc builds the Intents framework
/// is never loaded, so the TCC preflight never fires.
///
/// **When Developer ID lands** (tracked under #233), restoring the real Focus
/// read is automatic: the runtime gate flips to `true`, `loadIntents()` succeeds,
/// and `isFocusActive` returns the live state. No new release is required to
/// turn the feature on once a DevID-signed build hits users' machines. The
/// follow-up issue tracking the full restoration is #357.
final class FocusMonitor: FocusStateProviding, @unchecked Sendable {
    /// True when running as a real .app (production or dev bundle); false in
    /// xctest. The xctest host has no `.app` suffix on its bundle path, so we
    /// use that to skip Intents-framework calls — those crashed the test
    /// runner with signal 6 during SessionManager setUp when `FocusMonitor()`
    /// was constructed back-to-back across tests.
    private static let isAppContext: Bool = Bundle.main.bundlePath.hasSuffix(".app")

    /// Cached at init so every `isFocusActive` read is a plain ObjC dispatch
    /// without re-resolving the class. `nil` on ad-hoc builds (intentional —
    /// we never load Intents), or if NSClassFromString fails.
    private let intentsCenter: NSObject?

    init() {
        guard Self.isAppContext else {
            print("🌙 FocusMonitor: non-app context, isFocusActive will return false")
            self.intentsCenter = nil
            return
        }
        guard Self.isDeveloperIDSigned else {
            print("🌙 FocusMonitor: ad-hoc signed — Focus suppression disabled until Developer-ID lands (#357).")
            self.intentsCenter = nil
            return
        }
        self.intentsCenter = Self.resolveFocusStatusCenter()
        if intentsCenter == nil {
            print("🌙 FocusMonitor: INFocusStatusCenter class not resolvable; suppression disabled.")
        }
    }

    /// Synchronous live read. `INFocusStatusCenter.default.focusStatus` is
    /// process-safe and cheap; no caching needed for the rate at which we
    /// emit notifications. Returns `false` whenever the dynamic resolve
    /// didn't succeed (xctest, ad-hoc build, or unexpected runtime state).
    var isFocusActive: Bool {
        guard let center = intentsCenter else { return false }
        // Equivalent of `center.focusStatus.isFocused ?? false`, dispatched via ObjC.
        // We deliberately use `value(forKey:)` so this whole file compiles without
        // `import Intents`. If Apple ever renames the property the worst case is
        // a `nil` return (== isFocusActive: false), not a crash.
        guard let focusStatus = center.value(forKey: "focusStatus") as? NSObject else { return false }
        guard let isFocused = focusStatus.value(forKey: "isFocused") as? Bool else { return false }
        return isFocused
    }

    // MARK: - DevID gate + dynamic Intents resolution

    /// True iff the running binary has a Developer-ID Application code-signing
    /// identity. Ad-hoc (Signature=adhoc, TeamIdentifier="not set") returns
    /// false. Cached because the SecCode lookup is cheap-but-not-free and the
    /// answer doesn't change for the life of the process.
    private static let isDeveloperIDSigned: Bool = checkDeveloperIDSignature()

    private static func checkDeveloperIDSignature() -> Bool {
        var staticCode: SecStaticCode?
        let mainBundleURL = Bundle.main.bundleURL as CFURL
        let status = SecStaticCodeCreateWithPath(mainBundleURL, [], &staticCode)
        guard status == errSecSuccess, let code = staticCode else { return false }

        var info: CFDictionary?
        let infoStatus = SecCodeCopySigningInformation(code, SecCSFlags(rawValue: kSecCSSigningInformation), &info)
        guard infoStatus == errSecSuccess, let infoDict = info as? [String: Any] else { return false }

        // TeamIdentifier is set for Developer-ID-signed binaries. Ad-hoc binaries
        // either lack the key or store "not set" / empty. Apple's own signing
        // (system frameworks) also has a team identifier, but we're inspecting
        // *our* bundle so that's not relevant here.
        guard let team = infoDict["teamid"] as? String, !team.isEmpty, team != "not set" else {
            return false
        }
        // Confirm via the certificate-chain leaf CN. Developer-ID Application
        // certificates have CN beginning with "Developer ID Application:".
        if let certs = infoDict["certificates"] as? [SecCertificate], let leaf = certs.first {
            var commonName: CFString?
            if SecCertificateCopyCommonName(leaf, &commonName) == errSecSuccess,
               let cn = commonName as String?,
               cn.hasPrefix("Developer ID Application:") {
                return true
            }
        }
        return false
    }

    /// Looks up `INFocusStatusCenter` at runtime and returns its `default`
    /// instance, or `nil` if anything in the chain is unavailable. Only called
    /// from the DevID-signed branch of `init`, so on ad-hoc builds Intents.framework
    /// is never touched and the dynamic linker never loads it.
    private static func resolveFocusStatusCenter() -> NSObject? {
        guard let cls = NSClassFromString("INFocusStatusCenter") as? NSObject.Type else { return nil }
        // `+default` is a class method returning the singleton. ObjC dispatch.
        return cls.value(forKey: "default") as? NSObject
    }
}
