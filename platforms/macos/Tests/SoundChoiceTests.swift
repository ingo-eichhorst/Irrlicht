import XCTest
@testable import Irrlicht

final class SoundChoiceTests: XCTestCase {

    func testBuiltInRoundTrip() {
        for choice in SoundChoice.builtIns + [.none] + SoundChoice.speakChoices {
            let raw = choice.rawValue
            let decoded = SoundChoice(rawValue: raw)
            XCTAssertEqual(decoded, choice, "round-trip failed for \(choice)")
        }
    }

    func testBareSpeakDecodesToDefaultVoice() {
        // Backwards-compat: the earlier single-voice encoding stored "speak".
        // Existing users must keep working without losing their selection.
        XCTAssertEqual(SoundChoice(rawValue: "speak"), .speak(.default))
    }

    func testCustomRoundTrip() {
        let original = SoundChoice.custom(installedFilename: "IrrlichtCustom-ready.caf", displayPath: "/Users/x/Music/alert.mp3")
        let decoded = SoundChoice(rawValue: original.rawValue)
        XCTAssertEqual(decoded, original)
    }

    func testCustomWithPipeInPathRoundTrips() {
        // The grammar splits on the first `|`, so a display path that itself
        // contains `|` after the separator must survive intact.
        let original = SoundChoice.custom(installedFilename: "IrrlichtCustom-waiting.caf", displayPath: "/tmp/weird|name.wav")
        let decoded = SoundChoice(rawValue: original.rawValue)
        XCTAssertEqual(decoded, original)
    }

    func testRejectsUnknownRawValues() {
        XCTAssertNil(SoundChoice(rawValue: "bogus"))
        XCTAssertNil(SoundChoice(rawValue: "custom:"))
        XCTAssertNil(SoundChoice(rawValue: "custom:noseparator"))
    }

    func testBuiltInsResolveToSystemSounds() {
        XCTAssertEqual(SoundChoice.ping.systemSoundFilename, "Ping.aiff")
        XCTAssertEqual(SoundChoice.chime.systemSoundFilename, "Glass.aiff")
        XCTAssertEqual(SoundChoice.funk.systemSoundFilename, "Funk.aiff")
        XCTAssertEqual(SoundChoice.whoosh.systemSoundFilename, "Blow.aiff")
        XCTAssertEqual(SoundChoice.sosumi.systemSoundFilename, "Sosumi.aiff")
    }

    func testSpeakAndNoneHaveNoNotificationSoundName() {
        XCTAssertNil(SoundChoice.none.notificationSoundName)
        for choice in SoundChoice.speakChoices {
            XCTAssertNil(choice.notificationSoundName, "speak variant \(choice) must have no notification sound")
        }
    }

    func testCustomNotificationSoundUsesInstalledFilename() {
        let choice = SoundChoice.custom(installedFilename: "IrrlichtCustom-ready.caf", displayPath: "/x.mp3")
        XCTAssertEqual(choice.notificationSoundName, "IrrlichtCustom-ready.caf")
    }
}
