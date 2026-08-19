import AVFoundation
import UserNotifications
import XCTest
@testable import Irrlicht

final class SoundPlayerTests: XCTestCase {

    // MARK: - resolveNotificationSound

    /// The sound choice is read from a store this test owns, not from
    /// `UserDefaults.standard` (#1662) — which under `swift test` is the
    /// developer's real `com.apple.dt.xctest.tool` domain, and which these
    /// three cases wrote and then cleaned up in a `defer` that #1523's aborts,
    /// the 240s tree kill and `--budget` all skip.
    func testResolveBuiltInProducesNamedSound() throws {
        let defaults = InMemoryDefaults()
        let event = NotificationEvent.ready
        defaults.set(SoundChoice.chime.rawValue, forKey: event.soundKey)

        let sound = SessionManager.resolveNotificationSound(for: event, defaults: defaults)
        XCTAssertNotNil(sound, "built-in choice should produce a UNNotificationSound")
    }

    func testResolveNoneAndSpeakReturnNil() {
        let defaults = InMemoryDefaults()
        let event = NotificationEvent.waiting

        defaults.set(SoundChoice.none.rawValue, forKey: event.soundKey)
        XCTAssertNil(SessionManager.resolveNotificationSound(for: event, defaults: defaults))

        defaults.set(SoundChoice.speak(.default).rawValue, forKey: event.soundKey)
        XCTAssertNil(SessionManager.resolveNotificationSound(for: event, defaults: defaults))
    }

    // MARK: - SpokenVoice / voice(for:)

    func testSpokenVoiceRawValueRoundTrip() {
        for v in SpokenVoice.allCases {
            XCTAssertEqual(SpokenVoice(rawValue: v.rawValue), v)
        }
    }

    func testVoiceForEachSpokenVoiceIsNonNil() {
        // Every variant must resolve to a usable voice: when the canonical
        // name (Zoe / Jamie) is installed we get the best-quality variant of
        // it; when it isn't, we fall back to AVSpeechSynthesisVoice(
        // language: "en-US") which is always present.
        for v in SpokenVoice.allCases {
            XCTAssertNotNil(SoundPlayer.voice(for: v), "voice missing for \(v)")
        }
    }

    func testMatchesAcceptsBareAndQualitySuffixedNames() {
        // macOS 26.x reports premium/enhanced voices with the quality in
        // the name ("Zoe (Premium)"); older releases report bare "Zoe".
        XCTAssertTrue(SpokenVoice.female.matches(installedName: "Zoe"))
        XCTAssertTrue(SpokenVoice.female.matches(installedName: "Zoe (Premium)"))
        XCTAssertTrue(SpokenVoice.female.matches(installedName: "Zoe (Enhanced)"))
        XCTAssertTrue(SpokenVoice.male.matches(installedName: "Jamie"))
        XCTAssertTrue(SpokenVoice.male.matches(installedName: "Jamie (Premium)"))
    }

    func testMatchesRejectsOtherNames() {
        XCTAssertFalse(SpokenVoice.female.matches(installedName: "Zoey"),
            "near-miss names must not match")
        XCTAssertFalse(SpokenVoice.female.matches(installedName: "Jamie (Premium)"))
        XCTAssertFalse(SpokenVoice.male.matches(installedName: "Zoe (Premium)"))
        XCTAssertFalse(SpokenVoice.default.matches(installedName: "Zoe"),
            ".default has no canonical name and matches nothing")
    }

    func testResolveMissingCustomFallsBackToPing() {
        let defaults = InMemoryDefaults()
        let event = NotificationEvent.contextPressure
        let missing = SoundChoice.custom(installedFilename: "IrrlichtCustom-doesnotexist.caf", displayPath: "/tmp/x.mp3")  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        defaults.set(missing.rawValue, forKey: event.soundKey)

        // We can't introspect UNNotificationSound's name, so the assertion is
        // limited to "produces a non-nil fallback rather than crashing or
        // returning nil." Combined with the explicit Ping branch in
        // resolveNotificationSound, this guards against the regression.
        let sound = SessionManager.resolveNotificationSound(for: event, defaults: defaults)
        XCTAssertNotNil(sound, "missing custom should fall back to Ping, not nil")
    }

    // MARK: - installCustom

    func testInstallCustomPassthroughCopiesAiff() throws {
        let src = URL(fileURLWithPath: "/System/Library/Sounds/Glass.aiff")  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        try XCTSkipUnless(FileManager.default.fileExists(atPath: src.path), "system Glass.aiff missing")

        let installed = try waitInstall(src: src, event: .ready)
        defer { try? FileManager.default.removeItem(at: try SoundPlayer.soundsDirectory().appendingPathComponent(installed)) }

        XCTAssertEqual(installed, "IrrlichtCustom-ready.aiff")
        let dest = try SoundPlayer.soundsDirectory().appendingPathComponent(installed)
        XCTAssertTrue(FileManager.default.fileExists(atPath: dest.path))
    }

