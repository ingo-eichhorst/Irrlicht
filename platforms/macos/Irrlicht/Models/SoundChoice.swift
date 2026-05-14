import Foundation

/// What a user hears when a notification event fires. Encoded as a single
/// `String` so it can ride on `@AppStorage` directly.
///
/// Raw-value grammar:
///   - built-in / mode tokens: `"ping"`, `"chime"`, `"funk"`, `"whoosh"`,
///     `"sosumi"`, `"none"`, `"speak"`
///   - custom file: `"custom:<installedFilename>|<displayPath>"`
///     `installedFilename` is the basename inside `~/Library/Sounds/`;
///     `displayPath` is the user-facing source path shown in the UI.
enum SoundChoice: Hashable {
    case ping
    case chime
    case funk
    case whoosh
    case sosumi
    case none
    case speak
    case custom(installedFilename: String, displayPath: String)

    static let `default`: SoundChoice = .ping

    static let builtIns: [SoundChoice] = [.ping, .chime, .funk, .whoosh, .sosumi]

    var displayName: String {
        switch self {
        case .ping: return "Ping"
        case .chime: return "Chime"
        case .funk: return "Funk"
        case .whoosh: return "Whoosh"
        case .sosumi: return "Sosumi"
        case .none: return "None"
        case .speak: return "Speak aloud"
        case .custom(_, let displayPath):
            return "Custom: \((displayPath as NSString).lastPathComponent)"
        }
    }

    /// macOS system-sound filename (in `/System/Library/Sounds`) that backs
    /// each built-in choice. `nil` for non-audio choices.
    var systemSoundFilename: String? {
        switch self {
        case .ping: return "Ping.aiff"
        case .chime: return "Glass.aiff"
        case .funk: return "Funk.aiff"
        case .whoosh: return "Blow.aiff"
        case .sosumi: return "Sosumi.aiff"
        default: return nil
        }
    }

    /// Basename `UNNotificationSound(named:)` should resolve.
    /// `nil` means "no audible alert" (.none / .speak — speak is handled
    /// separately via AVSpeechSynthesizer).
    var notificationSoundName: String? {
        switch self {
        case .none, .speak:
            return nil
        case .custom(let installedFilename, _):
            return installedFilename
        default:
            return systemSoundFilename
        }
    }

    /// File URL used for in-app preview playback via `NSSound(contentsOf:)`.
    var previewURL: URL? {
        switch self {
        case .ping, .chime, .funk, .whoosh, .sosumi:
            guard let name = systemSoundFilename else { return nil }
            return URL(fileURLWithPath: "/System/Library/Sounds/\(name)")
        case .custom(let installedFilename, _):
            let library = FileManager.default.urls(for: .libraryDirectory, in: .userDomainMask).first
            return library?.appendingPathComponent("Sounds").appendingPathComponent(installedFilename)
        case .none, .speak:
            return nil
        }
    }
}

extension SoundChoice: RawRepresentable {
    init?(rawValue: String) {
        switch rawValue {
        case "ping": self = .ping
        case "chime": self = .chime
        case "funk": self = .funk
        case "whoosh": self = .whoosh
        case "sosumi": self = .sosumi
        case "none": self = .none
        case "speak": self = .speak
        default:
            guard rawValue.hasPrefix("custom:") else { return nil }
            let payload = String(rawValue.dropFirst("custom:".count))
            let parts = payload.split(separator: "|", maxSplits: 1, omittingEmptySubsequences: false)
            guard parts.count == 2 else { return nil }
            let installed = String(parts[0])
            let display = String(parts[1])
            guard !installed.isEmpty else { return nil }
            self = .custom(installedFilename: installed, displayPath: display)
        }
    }

    var rawValue: String {
        switch self {
        case .ping: return "ping"
        case .chime: return "chime"
        case .funk: return "funk"
        case .whoosh: return "whoosh"
        case .sosumi: return "sosumi"
        case .none: return "none"
        case .speak: return "speak"
        case .custom(let installed, let display):
            return "custom:\(installed)|\(display)"
        }
    }
}
