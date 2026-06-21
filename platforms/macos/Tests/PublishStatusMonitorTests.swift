import XCTest
import SwiftUI
@testable import Irrlicht

@MainActor
final class PublishStatusMonitorTests: XCTestCase {

    func testStateMappingFromEndpointFields() {
        typealias S = PublishStatusMonitor.State
        // enabled=false always reads as off, regardless of the reported state.
        XCTAssertEqual(S.from(enabled: false, state: "connected"), .off)
        XCTAssertEqual(S.from(enabled: false, state: nil), .off)
        // enabled=true maps the daemon's forwarder states 1:1.
        XCTAssertEqual(S.from(enabled: true, state: "connected"), .connected)
        XCTAssertEqual(S.from(enabled: true, state: "connecting"), .connecting)
        XCTAssertEqual(S.from(enabled: true, state: "auth_failed"), .authFailed)
        XCTAssertEqual(S.from(enabled: true, state: "disconnected"), .disconnected)
        // An unrecognized or missing state is surfaced as unknown, not a crash.
        XCTAssertEqual(S.from(enabled: true, state: "weird"), .unknown)
        XCTAssertEqual(S.from(enabled: true, state: nil), .unknown)
    }

    func testAuthFailedIsVisuallyDistinct() {
        // The whole point of the daemon endpoint: surface a bad token as red.
        XCTAssertEqual(PublishStatusMonitor.State.authFailed.dotColor, .red)
        XCTAssertEqual(PublishStatusMonitor.State.connected.dotColor, .green)
    }

    func testLabels() {
        XCTAssertEqual(PublishStatusMonitor.State.off.label, "off")
        XCTAssertEqual(PublishStatusMonitor.State.connected.label, "connected")
        XCTAssertEqual(PublishStatusMonitor.State.authFailed.label, "token rejected")
    }
}