    func testInstallCustomRejectsUnsupportedExtension() throws {
        let tmp = FileManager.default.temporaryDirectory.appendingPathComponent("not-audio-\(UUID().uuidString).txt")
        try "junk".write(to: tmp, atomically: true, encoding: .utf8)
        defer { try? FileManager.default.removeItem(at: tmp) }

        let result = waitInstallResult(src: tmp, event: .ready)
        switch result {
        case .success(let name):
            XCTFail("expected failure, got success(\(name))")
        case .failure(let error):
            guard let installError = error as? SoundPlayer.InstallError else {
                XCTFail("wrong error type: \(error)")
                return
            }
            if case .unsupportedFormat = installError { return }
            XCTFail("expected .unsupportedFormat, got \(installError)")
        }
    }

    func testInstallCustomTranscodesM4AToLPCMCAF() throws {
        // Synthesize a short m4a (AAC) at runtime so the test is self-contained.
        // The transcode path must decode AAC and re-encode as 16-bit LPCM in a
        // CAF container — anything else (passthrough, AAC-in-CAF) won't play
        // through UNNotificationSound.
        let m4aSrc = try makeSilentM4A(durationSec: 0.25)
        defer { try? FileManager.default.removeItem(at: m4aSrc) }

        let installed = try waitInstall(src: m4aSrc, event: .ready, timeout: 10)
        let dir = try SoundPlayer.soundsDirectory()
        let destURL = dir.appendingPathComponent(installed)
        defer { try? FileManager.default.removeItem(at: destURL) }

        XCTAssertEqual(installed, "IrrlichtCustom-ready.caf")
        XCTAssertTrue(FileManager.default.fileExists(atPath: destURL.path))

        // Verify the output is actually LPCM (UNNotificationSound's requirement),
        // not AAC-in-CAF or something else.
        let producedFile = try AVAudioFile(forReading: destURL)
        let asbd = producedFile.fileFormat.streamDescription.pointee
        XCTAssertEqual(asbd.mFormatID, kAudioFormatLinearPCM, "transcoded file must be LPCM, got format \(asbd.mFormatID)")
        XCTAssertGreaterThan(producedFile.length, 0, "transcoded file should have audio frames")
    }

    /// Writes a short AAC-encoded m4a file to `tmp` and returns its URL. Used
    /// only to feed `installCustom`'s transcode branch a real non-passthrough
    /// source.
    private func makeSilentM4A(durationSec: Double) throws -> URL {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("silent-\(UUID().uuidString).m4a")
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: 44100.0,
            AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: 32_000,
        ]
        let outputFile = try AVAudioFile(forWriting: url, settings: settings)

