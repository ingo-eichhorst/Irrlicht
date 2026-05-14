import AVFoundation
import UserNotifications
import XCTest
@testable import Irrlicht

final class SoundPlayerTests: XCTestCase {

    // MARK: - resolveNotificationSound

    func testResolveBuiltInProducesNamedSound() throws {
        let defaults = UserDefaults.standard
        let event = NotificationEvent.ready
        defaults.set(SoundChoice.chime.rawValue, forKey: event.soundKey)
        defer { defaults.removeObject(forKey: event.soundKey) }

        let sound = SessionManager.resolveNotificationSound(for: event)
        XCTAssertNotNil(sound, "built-in choice should produce a UNNotificationSound")
    }

    func testResolveNoneAndSpeakReturnNil() {
        let defaults = UserDefaults.standard
        let event = NotificationEvent.waiting
        defer { defaults.removeObject(forKey: event.soundKey) }

        defaults.set(SoundChoice.none.rawValue, forKey: event.soundKey)
        XCTAssertNil(SessionManager.resolveNotificationSound(for: event))

        defaults.set(SoundChoice.speak.rawValue, forKey: event.soundKey)
        XCTAssertNil(SessionManager.resolveNotificationSound(for: event))
    }

    func testResolveMissingCustomFallsBackToPing() {
        let defaults = UserDefaults.standard
        let event = NotificationEvent.contextPressure
        let missing = SoundChoice.custom(installedFilename: "IrrlichtCustom-doesnotexist.caf", displayPath: "/tmp/x.mp3")
        defaults.set(missing.rawValue, forKey: event.soundKey)
        defer { defaults.removeObject(forKey: event.soundKey) }

        // We can't introspect UNNotificationSound's name, so the assertion is
        // limited to "produces a non-nil fallback rather than crashing or
        // returning nil." Combined with the explicit Ping branch in
        // resolveNotificationSound, this guards against the regression.
        let sound = SessionManager.resolveNotificationSound(for: event)
        XCTAssertNotNil(sound, "missing custom should fall back to Ping, not nil")
    }

    // MARK: - installCustom

    func testInstallCustomPassthroughCopiesAiff() throws {
        let src = URL(fileURLWithPath: "/System/Library/Sounds/Glass.aiff")
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

    func testInstallCustomReplacesStaleVariant() throws {
        // First install a .aiff, then install a .wav for the same event and
        // confirm the .aiff is gone.
        let aiffSrc = URL(fileURLWithPath: "/System/Library/Sounds/Pop.aiff")
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

    // MARK: - helpers

    private func waitInstall(src: URL, event: NotificationEvent, timeout: TimeInterval = 5) throws -> String {
        switch waitInstallResult(src: src, event: event, timeout: timeout) {
        case .success(let name): return name
        case .failure(let error): throw error
        }
    }

    private func waitInstallResult(src: URL, event: NotificationEvent, timeout: TimeInterval = 5) -> Result<String, Error> {
        let expectation = XCTestExpectation(description: "installCustom completes")
        var captured: Result<String, Error>!
        SoundPlayer.installCustom(srcURL: src, event: event) { result in
            captured = result
            expectation.fulfill()
        }
        _ = XCTWaiter.wait(for: [expectation], timeout: timeout)
        return captured
    }
}
