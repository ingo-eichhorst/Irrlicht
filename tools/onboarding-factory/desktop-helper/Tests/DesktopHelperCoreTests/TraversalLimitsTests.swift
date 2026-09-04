import DesktopHelperCore
import XCTest

final class TraversalLimitsTests: XCTestCase {
    func testDefaultDepthCoversTheMeasuredDesktopComposerTree() throws {
        // Claude Desktop 1.46388.1 reached depth 34. Keep enough headroom for
        // normal renderer changes without removing the independent node cap.
        let limits = try TraversalLimits().validated()

        XCTAssertGreaterThanOrEqual(limits.maxDepth, 64)
        XCTAssertEqual(limits.maxNodes, 5_000)
    }

    func testDepthLimitAllowsBoundedRendererHeadroom() {
        XCTAssertNoThrow(
            try TraversalLimits(maxDepth: 128, maxNodes: 50_000).validated()
        )
        XCTAssertThrowsError(
            try TraversalLimits(maxDepth: 129, maxNodes: 50_000).validated()
        ) { error in
            XCTAssertEqual((error as? HelperFailure)?.code, .invalidRequest)
        }
    }
}
