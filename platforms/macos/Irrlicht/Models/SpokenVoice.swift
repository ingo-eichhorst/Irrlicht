import AVFoundation
import Foundation

/// Which voice the "Speak aloud" notification choice uses.
///
/// - `.default` uses whatever `AVSpeechSynthesisVoice(language: "en-US")`
///   returns on the host system.
/// - `.female` targets **Zoe**, a premium female macOS voice. Compact
///   quality on a stock install; premium when the user downloads it via
///   System Settings → Accessibility → Spoken Content → System Voice →
///   Manage Voices.
/// - `.male` targets **Jamie**, a premium male macOS voice.
///
/// `SoundPlayer.voice(for:)` walks the installed voices and picks the
/// highest-quality match for the canonical name, so installing the
/// premium variant later upgrades speech with no app change.
enum SpokenVoice: String, CaseIterable {
    case `default`
    case female
    case male

    var displayName: String {
        switch self {
        case .default: return "Default"
        case .female:  return "Zoe, Premium"
        case .male:    return "Jamie, Premium"
        }
    }

    /// The Apple voice name to look up. `nil` for `.default` (which means
    /// "use whatever `AVSpeechSynthesisVoice(language: "en-US")` returns").
    var canonicalName: String? {
        switch self {
        case .default: return nil
        case .female:  return "Zoe"
        case .male:    return "Jamie"
        }
    }

    /// Whether an installed voice's reported name refers to this voice.
    /// macOS 26.x suffixes the quality into the name — "Zoe (Premium)" —
    /// where earlier releases reported bare "Zoe", so accept both shapes.
    /// The " (" requirement keeps near-miss names (e.g. "Zoey") out.
    /// `.default` has no canonical name and matches nothing — callers
    /// (`isInstalled`, `SoundPlayer.voice(for:)`) special-case it first.
    func matches(installedName: String) -> Bool {
        guard let name = canonicalName else { return false }
        return installedName == name || installedName.hasPrefix(name + " (")
    }

    /// Whether a voice matching `canonicalName` is installed at any
    /// quality. Drives the "Install voice…" affordance in Settings.
    /// `.default` always reports `true` because the system-language voice
    /// is always available.
    ///
    /// - Important: calls `AVSpeechSynthesisVoice.speechVoices()`, which boots
    ///   the TextToSpeech/AXSpeech subsystem and does per-voice ICU locale work.
    ///   It is far too slow for a SwiftUI `body` — doing so dropped the Settings
    ///   panel to ~2fps (issue #729). Resolve it off the main thread (e.g. a
    ///   `.task` + `Task.detached`) and render from cached state; never in `body`.
    var isInstalled: Bool {
        guard canonicalName != nil else { return true }
        return AVSpeechSynthesisVoice.speechVoices().contains { matches(installedName: $0.name) }
    }
}