        guard let pcmFormat = AVAudioFormat(
            commonFormat: .pcmFormatFloat32,
            sampleRate: 44100,
            channels: 1,
            interleaved: false
        ) else {
            throw NSError(domain: "Test", code: 0, userInfo: [NSLocalizedDescriptionKey: "could not build PCM format"])
        }
        let frames = AVAudioFrameCount(44100 * durationSec)
        guard let buffer = AVAudioPCMBuffer(pcmFormat: pcmFormat, frameCapacity: frames) else {
            throw NSError(domain: "Test", code: 0, userInfo: [NSLocalizedDescriptionKey: "could not build PCM buffer"])
        }
        buffer.frameLength = frames
        // buffer is zero-filled by default → silence.
        try outputFile.write(from: buffer)
        return url
    }

    func testInstallCustomReplacesStaleVariant() throws {
        // First install a .aiff, then install a .wav for the same event and
        // confirm the .aiff is gone.
        let aiffSrc = URL(fileURLWithPath: "/System/Library/Sounds/Pop.aiff")  // NOSONAR (swift:S1075) — test fixture value, not a real endpoint
        try XCTSkipUnless(FileManager.default.fileExists(atPath: aiffSrc.path), "system Pop.aiff missing")

        let installedA = try waitInstall(src: aiffSrc, event: .waiting)
        let dir = try SoundPlayer.soundsDirectory()
        XCTAssertTrue(FileManager.default.fileExists(atPath: dir.appendingPathComponent(installedA).path))

        // Fabricate a tiny .wav by writing a header-less placeholder; installCustom
        // doesn't validate format internals, so a renamed copy is enough.
        let wavSrc = FileManager.default.temporaryDirectory.appendingPathComponent("fake-\(UUID().uuidString).wav")
        try Data(repeating: 0, count: 256).write(to: wavSrc)
        defer { try? FileManager.default.removeItem(at: wavSrc) }

        let installedB = try waitInstall(src: wavSrc, event: .waiting)
        defer { try? FileManager.default.removeItem(at: dir.appendingPathComponent(installedB)) }

        XCTAssertEqual(installedB, "IrrlichtCustom-waiting.wav")
        XCTAssertFalse(
            FileManager.default.fileExists(atPath: dir.appendingPathComponent(installedA).path),
            "stale .aiff should have been removed when the .wav was installed"
        )
    }

    /// Pins the parameter that removes #1523's deadlock: the completion is
    /// delivered on the queue the caller named, not unconditionally on the main
    /// queue. `deliver` is shared by every exit of `installCustom`, so probing
    /// one exit covers them all; this uses the unsupported-extension exit
    /// because it is the only one that writes nothing to `~/Library/Sounds/`.
    ///
    /// Restoring `DispatchQueue.main.async` in `installCustom` must make this
    /// fail rather than hang — which is why it asserts on the semaphore's
    /// result before asserting on the queue.
    func testInstallCustomDeliversCompletionOnCallerQueue() throws {
        let tmp = FileManager.default.temporaryDirectory
            .appendingPathComponent("queue-probe-\(UUID().uuidString).txt")
        try "junk".write(to: tmp, atomically: true, encoding: .utf8)
        defer { try? FileManager.default.removeItem(at: tmp) }

        let queue = DispatchQueue(label: "SoundPlayerTests.callerQueue")
        let key = DispatchSpecificKey<Bool>()
        queue.setSpecific(key: key, value: true)

        let done = DispatchSemaphore(value: 0)
        var onCallerQueue = false
        SoundPlayer.installCustom(srcURL: tmp, event: .ready, completionQueue: queue) { _ in
            onCallerQueue = DispatchQueue.getSpecific(key: key) == true
            done.signal()
        }

        XCTAssertEqual(done.wait(timeout: .now() + 5), .success,
            "installCustom never invoked its completion")
        XCTAssertTrue(onCallerQueue,
            "completion must run on the queue the caller passed, not unconditionally on main")
    }

    // MARK: - helpers

    /// The queue `installCustom`'s completion is delivered on throughout this
    /// suite. Deliberately **not** the main queue: XCTest runs test methods on
    /// the main thread and `waitInstallResult` blocks it until the completion
    /// arrives, so delivering on main is a cycle — the block cannot run until
    /// the test returns, and the test cannot return until the block runs.
    private static let completionQueue = DispatchQueue(label: "SoundPlayerTests.installCustom")

    private func waitInstall(
        src: URL,
        event: NotificationEvent,
        timeout: TimeInterval = 5,
        file: StaticString = #filePath,
        line: UInt = #line
    ) throws -> String {
        switch waitInstallResult(src: src, event: event, timeout: timeout, file: file, line: line) {
        case .success(let name): return name
        case .failure(let error): throw error
        }
    }

    /// Runs `installCustom` and blocks until its completion arrives — or fails
    /// this one test if it does not.
    ///
    /// Three deliberate choices, all of them about #1523's blast radius rather
    /// than about this helper's ergonomics:
    ///
    /// 1. A `DispatchSemaphore`, not `XCTestExpectation`/`XCTWaiter`. A stalled
    ///    `XCTWaiter` is not a failed test: XCTest's stall detector `abort()`s
    ///    the **process**, which drops the eight suites that sort after this one
    ///    and suppresses the aggregate total, while every suite that already
    ///    reported still says "0 failures". A semaphore always returns by its
    ///    deadline, so the worst case is one red test.
    /// 2. `XCTFail` plus a returned `.failure` on timeout, rather than force-
    ///    unwrapping. The previous helper unwrapped a `Result!` that is nil
    ///    exactly when the wait timed out, so the loud local failure it was
    ///    reaching for was in fact a second process-fatal path.
    /// 3. The completion is delivered on `Self.completionQueue` — see there.
    ///
    /// `captured` needs no lock: it is written on `completionQueue` before
    /// `signal()` and read here only after `wait()` returns, and that pair is a
    /// happens-before edge.
    private func waitInstallResult(
        src: URL,
        event: NotificationEvent,
        timeout: TimeInterval = 5,
        file: StaticString = #filePath,
        line: UInt = #line
    ) -> Result<String, Error> {
        let done = DispatchSemaphore(value: 0)
        var captured: Result<String, Error>?
        SoundPlayer.installCustom(srcURL: src, event: event, completionQueue: Self.completionQueue) { result in
            captured = result
            done.signal()
        }

        guard done.wait(timeout: .now() + timeout) == .success, let captured else {
            let message = "installCustom did not invoke its completion within \(timeout)s"
            XCTFail(message, file: file, line: line)
            return .failure(InstallTimeout(message: message))
        }
        return captured
    }

    /// Returned (in addition to the `XCTFail`) so a timeout reaches callers
    /// that `throw` rather than silently reading as a missing result.
    private struct InstallTimeout: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }
}
