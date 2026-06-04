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
        case .speak(let voice):
            speak(sampleText, voice: voice)
        default:
            guard let url = choice.previewURL else { return }
            NSSound(contentsOf: url, byReference: true)?.play()
        }
    }

    /// Synthesizes a single utterance with the given voice. Long-lived
    /// synthesizer keeps utterances from being cut short when the local
    /// reference goes out of scope.
    static func speak(_ text: String, voice spoken: SpokenVoice = .default) {
        let utterance = AVSpeechUtterance(string: text)
        utterance.voice = voice(for: spoken)
        synthesizer.speak(utterance)
    }

    /// Speaks `title`, then `body`, with a short pause between. The pause
    /// gives the listener a beat to register what's about to be detailed —
    /// "Agent ready" then "<project> (<branch>)" lands better than running
    /// them together as one sentence.
    static func speak(title: String, body: String, voice spoken: SpokenVoice = .default) {
        let v = voice(for: spoken)
        let titleUtterance = AVSpeechUtterance(string: title)
        titleUtterance.voice = v
        let bodyUtterance = AVSpeechUtterance(string: body)
        bodyUtterance.voice = v
        bodyUtterance.preUtteranceDelay = 0.4
        synthesizer.speak(titleUtterance)
        synthesizer.speak(bodyUtterance)
    }

    /// Resolves a SpokenVoice to a concrete AVSpeechSynthesisVoice. Picks
    /// the highest-installed quality (premium > enhanced > default) for the
    /// canonical name, so once the user downloads Zoe/Jamie Premium in
    /// System Settings the upgrade is automatic. Matching goes through
    /// `SpokenVoice.matches(installedName:)` because newer macOS reports
    /// names with the quality suffixed ("Zoe (Premium)"). Falls back to
    /// the system en-US voice when no name match exists at all.
    static func voice(for spoken: SpokenVoice) -> AVSpeechSynthesisVoice? {
        guard spoken.canonicalName != nil else {
            return AVSpeechSynthesisVoice(language: "en-US")
        }
        if let best = AVSpeechSynthesisVoice.speechVoices()
            .filter({ spoken.matches(installedName: $0.name) })
            .max(by: { $0.quality.rawValue < $1.quality.rawValue }) {
            return best
        }
        return AVSpeechSynthesisVoice(language: "en-US")
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
                try? FileManager.default.removeItem(at: destURL)
                try FileManager.default.copyItem(at: srcURL, to: destURL)
                main(.success(destName))
            } catch {
                main(.failure(InstallError.writeFailed(error.localizedDescription)))
            }
            return
        }

        // mp3 / m4a → transcode to LPCM-in-CAF. UNNotificationSound only plays
        // PCM/MA4/µLaw/aLaw packaged in aiff/wav/caf — it will refuse AAC, so
        // we must decode to PCM rather than passthrough the source codec.
        let destName = transcodeFilename(for: event)
        let destURL = destDir.appendingPathComponent(destName)
        do {
            try removeStaleVariants(for: event, in: destDir, keeping: destName)
            try? FileManager.default.removeItem(at: destURL)
        } catch {
            main(.failure(InstallError.writeFailed(error.localizedDescription)))
            return
        }

        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try transcodeToLPCMCAF(src: srcURL, dest: destURL)
                main(.success(destName))
            } catch {
                main(.failure(InstallError.exportFailed(error.localizedDescription)))
            }
        }
    }

    /// Decodes `src` (any AVFoundation-readable audio format — mp3, m4a, etc.)
    /// and writes LPCM into a `.caf` container at `dest`. CAF + LPCM is
    /// the combination UNNotificationSound plays reliably.
    ///
    /// The output mirrors the source's *processing format* (typically
    /// 32-bit float PCM) rather than forcing a fixed Int16/interleaved
    /// layout — forcing one combination produced
    /// `com.apple.coreaudio.avfaudio -50` (paramErr) for mp3s whose
    /// decoded frame layout didn't match. Float32 PCM is still LPCM and
    /// UNNotificationSound accepts it.
    ///
    /// Streams in 4096-frame chunks so a 10 MB mp3 doesn't allocate its
    /// full PCM expansion in memory.
    private static func transcodeToLPCMCAF(src: URL, dest: URL) throws {
        let sourceFile = try AVAudioFile(forReading: src)
        let processingFormat = sourceFile.processingFormat

        let outputFile = try AVAudioFile(
            forWriting: dest,
            settings: processingFormat.settings,
            commonFormat: processingFormat.commonFormat,
            interleaved: processingFormat.isInterleaved
        )

        let chunkFrames: AVAudioFrameCount = 4096
        guard let buffer = AVAudioPCMBuffer(pcmFormat: processingFormat, frameCapacity: chunkFrames) else {
            throw InstallError.exportFailed("could not allocate PCM buffer")
        }

        while sourceFile.framePosition < sourceFile.length {
            try sourceFile.read(into: buffer)
            if buffer.frameLength == 0 { break }
            try outputFile.write(from: buffer)
        }
    }

    /// Output filename for the transcode branch: always `.caf` because we
    /// always emit LPCM-in-CAF. Kept inside SoundPlayer because callers
    /// outside the transcode flow shouldn't need to know the extension.
    private static func transcodeFilename(for event: NotificationEvent) -> String {
        "IrrlichtCustom-\(event.rawValue).caf"
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
