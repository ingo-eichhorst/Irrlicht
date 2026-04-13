import XCTest
@testable import Irrlicht

final class MenuBarStatusRendererTests: XCTestCase {
    func testStateSegmentsUseStablePriorityOrderAndFractions() {
        let sessions = [
            makeSession(id: "1", state: .ready),
            makeSession(id: "2", state: .working),
            makeSession(id: "3", state: .ready),
            makeSession(id: "4", state: .waiting)
        ]

        let segments = MenuBarStatusRenderer.stateSegments(for: sessions)

        XCTAssertEqual(segments.count, 3)
        XCTAssertEqual(segments[0].state, .waiting)
        XCTAssertEqual(segments[0].count, 1)
        XCTAssertEqual(segments[0].fraction, 0.25, accuracy: 0.0001)
        XCTAssertEqual(segments[1].state, .working)
        XCTAssertEqual(segments[1].count, 1)
        XCTAssertEqual(segments[1].fraction, 0.25, accuracy: 0.0001)
        XCTAssertEqual(segments[2].state, .ready)
        XCTAssertEqual(segments[2].count, 2)
        XCTAssertEqual(segments[2].fraction, 0.5, accuracy: 0.0001)
    }

    func testAggregatedGroupSVGUsesPieSlicesWhenMultipleStatesArePresent() {
        let sessions = [
            makeSession(id: "1", state: .waiting),
            makeSession(id: "2", state: .working),
            makeSession(id: "3", state: .ready),
            makeSession(id: "4", state: .ready)
        ]

        let svg = MenuBarStatusRenderer.aggregatedGroupSVG(for: sessions)

        XCTAssertEqual(svg.components(separatedBy: "<path ").count - 1, 3)
        XCTAssertTrue(svg.contains(">4</text>"))
    }

    func testAggregatedGroupSVGFallsBackToSolidCircleForSingleStateProjects() {
        let sessions = [
            makeSession(id: "1", state: .working),
            makeSession(id: "2", state: .working),
            makeSession(id: "3", state: .working),
            makeSession(id: "4", state: .working)
        ]

        let svg = MenuBarStatusRenderer.aggregatedGroupSVG(for: sessions)

        XCTAssertEqual(svg.components(separatedBy: "<path ").count - 1, 0)
        XCTAssertEqual(svg.components(separatedBy: "<circle ").count - 1, 1)
        XCTAssertTrue(svg.contains(">4</text>"))
    }

    func testBuildStatusImageReturnsImageForSessions() {
        let image = MenuBarStatusRenderer.buildStatusImage(
            sessions: [makeSession(id: "1", state: .working)],
            projectGroupOrder: []
        )

        XCTAssertNotNil(image)
    }

    private func makeSession(id: String, state: SessionState.State) -> SessionState {
        SessionState(
            id: "sess_\(id)",
            state: state,
            model: "claude-3.7-sonnet",
            cwd: "/Users/test/projects/test",
            projectName: "test",
            firstSeen: Date(),
            updatedAt: Date()
        )
    }
}
