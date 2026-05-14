import XCTest
@testable import Irrlicht

final class SoundChoiceTests: XCTestCase {

    func testBuiltInRoundTrip() {
        for choice in SoundChoice.builtIns + [.none, .speak] {
            let raw = choice.rawValue
            let decoded = SoundChoice(rawValue: raw)
            XCTAssertEqual(decoded, choice, "round-trip failed for \(choice)")
        }
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
        XCTAssertNil(SoundChoice.speak.notificationSoundName)
    }

    func testCustomNotificationSoundUsesInstalledFilename() {
        let choice = SoundChoice.custom(installedFilename: "IrrlichtCustom-ready.caf", displayPath: "/x.mp3")
        XCTAssertEqual(choice.notificationSoundName, "IrrlichtCustom-ready.caf")
    }
}
