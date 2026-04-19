import ApplicationServices
import Foundation

/// macOS Accessibility permission check shared by AX-based activators.
/// First call with a non-trusted process triggers the system prompt;
/// subsequent calls short-circuit.
enum AccessibilityPermission {
    /// Returns true when the current process has been granted
    /// Accessibility access. When the user hasn't yet decided, shows the
    /// system prompt (controlled by `kAXTrustedCheckOptionPrompt`).
    static func ensureTrusted() -> Bool {
        let key = kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String
        let opts = [key: true] as CFDictionary
        return AXIsProcessTrustedWithOptions(opts)
    }
}
