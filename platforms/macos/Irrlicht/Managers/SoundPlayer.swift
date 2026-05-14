import AVFoundation
import AppKit
import Foundation

/// Plays preview sounds, installs custom audio files into `~/Library/Sounds/`
/// (transcoding mp3/m4a to CAF so `UNNotificationSound` can resolve them),
/// and drives text-to-speech for the "Speak aloud" choice.
enum SoundPlayer {
    enum InstallError: Error, LocalizedError {
        case unreadable
        case unsupportedFormat
        case tooLarge
        case exportFailed(String)
        case writeFailed(String)

        var errorDescription: String? {
            switch self {
            case .unreadable: return "Selected file could not be read."
            case .unsupportedFormat: return "Unsupported audio format."
            case .tooLarge: return "File is larger than 10 MB."
            case .exportFailed(let msg): return "Transcode failed: \(msg)"
            case .writeFailed(let msg): return "Could not install sound: \(msg)"
            }
        }
    }

    static let supportedExtensions: Set<String> = ["aiff", "aif", "wav", "caf", "mp3", "m4a"]
    private static let passthroughExtensions: Set<String> = ["aiff", "aif", "wav", "caf"]
    private static let maxBytes: Int64 = 10 * 1024 * 1024

    /// Plays the preview audio (or speaks a sample phrase) for the given
    /// choice. Returns immediately; playback runs on the system audio thread.
    static func preview(_ choice: SoundChoice, sampleText: String = "Agent ready") {
        switch choice {
        case .none:
            return
        case .speak:
            speak(sampleText)
        default:
            guard let url = choice.previewURL else { return }
            NSSound(contentsOf: url, byReference: true)?.play()
        }
    }

    /// Synthesizes speech. Holds onto a long-lived synthesizer so utterances
    /// aren't cut off when the local goes out of scope.
    static func speak(_ text: String) {
        let utterance = AVSpeechUtterance(string: text)
        // Notification titles/bodies are produced in English elsewhere in
        // the app, so pin the voice to en-US regardless of the system speech
        // locale. If the voice catalogue lacks en-US, AVSpeechSynthesisVoice
        // returns nil and the synthesizer falls back to its default voice.
        utterance.voice = AVSpeechSynthesisVoice(language: "en-US")
        synthesizer.speak(utterance)
    }

    /// Copies (or transcodes) `srcURL` into `~/Library/Sounds/` under the
    /// canonical filename for `event`, returning the installed basename.
    ///
    /// The completion fires on the main thread.
    static func installCustom(
        srcURL: URL,
        event: NotificationEvent,
        completion: @escaping (Result<String, Error>) -> Void
    ) {
        let main: (Result<String, Error>) -> Void = { result in
            DispatchQueue.main.async { completion(result) }
        }

        let ext = srcURL.pathExtension.lowercased()
        guard supportedExtensions.contains(ext) else {
            main(.failure(InstallError.unsupportedFormat))
            return
        }

        let attrs = try? FileManager.default.attributesOfItem(atPath: srcURL.path)
        if let size = attrs?[.size] as? NSNumber, size.int64Value > maxBytes {
            main(.failure(InstallError.tooLarge))
            return
        }
        guard FileManager.default.isReadableFile(atPath: srcURL.path) else {
            main(.failure(InstallError.unreadable))
            return
        }

        let destDir: URL
        do {
            destDir = try soundsDirectory()
        } catch {
            main(.failure(error))
            return
        }

        if passthroughExtensions.contains(ext) {
            let destName = "IrrlichtCustom-\(event.rawValue).\(ext)"
            let destURL = destDir.appendingPathComponent(destName)
            do {
                try removeStaleVariants(for: event, in: destDir, keeping: destName)
                if FileManager.default.fileExists(atPath: destURL.path) {
                    try FileManager.default.removeItem(at: destURL)
                }
                try FileManager.default.copyItem(at: srcURL, to: destURL)
                main(.success(destName))
            } catch {
                main(.failure(InstallError.writeFailed(error.localizedDescription)))
            }
            return
        }

        // mp3 / m4a → transcode to .caf via AVAssetExportSession.
        let destName = event.customFilename // "IrrlichtCustom-<event>.caf"
        let destURL = destDir.appendingPathComponent(destName)
        do {
            try removeStaleVariants(for: event, in: destDir, keeping: destName)
            if FileManager.default.fileExists(atPath: destURL.path) {
                try FileManager.default.removeItem(at: destURL)
            }
        } catch {
            main(.failure(InstallError.writeFailed(error.localizedDescription)))
            return
        }

        let asset = AVURLAsset(url: srcURL)
        guard let session = AVAssetExportSession(asset: asset, presetName: AVAssetExportPresetAppleM4A) else {
            main(.failure(InstallError.exportFailed("no exporter")))
            return
        }
        // `.caf` accepts AAC payload; UNNotificationSound plays CAF reliably.
        session.outputFileType = .caf
        session.outputURL = destURL
        session.exportAsynchronously {
            switch session.status {
            case .completed:
                main(.success(destName))
            case .failed, .cancelled:
                main(.failure(InstallError.exportFailed(session.error?.localizedDescription ?? "unknown")))
            default:
                main(.failure(InstallError.exportFailed("unexpected status \(session.status.rawValue)")))
            }
        }
    }

    /// Resolves `~/Library/Sounds/`, creating it if necessary.
    static func soundsDirectory() throws -> URL {
        let library = try FileManager.default.url(
            for: .libraryDirectory,
            in: .userDomainMask,
            appropriateFor: nil,
            create: false
        )
        let sounds = library.appendingPathComponent("Sounds", isDirectory: true)
        if !FileManager.default.fileExists(atPath: sounds.path) {
            try FileManager.default.createDirectory(at: sounds, withIntermediateDirectories: true)
        }
        return sounds
    }

    /// Wipes any previously-installed custom file for `event` whose extension
    /// differs from the one we're about to write, so a user re-picking
    /// `.mp3` after `.wav` doesn't leave a stale `.wav` behind.
    private static func removeStaleVariants(for event: NotificationEvent, in dir: URL, keeping name: String) throws {
        let prefix = "IrrlichtCustom-\(event.rawValue)."
        let contents = (try? FileManager.default.contentsOfDirectory(atPath: dir.path)) ?? []
        for entry in contents where entry.hasPrefix(prefix) && entry != name {
            try FileManager.default.removeItem(at: dir.appendingPathComponent(entry))
        }
    }

    private static let synthesizer = AVSpeechSynthesizer()
}
